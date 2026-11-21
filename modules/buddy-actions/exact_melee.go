package buddyactions

import (
	"context"
	"encoding/binary"
	"encoding/hex"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
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
	var requested []uint64
	if r.Stop {
		// A legacy own server-acknowledged attack can also be stopped exactly.
		if !s.state.AutoAttacking || s.state.AutoAttackTargetGuid != target || s.autoTarget != target || s.meleeAcknowledgedVisit != r.VisitId || s.encounterSwing.stopIssued {
			return nil, status.Error(codes.FailedPrecondition, "exact active own melee target is not acknowledged")
		}
	} else {
		if s.encounterSwing.target != 0 || !s.exactMeleeStartAvailableLocked(target) {
			return nil, status.Error(codes.FailedPrecondition, "current body and available melee custody required")
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
	if !r.Stop && !s.exactMeleeStartObservedLocked(w, target) {
		return nil, status.Error(codes.FailedPrecondition, "living controllable caster and selected current attackable creature required")
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
		if s.encounterSwing.target == target {
			s.encounterSwing.stopIssued = true
		}
		s.autoStopping = true
		s.state.AutoAttackRequested = false
		s.state.AutoAttackUnconfirmed = true
		s.state.Reason = "exact_melee_stop_waiting_for_server"
	} else {
		code, payload = opcode.CMSGAttackswing, binary.LittleEndian.AppendUint64(nil, target)
		if s.hostEncounterKey != "" {
			s.encounterSwing = encounterMeleeSwing{target: target, visit: r.VisitId}
		}
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
		!s.state.CastPending && !s.state.MotionUnconfirmed &&
		!s.state.AutoAttackRequested && !s.state.AutoAttacking && s.autoTarget == 0 && s.state.InteractionOwner == "" &&
		!s.state.Practicing && !s.fargodeep.active && !s.crowdControl.active && !s.recovery.state.Active &&
		!s.targetProtection.blocks(target, s.now())
}

// Attack start is not a swing or a hit. AzerothCore HandleAttackSwingOpcode
// and Unit::Attack admit a valid target regardless of range, facing, weapon,
// or motion. Lua positions/chases independently; server hit receipts determine
// whether damage actually landed.
func (s *Service) exactMeleeStartObservedLocked(w *worldv1.SnapshotResponse, target uint64) bool {
	return s.exactMeleeCasterObservedLocked(w) && s.exactMeleeStartAvailableLocked(target) &&
		exactOffenseTarget(w, w.RequestedCreature, target, nil)
}

func (s *Service) exactMeleeCasterObservedLocked(w *worldv1.SnapshotResponse) bool {
	self := w.GetSelf()
	return self.GetGuid() == s.selfGUID && self.GetPresent() && self.GetMapKnown() &&
		self.GetAliveKnown() && self.GetAlive() && self.GetHealthKnown() && self.GetHealth() > 0 &&
		self.GetUnitFlagsKnown() && self.GetUnitFlags()&0x08000000 == 0 // Mounted players cannot start attacks.
}
