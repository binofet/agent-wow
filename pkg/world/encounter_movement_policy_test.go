package world

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func TestEncounterMovementRecoversOnlyAfterActualStallAndRechecksRouteBetweenProbes(t *testing.T) {
	vm, ctx := movementPolicy(t), context.Background()
	obs := movementObservation()
	obs["navigation"] = map[string]any{"bodyMovementSupported": true}
	self := obs["players"].(map[string]any)["5"].(map[string]any)
	self["positionRevision"] = "1"
	obs["targetMovement"] = map[string]any{"targetGuid": "99", "visitId": "1", "phase": "follow_navigation_unavailable"}
	decide := func(sequence int) behavior.Decision {
		t.Helper()
		out, err := vm.Decide(ctx, map[string]any{"obs": obs, "target": "99", "sequence": strconv.Itoa(sequence), "options": map[string]any{"rangeYards": 4.5, "requireLos": true}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	for sequence := 1; sequence < 3; sequence++ {
		if got := decide(sequence).Output["action"].(map[string]any)["capability"]; got != "approach_target" {
			t.Fatal("stale single rejection triggered probe", got)
		}
	}
	for _, sequence := range []int{3, 8, 13} {
		decision := decide(sequence)
		action := decision.Output["action"].(map[string]any)
		if action["capability"] != "move_body" || action["visitId"] != "1" || action["sequenceId"] != strconv.Itoa(sequence) || action["jump"] != (sequence == 13) {
			t.Fatal("missing relative/alternative/hop recovery", decision.Output)
		}
		id, err := vm.ReserveAction(decision)
		if err != nil {
			t.Fatal(err)
		}
		self["x"] = float64(sequence) / 5
		_, err = vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "move", ActionID: id, Outcome: behavior.ActionSettledUnverified,
			Payload: map[string]any{"capability": "move_body", "sequence_id": strconv.Itoa(sequence), "phase": "body_input_written", "effect_observed": false}}})
		if err != nil {
			t.Fatal(err)
		}
		if got := decide(sequence + 1).Output["action"].(map[string]any)["capability"]; got != "approach_target" {
			t.Fatal("probe skipped normal route recheck", got)
		}
		if sequence < 13 {
			for next := sequence + 2; next < sequence+5; next++ {
				if got := decide(next).Output["action"].(map[string]any)["capability"]; got != "approach_target" {
					t.Fatal("probe ignored fresh local progress", got)
				}
			}
		}
	}
	// Actual progress and a new authoritative pose reset stale failure proof.
	self["positionRevision"] = "2"
	self["x"] = float64(4)
	if got := decide(19).Output["action"].(map[string]any)["capability"]; got != "approach_target" {
		t.Fatal("stale failure survived correction", got)
	}
}

func movementPolicy(t *testing.T) *behavior.Session {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "encounter-movement.lua"))
	if err != nil {
		t.Fatal(err)
	}
	script := "local movement=(function()\n" + string(source) + "\nend)()\n" + `return {
 on_event=movement.on_event,
 export_state=function() return {version=1,data={}} end,
 import_state=function() return true end,
 decide=function(frame)
 local input=frame.input
 if input.cast then return {action={capability="offense"}} end
 if input.idle then return movement.idle_recovery(input.obs,input.sequence or "8") end
 if input.spell then return movement.for_spell(input.obs,input.sequence or "8",input.target,input.spell,input.options) end
 return movement.decide(input.obs,input.sequence or "8",input.target,input.options)
 end
 }`
	vm, err := behavior.NewSession(context.Background(), "move", script)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vm.Close)
	return vm
}

func TestEncounterMovementRetriesRequestedProbeAfterGravityAndRouteRecheck(t *testing.T) {
	vm, ctx := movementPolicy(t), context.Background()
	obs := movementObservation()
	obs["navigation"] = map[string]any{"bodyMovementSupported": true}
	obs["targetMovement"] = map[string]any{"targetGuid": "99", "visitId": "1", "phase": "follow_navigation_unavailable"}
	decide := func(sequence int) behavior.Decision {
		t.Helper()
		out, err := vm.Decide(ctx, map[string]any{"obs": obs, "target": "99", "sequence": strconv.Itoa(sequence), "options": map[string]any{"rangeYards": 4.5}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	decide(1)
	decide(2)
	first := decide(3)
	probe := first.Output["action"].(map[string]any)
	id, err := vm.ReserveAction(first)
	if err != nil || probe["capability"] != "move_body" {
		t.Fatal("missing initial probe", first, err)
	}
	_, err = vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "move", ActionID: id, Outcome: behavior.ActionSettledUnverified,
		Payload: map[string]any{"capability": "move_body", "sequence_id": "3", "phase": "body_settled_written", "effect_observed": false}}})
	if err != nil {
		t.Fatal(err)
	}
	obs["players"].(map[string]any)["5"].(map[string]any)["z"] = float64(-1)
	for sequence := 4; sequence < 8; sequence++ {
		if got := decide(sequence).Output["action"].(map[string]any)["capability"]; got != "approach_target" {
			t.Fatal("settling skipped normal route recheck or fresh progress", sequence, got)
		}
	}
	retry := decide(8).Output["action"].(map[string]any)
	if retry["capability"] != "move_body" || retry["bearingRadians"] != probe["bearingRadians"] || retry["jump"] != probe["jump"] {
		t.Fatal("gravity consumed the requested probe", probe, retry)
	}
}

func movementObservation() map[string]any {
	return map[string]any{"sessionId": "move", "selfGuid": "5", "visitId": "1", "mapId": float64(0), "activeMeleeTargetGuid": "99",
		"players": map[string]any{"5": map[string]any{"present": true, "positionKnown": true, "x": float64(0), "y": float64(0), "z": float64(0)}},
		"targets": map[string]any{"99": map[string]any{"present": true, "positionKnown": true, "mapKnown": true, "mapId": float64(0), "x": float64(20), "y": float64(0), "z": float64(0), "orientation": float64(0)}}}
}

func TestEncounterMovementKeepsChasingDuringAutoswingAndNeedsNativeLos(t *testing.T) {
	vm := movementPolicy(t)
	obs := movementObservation()
	input := map[string]any{"obs": obs, "target": "99", "options": map[string]any{"rangeYards": float64(4.5), "requireLos": true, "behind": true}}
	decision, err := vm.Decide(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	action, _ := decision.Output["action"].(map[string]any)
	if action["capability"] != "approach_target" || action["targetGuid"] != "99" || action["behind"] != true || action["visitId"] != "1" {
		t.Fatalf("autoswing stopped movement: %+v", decision.Output)
	}
	// Being geometrically in range is insufficient until the native LoS/facing
	// step has succeeded. A caster must actually move around an obstruction.
	input["options"] = map[string]any{"rangeYards": float64(30), "requireLos": true}
	decision, err = vm.Decide(context.Background(), input)
	if err != nil || decision.Output["action"] == nil {
		t.Fatalf("LoS skipped: %+v %v", decision.Output, err)
	}
	obs["targetMovement"] = map[string]any{"targetGuid": "99", "visitId": "1", "rangeYards": float64(30), "requireLos": true, "ready": true}
	decision, err = vm.Decide(context.Background(), input)
	if err != nil || decision.Output["action"] != nil || decision.Output["ready"] != true {
		t.Fatalf("validated position refused: %+v %v", decision.Output, err)
	}
	target := obs["targets"].(map[string]any)["99"].(map[string]any)
	target["trajectory"] = map[string]any{"known": true, "durationMs": float64(1000), "ageMs": float64(500), "destinationKnown": true, "destinationX": float64(60), "destinationY": float64(0), "destinationZ": float64(0)}
	decision, err = vm.Decide(context.Background(), input)
	if err != nil || decision.Output["action"] == nil || decision.Output["ready"] != false {
		t.Fatalf("moving runner reused old readiness: %+v %v", decision.Output, err)
	}
}

func TestEncounterMovementDerivesCasterRangeFromReflectedSpell(t *testing.T) {
	vm := movementPolicy(t)
	obs := movementObservation()
	decision, err := vm.Decide(context.Background(), map[string]any{"obs": obs, "target": "99", "spell": map[string]any{"maxRange": float64(30)}})
	action, _ := decision.Output["action"].(map[string]any)
	if err != nil || action["rangeYards"] != float64(30) || action["requireLos"] != true {
		t.Fatalf("reflected range: %+v %v", decision.Output, err)
	}
}

func TestEncounterMovementFailedHostileCastRechecksMovingCoverBeforeRetry(t *testing.T) {
	for _, capability := range []string{"offense", "polymorph", "sap", "heal"} {
		t.Run(capability, func(t *testing.T) {
			vm := movementPolicy(t)
			ctx := context.Background()
			obs := movementObservation()
			view := map[string]any{"targetGuid": "99", "visitId": "1", "rangeYards": float64(30), "requireLos": true, "ready": true}
			obs["targetMovement"] = view
			input := map[string]any{"obs": obs, "target": "99", "options": map[string]any{"rangeYards": float64(30), "requireLos": true}}
			decision, err := vm.Decide(ctx, input)
			if err != nil || decision.Output["ready"] != true {
				t.Fatal("initial clear position", decision.Output, err)
			}
			// A moving target reaches cover without leaving the spell's radius. The
			// last native ready result still says true when its cast is rejected.
			cast, err := vm.Decide(ctx, map[string]any{"cast": true})
			if err != nil {
				t.Fatal(err)
			}
			id, err := vm.ReserveAction(cast)
			if err != nil {
				t.Fatal(err)
			}
			outcome := behavior.ActionFailed
			if capability == "sap" {
				outcome = behavior.ActionNotDispatched
			}
			if _, err = vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "move", ActionID: id, Outcome: outcome,
				Payload: map[string]any{"capability": capability, "target_guid": "99", "effect_observed": false}}}); err != nil {
				t.Fatal(err)
			}
			decision, err = vm.Decide(ctx, input)
			action, _ := decision.Output["action"].(map[string]any)
			if err != nil || action["capability"] != "approach_target" || decision.Output["ready"] != false {
				t.Fatalf("repeated cast from stale clear position: %+v %v", decision.Output, err)
			}
			id, err = vm.ReserveAction(decision)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "move", ActionID: id, Outcome: behavior.ActionSettledUnverified,
				Payload: map[string]any{"capability": "approach_target", "target_guid": "99"}}}); err != nil {
				t.Fatal(err)
			}
			view["ready"] = false
			decision, err = vm.Decide(ctx, input)
			if err != nil || decision.Output["action"] == nil || decision.Output["ready"] != false {
				t.Fatal("blocked LoS stopped approach", decision.Output, err)
			}
			// The mechanic now has a fresh clear position after walking around the
			// cover. Only then may the caller resume its original cast.
			view["ready"] = true
			decision, err = vm.Decide(ctx, input)
			if err != nil || decision.Output["ready"] != true || decision.Output["action"] != nil {
				t.Fatal("fresh clear position did not resume cast", decision.Output, err)
			}
		})
	}
}

func TestEncounterMovementAcceptsProtobufJSONTrajectoryAge(t *testing.T) {
	for _, age := range []string{"20", "28369"} {
		t.Run(age, func(t *testing.T) {
			vm := movementPolicy(t)
			obs := movementObservation()
			target := obs["targets"].(map[string]any)["99"].(map[string]any)
			// Real protojson: CreatureTrajectory.age_ms is uint64, so the wire JSON
			// value is a string even though duration_ms is a numeric uint32.
			target["trajectory"] = map[string]any{"known": true, "durationMs": float64(77), "ageMs": age,
				"destinationKnown": true, "destinationX": float64(25), "destinationY": float64(0), "destinationZ": float64(0)}
			obs["targetMovement"] = map[string]any{"targetGuid": "99", "visitId": "1", "rangeYards": float64(30), "requireLos": true, "ready": true}
			decision, err := vm.Decide(context.Background(), map[string]any{"obs": obs, "target": "99", "options": map[string]any{"rangeYards": float64(30), "requireLos": true}})
			if err != nil || decision.Output["ready"] != true || decision.Output["action"] != nil {
				t.Fatalf("server trajectory age caused infinite positioning despite native ready: %+v %v", decision.Output, err)
			}
		})
	}
}

func TestEncounterMovementAlternatingApproachKeepsActualStallEvidence(t *testing.T) {
	for _, idle := range []bool{false, true} {
		name := "target_approach"
		if idle {
			name = "idle_formation"
		}
		t.Run(name, func(t *testing.T) {
			vm, ctx := movementPolicy(t), context.Background()
			obs := movementObservation()
			self := obs["players"].(map[string]any)["5"].(map[string]any)
			self["positionRevision"], self["moving"] = "1", false
			nav := map[string]any{"bodyMovementSupported": true, "following": true, "goalKnown": true, "goalX": float64(20)}
			view := map[string]any{"targetGuid": "99", "visitId": "1"}
			obs["navigation"], obs["targetMovement"] = nav, view
			probes, recheck := 0, false
			for sequence := 1; sequence <= 18 && (probes < 3 || recheck); sequence++ {
				phase := "follow_navigation_unavailable"
				if sequence%2 == 0 {
					phase = "approaching"
					if idle {
						phase = "following_formation"
					}
				}
				view["phase"], nav["phase"] = phase, phase
				decision, err := vm.Decide(ctx, map[string]any{"idle": idle, "obs": obs, "target": "99", "sequence": strconv.Itoa(sequence), "options": map[string]any{"rangeYards": 4.5}})
				if err != nil {
					t.Fatal(err)
				}
				action, _ := decision.Output["action"].(map[string]any)
				if recheck {
					if action["capability"] == "move_body" || !idle && action["capability"] != "approach_target" {
						t.Fatal("correlated probe receipt skipped the ordinary route recheck", sequence, decision.Output)
					}
					recheck = false
				}
				if action["capability"] != "move_body" {
					continue
				}
				if sequence < 5 || action["sequenceId"] != strconv.Itoa(sequence) || action["visitId"] != "1" {
					t.Fatal("probe was not bound to three fresh stationary rejections", sequence, action)
				}
				wantBearing := float64(0)
				if probes == 1 {
					wantBearing = 1.5707963268
				}
				if action["bearingRadians"] != wantBearing || action["jump"] != (probes == 2) {
					t.Fatal("stationary alternating statuses reset the forward/left/hop progression", probes, action)
				}
				id, err := vm.ReserveAction(decision)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
					SessionID: "move", ActionID: id, Outcome: behavior.ActionSettledUnverified,
					Payload: map[string]any{"capability": "move_body", "sequence_id": strconv.Itoa(sequence), "phase": "body_input_written", "effect_observed": false},
				}}); err != nil {
					t.Fatal(err)
				}
				probes++
				recheck = true
			}
			if probes != 3 || recheck || self["positionRevision"] != "1" || self["x"] != float64(0) {
				t.Fatal("alternating non-progress statuses never reached step/side/hop recovery", probes, self)
			}
		})
	}
}
