package buddyactions

import (
	"context"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func cloneFlightpathScope(scope *modv1.TransportScope) *modv1.TransportScope {
	if scope == nil {
		return nil
	}
	return proto.Clone(scope).(*modv1.TransportScope)
}

func flightpathScopeBound(scope *modv1.TransportScope, self uint64, mapID uint32) bool {
	return scope != nil && scope.ConnectionId != "" && scope.SelfGuid == self && self != 0 && scope.VisitBound &&
		scope.VisitRevision != 0 && scope.MapId == mapID && scope.MotionRevision != 0
}

func sameFlightpathVisit(a, b *modv1.TransportScope) bool {
	return a != nil && b != nil && flightpathScopeBound(a, a.SelfGuid, a.MapId) && flightpathScopeBound(b, a.SelfGuid, a.MapId) &&
		a.ConnectionId == b.ConnectionId && a.VisitRevision == b.VisitRevision
}

func sameFlightpathSource(a, b *modv1.TransportScope) bool {
	return sameFlightpathVisit(a, b) && a.SplineActive && b.SplineActive && a.SplineIdKnown && b.SplineIdKnown && a.SplineRevision != 0 &&
		a.SplineRevision == b.SplineRevision && a.SplineId == b.SplineId && a.SplineSourceRevision != 0 &&
		a.SplineSourceRevision == b.SplineSourceRevision && a.SplineSourceOpcode == b.SplineSourceOpcode &&
		(a.SplineSourceOpcode == opcode.SMSGMonsterMove || a.SplineSourceOpcode == opcode.SMSGMonsterMoveTransport)
}

func (s *Service) flightpathAcceptedCurrentLocked(revision uint64) bool {
	return s.flightpathAcceptedOriginalLocked(revision) && s.flightpathSegmentCurrentLocked()
}

// This checks World observations only. RetirementCurrent is a separate current
// sticky-chain proof; historical Retired or ACK-authority Known alone is not it.
// No distance, destination selection, timer, quote or movement is inferred here.
func (s *Service) flightpathRetiredWorldReasonLocked(activation, completion uint64, w *worldv1.SnapshotResponse) string {
	if !s.flightpathAcceptedCurrentLocked(activation) || w == nil || w.Self == nil || !s.matchesCurrentEncounterVisitLocked(w) {
		return "flightpath_arrival_activation_or_body_changed"
	}
	c, raw, authority := w.SplineCompletion, w.SelfMovementFacts, w.SelfMotionAuthority
	if c == nil || !c.RetirementCurrent || c.Revision != completion || completion == 0 || c.SelfGuid != s.selfGUID ||
		c.VisitRevision != w.VisitRevision || c.Operation == nil || !c.Operation.Retired || c.Operation.Pending ||
		c.Operation.RetirementRevision != completion || c.Operation.Revision == 0 || c.Operation.Revision >= completion ||
		(!c.Operation.TransportWritten && !c.Operation.TransportUnknown) {
		return "flightpath_arrival_current_retired_completion_required"
	}
	op := c.Operation
	if op.SelfGuid != s.selfGUID || op.VisitRevision != w.VisitRevision || !op.SplineIdKnown ||
		op.RetiredMovementRevision <= op.MovementRevision || op.RetiredUnitFlagsRevision == 0 ||
		op.TransportScope == nil || op.TransportScope.SplineId != op.SplineId ||
		op.TransportScope.SplineSourceRevision > op.TransportScope.MotionRevision ||
		!s.flightpathBookingSourceLocked(op.TransportScope) ||
		!sameFlightpathSource(op.TransportScope, op.RetirementScope) ||
		op.RetirementScope.MotionRevision <= op.TransportScope.MotionRevision {
		return "flightpath_arrival_completion_does_not_follow_this_acceptance"
	}
	if authority == nil || authority.SelfGuid != s.selfGUID || authority.VisitRevision != w.VisitRevision || authority.Revision == 0 ||
		!sameFlightpathSource(authority.TransportScope, op.TransportScope) ||
		authority.TransportScope.MotionRevision < op.RetirementScope.MotionRevision || !authority.UnitFlagsKnown ||
		authority.UnitFlags&0x100000 != 0 || authority.UnitFlagsRevision < op.RetiredUnitFlagsRevision ||
		authority.UnitFlagsRevision == 0 || authority.UnitFlagsMotionRevision <= op.TransportScope.MotionRevision ||
		authority.UnitFlagsMotionRevision > authority.TransportScope.MotionRevision {
		return "flightpath_arrival_actual_current_taxi_clear_required"
	}
	if raw == nil || !raw.Known || raw.SelfGuid != s.selfGUID || raw.VisitRevision != w.VisitRevision || !raw.MapKnown ||
		raw.MapId != w.Self.MapId || raw.Info == nil || len(raw.WireImage) == 0 || raw.Revision <= op.MovementRevision ||
		raw.Revision < op.RetiredMovementRevision || !sameFlightpathSource(raw.TransportScope, op.TransportScope) ||
		raw.TransportScope.MotionRevision <= op.TransportScope.MotionRevision ||
		raw.TransportScope.MotionRevision > authority.TransportScope.MotionRevision || !w.Self.PositionKnown || w.Self.PositionInferred ||
		w.Self.X != raw.Info.X || w.Self.Y != raw.Info.Y || w.Self.Z != raw.Info.Z || w.Self.Orientation != raw.Info.Orientation ||
		!w.Self.UnitFlagsKnown || w.Self.UnitFlags != authority.UnitFlags || !w.Self.MovementFlagsKnown ||
		w.Self.MovementFlags != raw.Info.Flags || w.Self.ExtraMovementFlags != raw.Info.ExtraFlags || w.Self.MovementTime != raw.Info.Time {
		return "flightpath_arrival_new_actual_matching_movement_required"
	}
	return ""
}

func (s *Service) flightpathArrivalReplyScopeLocked(op *actionsv1.FlightpathOperation, scope *modv1.TransportScope) bool {
	if op == nil || op.Mode != "arrival" || !s.flightpathAcceptedCurrentLocked(op.ActivationRevision) ||
		op.CompletionRevision == 0 || (!op.TransportWritten && !op.TransportUnknown) {
		return false
	}
	return sameFlightpathSource(scope, op.TransportScope) && scope.MotionRevision >= op.TransportScope.MotionRevision &&
		s.flightpathBookingSourceLocked(scope)
}

// SettleFlightpath retires the exact accepted booking only after an actual
// destination-node menu and current independently retired World proof agree.
// It sends no packet and preserves every delivery/reply/history field.
func (s *Service) SettleFlightpath(ctx context.Context, request *actionsv1.SettleFlightpathRequest) (*actionsv1.FlightpathObservationResponse, error) {
	if request == nil || request.SessionId == "" || len(request.SessionId) > 128 || request.SelfGuid == 0 || request.VisitId == 0 ||
		request.ActivationRevision == 0 || request.MenuRevision == 0 || request.CompletionRevision == 0 ||
		request.AuthorityRevision == 0 || request.MovementRevision == 0 || request.UnitFlagsRevision == 0 || request.TransportScope == nil {
		return nil, status.Error(codes.InvalidArgument, "exact booking menu completion body and current World evidence binding required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	r := proto.Clone(request).(*actionsv1.SettleFlightpathRequest)
	s.mu.Lock()
	defer s.mu.Unlock()
	f := &s.flightpathActivation
	if f.settlementRequest != nil && proto.Equal(f.settlementRequest, r) {
		return proto.Clone(f.settlementResult).(*actionsv1.FlightpathObservationResponse), nil
	}
	a, menu, query := f.operation, s.flightpath.operation, s.flightpath.request
	if !s.flightpathAcceptedCurrentLocked(r.ActivationRevision) || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID ||
		r.VisitId != s.encounterVisit.revision || !s.flightpathOperationCurrentLocked(menu) || query == nil || menu.Pending || !menu.MenuKnown ||
		menu.Mode != "arrival" || menu.Phase != "menu_observed" || menu.Revision != r.MenuRevision || s.flightpath.revision != r.MenuRevision ||
		menu.ActivationRevision != r.ActivationRevision || menu.CompletionRevision != r.CompletionRevision ||
		menu.SourceNode != a.DestinationNode || !s.flightpathArrivalReplyScopeLocked(menu, s.flightpath.arrivalReplyScope) {
		return nil, status.Error(codes.FailedPrecondition, "current matching activation-bound destination menu is required")
	}
	baselineActivation := proto.Clone(a)
	baselineMenu := proto.Clone(menu)
	w, err := s.snapshot(ctx, menu.NpcGuid)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "current flightpath settlement World observations unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if reason := safeQuestCharacter(w, r.SelfGuid); reason != "" {
		return nil, status.Error(codes.FailedPrecondition, "current actual ground body is required")
	}
	if reason := s.flightpathRetiredWorldReasonLocked(r.ActivationRevision, r.CompletionRevision, w); reason != "" {
		return nil, status.Error(codes.FailedPrecondition, reason)
	}
	if w.SelfMotionAuthority.Revision != r.AuthorityRevision || w.SelfMovementFacts.Revision != r.MovementRevision ||
		w.SelfMotionAuthority.UnitFlagsRevision != r.UnitFlagsRevision || !proto.Equal(w.SelfMotionAuthority.TransportScope, r.TransportScope) ||
		!sameFlightpathSource(r.TransportScope, menu.TransportScope) || r.TransportScope.MotionRevision < menu.TransportScope.MotionRevision ||
		!sameFlightpathSource(r.TransportScope, s.flightpath.arrivalReplyScope) ||
		r.TransportScope.MotionRevision < s.flightpath.arrivalReplyScope.MotionRevision {
		return nil, status.Error(codes.FailedPrecondition, "flightpath settlement evidence revision or scope changed")
	}
	if f.operation != a || s.flightpath.operation != menu || s.flightpath.request != query ||
		!proto.Equal(a, baselineActivation) || !proto.Equal(menu, baselineMenu) ||
		!s.flightpathArrivalReplyScopeLocked(menu, s.flightpath.arrivalReplyScope) {
		return nil, status.Error(codes.Aborted, "flightpath booking or menu changed during settlement")
	}
	a.Pending, a.Phase, a.ArrivalKnown = false, "observed_arrival", true
	s.flightpath.certifiedArrivalMenuRevision = menu.Revision
	f.settlementRequest = r
	f.settlementResult = s.flightpathObservationLocked()
	return proto.Clone(f.settlementResult).(*actionsv1.FlightpathObservationResponse), nil
}
