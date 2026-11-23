package buddyactions

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func continuationPolicyRequest(t *testing.T, action map[string]any) *actionsv1.AdoptFlightpathTransferRequest {
	t.Helper()
	b, err := json.Marshal(action["params"])
	if err != nil {
		t.Fatal(err)
	}
	r := &actionsv1.AdoptFlightpathTransferRequest{}
	if err := protojson.Unmarshal(b, r); err != nil {
		t.Fatal(err)
	}
	return r
}

func continuationPolicyOldRequest(op *worldv1.SplineCompletionOperation) *worldv1.CompleteSplineRequest {
	return &worldv1.CompleteSplineRequest{SelfGuid: op.SelfGuid, VisitRevision: op.VisitRevision,
		ExpectedRevision: op.Revision - 1, AuthorityRevision: op.AuthorityRevision,
		MovementRevision: op.MovementRevision, SplineRevision: op.SplineRevision,
		TransportScope: cloneFlightpathScope(op.TransportScope)}
}

func continuationPolicyUnknownB(p *settlementPolicyFixture) *worldv1.SplineCompletionOperation {
	t := p.f.t
	t.Helper()
	p.lost["buddy-world.complete_spline"], p.f.base.x.f.failSend = true, true
	action := p.until("buddy-world.complete_spline")
	p.dispatch(action)
	op := proto.Clone(p.f.base.x.f.world.SplineCompletion.Operation).(*worldv1.SplineCompletionOperation)
	if op.VisitRevision != 2 || op.TransportScope.VisitRevision != 10 || !op.Pending || !op.TransportUnknown || op.Retired {
		t.Fatal("B must have its own actual consumed unknown report", op)
	}
	transferPolicyResolveHost(p, action, "splineCompletion.operation")
	p.lost["buddy-world.complete_spline"], p.f.base.x.f.failSend = false, false
	activationPolicyNoEffect(t, p.step())
	return op
}

func TestFlightpathTransferContinuationPolicyTwoAdjacentEdgesLostSecondReplyAndCAS(t *testing.T) {
	p, client, oldA := newTransferPolicyFixture(t, true, true)
	worldService := p.w
	booking := proto.Clone(p.f.base.x.observation(t).Activation).(*actionsv1.FlightpathActivationOperation)
	first := p.until("buddy-actions.adopt_flightpath_transfer")
	firstRequest := continuationPolicyRequest(t, first)
	p.dispatch(first)
	firstWorldRequest := proto.Clone(client.adoptRequest).(*worldv1.AdoptSplineTransferRequest)
	firstHandoff := proto.Clone(p.f.base.x.f.world.SplineCompletion.TransferAdoption).(*worldv1.SplineTransferAdoption)
	firstResult, err := p.f.base.x.s.AdoptFlightpathTransfer(context.Background(), firstRequest)
	if err != nil || firstResult.Activation.CurrentSegment.VisitId != 2 || client.adoptCalls != 1 {
		t.Fatal("exact first RPC replay lost its historical B identity", firstResult, err)
	}
	oldB := continuationPolicyUnknownB(p)
	// Reuse the actual packet parser in the SAME World service. Its Core scope
	// witnesses remain explicitly synthetic, separately qualified by Core tests.
	transferPolicyAdjacent(p, oldB)
	if p.w != worldService || p.f.base.x.f.world.VisitRevision != 3 || p.scope.VisitRevision != 11 {
		t.Fatal("second boundary replaced the service or mixed independent visit domains")
	}
	second := p.until("buddy-actions.adopt_flightpath_transfer")
	secondRequest := continuationPolicyRequest(t, second)
	if secondRequest.VisitId != 3 || secondRequest.ActivationRevision != firstRequest.ActivationRevision ||
		secondRequest.WorldTransferRevision <= firstRequest.WorldTransferRevision ||
		secondRequest.CoreTransferRevision <= firstRequest.CoreTransferRevision ||
		secondRequest.TransportScope.VisitRevision != 11 || second["id"] == first["id"] {
		t.Fatal("B-to-C request did not bind the next adjacent segment of the same booking", first, second)
	}
	p.lost["buddy-actions.adopt_flightpath_transfer"] = true
	client.adoptError = errors.New("second real World adoption completed; unary response lost")
	p.dispatch(second)
	secondWorldRequest := proto.Clone(client.adoptRequest).(*worldv1.AdoptSplineTransferRequest)
	secondHandoff := proto.Clone(p.f.base.x.f.world.SplineCompletion.TransferAdoption).(*worldv1.SplineTransferAdoption)
	if secondHandoff.FromVisitRevision != 2 || secondHandoff.ToVisitRevision != 3 || client.adoptCalls != 2 ||
		!proto.Equal(secondHandoff.Transfer.PredecessorCompletion, oldB) || !oldA.TransportUnknown || !oldB.TransportUnknown {
		t.Fatal("second native handoff rewrote an old outcome or skipped B", secondHandoff)
	}
	if err := p.session.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := p.session.State(context.Background())
	if err != nil || len(before.Pending) != 1 || before.Pending[0].Outcome != behavior.ActionUnknown {
		t.Fatal("lost second host reply failed to retain its exact reservation", before, err)
	}
	disabled := strings.Replace(p.f.source, "adopt_transfers=true", "adopt_transfers=false", 1)
	if _, err := p.session.Reload(context.Background(), activationPolicySessionSource(disabled), before.Status.Revision); err != nil {
		t.Fatal(err)
	}
	after, err := p.session.State(context.Background())
	if err != nil || !reflect.DeepEqual(before.Pending, after.Pending) || !reflect.DeepEqual(before.Checkpoint, after.Checkpoint) {
		t.Fatal("CAS lost the second request identity or prior continuation", before, after, err)
	}
	transferPolicyResolveHost(p, second, "activation")
	if client.adoptCalls != 2 || p.f.activationCall != 1 {
		t.Fatal("observing the second receipt reinvoked World or rebooked")
	}
	p.lost["buddy-actions.adopt_flightpath_transfer"], client.adoptError = false, nil
	p.dispatch(p.until("buddy-world.complete_spline"))
	current := proto.Clone(p.f.base.x.f.world.SplineCompletion.Operation).(*worldv1.SplineCompletionOperation)
	if current.VisitRevision != 3 || current.TransportScope.VisitRevision != 11 || !current.TransportWritten || current.TransportUnknown {
		t.Fatal("C did not receive its own distinct completion attempt", current)
	}
	// Old exact calls return THEIR historical identities, never the latest C
	// slot. They must not issue transport or restore an obsolete current segment.
	packets, calls := len(p.f.base.x.f.packets), client.adoptCalls
	for _, old := range []*worldv1.SplineCompletionOperation{oldA, oldB} {
		view, err := p.w.CompleteSpline(context.Background(), continuationPolicyOldRequest(old))
		if err != nil || !proto.Equal(view.Operation, old) || view.Operation.VisitRevision == current.VisitRevision {
			t.Fatal("old completion replay was relabeled as C or lost its unknown", view, err)
		}
	}
	for index, request := range []*worldv1.AdoptSplineTransferRequest{firstWorldRequest, secondWorldRequest} {
		view, err := p.w.AdoptSplineTransfer(context.Background(), request)
		want := []*worldv1.SplineTransferAdoption{firstHandoff, secondHandoff}[index]
		if err != nil || !proto.Equal(view.TransferAdoption, want) || view.TransferAdoptionCurrent {
			t.Fatal("historical handoff replay changed its identity or rearmed a consumed grant", view, err)
		}
	}
	historical, err := p.f.base.x.s.AdoptFlightpathTransfer(context.Background(), firstRequest)
	if err != nil || !proto.Equal(historical, firstResult) || historical.Activation.CurrentSegment.VisitId != 2 {
		t.Fatal("historical Actions RPC returned the latest C segment", historical, err)
	}
	p.refresh()
	live := p.f.base.x.observation(t).Activation
	if len(p.f.base.x.f.packets) != packets || client.adoptCalls != calls ||
		!proto.Equal(p.f.base.x.f.world.SplineCompletion.Operation, current) || live.CurrentSegment.VisitId != 3 {
		t.Fatal("old callbacks mutated C or wrote another report", live)
	}
	unchanged := proto.Clone(live).(*actionsv1.FlightpathActivationOperation)
	unchanged.CurrentSegment = nil
	if !proto.Equal(unchanged, booking) {
		t.Fatal("continuation changed original accepted departure facts", booking, unchanged)
	}
	p.ground()
	p.dispatch(p.until("buddy-world.retire_spline_completion"))
	for i := 0; i < 3; i++ {
		activationPolicyNoEffect(t, p.step())
	}
	oldToken := p.f.base.x.token
	if _, err := p.f.base.x.s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{
		Owner: "exploration", Token: oldToken}); err != nil {
		t.Fatal(err)
	}
	// Only the parent supplies fresh final-visit custody; the child emits no lease RPC.
	lease, err := p.f.base.x.s.AcquireIdleInteraction(context.Background(), &actionsv1.IdleInteractionRequest{
		SessionId: p.f.base.x.s.sessionID, SelfGuid: 1, VisitId: 3, Owner: "exploration", LeaseMs: 10000})
	if err != nil || lease == nil || lease.Token == oldToken {
		t.Fatal("fresh parent C-visit lease unavailable", lease, err)
	}
	p.f.base.x.token = lease.Token
	p.f.input["goal"].(map[string]any)["params"].(map[string]any)["explorationToken"] = lease.Token
	p.refresh()
	p.dispatch(p.until("buddy-actions.query_flightpath"))
	if p.f.base.x.observation(t).Activation.ArrivalKnown {
		t.Fatal("two transfers/reports or a query write invented arrival")
	}
	p.arriveMenu(p.f.activation.DestinationNode)
	p.dispatch(p.until("buddy-actions.settle_flightpath"))
	out := p.step()
	want := []string{"buddy-actions.activate_flightpath", "buddy-world.complete_spline", "buddy-actions.adopt_flightpath_transfer",
		"buddy-world.complete_spline", "buddy-actions.adopt_flightpath_transfer", "buddy-world.complete_spline",
		"buddy-world.retire_spline_completion", "buddy-actions.query_flightpath", "buddy-actions.settle_flightpath"}
	if out["done"] != true || out["action"] != nil || !p.f.base.x.observation(t).Activation.ArrivalKnown ||
		!reflect.DeepEqual(p.methods, want) || p.f.activationCall != 1 || client.adoptCalls != 2 ||
		countPackets(p.f.base.x.f, opcode.CMSGMoveSplineDone) != 3 || p.w != worldService {
		t.Fatal("same booking did not settle only after actual final evidence without replay/child custody", out, p.methods)
	}
}

func TestFlightpathTransferContinuationPolicySkippedOrUnlinkedEdgeCannotAdopt(t *testing.T) {
	for _, change := range []string{"skipped_core_visit", "unlinked_connection"} {
		t.Run(change, func(t *testing.T) {
			p, client, _ := newTransferPolicyFixture(t, true, true)
			p.dispatch(p.until("buddy-actions.adopt_flightpath_transfer"))
			oldB := continuationPolicyUnknownB(p)
			from := oldB.TransportScope
			// Deliberately contradictory synthetic Core metadata accompanies real
			// pending/entry packets. It must never become affirmative World proof.
			edge := &modv1.ObservedWorldTransfer{Revision: from.WorldTransfer.Revision + 1, Phase: "pending",
				ConnectionId: from.ConnectionId, SelfGuid: 1, FromVisitRevision: from.VisitRevision, FromMapId: from.MapId,
				FromMotionRevision: from.MotionRevision, FromSplineIdKnown: from.SplineIdKnown, FromSplineId: from.SplineId,
				FromSplineRevision: from.SplineRevision, FromSplineSourceRevision: from.SplineSourceRevision,
				FromSplineSourceOpcode: from.SplineSourceOpcode, FromSplineActive: true, PendingMotionRevision: from.MotionRevision + 1,
				TargetMapKnown: true, TargetMapId: 0, CompletionReportWritten: true, CompletionReportMotionRevision: from.MotionRevision}
			p.scope = cloneFlightpathScope(from)
			p.scope.VisitBound, p.scope.VisitRevision, p.scope.SplineActive = false, 0, false
			p.scope.SplineIdKnown, p.scope.SplineSourceRevision, p.scope.SplineSourceOpcode = false, 0, 0
			p.scope.SplineRevision++
			p.scope.WorldTransfer = edge
			transferPolicyPacket(p, opcode.SMSGTransferPending, binary.LittleEndian.AppendUint32(nil, 0))
			entry := proto.Clone(edge).(*modv1.ObservedWorldTransfer)
			entry.Revision, entry.Known, entry.Phase = edge.Revision+2, true, "ack_written"
			entry.ToVisitRevision, entry.ToMapId = from.VisitRevision+1, 0
			entry.EntryMotionRevision, entry.WorldportAckMotionRevision = p.scope.MotionRevision+1, p.scope.MotionRevision+2
			entry.WorldportAckWritten = true
			if change == "skipped_core_visit" {
				entry.ToVisitRevision++
			} else {
				entry.ConnectionId = "unlinked-fixture-connection"
			}
			p.scope.VisitBound, p.scope.VisitRevision, p.scope.MapId = true, entry.ToVisitRevision, 0
			p.scope.WorldTransfer = entry
			p.scope.MotionRevision++
			transferPolicyPacket(p, opcode.SMSGNewWorld,
				settlementPolicyFloats(binary.LittleEndian.AppendUint32(nil, 0), 0, 0, 0, 0))
			for i := 0; i < 3; i++ {
				activationPolicyNoEffect(t, p.step())
			}
			w, op := p.f.base.x.f.world, p.f.base.x.observation(t).Activation
			if w.WorldTransfer.Known || w.WorldTransfer.ContinuationKnown || client.adoptCalls != 1 ||
				p.f.activationCall != 1 || len(p.methods) != 4 || op.CurrentSegment.VisitId != 2 || op.ArrivalKnown ||
				!proto.Equal(w.SplineCompletion.Operation, oldB) {
				t.Fatal("unlinked second boundary adopted/rebooked or released old unknown custody", change, w, op, p.methods)
			}
		})
	}
}
