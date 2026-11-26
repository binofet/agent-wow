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

// Exercise admission with a deliberately requested finite lease. A visible
// creature alone does not ask the encounter policy to hold the body.
func requestAdmissionTestLease(t *testing.T, host *SessionPolicy) {
	t.Helper()
	_, err := host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{
		"task": "ranged_only", "id": "admission-fixture", "mapId": 349, "visitId": "1", "creatureEntry": 12222,
		"minRangeYards": 14, "maxRangeYards": 36, "clearanceYards": 2, "durationMs": 60000,
	}})
	if err != nil {
		t.Fatal(err)
	}
}

// These fixtures prove admission/receipt custody, not the actor callback deadline.
// Keep production budgets and their separate strict tests unchanged.
func TestSelectedLeaseAdmissionFailureMustNotOrphanLuaOperation(t *testing.T) {
	r := newMovementPointsActorRig(t, followFallbackBundle(t), "4", behavior.SessionOptions{ExecutionLimit: time.Second})
	requestAdmissionTestLease(t, r.host)
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

func TestLeaseRejectionsKeepPreflightAndBackpressureTruth(t *testing.T) {
	for _, kind := range []string{"lease_timeout", "fenced_by_ingress", "queue_full_canceled"} {
		t.Run(kind, func(t *testing.T) {
			r := newMovementPointsActorRig(t, followFallbackBundle(t), "4", behavior.SessionOptions{ExecutionLimit: time.Second})
			requestAdmissionTestLease(t, r.host)
			obs := pointActorObservation("4")
			obs["sessionId"] = "points"
			target := obs["targets"].(map[string]any)["100"].(map[string]any)
			target["entry"] = 12222
			target["x"] = 3
			target["attackObservation"] = map[string]any{}
			seq, e := r.host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: obs})
			if e != nil {
				t.Fatal(e)
			}
			input := map[string]any{"kind": "encounter", "observationSequence": strconv.FormatUint(seq, 10)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			call := context.Context(ctx)
			if kind == "lease_timeout" {
				r.leaseError = status.Error(codes.DeadlineExceeded, "synthetic lease prerequisite outcome unknown")
				call = context.WithValue(call, policyEncounterVisitKey{}, policyEncounterObservation{visit: 1, sequence: strconv.FormatUint(seq, 10)})
			}
			sends := 0
			out, receipt, err := r.host.Step(call, input, func(a map[string]any) (PolicyDispatch, error) {
				if a["capability"] != "movement_owner" {
					t.Fatal(a)
				}
				if kind == "fenced_by_ingress" || kind == "queue_full_canceled" {
					count := 1
					if kind == "queue_full_canceled" {
						count = 256
					}
					for i := 0; i < count; i++ {
						if _, e := r.host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"note": "synthetic newer ingress"}}); e != nil {
							t.Fatal(e)
						}
					}
				}
				if kind == "queue_full_canceled" {
					cancel()
					return PolicyDispatch{}, status.Error(codes.FailedPrecondition, "synthetic unentered refusal")
				}
				return PolicyDispatch{Module: actionsModule, Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
					sends++
					return behavior.ActionUnknown, map[string]any{}
				}}, nil
			})
			if err == nil || receipt == nil || receipt.Outcome != behavior.ActionNotDispatched || !receipt.NeverDispatched || sends != 0 || r.host.lastDispatch.Load() != nil {
				t.Fatal("unentered rejection lost truth", out, receipt, err, sends)
			}
			view, e := r.host.observe(context.Background(), "points", "4")
			if e != nil || len(view.PendingActionIDs) != 0 {
				t.Fatal("rejection acquired native permission", view, e)
			}
			if kind == "lease_timeout" && receipt.Payload["prerequisiteOutcomeUnverified"] != true {
				t.Fatal("prerequisite timeout falsely called effect-free", receipt)
			}
			if kind == "queue_full_canceled" {
				if r.host.rejectedReceipt == receipt || r.host.rejectedReceipt == nil || r.host.rejectedReceipt.ActionID != receipt.ActionID || r.host.vm.Status().Buffered != 256 {
					t.Fatal("backpressure lost exact receipt", r.host.vm.Status())
				}
				// Returned values belong to the caller, not the retained canonical receipt.
				receipt.ActionID = 999
				receipt.Outcome = behavior.ActionUnknown
				receipt.Payload["owner"] = "foreign-mutated"
				receipt.Payload["mode"] = "release"
				receipt.NeverDispatched = false
				if _, _, e = r.host.Step(context.Background(), input, nil); e != nil {
					t.Fatal("receipt could not progress without replay", e)
				}
			} else if e = r.host.vm.Drain(context.Background()); e != nil {
				t.Fatal(e)
			}
			state, e := r.host.vm.State(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			hold := state.Checkpoint["data"].(map[string]any)["commands"].(map[string]any)["data"].(map[string]any)["rangedHold"].(map[string]any)
			if hold["operation"] != nil || hold["uncertain"] == true || len(state.Pending) != 0 || r.host.rejectedReceipt != nil || sends != 0 {
				t.Fatal("known rejection remained orphaned or replayed", hold, state.Status, sends)
			}
		})
	}
}

func TestEnteredUnknownLeaseIsNeverReclassifiedByAdmissionRepair(t *testing.T) {
	r := newMovementPointsActorRig(t, followFallbackBundle(t), "4", behavior.SessionOptions{ExecutionLimit: time.Second})
	requestAdmissionTestLease(t, r.host)
	obs := pointActorObservation("4")
	obs["sessionId"] = "points"
	target := obs["targets"].(map[string]any)["100"].(map[string]any)
	target["entry"] = 12222
	target["x"] = 3
	target["attackObservation"] = map[string]any{}
	seq, e := r.host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: obs})
	if e != nil {
		t.Fatal(e)
	}
	input := map[string]any{"kind": "encounter", "observationSequence": strconv.FormatUint(seq, 10)}
	sends := 0
	_, receipt, e := r.host.Step(context.Background(), input, func(a map[string]any) (PolicyDispatch, error) {
		return PolicyDispatch{Module: actionsModule, Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
			sends++
			return behavior.ActionUnknown, map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": a["owner"], "error": "synthetic entered transport unknown"}
		}}, nil
	})
	if e != nil || receipt == nil || receipt.Outcome != behavior.ActionUnknown || receipt.NeverDispatched || sends != 1 {
		t.Fatal(receipt, e, sends)
	}
	if e = r.host.vm.Drain(context.Background()); e != nil {
		t.Fatal(e)
	}
	seq, e = r.host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: obs})
	if e != nil {
		t.Fatal(e)
	}
	input["observationSequence"] = strconv.FormatUint(seq, 10)
	if _, _, e = r.host.Step(context.Background(), input, func(map[string]any) (PolicyDispatch, error) {
		t.Fatal("unknown action replayed")
		return PolicyDispatch{}, nil
	}); e != nil {
		t.Fatal(e)
	}
	pending := r.host.vm.Outstanding()
	if len(pending) != 1 || pending[0].ActionID != receipt.ActionID || pending[0].Outcome != behavior.ActionUnknown || pending[0].NeverDispatched || sends != 1 {
		t.Fatal(pending, sends)
	}
}
