package buddyactions

import (
	"context"
	"math"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// Original acceptance remains immutable across a transfer. Neither an unknown
// activation reply nor an observed flying source can promote this predicate.
func (s *Service) flightpathAcceptedOriginalLocked(revision uint64) bool {
	a, request, menu := s.flightpathActivation.operation, s.flightpathActivation.request, s.flightpathActivation.menu
	return a != nil && !s.closed && s.session != nil && a.Revision == revision && revision == s.flightpathActivation.revision && a.Pending &&
		a.SessionId == s.sessionID && a.SelfGuid == s.selfGUID && a.VisitId != 0 &&
		request != nil && menu != nil && request.SessionId == a.SessionId && request.SelfGuid == a.SelfGuid && request.VisitId == a.VisitId &&
		request.NpcGuid == a.NpcGuid && request.SourceNode == a.SourceNode && request.DestinationNode == a.DestinationNode &&
		request.MenuRevision == a.MenuRevision && menu.Revision == a.MenuRevision && menu.NpcGuid == a.NpcGuid &&
		a.Phase == "accepted_unsettled" && a.ReplyKnown && a.ReplyCode == 0 && a.ActivationAccepted &&
		(a.TransportWritten || a.TransportUnknown) && a.AcceptanceScope != nil &&
		flightpathScopeBound(a.AcceptanceScope, s.selfGUID, a.AcceptanceScope.MapId)
}

// Only settled exact RPC results are archived. Public observations retain one
// current attempt and segment; historical calls never restore that segment.
type flightpathTransferResult struct {
	request *actionsv1.AdoptFlightpathTransferRequest
	result  *actionsv1.FlightpathObservationResponse
}

func (f *flightpathActivationTracker) archiveTransferResult() {
	if f.transferRequest == nil || f.transferResult == nil {
		return
	}
	for _, old := range f.transferHistory {
		if proto.Equal(old.request, f.transferRequest) {
			return
		}
	}
	f.transferHistory = append(f.transferHistory, flightpathTransferResult{
		request: proto.Clone(f.transferRequest).(*actionsv1.AdoptFlightpathTransferRequest),
		result:  proto.Clone(f.transferResult).(*actionsv1.FlightpathObservationResponse),
	})
}

func (s *Service) flightpathTransferReceiptFactsLocked(receipt *worldv1.SplineTransferAdoption) bool {
	a := s.flightpathActivation.operation
	if a == nil || receipt == nil || receipt.Revision == 0 || receipt.SelfGuid != a.SelfGuid ||
		receipt.FromVisitRevision == 0 || receipt.ToVisitRevision == 0 || receipt.FromVisitRevision == math.MaxUint64 ||
		receipt.ToVisitRevision != receipt.FromVisitRevision+1 || receipt.Transfer == nil {
		return false
	}
	t := receipt.Transfer
	old, core := t.PredecessorCompletion, t.Core
	return t.Known && t.PredecessorQualified && t.ContinuationKnown && t.SelfGuid == a.SelfGuid &&
		t.FromVisitRevision == receipt.FromVisitRevision && t.ToVisitRevision == receipt.ToVisitRevision &&
		core != nil && core.Known && core.WorldportAckWritten && !core.WorldportAckUnknown && core.CompletionReportWritten &&
		core.SelfGuid == a.SelfGuid && core.TargetMapKnown && core.ToMapId == core.TargetMapId &&
		core.FromVisitRevision != math.MaxUint64 && core.ToVisitRevision == core.FromVisitRevision+1 &&
		old != nil && old.SelfGuid == a.SelfGuid && old.VisitRevision == receipt.FromVisitRevision && old.Revision != 0 && !old.Retired && !old.ArrivalKnown &&
		(old.TransportWritten || old.TransportUnknown) && old.TransportScope != nil &&
		flightpathScopeBound(old.TransportScope, a.SelfGuid, old.TransportScope.MapId) && old.TransportScope.SplineIdKnown &&
		old.SplineIdKnown && old.SplineId == old.TransportScope.SplineId &&
		core.FromVisitRevision == old.TransportScope.VisitRevision && core.FromMapId == old.TransportScope.MapId &&
		core.ConnectionId == old.TransportScope.ConnectionId && core.FromSplineIdKnown && core.FromSplineId == old.SplineId &&
		core.FromSplineRevision == old.TransportScope.SplineRevision && core.FromSplineSourceRevision == old.TransportScope.SplineSourceRevision &&
		core.FromSplineSourceOpcode == old.TransportScope.SplineSourceOpcode && core.FromSplineActive &&
		core.CompletionReportMotionRevision == old.TransportScope.MotionRevision &&
		core.CompletionReportMotionRevision <= core.FromMotionRevision &&
		t.CurrentScope != nil && flightpathScopeBound(t.CurrentScope, a.SelfGuid, core.ToMapId) &&
		t.CurrentScope.ConnectionId == core.ConnectionId && t.CurrentScope.VisitRevision == core.ToVisitRevision &&
		t.CurrentScope.SplineSourceRevision > core.WorldportAckMotionRevision && t.CurrentScope.SplineActive &&
		t.CurrentAuthorityRevision != 0 && t.CurrentMovementRevision != 0 && t.CurrentSplineRevision != 0 &&
		t.CurrentUnitFlagsKnown && t.CurrentUnitFlags&0x100000 != 0 && t.CurrentUnitFlagsRevision != 0 &&
		t.CurrentUnitFlagsMotionRevision > core.WorldportAckMotionRevision &&
		t.CurrentUnitFlagsMotionRevision <= t.CurrentScope.MotionRevision
}

func (s *Service) flightpathTransferBookingMatchesLocked(receipt *worldv1.SplineTransferAdoption) bool {
	if !s.flightpathTransferReceiptFactsLocked(receipt) {
		return false
	}
	a, old := s.flightpathActivation.operation, receipt.Transfer.PredecessorCompletion
	if segment := a.CurrentSegment; segment != nil {
		return s.flightpathSegmentAssociatedLocked() && receipt.FromVisitRevision == segment.VisitId &&
			receipt.Revision > segment.WorldAdoptionRevision && receipt.Transfer.Revision > segment.WorldTransferRevision &&
			receipt.Transfer.Core.Revision > segment.CoreTransferRevision &&
			sameFlightpathSource(old.TransportScope, segment.SourceScope) && old.TransportScope.MotionRevision >= segment.SourceScope.MotionRevision
	}
	return receipt.FromVisitRevision == a.VisitId && sameFlightpathVisit(old.TransportScope, a.AcceptanceScope) &&
		old.TransportScope.SplineSourceRevision > a.AcceptanceScope.MotionRevision
}

func (s *Service) flightpathSegmentAssociatedLocked() bool {
	f := &s.flightpathActivation
	a, receipt := f.operation, f.transferAdoption
	if a == nil || a.CurrentSegment == nil || f.transferBooking != a.Revision || !s.flightpathTransferReceiptFactsLocked(receipt) {
		return false
	}
	segment := a.CurrentSegment
	return segment.ParentLeaseRequired && segment.VisitId == receipt.ToVisitRevision &&
		flightpathScopeBound(segment.SourceScope, a.SelfGuid, segment.SourceScope.GetMapId()) &&
		segment.WorldAdoptionRevision == receipt.Revision && segment.WorldTransferRevision == receipt.Transfer.Revision &&
		segment.CoreTransferRevision == receipt.Transfer.Core.Revision && proto.Equal(segment.SourceScope, receipt.Transfer.CurrentScope)
}

func (s *Service) flightpathSegmentCurrentLocked() bool {
	a := s.flightpathActivation.operation
	if a == nil || !s.encounterVisit.bound {
		return false
	}
	segment := a.CurrentSegment
	if segment == nil {
		return a.VisitId == s.encounterVisit.revision && flightpathScopeBound(a.AcceptanceScope, s.selfGUID, s.encounterVisit.mapID)
	}
	return s.flightpathSegmentAssociatedLocked() && segment.VisitId == s.encounterVisit.revision &&
		flightpathScopeBound(segment.SourceScope, s.selfGUID, s.encounterVisit.mapID)
}

// After adoption, use the exact new source instead of comparing revision
// counters from different Core/World visits to the original acceptance floor.
func (s *Service) flightpathBookingSourceLocked(scope *modv1.TransportScope) bool {
	a := s.flightpathActivation.operation
	if a == nil || !s.flightpathSegmentCurrentLocked() {
		return false
	}
	if segment := a.CurrentSegment; segment != nil {
		return sameFlightpathSource(scope, segment.SourceScope) && scope.MotionRevision >= segment.SourceScope.MotionRevision
	}
	return sameFlightpathVisit(scope, a.AcceptanceScope) && scope.SplineSourceRevision > a.AcceptanceScope.MotionRevision
}

func flightpathTransferRequestMatchesReceipt(r *actionsv1.AdoptFlightpathTransferRequest, receipt *worldv1.SplineTransferAdoption) bool {
	if r == nil || receipt == nil || receipt.Transfer == nil || receipt.Transfer.Core == nil || r.ExpectedCompletionRevision == math.MaxUint64 {
		return false
	}
	t := receipt.Transfer
	return receipt.Revision == r.ExpectedCompletionRevision+1 && receipt.SelfGuid == r.SelfGuid && receipt.ToVisitRevision == r.VisitId &&
		t.Revision == r.WorldTransferRevision && t.Core.Revision == r.CoreTransferRevision &&
		t.CurrentAuthorityRevision == r.AuthorityRevision && t.CurrentMovementRevision == r.MovementRevision &&
		t.CurrentSplineRevision == r.SplineRevision && t.CurrentUnitFlagsRevision == r.UnitFlagsRevision &&
		proto.Equal(t.CurrentScope, r.TransportScope)
}

func (s *Service) flightpathTransferCurrentProofLocked(r *actionsv1.AdoptFlightpathTransferRequest, w *worldv1.SnapshotResponse) bool {
	if w == nil || !s.matchesCurrentEncounterVisitLocked(w) || w.VisitRevision != r.VisitId || w.WorldTransfer == nil ||
		!w.WorldTransfer.Known || !w.WorldTransfer.PredecessorQualified || !w.WorldTransfer.ContinuationKnown || w.WorldTransfer.Revision != r.WorldTransferRevision ||
		w.WorldTransfer.Core.GetRevision() != r.CoreTransferRevision || w.SelfMovementFacts == nil || w.SelfMotionAuthority == nil {
		return false
	}
	a, raw := w.SelfMotionAuthority, w.SelfMovementFacts
	return a.Known && a.SelfGuid == r.SelfGuid && a.VisitRevision == r.VisitId && a.UnitFlagsKnown && a.UnitFlags&0x100000 != 0 &&
		w.WorldTransfer.SelfGuid == r.SelfGuid && w.WorldTransfer.ToVisitRevision == r.VisitId &&
		w.WorldTransfer.CurrentAuthorityRevision == a.Revision && w.WorldTransfer.CurrentMovementRevision == raw.Revision &&
		w.WorldTransfer.CurrentSplineRevision == a.SplineRevision && w.WorldTransfer.CurrentUnitFlagsRevision == a.UnitFlagsRevision &&
		w.WorldTransfer.CurrentUnitFlagsKnown && w.WorldTransfer.CurrentUnitFlags == a.UnitFlags &&
		w.WorldTransfer.CurrentUnitFlagsMotionRevision == a.UnitFlagsMotionRevision && proto.Equal(w.WorldTransfer.CurrentScope, a.TransportScope) &&
		a.Revision >= r.AuthorityRevision && a.MovementRevision >= r.MovementRevision && a.SplineRevision == r.SplineRevision &&
		a.UnitFlagsRevision >= r.UnitFlagsRevision && sameFlightpathSource(a.TransportScope, r.TransportScope) &&
		a.TransportScope.MotionRevision >= r.TransportScope.MotionRevision &&
		raw.Known && raw.SelfGuid == r.SelfGuid && raw.VisitRevision == r.VisitId && raw.MapKnown && raw.MapId == w.Self.MapId && raw.Revision == a.MovementRevision &&
		raw.Revision >= r.MovementRevision && raw.Info != nil && len(raw.WireImage) != 0 &&
		// A full new-visit CREATE may precede the standalone flying header.
		// Preserve that raw pre-header scope instead of relabeling its source.
		sameFlightpathVisit(raw.TransportScope, r.TransportScope) &&
		raw.TransportScope.MotionRevision > w.WorldTransfer.Core.WorldportAckMotionRevision &&
		raw.TransportScope.MotionRevision <= a.TransportScope.MotionRevision &&
		proto.Equal(raw.TransportScope.WorldTransfer, w.WorldTransfer.Core)
}

func (s *Service) consumeFlightpathTransferLocked(r *actionsv1.AdoptFlightpathTransferRequest, op *actionsv1.FlightpathActivationOperation, baseline proto.Message, w *worldv1.SnapshotResponse) bool {
	f := &s.flightpathActivation
	c := w.GetSplineCompletion()
	if c == nil || !c.TransferAdoptionCurrent || c.Operation != nil || c.Revision != c.TransferAdoption.GetRevision() ||
		!flightpathTransferRequestMatchesReceipt(r, c.TransferAdoption) || !s.flightpathTransferBookingMatchesLocked(c.TransferAdoption) ||
		!s.flightpathTransferCurrentProofLocked(r, w) ||
		f.operation != op || !proto.Equal(op, baseline) || !s.flightpathAcceptedOriginalLocked(r.ActivationRevision) ||
		r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID || !s.encounterVisit.bound || r.VisitId != s.encounterVisit.revision {
		return false
	}
	f.transferAdoption = proto.Clone(c.TransferAdoption).(*worldv1.SplineTransferAdoption)
	f.transferBooking = op.Revision
	op.CurrentSegment = &actionsv1.FlightpathCurrentSegment{VisitId: r.VisitId,
		SourceScope:           cloneFlightpathScope(c.TransferAdoption.Transfer.CurrentScope),
		WorldAdoptionRevision: c.TransferAdoption.Revision, WorldTransferRevision: r.WorldTransferRevision,
		CoreTransferRevision: r.CoreTransferRevision, ParentLeaseRequired: true}
	f.transferRequest = r
	if f.transferAttempt == nil {
		f.transferAttempt = &actionsv1.FlightpathTransferAttempt{Request: proto.Clone(r).(*actionsv1.AdoptFlightpathTransferRequest), WorldInvoked: f.transferInvoked}
	}
	f.transferAttempt.Phase, f.transferAttempt.Error = "adopted", ""
	f.transferResult = s.flightpathObservationLocked()
	f.archiveTransferResult()
	return true
}

// Observation may finish only a previously attempted explicit handoff. It
// never invokes World adoption or acquires custody; a quiet lost reply can be
// reconciled without replaying the Lua mutation reservation.
func (s *Service) reconcileFlightpathTransferLocked(ctx context.Context) {
	f := &s.flightpathActivation
	if !f.transferInvoked || f.transferRequest == nil || f.transferResult != nil || f.transferActivation == nil ||
		!s.flightpathAcceptedOriginalLocked(f.transferRequest.ActivationRevision) ||
		!proto.Equal(f.operation, f.transferActivation) || ctx.Err() != nil {
		return
	}
	w, err := s.readWorldSnapshot(ctx)
	if err == nil && ctx.Err() == nil {
		s.consumeFlightpathTransferLocked(f.transferRequest, f.operation, f.transferActivation, w)
	}
}

// AdoptFlightpathTransfer changes only this accepted booking's current segment.
// World independently supersedes its old physical report; it never receives or
// manufactures acceptance. A lost World reply is reconciled by reads, not by
// another mutation or a new activation.
func (s *Service) AdoptFlightpathTransfer(ctx context.Context, request *actionsv1.AdoptFlightpathTransferRequest) (*actionsv1.FlightpathObservationResponse, error) {
	if request == nil || request.SessionId == "" || len(request.SessionId) > 128 || request.SelfGuid == 0 || request.VisitId == 0 ||
		request.ActivationRevision == 0 || request.WorldTransferRevision == 0 || request.CoreTransferRevision == 0 ||
		request.AuthorityRevision == 0 || request.MovementRevision == 0 || request.SplineRevision == 0 || request.UnitFlagsRevision == 0 ||
		request.ExpectedCompletionRevision == math.MaxUint64 || request.TransportScope == nil {
		return nil, status.Error(codes.InvalidArgument, "exact accepted booking and adjacent current World transfer evidence required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	r := proto.Clone(request).(*actionsv1.AdoptFlightpathTransferRequest)
	s.mu.Lock()
	defer s.mu.Unlock()
	f := &s.flightpathActivation
	for _, old := range f.transferHistory {
		if proto.Equal(old.request, r) {
			return proto.Clone(old.result).(*actionsv1.FlightpathObservationResponse), nil
		}
	}
	if f.transferRequest != nil {
		if !proto.Equal(f.transferRequest, r) {
			if f.transferResult == nil || f.transferAttempt.GetPhase() != "not_invoked" && f.transferAttempt.GetPhase() != "adopted" {
				return nil, status.Error(codes.Aborted, "another flightpath transfer request is retained")
			}
			if f.transferAttempt.GetPhase() == "adopted" && r.VisitId <= f.operation.GetCurrentSegment().GetVisitId() {
				return nil, status.Error(codes.Aborted, "request does not identify a later adjacent handoff")
			}
		} else if f.transferResult != nil {
			return proto.Clone(f.transferResult).(*actionsv1.FlightpathObservationResponse), nil
		}
	}
	if !s.flightpathAcceptedOriginalLocked(r.ActivationRevision) || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID ||
		!s.encounterVisit.bound || r.VisitId != s.encounterVisit.revision {
		return nil, status.Error(codes.FailedPrecondition, "original known accepted booking and current body visit required")
	}
	from := f.operation.VisitId
	if segment := f.operation.CurrentSegment; segment != nil {
		if !s.flightpathSegmentAssociatedLocked() {
			return nil, status.Error(codes.FailedPrecondition, "current booking segment association is unavailable")
		}
		from = segment.VisitId
	}
	if from == math.MaxUint64 || r.VisitId != from+1 {
		return nil, status.Error(codes.FailedPrecondition, "exact next adjacent booking visit required")
	}
	if f.transferRequest != nil && !proto.Equal(f.transferRequest, r) {
		// A settled attempt is historical. Preserve its exact result while the
		// current binding remains the predecessor of the new explicit edge.
		f.archiveTransferResult()
		f.transferRequest, f.transferResult, f.transferAttempt, f.transferActivation = nil, nil, nil, nil
		f.transferInvoked = false
	}
	notInvoked := func(reason string) (*actionsv1.FlightpathObservationResponse, error) {
		f.transferRequest = r
		f.transferAttempt = &actionsv1.FlightpathTransferAttempt{Request: proto.Clone(r).(*actionsv1.AdoptFlightpathTransferRequest),
			Phase: "not_invoked", Error: boundedPolicyDiagnostic(reason, 240)}
		f.transferResult = s.flightpathObservationLocked()
		f.archiveTransferResult()
		return proto.Clone(f.transferResult).(*actionsv1.FlightpathObservationResponse), nil
	}
	op := f.operation
	baseline := proto.Clone(op)
	w, err := s.readWorldSnapshot(ctx)
	if err != nil {
		if !f.transferInvoked {
			return notInvoked("current World transfer observations unavailable")
		}
		return nil, status.Error(codes.FailedPrecondition, "current World transfer observations unavailable")
	}
	if !s.flightpathTransferCurrentProofLocked(r, w) {
		if !f.transferInvoked {
			return notInvoked("current supported World transfer evidence changed")
		}
		return nil, status.Error(codes.FailedPrecondition, "current supported World transfer evidence changed")
	}
	c := w.SplineCompletion
	if c == nil {
		if !f.transferInvoked {
			return notInvoked("World completion transfer slot unavailable")
		}
		return nil, status.Error(codes.FailedPrecondition, "World completion transfer slot unavailable")
	}
	if !flightpathTransferRequestMatchesReceipt(r, c.TransferAdoption) {
		if f.transferInvoked {
			return nil, status.Error(codes.FailedPrecondition, "World transfer adoption outcome remains unconfirmed; no retry")
		}
		if c.Revision != r.ExpectedCompletionRevision || c.Operation == nil ||
			w.SelfMotionAuthority.Revision != r.AuthorityRevision || w.SelfMovementFacts.Revision != r.MovementRevision ||
			w.SelfMotionAuthority.UnitFlagsRevision != r.UnitFlagsRevision || !proto.Equal(w.SelfMotionAuthority.TransportScope, r.TransportScope) {
			return notInvoked("World transfer CAS or exact initial evidence changed")
		}
		candidate := &worldv1.SplineTransferAdoption{Revision: r.ExpectedCompletionRevision + 1, SelfGuid: r.SelfGuid,
			FromVisitRevision: w.WorldTransfer.FromVisitRevision, ToVisitRevision: w.WorldTransfer.ToVisitRevision, Transfer: w.WorldTransfer}
		if !s.flightpathTransferBookingMatchesLocked(candidate) {
			return notInvoked("World predecessor does not follow original accepted booking")
		}
		if err := ctx.Err(); err != nil {
			return notInvoked("request canceled before World transfer adoption")
		}
		if f.operation != op || !proto.Equal(op, baseline) || !s.flightpathAcceptedOriginalLocked(r.ActivationRevision) {
			return notInvoked("flightpath booking changed before World adoption")
		}
		worldRequest := &worldv1.AdoptSplineTransferRequest{SelfGuid: r.SelfGuid, VisitRevision: r.VisitId,
			ExpectedRevision: r.ExpectedCompletionRevision, TransferRevision: r.WorldTransferRevision, CoreTransferRevision: r.CoreTransferRevision,
			AuthorityRevision: r.AuthorityRevision, MovementRevision: r.MovementRevision, SplineRevision: r.SplineRevision,
			UnitFlagsRevision: r.UnitFlagsRevision, TransportScope: cloneFlightpathScope(r.TransportScope)}
		packed, packErr := anypb.New(worldRequest)
		if packErr != nil {
			return notInvoked("World transfer request unavailable")
		}
		f.transferRequest, f.transferInvoked = r, true
		f.transferActivation = proto.Clone(op).(*actionsv1.FlightpathActivationOperation)
		f.transferAttempt = &actionsv1.FlightpathTransferAttempt{Request: proto.Clone(r).(*actionsv1.AdoptFlightpathTransferRequest),
			Phase: "world_unconfirmed", WorldInvoked: true, Error: "World transfer adoption outcome is unconfirmed"}
		// World is a declared forward dependency and this RPC has no Actions
		// callback. Holding Actions.mu cannot create a reverse lock cycle.
		_, _ = s.session.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: "buddy-world", Method: "adopt_spline_transfer", Request: packed})
		// A snapshot reconciles both a returned receipt and a lost unary reply.
		// There is deliberately no second World mutation on any error path.
		w, err = s.readWorldSnapshot(ctx)
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, "World transfer adoption outcome remains unconfirmed")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if !s.consumeFlightpathTransferLocked(r, op, baseline, w) {
		return nil, status.Error(codes.FailedPrecondition, "exact current World adoption receipt and original booking required")
	}
	return proto.Clone(f.transferResult).(*actionsv1.FlightpathObservationResponse), nil
}
