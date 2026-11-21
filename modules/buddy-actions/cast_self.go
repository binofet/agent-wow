package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const selfCastCapability = "cast_self"

func isChanneledSpell(spell spellcatalog.Spell) bool {
	return spell.Attrs[1]&(0x4|0x40) != 0
}

func (s *Service) selfCastBusyLocked() bool {
	return s.state.CastPending || s.state.Moving || s.state.Traveling || s.state.MotionUnconfirmed ||
		s.state.AutoAttackUnconfirmed || s.state.InteractionOwner != "" || s.state.Practicing ||
		s.fargodeep.active || s.crowdControl.active || s.recovery.state.Active ||
		s.now().Before(s.nextHeal) || s.now().Before(s.nextAssist)
}

// Use target selectors and packet requirements from the learned spell's DBC
// row. The native client does not choose a spell or interpret its effects.
func supportsSelfCast(spell spellcatalog.Spell) bool {
	if spell.Attrs[0]&0x40 != 0 || !spell.TimingKnown || !spell.RangeKnown || len(spell.Effects) == 0 ||
		spell.ItemClass >= 0 || spell.Focus != 0 || spell.Facing != 0 || spell.Behind ||
		spell.TargetMask&^uint32(2) != 0 {
		return false
	}
	for _, effect := range spell.Effects {
		if (effect.A != 1 && effect.A != 21) || effect.B != 0 || effect.Chain > 1 || effect.Radius != 0 {
			return false
		}
	}
	return true
}

// CastSelf writes one ordinary learned spell explicitly targeted at this body.
func (s *Service) CastSelf(ctx context.Context, r *actionsv1.CastSelfRequest) (*actionsv1.State, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetSpellId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "session, exact caster and learned spell required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireSelfChannelLocked()
	if s.closed || r.GetSessionId() != s.sessionID || r.GetSelfGuid() != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current owned body required")
	}
	spell, learned := s.learnedContractsLocked()[r.GetSpellId()]
	if !s.spellbookKnown || !learned || !supportsSelfCast(spell) {
		return nil, status.Error(codes.FailedPrecondition, "learned spell with a supported self target required")
	}
	if s.selfCastBusyLocked() || s.spellOnCooldownLocked(spell) {
		return nil, status.Error(codes.FailedPrecondition, "current idle cast custody and spell ready required")
	}
	w, err := s.snapshot(ctx)
	if err != nil || safeSelfCombat(w, s.selfGUID) != "" || !s.refreshPositionLocked(w) {
		return nil, status.Error(codes.FailedPrecondition, "current living caster observation required")
	}
	self := w.Self
	cost := spellCost(spell, self)
	if self.MovementFlags&0xff != 0 || self.ChannelSpellId != 0 ||
		cost != 0 && (!self.PowerKnown || int32(self.PowerType) != spell.Power || uint64(self.Power) < cost) ||
		!s.casterRequirementsLocked(spell) {
		return nil, status.Error(codes.FailedPrecondition, "stationary caster with spell requirements required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.castTargetedSupportLocked(ctx, spell, s.selfGUID, 0, selfCastCapability)
	s.supportOp.selfChannel = isChanneledSpell(spell)
	return s.stateLocked(), nil
}

func (s *Service) pendingSelfChannelLocked() *supportAttempt {
	op := s.supportOp
	if op != nil && op.selfChannel && s.state.CastPending && s.pendingSpell == op.view.SpellId &&
		s.pendingTarget == s.selfGUID && s.castCount == op.count {
		return op
	}
	return nil
}

func (s *Service) finishSelfChannelLocked(phase string) {
	if op := s.pendingSelfChannelLocked(); op != nil {
		op.view.Phase = phase
		s.pendingSpell, s.pendingTarget, s.state.CastPending = 0, 0, false
	}
}

func (s *Service) expireSelfChannelLocked() {
	if op := s.pendingSelfChannelLocked(); op != nil && !s.now().Before(op.until) {
		s.finishSelfChannelLocked("outcome_unconfirmed_no_retry")
	}
}

func (s *Service) observeSelfChannelLocked(code uint32, p []byte) error {
	s.expireSelfChannelLocked()
	op := s.pendingSelfChannelLocked()
	if op == nil {
		return nil
	}
	actor, n, err := readGUID(p)
	if err != nil {
		return err
	}
	if actor != s.selfGUID {
		return nil
	}
	if code == opcode.MSGChannelStart {
		if len(p) != n+8 {
			return errors.New("malformed channel start")
		}
		if binary.LittleEndian.Uint32(p[n:]) != op.view.SpellId || op.view.ChannelStarted {
			return nil
		}
		n += 4
		if binary.LittleEndian.Uint32(p[n:]) == 0 {
			return errors.New("zero channel duration")
		}
		op.view.ChannelStarted, op.view.Phase = true, "channeling"
	} else if len(p) != n+4 {
		return errors.New("malformed channel update")
	} else if !op.view.ChannelStarted || op.view.ChannelEnded {
		return nil
	}
	op.view.ChannelRemainingMs = binary.LittleEndian.Uint32(p[n:])
	if op.view.ChannelRemainingMs == 0 {
		op.view.ChannelEnded = true
		if op.view.ServerCastGo {
			s.finishSelfChannelLocked("channel_ended_effect_unverified")
		}
		return nil
	}
	// Channel duration is provided by the server after spell modifiers.
	op.until = s.now().Add(time.Duration(op.view.ChannelRemainingMs)*time.Millisecond + 3*time.Second)
	s.castUntil = op.until
	return nil
}
