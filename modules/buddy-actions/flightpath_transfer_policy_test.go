package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

// The Core transfer scopes below are explicitly synthetic owner-loop facts.
// PR200's separate Core peer/owner-loop tests prove their production generation.
// World parses actual packets here, and all adoption/retirement/settlement
// receipts come from the real native methods, never affirmative fixture edits.
func transferPolicyPacket(p *settlementPolicyFixture, op uint32, body []byte) {
	p.f.t.Helper()
	p.worldPacket(op, body)
	flightpathSettlementPacket(p.f.t, p.f.base.x, op, body, cloneFlightpathScope(p.scope))
	p.refresh()
}

func transferPolicyAdjacent(p *settlementPolicyFixture, old *worldv1.SplineCompletionOperation) {
	t := p.f.t
	t.Helper()
	from := old.TransportScope
	fromWorldVisit := p.f.base.x.f.world.VisitRevision
	nextSplineID := from.SplineId + 1
	edge := &modv1.ObservedWorldTransfer{Revision: from.GetWorldTransfer().GetRevision() + 1, Phase: "pending", ConnectionId: from.ConnectionId,
		SelfGuid: from.SelfGuid, FromVisitRevision: from.VisitRevision, FromMapId: from.MapId,
		FromMotionRevision: from.MotionRevision, FromSplineIdKnown: from.SplineIdKnown, FromSplineId: from.SplineId,
		FromSplineRevision: from.SplineRevision, FromSplineSourceRevision: from.SplineSourceRevision,
		FromSplineSourceOpcode: from.SplineSourceOpcode, FromSplineActive: from.SplineActive,
		PendingMotionRevision: from.MotionRevision + 1, TargetMapKnown: true, TargetMapId: 0,
		CompletionReportWritten: true, CompletionReportMotionRevision: from.MotionRevision}
	p.scope = cloneFlightpathScope(from)
	p.scope.VisitBound, p.scope.VisitRevision = false, 0
	p.scope.SplineIdKnown, p.scope.SplineActive = false, false
	p.scope.SplineId, p.scope.SplineSourceRevision, p.scope.SplineSourceOpcode = 0, 0, 0
	p.scope.SplineRevision++
	p.scope.WorldTransfer = edge
	transferPolicyPacket(p, opcode.SMSGTransferPending, binary.LittleEndian.AppendUint32(nil, 0))
	if p.f.base.x.f.world.WorldTransfer.ContinuationKnown ||
		!proto.Equal(p.f.base.x.f.world.SplineCompletion.Operation, old) {
		t.Fatal("pending boundary changed the old receipt or invented continuation")
	}
	entry := proto.Clone(edge).(*modv1.ObservedWorldTransfer)
	entry.Revision, entry.Phase, entry.Known = edge.Revision+2, "ack_written", true
	entry.ToVisitRevision, entry.ToMapId = from.VisitRevision+1, 0
	entry.EntryMotionRevision = p.scope.MotionRevision + 1
	entry.WorldportAckMotionRevision = entry.EntryMotionRevision + 1
	entry.WorldportAckWritten = true
	p.scope.VisitBound, p.scope.VisitRevision, p.scope.MapId = true, entry.ToVisitRevision, 0
	p.scope.SplineRevision++
	p.scope.WorldTransfer = entry
	p.scope.MotionRevision++ // Separate post-entry automatic ACK barrier.
	transferPolicyPacket(p, opcode.SMSGNewWorld,
		settlementPolicyFloats(binary.LittleEndian.AppendUint32(nil, 0), 0, 0, 0, 0))
	if w := p.f.base.x.f.world; w.VisitRevision != fromWorldVisit+1 || w.WorldTransfer.Core.ToVisitRevision != from.VisitRevision+1 ||
		w.WorldTransfer.ContinuationKnown || w.SelfMovementFacts.Known {
		t.Fatal("entry conflated World/Core visits or reused old source facts", w)
	}
	info, err := movementinfo.Encode(movementinfo.Info{Time: 222})
	if err != nil {
		t.Fatal(err)
	}
	create := binary.LittleEndian.AppendUint32(nil, 1)
	create = append(create, 3, 1, 1, 4, 0x20, 0)
	create = append(create, info...)
	for _, speed := range []float32{2.5, 7, 4.5, 4.7, 2.5, 7, 4.5, 3.14, 3.14} {
		create = settlementPolicyFloats(create, speed)
	}
	create = append(create, settlementPolicyValues(map[int]uint32{
		0x17: 1 | (5 << 8), 0x18: 50, 0x20: 100, 0x36: 5, 0x3b: 0x100000, 0x96: 0, 0x492: 12345})...)
	transferPolicyPacket(p, opcode.SMSGUpdateObject, create)
	// The destination NPC is freshly observed after NEW_WORLD cleared caches.
	npc := binary.LittleEndian.AppendUint32(nil, 1)
	npc = append(npc, 3)
	npc = append(npc, settlementPolicyGUID(p.destinationNPC)...)
	npc = append(npc, 3, 0x40, 0)
	npc = settlementPolicyFloats(npc, float32(p.destination.X)+1, float32(p.destination.Y), float32(p.destination.Z), 0)
	npc = append(npc, settlementPolicyValues(map[int]uint32{3: 0x124, 0x18: 100, 0x20: 100, 0x52: 0x2000})...)
	transferPolicyPacket(p, opcode.SMSGUpdateObject, npc)
	spline := []byte{1, 1, 0}
	spline = settlementPolicyFloats(spline, 0, 0, 0)
	spline = binary.LittleEndian.AppendUint32(spline, nextSplineID) // Distinct advertised spline ID.
	spline = append(spline, 0)
	spline = binary.LittleEndian.AppendUint32(spline, 0x2000)
	spline = binary.LittleEndian.AppendUint32(spline, 1)
	spline = binary.LittleEndian.AppendUint32(spline, 2)
	spline = settlementPolicyFloats(spline, 0, 0, 0, float32(p.destination.X), float32(p.destination.Y), float32(p.destination.Z))
	p.scope.SplineIdKnown, p.scope.SplineId, p.scope.SplineActive = true, nextSplineID, true
	p.scope.SplineRevision++
	p.scope.SplineSourceRevision, p.scope.SplineSourceOpcode = p.scope.MotionRevision+1, opcode.SMSGMonsterMove
	transferPolicyPacket(p, opcode.SMSGMonsterMove, spline)
	deadline := time.Now().Add(time.Second)
	for p.f.base.x.f.world.SelfMotionAuthority.DurationRemainingMs != 0 {
		if time.Now().After(deadline) {
			t.Fatal("new protocol spline cadence did not become due")
		}
		runtime.Gosched()
		p.refresh()
	}
	w := p.f.base.x.f.world
	if !w.WorldTransfer.ContinuationKnown || w.WorldTransfer.BookingCausalityKnown || w.WorldTransfer.ArrivalKnown ||
		!w.SelfMotionAuthority.Known || !proto.Equal(w.WorldTransfer.PredecessorCompletion, old) {
		t.Fatal("parsed adjacent source lost unknown history or invented booking/arrival", w)
	}
}

func transferPolicyResolveHost(p *settlementPolicyFixture, action map[string]any, path string) {
	t := p.f.t
	t.Helper()
	out := p.step()
	resolve, ok := out["resolve"].(map[string]any)
	if !ok || resolve["id"] != action["id"] || resolve["path"] != path || out["action"] != nil || out["done"] == true {
		t.Fatal("lost host call did not reconcile only its exact observed native receipt", out, action)
	}
	// This settles only host delivery as unverified, never the native outcome.
	if _, err := p.session.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
		SessionID: p.session.Status().SessionID, ActionID: p.lastReservation, Outcome: behavior.ActionSettledUnverified}}); err != nil {
		t.Fatal(err)
	}
	if err := p.session.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.f.input["pending"] = []any{}
}

func newTransferPolicyFixture(t *testing.T, adoptionEnabled, oldUnknown bool) (*settlementPolicyFixture, *flightpathTransferSession, *worldv1.SplineCompletionOperation) {
	t.Helper()
	p := newSettlementPolicyFixture(t, false)
	source := p.f.source
	if !adoptionEnabled {
		source = strings.Replace(source, "adopt_transfers=true", "adopt_transfers=false", 1)
		if source == p.f.source {
			t.Fatal("editable transfer choice is absent")
		}
	}
	s, err := behavior.NewSessionWithOptions(context.Background(), p.f.base.x.s.sessionID,
		activationPolicySessionSource(source), behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	p.session = s
	p.accept()
	activationPolicyNoEffect(t, p.step())
	p.qualifySpline()
	p.lost["buddy-world.complete_spline"] = oldUnknown
	p.f.base.x.f.failSend = oldUnknown
	completion := p.until("buddy-world.complete_spline")
	p.dispatch(completion)
	old := proto.Clone(p.f.base.x.f.world.SplineCompletion.Operation).(*worldv1.SplineCompletionOperation)
	if old.Retired || old.ArrivalKnown || old.TransportUnknown != oldUnknown ||
		oldUnknown && !old.Pending || !oldUnknown && (old.Pending || !old.TransportWritten) {
		t.Fatal("old report must retain its actual known/unknown classification", old)
	}
	if oldUnknown {
		transferPolicyResolveHost(p, completion, "splineCompletion.operation")
		activationPolicyNoEffect(t, p.step())
	}
	p.lost["buddy-world.complete_spline"], p.f.base.x.f.failSend = false, false
	// The known-reply variant deliberately reaches the boundary before another
	// Decide: its old typed reply/waiting method must be consumed, not replaced.
	client := &flightpathTransferSession{flightpathSession: p.f.base.x.client, world: p.w}
	p.f.base.x.s.session = client
	transferPolicyAdjacent(p, old)
	return p, client, old
}

func finishTransferPolicy(p *settlementPolicyFixture, client *flightpathTransferSession, old *worldv1.SplineCompletionOperation) {
	t := p.f.t
	t.Helper()
	adoption := proto.Clone(p.f.base.x.f.world.SplineCompletion.TransferAdoption).(*worldv1.SplineTransferAdoption)
	complete := p.until("buddy-world.complete_spline")
	p.dispatch(complete)
	if op := p.f.base.x.f.world.SplineCompletion.Operation; op.VisitRevision != 2 || !op.TransportWritten ||
		op.TransportScope.VisitRevision != 10 || op.TransportUnknown || op.ArrivalKnown {
		t.Fatal("new source completion was not one distinct native attempt", op)
	}
	if !proto.Equal(old, adoption.Transfer.PredecessorCompletion) ||
		adoption.Transfer.BookingCausalityKnown || adoption.Transfer.ArrivalKnown {
		t.Fatal("new report rewrote the original outcome or invented causality", adoption)
	}
	p.ground()
	p.dispatch(p.until("buddy-world.retire_spline_completion"))
	for i := 0; i < 3; i++ {
		activationPolicyNoEffect(t, p.step())
	}
	oldToken := p.f.base.x.token
	// Only the test's parent owns lease cleanup/acquisition. Lua cannot issue it.
	if _, err := p.f.base.x.s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{
		Owner: "exploration", Token: oldToken}); err != nil {
		t.Fatal(err)
	}
	lease, err := p.f.base.x.s.AcquireIdleInteraction(context.Background(), &actionsv1.IdleInteractionRequest{
		SessionId: p.f.base.x.s.sessionID, SelfGuid: 1, VisitId: 2, Owner: "exploration", LeaseMs: 10000})
	if err != nil || lease == nil || lease.Token == oldToken || lease.Token == "" {
		t.Fatal("fresh actual parent-owned visit lease unavailable", lease, err)
	}
	p.f.base.x.token = lease.Token
	p.f.input["goal"].(map[string]any)["params"].(map[string]any)["explorationToken"] = lease.Token
	p.refresh()
	p.dispatch(p.until("buddy-actions.query_flightpath"))
	if p.f.base.x.observation(t).Activation.ArrivalKnown {
		t.Fatal("query write or retired completion invented arrival")
	}
	p.arriveMenu(p.f.activation.DestinationNode)
	p.dispatch(p.until("buddy-actions.settle_flightpath"))
	out := p.step()
	if out["done"] != true || out["action"] != nil || !p.f.base.x.observation(t).Activation.ArrivalKnown ||
		p.f.activationCall != 1 || client.adoptCalls != 1 {
		t.Fatal("one accepted booking failed to settle after actual ground/menu evidence", out, p.methods)
	}
	want := []string{"buddy-actions.activate_flightpath", "buddy-world.complete_spline", "buddy-actions.adopt_flightpath_transfer",
		"buddy-world.complete_spline", "buddy-world.retire_spline_completion", "buddy-actions.query_flightpath", "buddy-actions.settle_flightpath"}
	if !reflect.DeepEqual(p.methods, want) || countPackets(p.f.base.x.f, opcode.CMSGMoveSplineDone) != 2 {
		t.Fatal("child replayed an old write, rebooked, or changed parent custody", p.methods, want)
	}
	if !proto.Equal(adoption, p.f.base.x.f.world.SplineCompletion.TransferAdoption) {
		t.Fatal("arrival erased retained transfer/unknown history")
	}
}

func TestFlightpathTransferPolicyActualAdjacentAdoptionCompletesOnlyNewSegment(t *testing.T) {
	p, client, old := newTransferPolicyFixture(t, true, true)
	action := p.until("buddy-actions.adopt_flightpath_transfer")
	p.dispatch(action)
	if op := p.f.base.x.observation(t).Activation; op.CurrentSegment == nil || !op.CurrentSegment.ParentLeaseRequired ||
		op.CurrentSegment.VisitId != 2 || op.VisitId != 1 || !op.Pending || op.ArrivalKnown {
		t.Fatal("actual adoption replaced original booking or certified arrival", op)
	}
	finishTransferPolicy(p, client, old)
}

func TestFlightpathTransferPolicyCASRetainsLostAdoptionAndOriginalUnknown(t *testing.T) {
	p, client, old := newTransferPolicyFixture(t, false, true)
	for i := 0; i < 3; i++ {
		activationPolicyNoEffect(t, p.step())
	}
	before, err := p.session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.session.Reload(context.Background(), activationPolicySessionSource(p.f.source), before.Status.Revision); err != nil {
		t.Fatal(err)
	}
	after, err := p.session.State(context.Background())
	if err != nil || !reflect.DeepEqual(before.Checkpoint, after.Checkpoint) || !reflect.DeepEqual(before.Pending, after.Pending) {
		t.Fatal("enabling adoption changed committed continuation/custody", before, after, err)
	}
	action := p.until("buddy-actions.adopt_flightpath_transfer")
	p.lost["buddy-actions.adopt_flightpath_transfer"] = true
	client.adoptError = errors.New("World adoption happened; unary response lost")
	p.dispatch(action)
	if err := p.session.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err = p.session.State(context.Background())
	if err != nil || len(before.Pending) != 1 || before.Pending[0].Outcome != behavior.ActionUnknown || client.adoptCalls != 1 {
		t.Fatal("actual lost adoption did not retain exactly one unknown host call", before, err)
	}
	disabled := strings.Replace(p.f.source, "adopt_transfers=true", "adopt_transfers=false", 1)
	if _, err := p.session.Reload(context.Background(), activationPolicySessionSource(disabled), before.Status.Revision); err != nil {
		t.Fatal(err)
	}
	after, err = p.session.State(context.Background())
	if err != nil || !reflect.DeepEqual(before.Pending, after.Pending) || !reflect.DeepEqual(before.Checkpoint, after.Checkpoint) {
		t.Fatal("CAS erased the issued adoption identity or uncertain reservation", before, after, err)
	}
	transferPolicyResolveHost(p, action, "activation")
	if client.adoptCalls != 1 || !proto.Equal(old, p.f.base.x.f.world.SplineCompletion.TransferAdoption.Transfer.PredecessorCompletion) {
		t.Fatal("read reconciliation replayed World mutation or changed old unknown")
	}
	finishTransferPolicy(p, client, old)
}

func TestFlightpathTransferPolicyKnownCompletionReplyBeforeNextDecisionKeepsWaitingMethod(t *testing.T) {
	p, client, old := newTransferPolicyFixture(t, true, false)
	action := p.until("buddy-actions.adopt_flightpath_transfer")
	p.dispatch(action)
	if !old.TransportWritten || old.TransportUnknown || p.f.activationCall != 1 {
		t.Fatal("fast transfer lost its actual completed host/native reply", old, p.methods)
	}
	finishTransferPolicy(p, client, old)
}

func TestFlightpathTransferPolicyLostNotInvokedResolvesAndRequiresExplicitFreshRetry(t *testing.T) {
	p, client, old := newTransferPolicyFixture(t, true, true)
	action := p.until("buddy-actions.adopt_flightpath_transfer")
	p.lost["buddy-actions.adopt_flightpath_transfer"] = true
	// Fail the first dependency read, before Actions can invoke WorldAdopt.
	// This hook changes only transport behavior, never a positive native receipt.
	client.beforeWorld = func() { p.f.base.x.f.failSnapshot = true }
	p.dispatch(action)
	client.beforeWorld, p.f.base.x.f.failSnapshot = nil, false
	p.refresh()
	if err := p.session.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := p.session.State(context.Background())
	if err != nil || len(before.Pending) != 1 || before.Pending[0].Outcome != behavior.ActionUnknown {
		t.Fatal("lost known-no-call reply did not retain its host reservation", before, err)
	}
	attempt := p.f.base.x.observation(t).TransferAttempt
	if attempt == nil || attempt.Phase != "not_invoked" || attempt.WorldInvoked || attempt.Error == "" ||
		client.adoptCalls != 0 || !reflect.DeepEqual(flightpathPlanJSON(t, attempt.Request),
		p.f.input["pending"].([]any)[0].(map[string]any)["params"]) {
		t.Fatal("dependency failure did not expose the exact authoritative no-call request", attempt)
	}
	disabled := strings.Replace(p.f.source, "adopt_transfers=true", "adopt_transfers=false", 1)
	if _, err := p.session.Reload(context.Background(), activationPolicySessionSource(disabled), before.Status.Revision); err != nil {
		t.Fatal(err)
	}
	after, err := p.session.State(context.Background())
	if err != nil || !reflect.DeepEqual(before.Pending, after.Pending) || !reflect.DeepEqual(before.Checkpoint, after.Checkpoint) {
		t.Fatal("CAS erased known-no-call pending identity or committed memory", before, after, err)
	}
	transferPolicyResolveHost(p, action, "transferAttempt")
	for i := 0; i < 3; i++ {
		activationPolicyNoEffect(t, p.step())
	}
	if client.adoptCalls != 0 || p.f.activationCall != 1 || len(p.methods) != 3 ||
		p.f.base.x.f.world.SplineCompletion.TransferAdoption != nil ||
		!proto.Equal(old, p.f.base.x.f.world.SplineCompletion.Operation) {
		t.Fatal("default no-call resolution replayed World mutation/rebooking or cleared old unknown", p.methods)
	}
	// New actual same-source MI supplies distinct retry evidence. Tuning still
	// defaults to waiting until the parent explicitly edits the existing key.
	info, err := movementinfo.Encode(movementinfo.Info{Flags: 0x08000000, Time: 333})
	if err != nil {
		t.Fatal(err)
	}
	transferPolicyPacket(p, opcode.MSGMoveHeartbeat, append([]byte{1, 1}, info...))
	activationPolicyNoEffect(t, p.step())
	retry := strings.Replace(p.f.source, "retry_refused_adoptions=false", "retry_refused_adoptions=true", 1)
	if retry == p.f.source {
		t.Fatal("actual explicit adoption retry tuning is absent")
	}
	if _, err := p.session.Reload(context.Background(), activationPolicySessionSource(retry), p.session.Status().Revision); err != nil {
		t.Fatal(err)
	}
	next := p.until("buddy-actions.adopt_flightpath_transfer")
	if next["id"] == action["id"] || reflect.DeepEqual(next["params"], action["params"]) {
		t.Fatal("explicit retry reused the consumed host ID or stale factual request", next, action)
	}
	p.lost["buddy-actions.adopt_flightpath_transfer"] = false
	p.dispatch(next)
	if client.adoptCalls != 1 || p.f.activationCall != 1 || p.f.base.x.observation(t).Activation.CurrentSegment == nil ||
		!proto.Equal(old, p.f.base.x.f.world.SplineCompletion.TransferAdoption.Transfer.PredecessorCompletion) {
		t.Fatal("explicit fresh retry did not adopt once while preserving the old unknown")
	}
}
