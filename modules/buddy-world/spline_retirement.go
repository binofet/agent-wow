package buddyworld

import (
	"context"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Retirement ends only the local outstanding-attempt lifecycle. It preserves
// send uncertainty and the consumed physical-generation tombstone, and grants
// neither arrival nor fresh completion authority.
func (s *Service) blockSplineRetirementLocked() {
	if s.completion.operation != nil {
		s.completion.retirementBlocked = true
	}
}

func (s *Service) observeSplineRetirementProofLocked(packet *modv1.WorldPacket, current observed, next *observations, bound, continuous bool) {
	c := &s.completion
	op := c.operation
	if op == nil || c.retirementBlocked {
		return
	}
	life := snapshot(current, time.Time{})
	if !bound || !continuous || !packet.TransportScope.GetSplineActive() || current.guid != op.SelfGuid || next.visitRevision != op.VisitRevision || !sameMotionSource(op.TransportScope, packet.TransportScope) || current.receivedOwnCreate || current.receivedSpline || current.receivedMotionInvalid || current.receivedPosition && !current.receivedMovement || current.receivedMovement && !current.receivedMovementInfo.AllFinite() || !life.AliveKnown || !life.Alive {
		c.retirementBlocked = true
	}
}

func (s *Service) splineRetirementFactsFailureLocked() string {
	c, m, p, raw := &s.completion, &s.motion, s.state.players[0], s.state.ownMovement
	op := c.operation
	if op == nil || c.retirementBlocked || (!op.TransportWritten && !op.TransportUnknown) || op.Phase != "transport_written" && op.Phase != "transport_unknown" {
		return "attempted completion with uninterrupted supported source history is unavailable"
	}
	if p.guid != op.SelfGuid || s.state.visitRevision != op.VisitRevision || !scopeMatchesSelf(m.scope, p, s.state.visitRevision) || !m.scope.SplineActive || !sameMotionSource(op.TransportScope, m.scope) {
		return "current retirement body, visit and source binding is unavailable"
	}
	// Source identity is strict, including the original source counter. A STOP,
	// new header or identical retransmission is deliberately unsupported here.
	if !raw.known || len(raw.wire) == 0 || !raw.info.AllFinite() || raw.self != op.SelfGuid || raw.visit != op.VisitRevision || !raw.mapKnown || raw.mapID != p.mapID || raw.revision <= op.MovementRevision || !raw.scope.GetSplineActive() || !sameMotionSource(raw.scope, op.TransportScope) || raw.scope.GetMotionRevision() <= op.TransportScope.GetMotionRevision() || raw.scope.GetMotionRevision() > m.scope.MotionRevision || raw.info.Flags&0x08000000 != 0 {
		return "new actual own MovementInfo for the attempted spline is unavailable"
	}
	if !p.positionKnown || p.positionInferred || !p.movementKnown || p.x != raw.info.X || p.y != raw.info.Y || p.z != raw.info.Z || p.orientation != raw.info.Orientation || p.movementFlags != raw.info.Flags || p.extraFlags != uint32(raw.info.ExtraFlags) || p.movementTime != raw.info.Time {
		return "current non-inferred own pose does not match the received movement image"
	}
	if !m.unitFlagsKnown || m.unitFlags&0x100000 != 0 || m.unitFlagsMotion <= op.TransportScope.MotionRevision || m.unitFlagsMotion > m.scope.MotionRevision {
		return "new actual taxi-cleared unit flags are unavailable"
	}
	life := snapshot(p, time.Time{})
	if !life.AliveKnown || !life.Alive {
		return "actual alive body unavailable"
	}
	return ""
}

// RetireSplineCompletion is state-only. The exact newer received image and
// taxi-cleared flags retire a prior report without deciding destination arrival.
func (s *Service) RetireSplineCompletion(_ context.Context, req *worldv1.RetireSplineCompletionRequest) (*worldv1.SplineCompletionObservation, error) {
	if req == nil || req.SelfGuid == 0 || req.VisitRevision == 0 || req.AuthorityRevision == 0 || req.MovementRevision == 0 || req.UnitFlagsRevision == 0 || req.TransportScope == nil {
		return nil, status.Error(codes.InvalidArgument, "exact current retirement evidence binding is required")
	}
	req = proto.Clone(req).(*worldv1.RetireSplineCompletionRequest)
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &s.completion
	// Retirement's expected CAS is the operation's reservation revision;
	// the archived Complete request expected the immediately preceding CAS.
	if req.ExpectedRevision != 0 {
		if old, ok := c.completionArchive[req.ExpectedRevision-1]; ok && old.retirementRequest != nil && proto.Equal(old.retirementRequest, req) {
			r := completionBoundObservation(old.operation)
			r.Revision = old.operation.RetirementRevision
			return r, nil
		}
	}
	if c.retirementRequest != nil && proto.Equal(c.retirementRequest, req) {
		return s.completionSnapshotLocked(), nil
	}
	if c.revision == ^uint64(0) {
		return nil, status.Error(codes.FailedPrecondition, "retirement revision exhausted")
	}
	if c.operation.GetRetired() || req.ExpectedRevision != c.revision || req.SelfGuid != s.state.players[0].guid || req.VisitRevision != s.state.visitRevision || req.AuthorityRevision != s.motion.revision || req.MovementRevision != s.state.ownMovement.revision || req.UnitFlagsRevision != s.motion.unitFlagsRevision || !proto.Equal(req.TransportScope, s.motion.scope) {
		return nil, status.Error(codes.FailedPrecondition, "retirement CAS or exact current evidence binding is stale")
	}
	if failure := s.splineRetirementFactsFailureLocked(); failure != "" {
		return nil, status.Error(codes.FailedPrecondition, failure)
	}
	c.revision++
	c.retirementRequest = req
	op := c.operation
	op.Pending, op.Retired = false, true
	op.RetirementRevision = c.revision
	op.RetiredMovementRevision = req.MovementRevision
	op.RetiredUnitFlagsRevision = req.UnitFlagsRevision
	op.RetirementScope = cloneMotionScope(req.TransportScope)
	return s.completionSnapshotLocked(), nil
}
