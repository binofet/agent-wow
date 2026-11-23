package buddyworld

import (
	"context"
	"errors"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func transferAdoptionRequest(t *testing.T, x *completionRig) *worldv1.AdoptSplineTransferRequest {
	t.Helper()
	r := ownMovementSnapshot(t, x.s)
	f := r.WorldTransfer
	return &worldv1.AdoptSplineTransferRequest{SelfGuid: r.Self.Guid, VisitRevision: r.VisitRevision, ExpectedRevision: r.SplineCompletion.Revision, TransferRevision: f.Revision, CoreTransferRevision: f.Core.GetRevision(), AuthorityRevision: f.CurrentAuthorityRevision, MovementRevision: f.CurrentMovementRevision, SplineRevision: f.CurrentSplineRevision, UnitFlagsRevision: f.CurrentUnitFlagsRevision, TransportScope: cloneMotionScope(f.CurrentScope)}
}

func transferAdoptionRig(t *testing.T, unknown bool) (*completionRig, *worldv1.CompleteSplineRequest, *worldv1.SplineCompletionOperation) {
	t.Helper()
	x, oldRequest, old := transferAttempt(t, unknown)
	edge := transferPendingFixture(x, 1, true)
	transferPendingPacket(t, x, edge)
	transferEntryPacket(t, x, edge)
	if r := transferNewTaxi(t, x); !r.WorldTransfer.ContinuationKnown {
		t.Fatal("fixture lacks independently qualified adjacent continuation", r)
	}
	return x, oldRequest, old
}

func TestAdoptSplineTransferRetainsOldOutcomeAndReservesDistinctNewSlot(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "written", true: "unknown"}[unknown], func(t *testing.T) {
			x, oldRequest, old := transferAdoptionRig(t, unknown)
			before := ownMovementSnapshot(t, x.s)
			oldPointer := x.s.completion.operation
			r, err := x.s.AdoptSplineTransfer(context.Background(), transferAdoptionRequest(t, x))
			if err != nil || r.Revision != 2 || r.Operation != nil || r.TransferAdoption == nil || !r.TransferAdoptionCurrent || r.TransferAdoption.Revision != 2 || r.TransferAdoption.SelfGuid != 1 || r.TransferAdoption.FromVisitRevision != 1 || r.TransferAdoption.ToVisitRevision != 2 || !proto.Equal(r.TransferAdoption.Transfer, before.WorldTransfer) {
				t.Fatal("mechanical handoff did not detach exact adjacent context", r, err)
			}
			if x.s.worldTransfer.completion != oldPointer || !proto.Equal(old, oldPointer) || !proto.Equal(old, r.TransferAdoption.Transfer.PredecessorCompletion) || !x.s.completion.consumed[completionKey(old.TransportScope)] || len(x.f.packets) != 1 || x.f.clockCalls != 1 {
				t.Fatal("handoff changed old receipt or transport custody", old, r)
			}
			if r.TransferAdoption.Transfer.ArrivalKnown || r.TransferAdoption.Transfer.BookingCausalityKnown {
				t.Fatal("generic handoff invented booking causality or arrival", r)
			}
			receipt := proto.Clone(r.TransferAdoption).(*worldv1.SplineTransferAdoption)
			x.f.sendError = nil
			x.now = x.now.Add(time.Second)
			newRequest := x.request(t)
			fresh, err := x.s.CompleteSpline(context.Background(), newRequest)
			if err != nil || fresh.Revision != 3 || fresh.Operation.Revision != 3 || !fresh.Operation.TransportWritten || fresh.Operation.Pending || fresh.TransferAdoptionCurrent || len(x.f.packets) != 2 || x.f.clockCalls != 2 || x.s.completion.operation == oldPointer || !proto.Equal(receipt, fresh.TransferAdoption) {
				t.Fatal("new source reused old slot or lost handoff history", fresh, err)
			}
			if x.f.packets[0].TransportScope.VisitRevision != 9 || x.f.packets[1].TransportScope.VisitRevision != 10 || !x.s.completion.consumed[completionKey(old.TransportScope)] || !x.s.completion.consumed[completionKey(newRequest.TransportScope)] {
				t.Fatal("physical old/new Core visit keys were conflated", x.f.packets)
			}
			currentPointer := x.s.completion.operation
			for i := 0; i < 2; i++ {
				replay, err := x.s.CompleteSpline(context.Background(), oldRequest)
				if err != nil || !proto.Equal(replay.Operation, old) || replay.SelfGuid != old.SelfGuid || replay.VisitRevision != old.VisitRevision || replay.Revision != old.Revision || replay.RetirementAvailable || replay.RetirementCurrent || replay.TransferAdoption != nil || replay.TransferAdoptionCurrent || x.s.completion.operation != currentPointer || !proto.Equal(old, oldPointer) || len(x.f.packets) != 2 || x.f.clockCalls != 2 {
					t.Fatal("old retry rewrote or replayed the adopted new slot", replay, err)
				}
			}
		})
	}
}

func TestAdoptSplineTransferRequiredForWrittenAndUnknownCrossVisitAttempts(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "written", true: "unknown"}[unknown], func(t *testing.T) {
			x, _, old := transferAdoptionRig(t, unknown)
			x.now = x.now.Add(time.Second)
			if !ownMovementSnapshot(t, x.s).SelfMotionAuthority.Known {
				t.Fatal("fixture lacks due current new source")
			}
			if _, err := x.s.CompleteSpline(context.Background(), x.request(t)); status.Code(err) != codes.FailedPrecondition || len(x.f.packets) != 1 || x.f.clockCalls != 1 || !proto.Equal(old, x.s.completion.operation) {
				t.Fatal("unretired prior-visit receipt was silently replaced", err)
			}
		})
	}
}

func TestAdoptSplineTransferExactRetryIsDetachedAndDifferentHandoffCannotReplaceHistory(t *testing.T) {
	x, _, old := transferAdoptionRig(t, true)
	request := transferAdoptionRequest(t, x)
	r, err := x.s.AdoptSplineTransfer(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	expected := proto.Clone(r.TransferAdoption).(*worldv1.SplineTransferAdoption)
	r.TransferAdoption.Transfer.Core.TargetMapId = 999
	r.TransferAdoption.Transfer.PredecessorCompletion.TransportUnknown = false
	r.TransferAdoption.Transfer.CurrentScope.ConnectionId = "caller"
	request.TransportScope.ConnectionId = "caller"
	original := proto.Clone(x.s.completion.transferRequest).(*worldv1.AdoptSplineTransferRequest)
	x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{unitHealth: 40}))
	replay, err := x.s.AdoptSplineTransfer(context.Background(), original)
	if err != nil || !replay.TransferAdoptionCurrent || !proto.Equal(expected, replay.TransferAdoption) || !proto.Equal(old, x.s.completion.transferPredecessor) || replay.Revision != 2 || replay.Operation != nil || len(x.f.packets) != 1 {
		t.Fatal("exact retry mutated receipt or froze harmless current-source advancement", replay, err)
	}
	different := transferAdoptionRequest(t, x)
	if proto.Equal(different, original) {
		t.Fatal("fixture did not create a distinct retry binding")
	}
	if _, err := x.s.AdoptSplineTransfer(context.Background(), different); err == nil || x.s.completion.revision != 2 || !proto.Equal(expected, x.s.completion.transferAdoption) {
		t.Fatal("different handoff replaced retained predecessor history", err)
	}
	x.now = x.now.Add(time.Second)
	if fresh, err := x.s.CompleteSpline(context.Background(), x.request(t)); err != nil || fresh.TransferAdoptionCurrent || len(x.f.packets) != 2 {
		t.Fatal("harmless current-scope advancement invalidated same-source handoff", fresh, err)
	}
	replay, err = x.s.AdoptSplineTransfer(context.Background(), original)
	if err != nil || replay.SelfGuid != expected.SelfGuid || replay.VisitRevision != expected.ToVisitRevision || replay.Revision != expected.Revision || replay.Operation != nil || replay.RetirementAvailable || replay.RetirementCurrent || replay.TransferAdoptionCurrent || !proto.Equal(expected, replay.TransferAdoption) || len(x.f.packets) != 2 {
		t.Fatal("historical handoff retry replayed a consumed grant", replay, err)
	}
}

func TestAdoptSplineTransferRejectsEveryStaleBindingWithoutMutation(t *testing.T) {
	for _, name := range []string{"self", "visit", "counter", "transfer", "core_transfer", "authority", "movement", "spline", "flags", "scope", "missing_scope"} {
		t.Run(name, func(t *testing.T) {
			x, _, old := transferAdoptionRig(t, true)
			req := transferAdoptionRequest(t, x)
			switch name {
			case "self":
				req.SelfGuid++
			case "visit":
				req.VisitRevision++
			case "counter":
				req.ExpectedRevision++
			case "transfer":
				req.TransferRevision++
			case "core_transfer":
				req.CoreTransferRevision++
			case "authority":
				req.AuthorityRevision++
			case "movement":
				req.MovementRevision++
			case "spline":
				req.SplineRevision++
			case "flags":
				req.UnitFlagsRevision++
			case "scope":
				req.TransportScope.WorldTransfer.PendingMotionRevision++
			case "missing_scope":
				req.TransportScope = nil
			}
			if _, err := x.s.AdoptSplineTransfer(context.Background(), req); err == nil || x.s.completion.revision != 1 || x.s.completion.transferAdoption != nil || !proto.Equal(old, x.s.completion.operation) || len(x.f.packets) != 1 || x.f.clockCalls != 1 {
				t.Fatal("stale handoff changed active custody", name, err)
			}
		})
	}
}

func TestAdoptSplineTransferUnusedGrantCannotRearmAfterSourceLoss(t *testing.T) {
	for _, name := range []string{"control", "gap", "missing_scope", "replacement_source", "taxi_clear_restore"} {
		t.Run(name, func(t *testing.T) {
			x, _, old := transferAdoptionRig(t, true)
			req := transferAdoptionRequest(t, x)
			r, err := x.s.AdoptSplineTransfer(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			receipt := proto.Clone(r.TransferAdoption).(*worldv1.SplineTransferAdoption)
			switch name {
			case "control":
				x.deliver(t, opcode.SMSGClientControlUpdate, []byte{1, 1, 1})
			case "gap":
				x.scope.MotionRevision++
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 49}))
			case "missing_scope":
				packet(t, x.s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 49}))
			case "replacement_source":
				x.start(t, completionFlyingHeader(0, 16), 21)
			case "taxi_clear_restore":
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitFlags: 0}))
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitFlags: 0x100000}))
			}
			x.now = x.now.Add(time.Second)
			view := ownMovementSnapshot(t, x.s)
			if view.SplineCompletion.TransferAdoptionCurrent || !proto.Equal(receipt, view.SplineCompletion.TransferAdoption) || view.SplineCompletion.Operation != nil || !proto.Equal(old, x.s.completion.transferPredecessor) {
				t.Fatal("lost source rewrote retained handoff history", name, view)
			}
			if _, err := x.s.CompleteSpline(context.Background(), x.request(t)); err == nil || len(x.f.packets) != 1 || x.f.clockCalls != 1 {
				t.Fatal("unused stale handoff granted a fallback source", name, err)
			}
			replay, err := x.s.AdoptSplineTransfer(context.Background(), req)
			if err != nil || replay.TransferAdoptionCurrent || !proto.Equal(receipt, replay.TransferAdoption) || replay.Revision != 2 {
				t.Fatal("exact history replay rearmed stale handoff", name, replay, err)
			}
		})
	}
}

func TestAdoptSplineTransferWaitsForActualOldDispatchClassification(t *testing.T) {
	x := newCompletionRig(t)
	x.now = x.now.Add(time.Second)
	x.f.sendError = errors.New("known Core write with lost module response")
	var request *worldv1.AdoptSplineTransferRequest
	x.f.onSend = func() {
		edge := transferPendingFixture(x, 1, true)
		transferPendingPacket(t, x, edge)
		transferEntryPacket(t, x, edge)
		transferNewTaxi(t, x)
		request = transferAdoptionRequest(t, x)
		if _, err := x.s.AdoptSplineTransfer(context.Background(), request); status.Code(err) != codes.FailedPrecondition || x.s.completion.operation.Phase != "dispatching" || x.s.completion.transferAdoption != nil || x.s.completion.revision != 1 {
			t.Fatal("in-flight predecessor was detached before its callback classified it", err)
		}
	}
	old, err := x.s.CompleteSpline(context.Background(), x.request(t))
	if err != nil || !old.Operation.TransportUnknown || old.SelfGuid != old.Operation.SelfGuid || old.VisitRevision != old.Operation.VisitRevision || old.Revision != old.Operation.Revision || old.VisitRevision != 1 || old.TransferAdoption != nil || old.TransferAdoptionCurrent || old.RetirementAvailable || old.RetirementCurrent {
		t.Fatal(old, err)
	}
	r, err := x.s.AdoptSplineTransfer(context.Background(), request)
	if err != nil || r.Operation != nil || !r.TransferAdoptionCurrent || !proto.Equal(old.Operation, r.TransferAdoption.Transfer.PredecessorCompletion) || !r.TransferAdoption.Transfer.PredecessorCompletion.Pending || len(x.f.packets) != 1 {
		t.Fatal("terminal callback did not remain bound to old retained record", r, err)
	}
}

func TestAdoptSplineTransferRefusesExhaustionAndCancelledCalls(t *testing.T) {
	x, _, old := transferAdoptionRig(t, true)
	req := transferAdoptionRequest(t, x)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := x.s.AdoptSplineTransfer(ctx, req); status.Code(err) != codes.Canceled || x.s.completion.revision != 1 || !proto.Equal(old, x.s.completion.operation) {
		t.Fatal("cancelled handoff committed metadata", err)
	}
	x.s.completion.revision = ^uint64(0)
	req.ExpectedRevision = ^uint64(0)
	if _, err := x.s.AdoptSplineTransfer(context.Background(), req); status.Code(err) != codes.ResourceExhausted || x.s.completion.revision != ^uint64(0) || x.s.completion.transferAdoption != nil || !proto.Equal(old, x.s.completion.operation) || len(x.f.packets) != 1 {
		t.Fatal("handoff exhausted counter wrapped or dropped old history", err)
	}
}

func TestAdoptSplineTransferKnownEdgeAloneCannotReleaseOldUnknown(t *testing.T) {
	for _, name := range []string{"missing_core_report", "missing_new_facts"} {
		t.Run(name, func(t *testing.T) {
			x, _, old := transferAttempt(t, true)
			edge := transferPendingFixture(x, 1, name != "missing_core_report")
			transferPendingPacket(t, x, edge)
			transferEntryPacket(t, x, edge)
			if name == "missing_core_report" {
				transferNewTaxi(t, x)
			}
			view := ownMovementSnapshot(t, x.s)
			if !view.WorldTransfer.Known || view.WorldTransfer.ContinuationKnown {
				t.Fatal("fixture failed to isolate known edge from qualified continuation", name, view)
			}
			if _, err := x.s.AdoptSplineTransfer(context.Background(), transferAdoptionRequest(t, x)); status.Code(err) != codes.FailedPrecondition || x.s.completion.revision != 1 || !proto.Equal(old, x.s.completion.operation) || x.s.completion.transferAdoption != nil || len(x.f.packets) != 1 || x.f.clockCalls != 1 {
				t.Fatal("unqualified edge released retained unknown attempt", name, err)
			}
		})
	}
}

func TestCompleteSplineClockBoundaryReturnsOnlyCapturedReservationIdentity(t *testing.T) {
	x := newCompletionRig(t)
	x.now = x.now.Add(time.Second)
	req := x.request(t)
	x.f.onClock = func() {
		edge := transferPendingFixture(x, 1, false)
		transferPendingPacket(t, x, edge)
		transferEntryPacket(t, x, edge)
	}
	r, err := x.s.CompleteSpline(context.Background(), req)
	if err != nil || r.SelfGuid != req.SelfGuid || r.VisitRevision != req.VisitRevision || r.Revision != r.Operation.Revision || r.Revision != 1 || r.Operation.Phase != "not_dispatched" || r.Operation.Pending || r.Operation.TransportWritten || r.Operation.TransportUnknown || r.RetirementAvailable || r.RetirementCurrent || r.TransferAdoption != nil || r.TransferAdoptionCurrent || !proto.Equal(r.Operation.TransportScope, req.TransportScope) || len(x.f.packets) != 0 || x.f.clockCalls != 1 {
		t.Fatal("late clock result borrowed replacement body/visit projections", r, err)
	}
	if current := ownMovementSnapshot(t, x.s); current.VisitRevision != 2 || current.SplineCompletion.VisitRevision != 2 || current.SplineCompletion.Operation.VisitRevision != 1 {
		t.Fatal("fixture did not distinguish current snapshot from old RPC result", current)
	}
	replay, err := x.s.CompleteSpline(context.Background(), req)
	if err != nil || !proto.Equal(r, replay) || len(x.f.packets) != 0 || x.f.clockCalls != 1 {
		t.Fatal("old retry switched back to replacement identity", r, replay, err)
	}
}

func TestCompleteSplineSendBoundaryPreservesOldIdentityOutcomeAndConsumedKey(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		for _, entered := range []bool{false, true} {
			t.Run(map[bool]string{false: "written", true: "unknown"}[unknown]+"/"+map[bool]string{false: "pending", true: "entered"}[entered], func(t *testing.T) {
				x := newCompletionRig(t)
				x.now = x.now.Add(time.Second)
				req := x.request(t)
				if unknown {
					x.f.sendError = errors.New("old transport unary reply lost")
				}
				x.f.onSend = func() {
					edge := transferPendingFixture(x, 1, true)
					transferPendingPacket(t, x, edge)
					if entered {
						transferEntryPacket(t, x, edge)
					}
				}
				r, err := x.s.CompleteSpline(context.Background(), req)
				if err != nil || r.SelfGuid != req.SelfGuid || r.VisitRevision != req.VisitRevision || r.Revision != 1 || r.Operation.Revision != 1 || r.Operation.Pending != unknown || r.Operation.TransportUnknown != unknown || r.Operation.TransportWritten == unknown || r.RetirementAvailable || r.RetirementCurrent || r.TransferAdoption != nil || r.TransferAdoptionCurrent || !proto.Equal(r.Operation.TransportScope, req.TransportScope) || !x.s.completion.consumed[completionKey(req.TransportScope)] || len(x.f.packets) != 1 || x.f.clockCalls != 1 {
					t.Fatal("late send result borrowed replacement identity or rewrote old uncertainty", r, err)
				}
				saved := proto.Clone(r).(*worldv1.SplineCompletionObservation)
				r.Operation.TransportScope.ConnectionId = "caller"
				replay, err := x.s.CompleteSpline(context.Background(), req)
				if err != nil || !proto.Equal(saved, replay) || len(x.f.packets) != 1 || x.f.clockCalls != 1 || !x.s.completion.consumed[completionKey(req.TransportScope)] {
					t.Fatal("late result aliased or replayed the retained old record", saved, replay, err)
				}
			})
		}
	}
}

func TestCompleteSplineLateNoDispatchCannotDeleteReplacementConsumedHistory(t *testing.T) {
	x := newCompletionRig(t)
	x.now = x.now.Add(time.Second)
	req := x.request(t)
	x.f.sendError = modcb.PacketNotDispatchedError("old owner-loop request refused before transport")
	var old, replacement *worldv1.SplineCompletionOperation
	x.f.onSend = func() {
		x.s.mu.Lock()
		defer x.s.mu.Unlock()
		old = x.s.completion.operation
		// Inject a replacement slot at this callback boundary to test the late
		// writer fence directly. Adopt itself refuses a dispatching predecessor.
		replacement = proto.Clone(old).(*worldv1.SplineCompletionOperation)
		replacement.Revision = 2
		replacement.Phase = "replacement_reserved"
		x.s.completion.operation = replacement
		x.s.completion.revision = 2
		if !x.s.completion.consumed[completionKey(req.TransportScope)] {
			t.Fatal("fixture lost the captured consumed proof before replacement")
		}
	}
	r, err := x.s.CompleteSpline(context.Background(), req)
	if err != nil || r.SelfGuid != req.SelfGuid || r.VisitRevision != req.VisitRevision || r.Revision != 1 || r.Operation.Revision != 1 || r.Operation.Phase != "not_dispatched" || r.Operation.Pending || r.Operation.TransportWritten || r.Operation.TransportUnknown || x.s.completion.operation != replacement || replacement.Phase != "replacement_reserved" || !replacement.Pending || x.s.completion.revision != 2 || !x.s.completion.consumed[completionKey(req.TransportScope)] || r.RetirementAvailable || r.RetirementCurrent || r.TransferAdoption != nil || r.TransferAdoptionCurrent {
		t.Fatal("late old no-dispatch result mutated replacement state or erased consumed history", r, err)
	}
	if old.Phase != "not_dispatched" || old == replacement || len(x.f.packets) != 1 || x.f.clockCalls != 1 {
		t.Fatal("old outcome escaped its captured record", old, replacement)
	}
}
