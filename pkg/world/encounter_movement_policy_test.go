package world

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func movementPolicy(t *testing.T) *behavior.Session {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "encounter-movement.lua"))
	if err != nil {
		t.Fatal(err)
	}
	script := "local movement=(function()\n" + string(source) + "\nend)()\n" + `return {
 on_event=function() return true end,
 export_state=function() return {version=1,data={}} end,
 import_state=function() return true end,
 decide=function(frame)
 local input=frame.input
 if input.spell then return movement.for_spell(input.obs,"8",input.target,input.spell,input.options) end
 return movement.decide(input.obs,"8",input.target,input.options)
 end
 }`
	vm, err := behavior.NewSession(context.Background(), "move", script)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vm.Close)
	return vm
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
