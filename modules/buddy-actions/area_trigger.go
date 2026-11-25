package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"strings"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/areatriggerreference"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type areaTriggerRecord struct {
	request   *actionsv1.NotifyAreaTriggerRequest
	operation *actionsv1.AreaTriggerOperation
}

// Only one unresolved physical attempt can own this slot. Retained request
// identities are never evicted or rebound; the observation exposes latest only.
type areaTriggerTracker struct {
	revision  uint64
	operation *actionsv1.AreaTriggerOperation
	current   *areaTriggerRecord
	requests  map[string]*areaTriggerRecord
}

func areaTriggerReference(id uint32) (areatriggerreference.Trigger, bool) {
	for _, row := range areatriggerreference.All() {
		if row.ID == id {
			return row, true
		}
	}
	return areatriggerreference.Trigger{}, false
}

// These detached reference volumes make no route, live admission or owner
// location claim. Destination filtering is exact; map zero is a valid map.
func (s *Service) AreaTriggerReference(ctx context.Context, r *actionsv1.AreaTriggerReferenceRequest) (*actionsv1.AreaTriggerReferences, error) {
	if r.GetSessionId() == "" || len(r.SessionId) > 128 || r.SelfGuid == 0 || r.VisitId == 0 {
		return nil, status.Error(codes.InvalidArgument, "exact current session body and visit required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := func() bool {
		return !s.closed && r.SessionId == s.sessionID && r.SelfGuid == s.selfGUID && s.encounterVisit.bound && r.VisitId == s.encounterVisit.revision
	}
	if !current() {
		return nil, status.Error(codes.FailedPrecondition, "current connected body and bound visit required")
	}
	w, err := s.readWorldSnapshot(ctx)
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if err != nil || !current() || !s.matchesCurrentEncounterVisitLocked(w) || w.VisitRevision != r.VisitId {
		return nil, status.Error(codes.FailedPrecondition, "current matching World map and visit unavailable")
	}
	out := &actionsv1.AreaTriggerReferences{SessionId: r.SessionId, SelfGuid: r.SelfGuid, VisitId: r.VisitId, SourceCommit: areatriggerreference.Commit}
	for _, row := range areatriggerreference.All() {
		if row.SourceMap == w.Self.MapId && row.DestinationMap == r.DestinationMapId {
			out.Volumes = append(out.Volumes, &actionsv1.AreaTriggerVolume{TriggerId: row.ID, SourceMapId: row.SourceMap, DestinationMapId: row.DestinationMap,
				X: row.X, Y: row.Y, Z: row.Z, Radius: row.Radius, Length: row.Length, Width: row.Width, Height: row.Height, Orientation: row.Orientation, ReferenceOnly: true})
		}
	}
	return out, nil
}

func (s *Service) areaTriggerObservationLocked() *actionsv1.AreaTriggerOperation {
	if s.areaTrigger.operation == nil {
		return &actionsv1.AreaTriggerOperation{Revision: s.areaTrigger.revision, SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: s.encounterVisit.revision, Phase: "idle"}
	}
	return proto.Clone(s.areaTrigger.operation).(*actionsv1.AreaTriggerOperation)
}

func (s *Service) AreaTriggerObservation(ctx context.Context, _ *emptypb.Empty) (*actionsv1.AreaTriggerOperation, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	op := s.areaTrigger.operation
	if op.GetPending() && (op.TransportWritten || op.TransportUnknown) {
		if w, err := s.readWorldSnapshot(ctx); err == nil && ctx.Err() == nil {
			s.observeAreaTriggerDestinationLocked(op, w)
		}
	}
	return s.areaTriggerObservationLocked(), nil
}

func (s *Service) areaTriggerIdleCurrentLocked(r *actionsv1.NotifyAreaTriggerRequest, now time.Time) bool {
	b := s.idleInteraction
	return !s.followPortal.notified && !s.flightpath.operation.GetPending() && !s.flightpathActivation.operation.GetPending() &&
		s.ownsExploration(r.Token) && s.interactionUntil.Sub(now) >= time.Second &&
		b.sessionID == r.SessionId && b.selfGUID == r.SelfGuid && b.visit == r.VisitId && b.token == r.Token &&
		s.idleInteractionGuardLocked(&actionsv1.IdleInteractionRequest{SessionId: r.SessionId, SelfGuid: r.SelfGuid, VisitId: r.VisitId, Owner: "exploration", Token: r.Token}, now) == ""
}

func (s *Service) NotifyAreaTrigger(ctx context.Context, request *actionsv1.NotifyAreaTriggerRequest) (*actionsv1.AreaTriggerOperation, error) {
	ctx = modcb.ForwardPolicySpell(ctx)
	if request.GetSessionId() == "" || len(request.SessionId) > 128 || request.SelfGuid == 0 || request.VisitId == 0 ||
		request.Token == "" || len(request.Token) > 128 || request.TriggerId == 0 || request.ExpectedPositionRevision == 0 ||
		request.ClientRequestId == "" || len(request.ClientRequestId) > 128 || strings.TrimSpace(request.ClientRequestId) != request.ClientRequestId {
		return nil, status.Error(codes.InvalidArgument, "exact body visit idle token trigger controller revision and client request identity required")
	}
	volume, found := areaTriggerReference(request.TriggerId)
	if !found {
		return nil, status.Error(codes.InvalidArgument, "qualified cross-map trigger reference required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	r := proto.Clone(request).(*actionsv1.NotifyAreaTriggerRequest)
	s.mu.Lock()
	defer s.mu.Unlock()
	f := &s.areaTrigger
	if old := f.requests[r.ClientRequestId]; old != nil {
		if !proto.Equal(old.request, r) {
			return nil, status.Error(codes.AlreadyExists, "client request identity already binds a different exact request")
		}
		return proto.Clone(old.operation).(*actionsv1.AreaTriggerOperation), nil
	}
	if r.ExpectedRevision != f.revision {
		return nil, status.Error(codes.Aborted, "area trigger operation revision changed")
	}
	if f.operation.GetPending() || s.followPortal.notified {
		return nil, status.Error(codes.FailedPrecondition, "a previous normal entry attempt remains unresolved")
	}
	if f.revision == math.MaxUint64 {
		return nil, status.Error(codes.ResourceExhausted, "area trigger operation revision exhausted")
	}
	if !s.areaTriggerIdleCurrentLocked(r, s.now()) {
		return nil, status.Error(codes.FailedPrecondition, "current bound idle exploration custody required")
	}
	// Reserve before every dependency, STOP and trigger. A lost unary reply
	// can inspect a retained known trigger refusal without resending either.
	f.revision++
	op := &actionsv1.AreaTriggerOperation{Revision: f.revision, ClientRequestId: r.ClientRequestId, SessionId: r.SessionId, SelfGuid: r.SelfGuid,
		VisitId: r.VisitId, TriggerId: r.TriggerId, ExpectedPositionRevision: r.ExpectedPositionRevision, DestinationMapId: volume.DestinationMap, Phase: "reserved", Pending: true}
	record := &areaTriggerRecord{request: r, operation: op}
	if f.requests == nil {
		f.requests = make(map[string]*areaTriggerRecord)
	}
	f.requests[r.ClientRequestId], f.current, f.operation = record, record, op
	result := func() *actionsv1.AreaTriggerOperation { return proto.Clone(op).(*actionsv1.AreaTriggerOperation) }
	refuse := func(reason string) (*actionsv1.AreaTriggerOperation, error) {
		op.Pending, op.NotDispatched, op.Phase, op.Error = false, true, "not_dispatched", boundedPolicyDiagnostic(reason, 240)
		return result(), nil
	}
	w, err := s.snapshot(ctx)
	if err != nil {
		return refuse("area_trigger_world_unavailable")
	}
	if safeQuestCharacter(w, r.SelfGuid) != "" || !s.refreshPositionLocked(w) || s.posRevision != r.ExpectedPositionRevision {
		return refuse("area_trigger_actual_body_or_controller_revision_changed")
	}
	p := s.areaTriggerProposalLocked(w)
	current := areaTriggerAdmission(func(w *worldv1.SnapshotResponse, revision uint64, now time.Time) bool {
		return f.current == record && f.operation == op && f.revision == op.Revision && record.request == r &&
			p.clientRevision == r.ExpectedPositionRevision && p.visit == r.VisitId && s.areaTriggerIdleCurrentLocked(r, now) &&
			s.areaTriggerPoseCurrentLocked(ctx, p, w, revision, now) && volume.Contains(p.mapID, float64(p.pose.x), float64(p.pose.y), float64(p.pose.z), .03)
	})
	report, err := s.prepareAreaTriggerLocked(ctx, w, p, current, nil)
	if err != nil {
		return refuse("area_trigger_not_sent; " + err.Error())
	}
	// No dependency or clock follows final admission. The normal transport
	// owner rechecks this exact report scope before the ID-only packet write.
	op.SourceScope = clonePortalScope(report.TransportScope)
	op.Phase = "requested"
	err = s.sendPacketLocked(ctx, opcode.CMSGAreatrigger, binary.LittleEndian.AppendUint32(nil, r.TriggerId), report.TransportScope)
	if err == nil {
		op.TransportWritten, op.Phase = true, "written_waiting_destination"
	} else if modcb.PacketWasNotDispatched(err) {
		return refuse("area_trigger_transport_not_dispatched: " + err.Error())
	} else {
		op.TransportUnknown, op.Phase, op.Error = true, "transport_unknown", boundedPolicyDiagnostic(err.Error(), 240)
	}
	return result(), nil
}

// This is observed adjacent map entry, not proof that this particular trigger
// caused it, server admission/party-copy agreement, or a successful regroup.
func (s *Service) observeAreaTriggerDestinationLocked(op *actionsv1.AreaTriggerOperation, w *worldv1.SnapshotResponse) {
	if op != s.areaTrigger.operation {
		return
	}
	s.observeScopedTriggerDestinationLocked(op, w)
}

// Shared factual transfer reconciliation; callers retain their own operation custody.
func (s *Service) observeScopedTriggerDestinationLocked(op *actionsv1.AreaTriggerOperation, w *worldv1.SnapshotResponse) {
	if op == nil || !op.Pending || !op.TransportWritten && !op.TransportUnknown ||
		s.closed || op.SessionId != s.sessionID || op.SelfGuid != s.selfGUID || op.VisitId == math.MaxUint64 ||
		w.GetVisitRevision() != op.VisitId+1 || !s.matchesCurrentEncounterVisitLocked(w) {
		return
	}
	p, source, current := w.GetSelf(), op.SourceScope, portalObservedWorldScope(w, op.SelfGuid)
	if !p.GetPositionKnown() || p.GetPositionInferred() || !finite(playerPosition(p)) || p.GetPositionRevision() == 0 ||
		p.GetMapId() != op.DestinationMapId || source == nil || current == nil || source.VisitRevision == math.MaxUint64 {
		return
	}
	edge := current.GetWorldTransfer()
	if edge == nil || !edge.Known || !edge.TargetMapKnown || !edge.WorldportAckWritten || edge.WorldportAckUnknown ||
		edge.ConnectionId != source.ConnectionId || edge.SelfGuid != op.SelfGuid || edge.FromVisitRevision != source.VisitRevision ||
		edge.FromMapId != source.MapId || edge.FromMotionRevision < source.MotionRevision || edge.TargetMapId != op.DestinationMapId ||
		edge.ToMapId != op.DestinationMapId || edge.ToVisitRevision != source.VisitRevision+1 ||
		edge.PendingMotionRevision <= edge.FromMotionRevision || edge.EntryMotionRevision <= edge.PendingMotionRevision ||
		edge.WorldportAckMotionRevision <= edge.EntryMotionRevision || current.ConnectionId != source.ConnectionId ||
		current.VisitRevision != edge.ToVisitRevision || current.MapId != op.DestinationMapId || current.MotionRevision < edge.WorldportAckMotionRevision {
		return
	}
	op.Pending, op.DestinationObserved, op.ObservedVisitId, op.Phase = false, true, w.VisitRevision, "destination_observed"
}
