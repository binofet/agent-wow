package world

import (
	"context"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func waitTaskObservation() *actionsv1.EncounterObservationResponse {
	o := fallbackObservation()
	o.Party = &actionsv1.EncounterParty{Group: fallbackRoster(true)}
	o.Players["3"] = fallbackPlayer(3)
	for _, player := range o.Players {
		player.PositionKnown, player.MovementKnown = true, true
		player.X, player.Y, player.Z = 10, 10, 10
	}
	o.Players["3"].X = 50
	o.Navigation = &actionsv1.NavigationObservation{Following: true, OwnerGuid: 3, MovementOwner: &actionsv1.MovementOwnerState{}}
	return o
}

func waitTaskCommand(t *testing.T, p *fallbackPolicyTest, value map[string]any) {
	t.Helper()
	if _, err := p.host.Enqueue(behavior.Event{Kind: "command", Value: value}); err != nil {
		t.Fatal(err)
	}
}

func settleWaitOwnership(t *testing.T, p *fallbackPolicyTest, decision behavior.Decision, outcome behavior.ActionOutcome, mode, owner, token string) {
	t.Helper()
	id, err := p.host.ReserveAction(decision)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]any{}
	if token != "" {
		result["token"] = token
	}
	if _, err := p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
		SessionID: fallbackSession, ActionID: id, Outcome: outcome,
		Payload: map[string]any{"capability": "movement_owner", "mode": mode, "owner": owner, "result": result},
	}}); err != nil {
		t.Fatal(err)
	}
}

func beginWaitTask(t *testing.T) (*fallbackPolicyTest, *actionsv1.EncounterObservationResponse) {
	t.Helper()
	p, obs := newFallbackPolicyTest(t, ""), waitTaskObservation()
	waitTaskCommand(t, p, map[string]any{"task": "wait_for_pull", "id": "pull-1"})
	decision := p.decide(observeSparseEncounter(t, p, obs))
	action := decision.Output["action"].(map[string]any)
	if action["capability"] != "movement_owner" || action["mode"] != "acquire" || action["owner"] != "task:pull-1" {
		t.Fatal("wait task did not acquire finite movement ownership", decision.Output)
	}
	settleWaitOwnership(t, p, decision, behavior.ActionConfirmed, "acquire", "task:pull-1", "exact-token")
	obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "task:pull-1", RemainingMs: 3000, Revision: 1}
	out := p.decide(observeSparseEncounter(t, p, obs)).Output
	if out["action"] != nil || out["task"].(map[string]any)["phase"] != "waiting_for_owner_pull" || !obs.Navigation.Following {
		t.Fatal("wait changed standing follow intent or failed to hold", out)
	}
	return p, obs
}

func TestEncounterTemporaryWaitPreservesQueueAndDefendsActualIncomingAttack(t *testing.T) {
	p, obs := beginWaitTask(t)
	addFallbackTarget(obs, fallbackNPC, 3, 500)
	obs.Targets[fallbackGUID(fallbackNPC)].X, obs.Targets[fallbackGUID(fallbackNPC)].Y, obs.Targets[fallbackGUID(fallbackNPC)].Z = 40, 10, 10
	addFallbackTarget(obs, fallbackNPC+1, 0, 1)
	obs.ObservedOwnerFocus = fallbackNPC
	out := p.decide(observeSparseEncounter(t, p, obs)).Output
	if out["action"] != nil || out["focusGuid"] != fallbackGUID(fallbackNPC) || len(out["queue"].([]any)) != 2 {
		t.Fatal("wait chased the owner pull or discarded the shared queue", out)
	}
	obs.Engagement[fallbackGUID(fallbackNPC)].VictimGuid = fallbackSelf
	p.expectOffense(observeSparseEncounter(t, p, obs), "engaged_defense", fallbackNPC, 133)
}

func TestEncounterTemporaryWaitKeepsRealHealingAndReloadState(t *testing.T) {
	p, obs := beginWaitTask(t)
	if _, err := p.host.Reload(context.Background(), readEncounterBundle(t)+"\n-- task reload", p.host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if out := p.decide(observeSparseEncounter(t, p, obs)).Output; out["action"] != nil || out["task"].(map[string]any)["phase"] != "waiting_for_owner_pull" {
		t.Fatal("reload lost the active task or private ownership token", out)
	}
	heal := positionedHealObservation()
	heal.Navigation = obs.Navigation
	out := p.decide(observeSparseEncounter(t, p, heal)).Output
	action, _ := out["action"].(map[string]any)
	if action["capability"] != "heal" || action["targetGuid"] != "8" || action["spellId"] != float64(6063) {
		t.Fatal("temporary wait disabled ordinary healing", out)
	}
}

func TestEncounterTemporaryWaitAllowsHealingRouteRecovery(t *testing.T) {
	p, obs := beginWaitTask(t)
	heal := positionedHealObservation()
	heal.Navigation = obs.Navigation
	heal.Navigation.BodyMovementSupported = true
	heal.Healing.Patients[0].Distance = float64Pointer(45)
	heal.Players["8"].X = 55
	heal.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: 8, VisitId: 1, RangeYards: 40,
		RequireLos: true, Phase: "follow_navigation_unavailable"}
	for i := 0; i < 3; i++ {
		out := p.decide(observeSparseEncounter(t, p, heal)).Output
		action, _ := out["action"].(map[string]any)
		want := "approach_target"
		if i == 2 {
			want = "move_body"
		}
		if action["capability"] != want {
			t.Fatal("temporary hold blocked selected healing movement/recovery", i, out)
		}
	}
}

func TestEncounterTemporaryWaitReleasesOnArrivalAndNeverReplaysRetiredIntent(t *testing.T) {
	for _, reason := range []string{"pull_arrived", "canceled", "visit_changed", "dead"} {
		t.Run(reason, func(t *testing.T) {
			p, obs := beginWaitTask(t)
			switch reason {
			case "pull_arrived":
				addFallbackTarget(obs, fallbackNPC, 3, 500)
				npc := obs.Targets[fallbackGUID(fallbackNPC)]
				npc.X, npc.Y, npc.Z = 15, 10, 10
				obs.ObservedOwnerFocus = fallbackNPC
			case "canceled":
				waitTaskCommand(t, p, map[string]any{"task": "cancel_task", "id": "pull-1"})
			case "visit_changed":
				obs.VisitId++
			case "dead":
				obs.Players["4"].Alive = boolPointer(false)
			}
			decision := p.decide(observeSparseEncounter(t, p, obs))
			action, _ := decision.Output["action"].(map[string]any)
			if action["mode"] != "release" || action["token"] != "exact-token" || decision.Output["task"].(map[string]any)["reason"] != reason {
				t.Fatal("task did not release only its exact ownership", reason, decision.Output)
			}
			settleWaitOwnership(t, p, decision, behavior.ActionConfirmed, "release", "task:pull-1", "")
			obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Revision: 2}
			out := p.decide(observeSparseEncounter(t, p, obs)).Output
			if out["task"].(map[string]any)["phase"] != "complete" || out["task"].(map[string]any)["reason"] != reason {
				t.Fatal("task did not pop after release", out)
			}
			waitTaskCommand(t, p, map[string]any{"task": "wait_for_pull", "id": "pull-1"})
			out = p.decide(observeSparseEncounter(t, p, obs)).Output
			if a, ok := out["action"].(map[string]any); ok && a["capability"] == "movement_owner" {
				t.Fatal("late old intent reactivated a completed task", out)
			}
		})
	}
}

func TestEncounterTemporaryWaitSupersedingTaskCleansOldOwnerFirst(t *testing.T) {
	p, obs := beginWaitTask(t)
	waitTaskCommand(t, p, map[string]any{"task": "wait_for_pull", "id": "pull-2"})
	decision := p.decide(observeSparseEncounter(t, p, obs))
	action := decision.Output["action"].(map[string]any)
	if action["mode"] != "release" || action["owner"] != "task:pull-1" {
		t.Fatal("new task replaced old custody without cleanup", decision.Output)
	}
	settleWaitOwnership(t, p, decision, behavior.ActionConfirmed, "release", "task:pull-1", "")
	obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Revision: 2}
	p.decide(observeSparseEncounter(t, p, obs))
	out := p.decide(observeSparseEncounter(t, p, obs)).Output
	action = out["action"].(map[string]any)
	if action["mode"] != "acquire" || action["owner"] != "task:pull-2" || action["token"] != nil {
		t.Fatal("replacement reused the retired owner's token", out)
	}
}

func boolPointer(value bool) *bool          { return &value }
func float64Pointer(value float64) *float64 { return &value }

func TestTemporaryTaskRequiresClockAndLeaseAndReconcilesUnknownGrant(t *testing.T) {
	body := `local i=frame.input
 if i.command then module.on_event({kind="command",value=i.command}) end
 if i.receipt then module.on_event({receipt=i.receipt}) end
 local view=module.step(i.obs,{clock=i.clock},"1",{},false,true)
 if i.commit and view.action then module.commit(view,view.action) end
 return {status=view.status,action=view.action,allowed=module.allow(view,i.obs,i.proposed)}`
	obs := map[string]any{"sessionId": "body", "selfGuid": "4", "visitId": "1", "mapId": float64(0),
		"players":    map[string]any{"4": map[string]any{"present": true, "aliveKnown": true, "alive": true, "positionKnown": true, "mapKnown": true, "mapId": float64(0), "movementKnown": true}},
		"navigation": map[string]any{"ownerGuid": "3", "movementOwner": map[string]any{}}}
	for _, missing := range []string{"clock", "lease"} {
		host := urgencyModule(t, "encounter-tasks.lua", body)
		input := map[string]any{"obs": obs, "command": map[string]any{"task": "wait_for_pull", "id": "w"}}
		want := "task_clock_unavailable"
		if missing == "lease" {
			input["clock"] = map[string]any{"monotonicMs": float64(0)}
			delete(obs["navigation"].(map[string]any), "movementOwner")
			want = "movement_owner_unavailable"
		}
		out, err := host.Decide(context.Background(), input)
		if err != nil || out.Output["status"].(map[string]any)["phase"] != want || out.Output["action"] != nil {
			t.Fatal("missing mechanic was hidden or task claimed to hold", out.Output, err)
		}
	}
	obs["navigation"].(map[string]any)["movementOwner"] = map[string]any{}
	host := urgencyModule(t, "encounter-tasks.lua", body)
	input := map[string]any{"obs": obs, "clock": map[string]any{"monotonicMs": float64(0)}, "commit": true,
		"command": map[string]any{"task": "wait_for_pull", "id": "w"}}
	if _, err := host.Decide(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	delete(input, "command")
	input["receipt"] = map[string]any{"outcome": "settled_unverified", "payload": map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": "task:w"}}
	input["proposed"] = map[string]any{"capability": "heal", "targetGuid": "4"}
	out, err := host.Decide(context.Background(), input)
	if err != nil || out.Output["action"] != nil || out.Output["allowed"] != true || out.Output["status"].(map[string]any)["phase"] != "reconciling_movement_ownership" {
		t.Fatal("unknown grant replayed or blocked healing", out.Output, err)
	}
	delete(input, "receipt")
	input["clock"] = map[string]any{"monotonicMs": float64(3000)}
	out, err = host.Decide(context.Background(), input)
	if err != nil || out.Output["action"].(map[string]any)["mode"] != "acquire" || out.Output["action"].(map[string]any)["token"] != nil {
		t.Fatal("observed finite expiry did not permit a fresh acquisition", out.Output, err)
	}
}

func TestTemporaryTaskCallerDeadlineAndBodyReplacement(t *testing.T) {
	body := `local i=frame.input
 if i.command then module.on_event({kind="command",value=i.command}) end
 if i.receipt then module.on_event({receipt=i.receipt}) end
 local view=module.step(i.obs,{clock=i.clock},"1",{},false,true)
 if view.action then module.commit(view,view.action) end
 return {status=view.status,action=view.action}`
	for _, mode := range []string{"deadline", "no_default_timeout", "body_changed"} {
		t.Run(mode, func(t *testing.T) {
			host := urgencyModule(t, "encounter-tasks.lua", body)
			owner := map[string]any{}
			obs := map[string]any{"sessionId": "body", "selfGuid": "4", "visitId": "1", "mapId": float64(0),
				"players":    map[string]any{"4": map[string]any{"present": true, "aliveKnown": true, "alive": true, "positionKnown": true, "mapKnown": true, "mapId": float64(0), "movementKnown": true}},
				"navigation": map[string]any{"ownerGuid": "3", "movementOwner": owner}}
			command := map[string]any{"task": "wait_for_pull", "id": "w"}
			if mode == "deadline" {
				command["deadlineMonotonicMs"] = float64(1000)
			}
			input := map[string]any{"obs": obs, "clock": map[string]any{"monotonicMs": float64(0)}, "command": command}
			if _, err := host.Decide(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			delete(input, "command")
			owner["active"], owner["owner"], owner["remainingMs"] = true, "task:w", "3000"
			input["receipt"] = map[string]any{"outcome": "confirmed", "payload": map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": "task:w", "result": map[string]any{"token": "exact"}}}
			if _, err := host.Decide(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			delete(input, "receipt")
			input["clock"] = map[string]any{"monotonicMs": float64(1000000)}
			if mode == "body_changed" {
				obs["sessionId"] = "new-body"
			}
			out, err := host.Decide(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			status := out.Output["status"].(map[string]any)
			switch mode {
			case "deadline":
				if status["reason"] != "deadline" || out.Output["action"].(map[string]any)["mode"] != "release" {
					t.Fatal("caller deadline did not clean up", out.Output)
				}
			case "body_changed":
				if status["reason"] != "body_changed" || status["phase"] != "complete" || out.Output["action"] != nil {
					t.Fatal("old token was used against a new body", out.Output)
				}
			default:
				if status["phase"] != "waiting_for_owner_pull" || out.Output["action"] != nil {
					t.Fatal("script invented a default gameplay task timeout", out.Output)
				}
			}
		})
	}
}
