package buddyactions

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"math"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Melee writes one ordinary swing start or stop for the exact current target
// and visit. The session callback authenticates the policy token and packet.
func (s *Service) Melee(ctx context.Context, r *actionsv1.MeleeRequest) (*actionsv1.State, error) {
	target := r.GetTargetGuid()
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || target == 0 || r.GetVisitId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "session, exact caster and target, and current visit required")
	}
	if kind := target >> 48; !r.Stop && (target&0xffffff == 0 || kind != 0xf130 && kind != 0xf150) {
		return nil, status.Error(codes.InvalidArgument, "melee start requires an exact creature GUID")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	token, supplied := modcb.IncomingPolicySpell(ctx)
	var tokenBytes [32]byte
	if n, err := hex.Decode(tokenBytes[:], []byte(token)); !supplied || err != nil || n != len(tokenBytes) || hex.EncodeToString(tokenBytes[:]) != token {
		return nil, status.Error(codes.PermissionDenied, "authenticated policy melee operation required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireMeleeOperationLocked()
	if s.closed || s.session == nil || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID || s.pendingMeleeOperationLocked() != nil {
		return nil, status.Error(codes.FailedPrecondition, "current body and idle melee operation required")
	}
	if s.autoStopping || s.state.AutoAttackUnconfirmed {
		return nil, status.Error(codes.FailedPrecondition, "prior melee state is unconfirmed")
	}
	s.expireInteractionLocked()
	var online map[uint64]bool
	var requested []uint64
	if r.Stop {
		// A legacy own server-acknowledged attack can also be stopped exactly.
		if !s.state.AutoAttacking || s.state.AutoAttackTargetGuid != target || s.autoTarget != target || s.meleeAcknowledgedVisit != r.VisitId {
			return nil, status.Error(codes.FailedPrecondition, "exact active own melee target is not acknowledged")
		}
	} else {
		if !s.exactMeleeStartAvailableLocked(target) {
			return nil, status.Error(codes.FailedPrecondition, "current body and available stationary melee custody required")
		}
		if !s.independent {
			party := s.readPartyStateLocked(ctx)
			if party.GetSessionId() == s.sessionID {
				online = s.onlineHealingGroup(party.GetGroup())
			}
		}
		requested = []uint64{target}
	}
	w, err := s.snapshot(ctx, requested...)
	self := w.GetSelf()
	visit := s.encounterVisit
	if err != nil || !visit.bound || visit.revision != r.VisitId || w.GetVisitRevision() != r.VisitId ||
		self.GetGuid() != s.selfGUID || !self.GetPresent() || !self.GetMapKnown() || self.GetMapId() != visit.mapID {
		return nil, status.Error(codes.FailedPrecondition, "current own world visit is not established")
	}
	if !r.Stop && !s.exactMeleeStartObservedLocked(w, target, online) {
		return nil, status.Error(codes.FailedPrecondition, "armed stationary caster and current attacker in melee range and facing required")
	}
	if !s.admitsPolicyEncounterLocked(ctx, w) {
		return nil, status.Error(codes.FailedPrecondition, "active Lua owner for the current encounter required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}

	op := s.reserveMeleeOperationLocked(token, target, !r.Stop)
	var code uint32
	var payload []byte
	if r.Stop {
		code = opcode.CMSGAttackstop
		s.autoStopping = true
		s.state.AutoAttackRequested = false
		s.state.AutoAttackUnconfirmed = true
		s.state.Reason = "exact_melee_stop_waiting_for_server"
	} else {
		code, payload = opcode.CMSGAttackswing, binary.LittleEndian.AppendUint64(nil, target)
		s.autoTarget = target
		s.state.AutoAttackRequested = true
		s.state.Reason = "exact_melee_start_waiting_for_server"
	}
	// The generic send helper may trigger defensive cleanup on error. This
	// request makes exactly one write and lets matching server evidence settle it.
	if _, err := s.session.SendPacket(modcb.ForwardPolicySpell(ctx), &modv1.SendPacketRequest{Opcode: code, Payload: payload}); err != nil {
		op.view.Phase = "transport_unconfirmed_no_retry"
		s.state.AutoAttackUnconfirmed = true
		s.state.Reason = "exact_melee_transport_unconfirmed"
	} else {
		op.view.TransportWritten = true
		s.state.TransportWrites++
	}
	return s.stateLocked(), nil
}

func (s *Service) exactMeleeStartAvailableLocked(target uint64) bool {
	return s.spellbookKnown && s.state.KnownAutoAttack && !s.clientControlLost && !s.clientControlRestoring &&
		!s.state.CastPending && !s.state.Moving && !s.state.Traveling && !s.state.MotionUnconfirmed &&
		!s.state.AutoAttackRequested && !s.state.AutoAttacking && s.autoTarget == 0 && s.state.InteractionOwner == "" &&
		!s.state.Practicing && !s.engage.active && !s.fargodeep.active && !s.crowdControl.active && !s.recovery.state.Active &&
		!s.targetProtection.blocks(target, s.now())
}

func (s *Service) exactMeleeStartObservedLocked(w *worldv1.SnapshotResponse, target uint64, online map[uint64]bool) bool {
	if safeSelfCombat(w, s.selfGUID) != "" || !s.refreshPositionLocked(w) || !s.exactMeleeStartAvailableLocked(target) {
		return false
	}
	self, creature := w.Self, w.RequestedCreature
	if !exactMeleeStance(self) || self.MovementFlags&0xff != 0 ||
		self.UnitFlags&(0x20000|0x200000) != 0 || !self.ChannelObjectKnown || self.ChannelObjectGuid != 0 ||
		!self.ChannelSpellKnown || self.ChannelSpellId != 0 || !self.BaseAttackTimeKnown || self.BaseAttackTimeMs == 0 || !s.weaponReady(self) ||
		!exactOffenseTarget(w, creature, target, online) {
		return false
	}
	to, known, estimated := creaturePosition(creature)
	if !known || estimated || !movementflags.GroundCompatible(creature.MovementFlags, 0) || creature.MovementFlags&0xff != 0 ||
		!self.CombatReachKnown || !creature.CombatReachKnown {
		return false
	}
	limit := exactMeleeRange(self.CombatReach, creature.CombatReach)
	if limit == 0 || distance(s.pos, to) > limit {
		return false
	}
	dx, dy := float64(to.x-s.pos.x), float64(to.y-s.pos.y)
	return dx == 0 && dy == 0 || math.Abs(math.Remainder(math.Atan2(dy, dx)-float64(s.pos.o), 2*math.Pi)) <= math.Pi/2
}

func exactMeleeStance(self *worldv1.PlayerState) bool {
	if !self.ClassKnown || self.ClassId == 0 || !self.ShapeshiftFormKnown {
		return false
	}
	// The ordinary Warrior weapon stances (2457, 71, 2458) preserve melee.
	form := self.ShapeshiftForm
	return form == 0 || self.ClassId == 1 && (form == 17 || form == 18 || form == 19)
}

func exactMeleeRange(caster, target float32) float64 {
	a, b := float64(caster), float64(target)
	// World only sets CombatReachKnown for its supported 0..50 range.
	if !(a > 0 && a <= 50 && b > 0 && b <= 50) {
		return 0
	}
	return math.Max(5, a+b+4.0/3.0) - 0.5
}
