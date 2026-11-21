package buddyactions

import (
	"context"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) cleanseCasterAvailableLocked(w *worldv1.SnapshotResponse, r spellcatalog.Spell) bool {
	self := w.GetSelf()
	return !s.closed && safeSelfCombat(w, s.selfGUID) == "" && self.UnitFlags&0x2000 == 0 &&
		self.ChannelSpellId == 0 && !s.clientControlLost && !s.clientControlRestoring &&
		!s.state.CastPending && !s.state.MotionUnconfirmed && s.state.InteractionOwner == "" &&
		(r.CastMs == 0 || !s.state.Moving && self.MovementFlags&0xff == 0) &&
		(r.GcdMs == 0 || !s.now().Before(s.nextHeal) && !s.now().Before(s.nextAssist))
}

func (s *Service) cleanseRankAvailableLocked(self *worldv1.PlayerState, r spellcatalog.Spell) bool {
	return r.Capability().Kind == spellcatalog.Cleanse && s.knownSpells[r.ID] && self != nil &&
		self.PowerKnown && int32(self.PowerType) == r.Power && uint64(self.Power) >= spellCost(r, self) &&
		!s.spellOnCooldownLocked(r) && casterRequirementsObserved(r, self)
}

func (s *Service) cleanseTargetObservedLocked(w *worldv1.SnapshotResponse, target uint64, online map[uint64]bool) bool {
	p := observedHealingPlayer(w, target)
	if !healablePlayer(p, w.GetSelf().GetMapId()) {
		return false
	}
	if target == s.selfGUID || online[target] {
		return true
	}
	team := observedPlayerTeam(w.Self)
	return team != 0 && observedPlayerTeam(p) == team
}

// CastCleanse executes one caller-selected learned dispel on an exact observed
// harmful aura. It never chooses a patient, aura, spell rank or support priority.
// The server chooses which eligible aura a dispel actually removes; the effect
// receipt requires its log and the requested slot's observed removal.
func (s *Service) CastCleanse(ctx context.Context, r *actionsv1.CastCleanseRequest) (*actionsv1.State, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetTargetGuid() == 0 || r.GetSpellId() == 0 ||
		r.GetAuraSlot() >= 255 || r.GetAuraRevision() == 0 || r.GetAuraSpellId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "current body, exact learned spell, target and aura slot/revision/spell required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.session == nil || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current connected body required")
	}
	s.expireInteractionLocked()
	online := s.healingPartyLocked(ctx)
	w, err := s.snapshot(ctx)
	spell, learned := s.learnedContractsLocked()[r.SpellId]
	if err != nil || !s.spellbookKnown || !learned || !s.cleanseCasterAvailableLocked(w, spell) ||
		!s.cleanseRankAvailableLocked(w.GetSelf(), spell) || !s.refreshPositionLocked(w) ||
		!s.cleanseTargetObservedLocked(w, r.TargetGuid, online) {
		return nil, status.Error(codes.FailedPrecondition, "ready learned cleanse and current living friendly caster/target required")
	}
	set := s.buffAuras[r.TargetGuid]
	if set == nil {
		return nil, status.Error(codes.FailedPrecondition, "observed harmful aura required")
	}
	aura := set.slots[r.AuraSlot]
	if aura.revision != r.AuraRevision || aura.spell != r.AuraSpellId ||
		s.observedDispelMask(aura)&spell.FriendlyDispelMask() == 0 {
		return nil, status.Error(codes.FailedPrecondition, "requested aura changed or learned spell cannot dispel it")
	}
	target := observedHealingPlayer(w, r.TargetGuid)
	d := distance(s.pos, playerPosition(target))
	if r.TargetGuid != s.selfGUID && (d < spell.MinFriendly || d > spell.MaxFriendly) {
		return nil, status.Error(codes.FailedPrecondition, "requested cleanse target is outside spell range")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.castTargetedSupportLocked(ctx, spell, r.TargetGuid, r.TargetGuid, string(spellcatalog.Cleanse))
	s.supportOp.cleanseAura = aura.spell
	s.supportOp.cleanseSlot, s.supportOp.cleanseRevision = uint8(r.AuraSlot), aura.revision
	return s.stateLocked(), nil
}
