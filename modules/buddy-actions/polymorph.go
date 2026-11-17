package buddyactions

import (
	"context"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Polymorph either prepares a route for one selected creature or makes one
// normal cast from a stationary caster.
func (s *Service) Polymorph(ctx context.Context, r *actionsv1.PolymorphRequest) (*actionsv1.State, error) {
	return s.castCrowdControl(ctx, r.GetSessionId(), r.GetSpellId(), r.GetTargetGuid(), spellcatalog.Polymorph, 8, r.GetApproach(), r.GetCancel())
}

func (s *Service) Sap(ctx context.Context, r *actionsv1.SapRequest) (*actionsv1.State, error) {
	return s.castCrowdControl(ctx, r.GetSessionId(), r.GetSpellId(), r.GetTargetGuid(), spellcatalog.Sap, 4, r.GetApproach(), r.GetCancel())
}

func (s *Service) castCrowdControl(ctx context.Context, session string, rank uint32, target uint64, kind spellcatalog.Kind, class uint32, approach, cancel bool) (*actionsv1.State, error) {
	if session == "" || rank == 0 || target>>48 != 0xf130 || target&0xffffff == 0 || approach && cancel {
		return nil, status.Error(codes.InvalidArgument, "current session, exact spell rank and NPC GUID required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireCrowdControlLocked()
	if s.closed || session != s.sessionID {
		return nil, status.Error(codes.FailedPrecondition, "current crowd control body required")
	}
	if cancel || approach || s.crowdControl.active || s.crowdControl.matches(rank, target, kind) {
		return s.requestCrowdControlApproachLocked(ctx, rank, target, kind, class, approach, cancel)
	}
	if !s.crowdControlIdleLocked() {
		return nil, status.Error(codes.FailedPrecondition, "current session and idle casting custody required")
	}
	spell, learned := s.learnedContractsLocked()[rank]
	if !s.spellbookKnown || !learned || spell.Capability().Kind != kind || s.now().Before(s.spellReady[spell.ID]) {
		return nil, status.Error(codes.FailedPrecondition, "exact learned crowd-control rank required")
	}
	// This is the last dependency read before writing. Use only the requested
	// current world object and never a marker, owner selection or cached fallback.
	w, err := s.snapshot(ctx, target)
	if err != nil || safeSelfCombat(w, s.selfGUID) != "" || !s.refreshPositionLocked(w) {
		return nil, status.Error(codes.FailedPrecondition, "current living caster observation required")
	}
	self, c := w.Self, w.RequestedCreature
	if !self.ClassKnown || self.ClassId != class || self.MovementFlags&0xff != 0 || self.ChannelSpellId != 0 ||
		!self.PowerKnown || int32(self.PowerType) != spell.Power || uint64(self.Power) < spellCost(spell, self) {
		return nil, status.Error(codes.FailedPrecondition, "stationary caster with observed power and spell requirements required")
	}
	if c == nil || c.Guid != target || !c.Present || !c.MapKnown || c.MapId != self.MapId ||
		!c.AliveKnown || !c.Alive || !c.HealthKnown || c.Health == 0 || !c.UnitFlagsKnown ||
		c.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) != 0 || c.UnitFlags_2&1 != 0 || c.DynamicFlags&0x20 != 0 {
		return nil, status.Error(codes.FailedPrecondition, "selected living attackable NPC is not observed on this map")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	observed := position{c.X, c.Y, c.Z, c.Orientation}
	observedKnown := c.PositionKnown && finite(observed)
	if kind == spellcatalog.Sap {
		pos, known, estimated := creaturePosition(c)
		if !known || estimated && (c.GetTrajectory().GetDurationMs() == 0 || !observedKnown) {
			return nil, status.Error(codes.FailedPrecondition, "selected NPC position is not observed within crowd-control range")
		}
		ready, _, err := s.sapReadyLocked(ctx, w, spell)
		if err != nil {
			return nil, err
		}
		if !ready {
			return s.stateLocked(), nil
		}
		if !s.stealthedLocked() {
			if err := s.prepareSapStealthLocked(ctx, c.Guid); err != nil {
				return nil, err
			}
			return s.stateLocked(), nil
		}
		inRange := func(p position) bool {
			d := distance(s.pos, p)
			return d >= spell.MinHostile && d <= spell.MaxHostile
		}
		if !inRange(pos) || estimated && !inRange(observed) {
			return nil, status.Error(codes.FailedPrecondition, "selected NPC is outside crowd-control spell range")
		}
	} else if !observedKnown {
		return nil, status.Error(codes.FailedPrecondition, "selected NPC position is not observed within crowd-control range")
	}
	// This exact Polymorph request sends no movement. The server determines
	// current spell range and line of sight even when the creature is moving.
	if !s.casterRequirementsLocked(spell) {
		return nil, status.Error(codes.FailedPrecondition, "observed caster stance or aura requirements not satisfied")
	}
	s.castSupportLocked(ctx, spell, c.Guid, 0)
	return s.stateLocked(), nil
}

func (s *Service) crowdControlIdleLocked() bool {
	return !s.closed && !s.state.CastPending && !s.state.Moving && !s.state.MotionUnconfirmed &&
		!s.state.AutoAttacking && !s.state.AutoAttackRequested && !s.state.AutoAttackUnconfirmed && s.autoTarget == 0 && !s.autoStopping &&
		s.state.InteractionOwner == "" && !s.state.Practicing && !s.engage.active && !s.fargodeep.active && !s.state.Traveling && !s.recovery.state.Active &&
		!s.state.Assisting && !s.now().Before(s.nextHeal) && !s.now().Before(s.nextAssist)
}

func (s *Service) pendingCrowdControlLocked() *supportAttempt {
	op := s.supportOp
	if op != nil && spellcatalog.Kind(op.view.Capability).BreaksOnDamage() && op.view.Phase == "waiting_server" &&
		s.state.CastPending && s.pendingSpell == op.view.SpellId && s.pendingTarget == op.view.TargetGuid && s.castCount == op.count {
		return op
	}
	return nil
}

func (s *Service) finishCrowdControlLocked(phase string) {
	if op := s.pendingCrowdControlLocked(); op != nil {
		op.view.Phase = phase
		s.pendingSpell, s.pendingTarget, s.state.CastPending = 0, 0, false
	}
}

func (s *Service) expireCrowdControlLocked() {
	if op := s.pendingCrowdControlLocked(); op != nil && !s.now().Before(op.until) {
		phase := "outcome_unconfirmed_no_retry"
		if op.view.ServerCastGo {
			phase = "server_cast_go_effect_unverified"
		}
		s.finishCrowdControlLocked(phase)
	}
}

func (s *Service) confirmCrowdControlLocked() {
	if op := s.pendingCrowdControlLocked(); op != nil && op.view.ServerCastGo &&
		op.controlAura.spell != 0 && (!op.controlAura.durationKnown || op.controlAura.remaining(s.now()) > 0) {
		op.view.EffectObserved = true
		s.finishCrowdControlLocked("confirmed_aura")
	}
}

func (s *Service) crowdControlAuraLocked(guid uint64, updates []auraDelta, all bool) {
	op := s.pendingCrowdControlLocked()
	if op == nil || op.view.TargetGuid != guid {
		return
	}
	// A full snapshot can invalidate a candidate, but never prove a fresh
	// application. Only a delta after dispatch may create the candidate.
	if all {
		op.controlAura = observedBuffAura{}
		return
	}
	for _, update := range updates {
		a := update.aura
		if update.slot == op.controlSlot {
			op.controlAura = observedBuffAura{}
		}
		if a.spell == op.view.SpellId && a.caster == s.selfGUID && a.negative && (!a.durationKnown || a.duration > 0) {
			a.at = s.now()
			op.controlAura, op.controlSlot = a, update.slot
		}
	}
	s.confirmCrowdControlLocked()
}
