package buddyactions

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

type ordinaryFollowMovementKey struct{}

type ordinaryMovementAttempt struct {
	id, sid                     string
	self, sequence, rawRevision uint64
	visit                       encounterVisit
	explorationToken            string
	scope                       *modv1.TransportScope
	op                          uint32
	payload                     []byte
	pose                        position
	moving, swimming            bool
}

func (s *Service) newOrdinaryMovementAttemptLocked(w *worldv1.SnapshotResponse, op uint32, payload []byte, p position, moving, swimming bool) *ordinaryMovementAttempt {
	if s.movementOwner.owner != "" || s.targetMovementActiveLocked() || s.currentFollowFormationGoalLocked(s.followGoal, w) || w == nil || w.Self == nil || !s.matchesCurrentEncounterVisitLocked(w) || (op != opcode.MSGMoveStartForward && op != opcode.MSGMoveHeartbeat && op != opcode.MSGMoveStop && op != opcode.MSGMoveSetFacing) {
		return nil
	}
	scope := portalObservedWorldScope(w, s.selfGUID)
	if scope == nil || !scope.VisitBound || scope.ConnectionId == "" {
		return nil
	}
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return nil
	}
	return &ordinaryMovementAttempt{id: hex.EncodeToString(entropy[:]), sid: s.sessionID, self: s.selfGUID, visit: s.encounterVisit, scope: clonePortalScope(scope), rawRevision: w.Self.PositionRevision, op: op, payload: bytes.Clone(payload), pose: p, moving: moving, swimming: swimming}
}

// Legacy exploration uses the same diagnostic reader for its terminal STOP,
// never for a START/heartbeat or an idle/harvest/finite movement operation.
func (s *Service) newExplorationStopAttemptLocked(w *worldv1.SnapshotResponse, op uint32, payload []byte, p position) *ordinaryMovementAttempt {
	if op != opcode.MSGMoveStop || s.state.InteractionOwner != "exploration" || s.interactionToken == "" || s.state.InteractionHarvest || s.idleInteraction.token != "" || s.swimming {
		return nil
	}
	a := s.newOrdinaryMovementAttemptLocked(w, op, payload, p, false, false)
	if a != nil {
		a.explorationToken = s.interactionToken
	}
	return a
}

func (s *Service) explorationStopAttemptCurrentLocked() bool {
	a := s.ordinaryMovement
	return a != nil && a.explorationToken != "" && a.op == opcode.MSGMoveStop && !a.moving && !a.swimming &&
		s.state.InteractionOwner == "exploration" && s.interactionToken == a.explorationToken && !s.state.InteractionHarvest && s.idleInteraction.token == ""
}

// A timeout receipt stays UNKNOWN. This read can recover only physical write
// knowledge for that exact attempt, never authorize replay or prove realm effect.
func (s *Service) reconcileOrdinaryMovementLocked(ctx context.Context, w *worldv1.SnapshotResponse) {
	a := s.ordinaryMovement
	failure := s.state.LastTransportFailure
	if a == nil || failure == nil || !failure.OutcomeUnknown || failure.Sequence != a.sequence || s.transportSequence != a.sequence || failure.Opcode != a.op ||
		s.closed || s.sessionID != a.sid || s.selfGUID != a.self || s.encounterVisit != a.visit || !a.visit.bound || s.movementOwner.owner != "" || s.targetMovementActiveLocked() || s.currentFollowFormationGoalLocked(s.followGoal, w) || s.clientControlLost || s.clientControlRestoring {
		return
	}
	if a.explorationToken != "" {
		if !s.explorationStopAttemptCurrentLocked() || safeSelfCombat(w, s.selfGUID) != "" {
			return
		}
	} else if s.state.InteractionOwner != "" {
		return
	}
	if w == nil || !s.matchesCurrentEncounterVisitLocked(w) || w.Self == nil || !w.Self.Present || !w.Self.PositionKnown || !w.Self.MapKnown || w.Self.PositionRevision != a.rawRevision || !samePortalCoreVisit(a.scope, portalObservedWorldScope(w, a.self)) {
		return
	}
	r, err := modcb.ReadOwnMovementAttempt(ctx, s.session, a.sid, a.self, a.id)
	if err != nil || r.Opcode != a.op || !bytes.Equal(r.Payload, a.payload) || !samePortalCoreVisit(a.scope, r.TransportScope) {
		return
	}
	p, ok := idleMotionPacketPose(r.Payload, a.self)
	if !ok || p != a.pose || a.op != opcode.MSGMoveStartForward && a.op != opcode.MSGMoveHeartbeat && a.op != opcode.MSGMoveStop && a.op != opcode.MSGMoveSetFacing {
		return
	}
	s.pos, s.posKnown, s.posMap = a.pose, true, a.visit.mapID
	s.posEstimated, s.posAt = true, s.now()
	s.posRevision++
	s.swimming = a.swimming
	s.state.Moving = a.moving
	s.state.MotionUnconfirmed = false
	s.state.TransportWrites++
	s.lastStep = s.now()
	s.followNav.Reset()
	s.ordinaryMovement = nil
	s.state.Reason = "ordinary_movement_exact_write_reconciled"
	if a.explorationToken != "" {
		s.state.Reason = "exploration_stop_exact_write_reconciled"
	}
	s.publishClientMovementLocked(ctx, w, a.pose, true)
}
