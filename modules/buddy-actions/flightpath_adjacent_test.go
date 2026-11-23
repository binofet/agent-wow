package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Coherent synthetic World facts isolate Actions association/history. Real
// packet qualification and completion archives have independent World tests.
func flightpathAdjacentCandidate(t *testing.T, x *flightpathRig, target uint32) *actionsv1.AdoptFlightpathTransferRequest {
	t.Helper()
	w, a := x.f.world, x.s.flightpathActivation.operation
	fromVisit := a.VisitId
	old := cloneFlightpathScope(a.AcceptanceScope)
	if segment := a.CurrentSegment; segment != nil {
		fromVisit, old = segment.VisitId, cloneFlightpathScope(segment.SourceScope)
	} else {
		// A later booking needs its own actual source after its server0 boundary.
		old.SplineSourceRevision = old.MotionRevision + 1
		old.MotionRevision = old.SplineSourceRevision
		old.SplineRevision++
		old.SplineIdKnown, old.SplineActive, old.SplineSourceOpcode = true, true, opcode.SMSGMonsterMove
	}
	report := cloneFlightpathScope(old)
	report.MotionRevision++
	from := cloneFlightpathScope(report)
	from.MotionRevision++
	predecessor := &worldv1.SplineCompletionOperation{Revision: w.SplineCompletion.Revision + 1, SelfGuid: 1, VisitRevision: fromVisit,
		AuthorityRevision: w.SelfMotionAuthority.Revision + 1, MovementRevision: w.SelfMovementFacts.Revision,
		SplineRevision: w.SelfMotionAuthority.SplineRevision, TransportScope: report, SplineIdKnown: true, SplineId: report.SplineId,
		Pending: true, TransportUnknown: true, Phase: "transport_unknown", Error: "retained adjacent report uncertainty"}
	core := &modv1.ObservedWorldTransfer{Revision: w.WorldTransfer.Core.Revision + 3, Known: true, Phase: "ack_written",
		ConnectionId: from.ConnectionId, SelfGuid: 1, FromVisitRevision: from.VisitRevision, FromMapId: from.MapId,
		FromMotionRevision: from.MotionRevision, FromSplineIdKnown: true, FromSplineId: from.SplineId, FromSplineRevision: from.SplineRevision,
		FromSplineSourceRevision: from.SplineSourceRevision, FromSplineSourceOpcode: from.SplineSourceOpcode, FromSplineActive: true,
		PendingMotionRevision: from.MotionRevision + 1, TargetMapKnown: true, TargetMapId: target,
		ToVisitRevision: from.VisitRevision + 1, ToMapId: target, EntryMotionRevision: from.MotionRevision + 2,
		WorldportAckMotionRevision: from.MotionRevision + 3, WorldportAckWritten: true,
		CompletionReportWritten: true, CompletionReportMotionRevision: report.MotionRevision}
	current := &modv1.TransportScope{ConnectionId: from.ConnectionId, SelfGuid: 1, VisitBound: true, VisitRevision: core.ToVisitRevision,
		MapId: target, MotionRevision: core.WorldportAckMotionRevision + 3, SplineIdKnown: true, SplineActive: true,
		SplineRevision: from.SplineRevision + 3, SplineSourceRevision: core.WorldportAckMotionRevision + 2,
		SplineSourceOpcode: opcode.SMSGMonsterMove, WorldTransfer: core}
	flightpathSettlementPacket(t, x, opcode.SMSGTransferPending, binary.LittleEndian.AppendUint32(nil, target), nil)
	flightpathSettlementPacket(t, x, opcode.SMSGNewWorld, settlementPolicyFloats(binary.LittleEndian.AppendUint32(nil, target), 10, 20, 30, .5), current)
	w.VisitRevision, w.Self.MapId, w.Self.UnitFlags = fromVisit+1, target, 0x100000
	w.Self.PositionRevision++
	w.Self.X, w.Self.Y, w.Self.Z, w.Self.Orientation = 10, 20, 30, .5
	w.Self.PositionInferred = false
	info := movementinfo.Info{Time: uint32(w.Self.PositionRevision * 100), X: 10, Y: 20, Z: 30, Orientation: .5}
	w.Self.MovementTime = info.Time
	wire, err := movementinfo.Encode(info)
	if err != nil {
		t.Fatal(err)
	}
	raw := cloneFlightpathScope(current)
	raw.MotionRevision, raw.SplineRevision = core.WorldportAckMotionRevision+1, from.SplineRevision+2
	raw.SplineActive, raw.SplineIdKnown, raw.SplineSourceRevision, raw.SplineSourceOpcode = false, false, 0, 0
	movement, spline, authority, flags := w.SelfMovementFacts.Revision+1, w.SelfMotionAuthority.SplineRevision+1, w.SelfMotionAuthority.Revision+4, w.SelfMotionAuthority.UnitFlagsRevision+1
	w.SelfMovementFacts = &worldv1.OwnMovementFacts{Known: true, SelfGuid: 1, VisitRevision: fromVisit + 1, Revision: movement,
		MapKnown: true, MapId: target, SourceOpcode: opcode.SMSGUpdateObject, TransportScope: raw, WireImage: wire,
		Info: &worldv1.MovementInfoFacts{Time: info.Time, X: info.X, Y: info.Y, Z: info.Z, Orientation: info.Orientation}}
	w.SelfMotionAuthority = &worldv1.OwnMotionAuthority{Known: true, SelfGuid: 1, VisitRevision: fromVisit + 1, Revision: authority,
		MovementRevision: movement, SplineRevision: spline, TransportScope: current, UnitFlagsKnown: true, UnitFlags: 0x100000,
		UnitFlagsRevision: flags, UnitFlagsMotionRevision: raw.MotionRevision}
	w.WorldTransfer = &worldv1.WorldTransferObservation{Revision: w.WorldTransfer.Revision + 3, Known: true, SelfGuid: 1,
		FromVisitRevision: fromVisit, ToVisitRevision: fromVisit + 1, Core: core,
		PredecessorCompletion: proto.Clone(predecessor).(*worldv1.SplineCompletionOperation),
		PredecessorAuthority:  &worldv1.OwnMotionAuthority{Known: true, SelfGuid: 1, VisitRevision: fromVisit, TransportScope: from},
		PredecessorQualified:  true, ContinuationKnown: true, CurrentScope: cloneFlightpathScope(current),
		CurrentAuthorityRevision: authority, CurrentMovementRevision: movement, CurrentSplineRevision: spline,
		CurrentUnitFlagsKnown: true, CurrentUnitFlags: 0x100000, CurrentUnitFlagsRevision: flags, CurrentUnitFlagsMotionRevision: raw.MotionRevision}
	w.SplineCompletion.Revision, w.SplineCompletion.Operation, w.SplineCompletion.VisitRevision = predecessor.Revision, predecessor, fromVisit+1
	w.SplineCompletion.TransferAdoptionCurrent, w.SplineCompletion.RetirementCurrent = false, false
	return &actionsv1.AdoptFlightpathTransferRequest{SessionId: x.s.sessionID, SelfGuid: 1, VisitId: w.VisitRevision,
		ActivationRevision: a.Revision, WorldTransferRevision: w.WorldTransfer.Revision, ExpectedCompletionRevision: predecessor.Revision,
		AuthorityRevision: authority, MovementRevision: movement, SplineRevision: spline, UnitFlagsRevision: flags,
		TransportScope: cloneFlightpathScope(current), CoreTransferRevision: core.Revision}
}

func flightpathAdjacentAdopt(t *testing.T, x *flightpathRig, request *actionsv1.AdoptFlightpathTransferRequest) *actionsv1.FlightpathObservationResponse {
	t.Helper()
	view, err := x.s.AdoptFlightpathTransfer(context.Background(), request)
	if err != nil || view.GetActivation().GetCurrentSegment().GetVisitId() != request.VisitId || view.GetTransferAttempt().GetPhase() != "adopted" {
		t.Fatal("adjacent native handoff was not adopted", view, err)
	}
	return view
}

func TestFlightpathAdjacentTwoEdgesPreserveBookingAndOldExactRPCProof(t *testing.T) {
	x, client := newFlightpathTransferRig(t)
	original := proto.Clone(x.s.flightpathActivation.operation).(*actionsv1.FlightpathActivationOperation)
	request := proto.Clone(x.s.flightpathActivation.request).(*actionsv1.FlightpathActivationRequest)
	menu := proto.Clone(x.s.flightpathActivation.menu).(*actionsv1.FlightpathOperation)
	firstRequest := flightpathTransferRequest(t, x)
	first := flightpathAdjacentAdopt(t, x, firstRequest)
	secondRequest := flightpathAdjacentCandidate(t, x, 0)
	second := flightpathAdjacentAdopt(t, x, secondRequest)
	if client.adoptCalls != 2 || second.Activation.VisitId != 1 || second.Activation.CurrentSegment.VisitId != 3 ||
		second.Activation.CurrentSegment.SourceScope.VisitRevision != 11 || len(x.s.flightpathActivation.transferHistory) != 2 {
		t.Fatal("distinct adjacent edges were conflated with booking/World/Core visit counters", second, client.adoptCalls)
	}
	assertFlightpathTransferOriginal(t, x, original, request, menu)
	reads, calls := x.client.worldReads, client.adoptCalls
	latest := proto.Clone(x.s.flightpathActivation.operation.CurrentSegment)
	for _, old := range []struct {
		request *actionsv1.AdoptFlightpathTransferRequest
		result  *actionsv1.FlightpathObservationResponse
	}{{firstRequest, first}, {secondRequest, second}} {
		replay, err := x.s.AdoptFlightpathTransfer(context.Background(), proto.Clone(old.request).(*actionsv1.AdoptFlightpathTransferRequest))
		if err != nil || !proto.Equal(replay, old.result) || x.client.worldReads != reads || client.adoptCalls != calls ||
			!proto.Equal(latest, x.s.flightpathActivation.operation.CurrentSegment) {
			t.Fatal("historical exact RPC replay mutated latest binding or invoked dependencies", replay, err)
		}
		replay.Activation.CurrentSegment.SourceScope.ConnectionId = "caller mutation"
	}
	if !proto.Equal(latest, x.s.flightpathActivation.operation.CurrentSegment) {
		t.Fatal("history response aliases the current association")
	}
}

func TestFlightpathAdjacentRejectsSkipAndUnlinkedPredecessorsBeforeWorldMutation(t *testing.T) {
	for _, change := range []string{"skipped visit", "wrong World from", "wrong old source", "wrong old Core visit", "reused edge revision"} {
		t.Run(change, func(t *testing.T) {
			x, client := newFlightpathTransferRig(t)
			flightpathAdjacentAdopt(t, x, flightpathTransferRequest(t, x))
			before := proto.Clone(x.s.flightpathActivation.operation.CurrentSegment)
			r := flightpathAdjacentCandidate(t, x, 0)
			switch change {
			case "skipped visit":
				x.s.encounterVisit.revision++
				x.f.world.VisitRevision++
				r.VisitId++
			case "wrong World from":
				x.f.world.WorldTransfer.FromVisitRevision = 1
			case "wrong old source":
				x.f.world.WorldTransfer.PredecessorCompletion.TransportScope.SplineSourceRevision++
			case "wrong old Core visit":
				x.f.world.WorldTransfer.PredecessorCompletion.TransportScope.VisitRevision++
			case "reused edge revision":
				x.f.world.WorldTransfer.Core.Revision = 3
				r.CoreTransferRevision = 3
			}
			view, err := x.s.AdoptFlightpathTransfer(context.Background(), r)
			if err == nil && view.GetTransferAttempt().GetPhase() != "not_invoked" || client.adoptCalls != 1 ||
				!proto.Equal(before, x.s.flightpathActivation.operation.CurrentSegment) || len(x.f.packets) != 0 {
				t.Fatal("skipped/unlinked predecessor reached another World mutation", change, view, err, client.adoptCalls)
			}
		})
	}
}

func TestFlightpathAdjacentUnknownSecondEdgeOnlyReconcilesItsOwnReceipt(t *testing.T) {
	x, client := newFlightpathTransferRig(t)
	firstRequest := flightpathTransferRequest(t, x)
	first := flightpathAdjacentAdopt(t, x, firstRequest)
	secondRequest := flightpathAdjacentCandidate(t, x, 0)
	client.adoptError = errors.New("World state committed but unary reply lost")
	client.afterAdopt = func() { x.f.failSnapshot = true }
	if _, err := x.s.AdoptFlightpathTransfer(context.Background(), secondRequest); err == nil || client.adoptCalls != 2 ||
		x.s.flightpathActivation.operation.CurrentSegment.VisitId != 2 {
		t.Fatal("fixture did not retain uncertain second handoff without promoting its binding", err)
	}
	reads := x.client.worldReads
	if old, err := x.s.AdoptFlightpathTransfer(context.Background(), firstRequest); err != nil || !proto.Equal(old, first) ||
		x.client.worldReads != reads || client.adoptCalls != 2 || x.s.flightpathActivation.operation.CurrentSegment.VisitId != 2 {
		t.Fatal("old completed replay disturbed pending next handoff", old, err)
	}
	conflict := proto.Clone(secondRequest).(*actionsv1.AdoptFlightpathTransferRequest)
	conflict.AuthorityRevision++
	if _, err := x.s.AdoptFlightpathTransfer(context.Background(), conflict); err == nil || client.adoptCalls != 2 || x.client.worldReads != reads {
		t.Fatal("unresolved World invocation was replaced by another request", err)
	}
	x.f.failSnapshot = false
	view, err := x.s.FlightpathObservation(context.Background(), &emptypb.Empty{})
	if err != nil || view.GetActivation().GetCurrentSegment().GetVisitId() != 3 || client.adoptCalls != 2 || len(x.f.packets) != 0 {
		t.Fatal("getter failed to reconcile only the pending next receipt", view, err)
	}
}

func flightpathAdjacentSettle(t *testing.T, x *flightpathRig) {
	t.Helper()
	w, segment := x.f.world, x.s.flightpathActivation.operation.CurrentSegment
	source := segment.SourceScope
	w.Self.UnitFlags, w.Self.MovementFlags, w.Self.ExtraMovementFlags = 0, 0, 0
	w.Self.MovementTime++
	w.SelfMotionAuthority.Known, w.SelfMotionAuthority.UnitFlags = false, 0
	w.SelfMotionAuthority.Revision++
	w.SelfMotionAuthority.MovementRevision++
	w.SelfMotionAuthority.UnitFlagsRevision++
	w.SelfMotionAuthority.TransportScope = cloneFlightpathScope(source)
	w.SelfMotionAuthority.TransportScope.MotionRevision += 3
	w.SelfMotionAuthority.UnitFlagsMotionRevision = w.SelfMotionAuthority.TransportScope.MotionRevision
	w.SelfMovementFacts.Revision, w.SelfMovementFacts.Info.Time = w.SelfMotionAuthority.MovementRevision, w.Self.MovementTime
	wire, err := movementinfo.Encode(movementinfo.Info{Time: w.Self.MovementTime, X: w.Self.X, Y: w.Self.Y, Z: w.Self.Z, Orientation: w.Self.Orientation})
	if err != nil {
		t.Fatal(err)
	}
	w.SelfMovementFacts.WireImage = wire
	w.SelfMovementFacts.TransportScope = cloneFlightpathScope(source)
	w.SelfMovementFacts.TransportScope.MotionRevision += 2
	completion := &worldv1.SplineCompletionOperation{Revision: w.SplineCompletion.Revision + 1, SelfGuid: 1,
		VisitRevision: segment.VisitId, MovementRevision: w.SelfMovementFacts.Revision - 1, SplineIdKnown: true, SplineId: source.SplineId,
		TransportScope: cloneFlightpathScope(source), TransportWritten: true, Phase: "transport_written", Retired: true,
		RetiredMovementRevision: w.SelfMovementFacts.Revision, RetiredUnitFlagsRevision: w.SelfMotionAuthority.UnitFlagsRevision,
		RetirementScope: cloneFlightpathScope(w.SelfMotionAuthority.TransportScope)}
	completion.TransportScope.MotionRevision++
	completion.RetirementRevision = completion.Revision + 1
	w.SplineCompletion.Revision, w.SplineCompletion.Operation = completion.RetirementRevision, completion
	w.SplineCompletion.TransferAdoptionCurrent, w.SplineCompletion.RetirementCurrent = false, true
	x.npc++
	for _, npc := range w.NearbyCreatures {
		npc.Guid, npc.MapId, npc.X, npc.Y, npc.Z = x.npc, w.Self.MapId, w.Self.X+1, w.Self.Y, w.Self.Z
		npc.PositionRevision++
	}
	if _, err := x.s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{SessionId: x.s.sessionID,
		SelfGuid: 1, VisitId: w.VisitRevision, Owner: "exploration", Token: x.s.interactionToken}); err != nil {
		t.Fatal(err)
	}
	lease, err := x.s.AcquireIdleInteraction(context.Background(), &actionsv1.IdleInteractionRequest{SessionId: x.s.sessionID,
		SelfGuid: 1, VisitId: w.VisitRevision, Owner: "exploration", LeaseMs: 15000})
	if err != nil {
		t.Fatal("current adopted ground lease", err)
	}
	x.token = lease.Token
	query := &actionsv1.FlightpathQueryRequest{SessionId: x.s.sessionID, SelfGuid: 1, VisitId: w.VisitRevision,
		Token: x.token, NpcGuid: x.npc, Mode: "arrival", ExpectedRevision: x.s.flightpath.revision,
		ActivationRevision: x.s.flightpathActivation.revision, CompletionRevision: w.SplineCompletion.Revision}
	if view, err := x.s.QueryFlightpath(context.Background(), query); err != nil || view.GetOperation().GetPhase() != "requested" {
		t.Fatal("current adjacent segment arrival query", view, err)
	}
	flightpathSettlementPacket(t, x, opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, x.s.flightpathActivation.operation.DestinationNode, 2, 12, 448), w.SelfMotionAuthority.TransportScope)
	if view, err := x.s.SettleFlightpath(context.Background(), &actionsv1.SettleFlightpathRequest{SessionId: x.s.sessionID, SelfGuid: 1,
		VisitId: w.VisitRevision, ActivationRevision: x.s.flightpathActivation.revision, MenuRevision: x.s.flightpath.revision,
		CompletionRevision: w.SplineCompletion.Revision, AuthorityRevision: w.SelfMotionAuthority.Revision,
		MovementRevision: w.SelfMovementFacts.Revision, UnitFlagsRevision: w.SelfMotionAuthority.UnitFlagsRevision,
		TransportScope: cloneFlightpathScope(w.SelfMotionAuthority.TransportScope)}); err != nil || view.GetActivation().GetPending() || !view.GetActivation().GetArrivalKnown() {
		t.Fatal("current adjacent segment settlement", view, err)
	}
}

func TestFlightpathAdjacentLaterBookingKeepsHistoricalProofsAndStartsItsOwnLineage(t *testing.T) {
	x, client := newFlightpathTransferRig(t)
	firstRequest := flightpathTransferRequest(t, x)
	first := flightpathAdjacentAdopt(t, x, firstRequest)
	secondRequest := flightpathAdjacentCandidate(t, x, 0)
	second := flightpathAdjacentAdopt(t, x, secondRequest)
	flightpathAdjacentSettle(t, x)
	w := x.f.world
	activation := &actionsv1.FlightpathActivationRequest{SessionId: x.s.sessionID, SelfGuid: 1, VisitId: w.VisitRevision,
		Token: x.token, NpcGuid: x.npc, SourceNode: 2, DestinationNode: 12, MenuRevision: x.s.flightpath.revision,
		ExpectedRevision: x.s.flightpathActivation.revision, ExpectedCopperRevision: w.SelfCopperRevision,
		ExpectedCopper: w.SelfCopper, AcceptUnknownFare: true}
	if view := flightpathActivate(t, x, activation); view.Activation.CurrentSegment != nil || view.TransferAttempt != nil ||
		x.s.flightpathActivation.transferAdoption != nil || len(x.s.flightpathActivation.transferHistory) != 2 {
		t.Fatal("later booking failed to reset only current association", view)
	}
	acceptance := cloneFlightpathScope(w.SelfMotionAuthority.TransportScope)
	acceptance.MotionRevision++
	acceptance.SplineActive = false
	flightpathSettlementPacket(t, x, opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0}, acceptance)
	current := proto.Clone(x.s.flightpathActivation.operation)
	reads, calls := x.client.worldReads, client.adoptCalls
	for _, old := range []struct {
		r *actionsv1.AdoptFlightpathTransferRequest
		v *actionsv1.FlightpathObservationResponse
	}{{firstRequest, first}, {secondRequest, second}} {
		view, err := x.s.AdoptFlightpathTransfer(context.Background(), old.r)
		if err != nil || !proto.Equal(view, old.v) || !proto.Equal(current, x.s.flightpathActivation.operation) ||
			x.client.worldReads != reads || client.adoptCalls != calls {
			t.Fatal("prior booking RPC replay changed the later booking", view, err)
		}
	}
	stale := proto.Clone(firstRequest).(*actionsv1.AdoptFlightpathTransferRequest)
	stale.CoreTransferRevision++
	if _, err := x.s.AdoptFlightpathTransfer(context.Background(), stale); err == nil || x.client.worldReads != reads || client.adoptCalls != calls {
		t.Fatal("unrecognized stale booking request invoked a dependency", err)
	}
	next := flightpathAdjacentCandidate(t, x, 1)
	writes := len(x.f.packets)
	view := flightpathAdjacentAdopt(t, x, next)
	if view.ActivationRevision != 2 || view.Activation.VisitId != 3 || view.Activation.CurrentSegment.VisitId != 4 || client.adoptCalls != 3 || len(x.f.packets) != writes {
		t.Fatal("later booking could not adopt its own post-acceptance source", view, client.adoptCalls)
	}
}
