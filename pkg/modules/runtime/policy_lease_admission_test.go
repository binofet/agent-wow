package modrt

import (
	"context"
	"errors"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strconv"
	"testing"
	"time"
)

func TestSelectedLeaseAdmissionFailureMustNotOrphanLuaOperation(t *testing.T) {
	r := newMovementPointsActorRig(t, followFallbackBundle(t), "4", behavior.SessionOptions{ExecutionLimit: 250 * time.Millisecond})
	obs := pointActorObservation("4")
	obs["sessionId"] = "points"
	target := obs["targets"].(map[string]any)["100"].(map[string]any)
	target["entry"] = 12222
	target["x"] = 3
	target["attackObservation"] = map[string]any{}
	seq, err := r.host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: obs})
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"kind": "encounter", "observationSequence": strconv.FormatUint(seq, 10)}
	refused := status.Error(codes.FailedPrecondition, "synthetic typed pre-dispatch refusal")
	out, receipt, err := r.host.Step(context.Background(), input, func(a map[string]any) (PolicyDispatch, error) {
		if a["capability"] != "movement_owner" || a["mode"] != "acquire" {
			t.Fatal("fixture did not select lease", a)
		}
		return PolicyDispatch{}, refused
	})
	if !errors.Is(err, refused) {
		t.Fatal(out, receipt, err)
	}
	// A known, unentered proposal must reach the same receipt/custody path. It
	// must not leave an operation that will wait forever with zero host pending.
	if receipt == nil || receipt.Outcome != behavior.ActionNotDispatched || !receipt.NeverDispatched {
		t.Fatal("selected lease has no known-not-dispatched receipt", out, receipt, err, r.host.vm.Status())
	}
	view, err := r.host.observe(context.Background(), "points", "4")
	if err != nil || len(view.PendingActionIDs) != 0 || r.host.lastDispatch.Load() != nil {
		t.Fatal("rejection granted native write permission or fabricated dispatch", view, err)
	}
	if err = r.host.vm.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := r.host.vm.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	commands := state.Checkpoint["data"].(map[string]any)["commands"].(map[string]any)["data"].(map[string]any)
	hold := commands["rangedHold"].(map[string]any)
	if hold["operation"] != nil || hold["uncertain"] == true || hold["refusal"] == nil {
		t.Fatal("known refusal did not retire exact selected operation", hold)
	}
	// Fresh input may now select a new operation; the rejected action itself is
	// never replayed and no native write occurred for it.
	seq, err = r.host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: obs})
	if err != nil {
		t.Fatal(err)
	}
	input["observationSequence"] = strconv.FormatUint(seq, 10)
	calls := 0
	_, nextReceipt, err := r.host.Step(context.Background(), input, func(a map[string]any) (PolicyDispatch, error) {
		return PolicyDispatch{Module: actionsModule, Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
			calls++
			return behavior.ActionConfirmed, map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": a["owner"], "result": map[string]any{"token": "synthetic-token", "state": map[string]any{"sessionId": "points", "selfGuid": "4", "movementOwner": map[string]any{"active": true, "owner": a["owner"]}}}}
		}}, nil
	})
	if err != nil || nextReceipt == nil || nextReceipt.ActionID == receipt.ActionID || calls != 1 {
		t.Fatal("fresh admissible operation could not progress exactly once", nextReceipt, err, calls)
	}
}
