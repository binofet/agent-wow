package buddyworld

import (
	"context"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// This is an unused mechanical handoff grant, not a booking association or an
// arrival receipt. Harmless same-source catchup may advance current facts; the
// immutable witness and retained predecessor are never rewritten by it.
func (s *Service) splineTransferAdoptionCurrentLocked(now time.Time) bool {
	c := &s.completion
	a := c.transferAdoption
	if a == nil || a.Transfer == nil || c.operation != nil || c.revision != a.Revision || c.transferPredecessor == nil || s.worldTransfer.completion != c.transferPredecessor {
		return false
	}
	old, current := a.Transfer, s.worldTransferSnapshotLocked(now)
	return current.Known && current.PredecessorQualified && current.ContinuationKnown && current.SelfGuid == a.SelfGuid &&
		current.FromVisitRevision == a.FromVisitRevision && current.ToVisitRevision == a.ToVisitRevision && current.Revision == old.Revision &&
		proto.Equal(current.Core, old.Core) && proto.Equal(current.PredecessorAuthority, old.PredecessorAuthority) && proto.Equal(current.PredecessorCompletion, old.PredecessorCompletion) &&
		sameMotionSource(current.CurrentScope, old.CurrentScope) && current.CurrentScope.GetSplineActive() && old.CurrentScope.GetSplineActive() &&
		current.CurrentScope.MotionRevision >= old.CurrentScope.MotionRevision && current.CurrentAuthorityRevision >= old.CurrentAuthorityRevision &&
		current.CurrentMovementRevision >= old.CurrentMovementRevision && current.CurrentSplineRevision >= old.CurrentSplineRevision &&
		current.CurrentUnitFlagsRevision >= old.CurrentUnitFlagsRevision && current.CurrentUnitFlagsMotionRevision >= old.CurrentUnitFlagsMotionRevision
}

func (s *Service) splineTransferAdoptionWitnessLocked(req *worldv1.AdoptSplineTransferRequest, now time.Time) (*worldv1.WorldTransferObservation, bool) {
	c, f := &s.completion, &s.worldTransfer
	w := s.worldTransferSnapshotLocked(now)
	if c.operation == nil || c.request == nil || c.operation != f.completion || c.operation.Revision != c.revision || req.ExpectedRevision != c.revision ||
		!w.Known || !w.PredecessorQualified || !w.ContinuationKnown || w.PredecessorCompletion == nil || w.PredecessorAuthority == nil || w.Core == nil ||
		req.SelfGuid != w.SelfGuid || req.VisitRevision != w.ToVisitRevision || req.VisitRevision != s.state.visitRevision || req.TransferRevision != w.Revision || req.CoreTransferRevision != w.Core.Revision ||
		req.AuthorityRevision != w.CurrentAuthorityRevision || req.MovementRevision != w.CurrentMovementRevision || req.SplineRevision != w.CurrentSplineRevision || req.UnitFlagsRevision != w.CurrentUnitFlagsRevision ||
		!proto.Equal(req.TransportScope, w.CurrentScope) || !proto.Equal(w.PredecessorCompletion, c.operation) || !c.consumed[completionKey(c.operation.TransportScope)] ||
		c.request.SelfGuid != c.operation.SelfGuid || c.request.VisitRevision != c.operation.VisitRevision || c.request.AuthorityRevision != c.operation.AuthorityRevision ||
		c.request.MovementRevision != c.operation.MovementRevision || c.request.SplineRevision != c.operation.SplineRevision || !proto.Equal(c.request.TransportScope, c.operation.TransportScope) {
		return nil, false
	}
	return w, true
}

// AdoptSplineTransfer changes only the active native reservation context. It
// retains the original report exactly, including Pending/TransportUnknown and
// its consumed key. Actions owns associating this mechanism with a booking.
func (s *Service) AdoptSplineTransfer(ctx context.Context, request *worldv1.AdoptSplineTransferRequest) (*worldv1.SplineCompletionObservation, error) {
	if request == nil || request.SelfGuid == 0 || request.VisitRevision == 0 || request.TransferRevision == 0 || request.CoreTransferRevision == 0 || request.AuthorityRevision == 0 || request.MovementRevision == 0 || request.SplineRevision == 0 || request.UnitFlagsRevision == 0 || request.TransportScope == nil {
		return nil, status.Error(codes.InvalidArgument, "exact current transfer, authority and completion bindings are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	req := proto.Clone(request).(*worldv1.AdoptSplineTransferRequest)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	c := &s.completion
	if c.transferRequest != nil && proto.Equal(req, c.transferRequest) {
		return s.completionSnapshotLocked(), nil
	}
	if c.transferAdoption != nil {
		return nil, status.Error(codes.FailedPrecondition, "a different adjacent handoff cannot replace retained predecessor history")
	}
	if c.revision == ^uint64(0) {
		return nil, status.Error(codes.ResourceExhausted, "completion revision exhausted")
	}
	w, ok := s.splineTransferAdoptionWitnessLocked(req, s.now())
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "exact current adjacent transfer and completed predecessor dispatch are unavailable")
	}
	c.revision++
	c.transferRequest = req
	c.transferPredecessor, c.transferPredecessorRequest = c.operation, c.request
	c.transferAdoption = &worldv1.SplineTransferAdoption{Revision: c.revision, SelfGuid: req.SelfGuid, FromVisitRevision: w.FromVisitRevision, ToVisitRevision: w.ToVisitRevision, Transfer: proto.Clone(w).(*worldv1.WorldTransferObservation)}
	// Detach the slot; do not retire, clear, cancel or relabel the old record.
	// A late retry returns that retained record, while the next distinct source
	// receives its own operation and independently fenced transport attempt.
	c.operation, c.request = nil, nil
	c.retirementRequest = nil
	return s.completionSnapshotLocked(), nil
}
