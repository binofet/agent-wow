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

type ordinaryMovementAttempt struct {
	id, sid                     string
	self, sequence, rawRevision uint64
	visit                       encounterVisit
	scope                       *modv1.TransportScope
	op                          uint32
	payload                     []byte
	pose                        position
	moving, swimming            bool
}

func (s *Service) newOrdinaryMovementAttemptLocked(w *worldv1.SnapshotResponse, op uint32, payload []byte, p position, moving, swimming bool) *ordinaryMovementAttempt {
	if w == nil || w.Self == nil || !s.matchesCurrentEncounterVisitLocked(w) || (op != opcode.MSGMoveStartForward && op != opcode.MSGMoveHeartbeat && op != opcode.MSGMoveStop && op != opcode.MSGMoveSetFacing) {
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

// A timeout receipt stays UNKNOWN. This read can recover only physical write
// knowledge for that exact attempt, never authorize replay or prove realm effect.
func (s *Service) reconcileOrdinaryMovementLocked(ctx context.Context, w *worldv1.SnapshotResponse) {
	a := s.ordinaryMovement
	failure := s.state.LastTransportFailure
	if a == nil || failure == nil || !failure.OutcomeUnknown || failure.Sequence != a.sequence || s.transportSequence != a.sequence || failure.Opcode != a.op ||
		s.closed || s.sessionID != a.sid || s.selfGUID != a.self || s.encounterVisit != a.visit || !a.visit.bound || s.state.InteractionOwner != "" || s.clientControlLost || s.clientControlRestoring {
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
	s.publishClientMovementLocked(ctx, w, a.pose, true)
}
