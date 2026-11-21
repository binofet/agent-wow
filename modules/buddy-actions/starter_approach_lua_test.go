package buddyactions

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-wow/agent-wow/behaviors"
	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func starterApproachInput(class uint32, yards float64) map[string]any {
	return map[string]any{"selection": "practice_approach", "sessionId": "approach-lua", "selfGuid": "4", "autoAttackKnown": false,
		"observation": map[string]any{"sessionId": "approach-lua", "selfGuid": "4", "visitId": "1", "mapKnown": true, "mapId": 0,
			"players": map[string]any{"4": map[string]any{"guid": "4", "present": true, "classKnown": true, "classId": class,
				"mapKnown": true, "mapId": 0, "positionKnown": true, "x": 0, "y": 0, "z": 0}}},
		"target": map[string]any{"guid": "17379390974694817793", "present": true, "mapKnown": true, "mapId": 0,
			"positionKnown": true, "x": yards, "y": 0, "z": 0},
		"route": map[string]any{"known": false, "remainingYards": 0, "direct": false}}
}

func assertStarterApproachChoice(t *testing.T, runtime *behavior.Runtime, input map[string]any, kind string, yards float64) {
	t.Helper()
	output, err := runtime.Evaluate(input)
	if err != nil || len(output) != 2 || output["kind"] != kind || output["standYards"] != yards {
		t.Fatalf("want %s/%g, got %+v (%v), runtime %+v", kind, yards, output, err, runtime.Status())
	}
}

func editPracticeApproachTuning(t *testing.T, source, old, replacement string) string {
	t.Helper()
	if strings.Count(source, old) != 1 {
		t.Fatalf("actual shared movement tuning is no longer uniquely editable: %q", old)
	}
	return strings.Replace(source, old, replacement, 1)
}

func TestStarterApproachLuaInclusiveClassStandoffAndVerifiedRouteIntent(t *testing.T) {
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	for _, test := range []struct {
		class uint32
		yards float64
	}{{4, 3.5}, {5, 25}, {8, 25}} {
		t.Run(fmt.Sprint(test.class), func(t *testing.T) {
			input := starterApproachInput(test.class, test.yards)
			assertStarterApproachChoice(t, runtime, input, "arrived", test.yards)
			input["target"].(map[string]any)["x"] = test.yards + .001
			assertStarterApproachChoice(t, runtime, input, "approach", test.yards)
			input["target"].(map[string]any)["x"] = test.yards - 1
			route := input["route"].(map[string]any)
			route["known"], route["remainingYards"], route["direct"] = true, test.yards, true
			assertStarterApproachChoice(t, runtime, input, "arrived", test.yards)
			route["remainingYards"] = test.yards + .001
			assertStarterApproachChoice(t, runtime, input, "approach", test.yards)
			route["remainingYards"], route["direct"] = test.yards, false
			assertStarterApproachChoice(t, runtime, input, "approach", test.yards)
		})
	}
}

func TestStarterApproachLuaUnknownIdentityPoseAndRouteFactsFailClosed(t *testing.T) {
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	for name, change := range map[string]func(map[string]any){
		"session": func(input map[string]any) { input["sessionId"] = "replacement" },
		"self": func(input map[string]any) {
			input["observation"].(map[string]any)["players"].(map[string]any)["4"].(map[string]any)["guid"] = "5"
		},
		"unknown_class": func(input map[string]any) {
			input["observation"].(map[string]any)["players"].(map[string]any)["4"].(map[string]any)["classKnown"] = false
		},
		"unsupported_class": func(input map[string]any) {
			input["observation"].(map[string]any)["players"].(map[string]any)["4"].(map[string]any)["classId"] = 9
		},
		"missing_target": func(input map[string]any) { delete(input, "target") },
		"unknown_pose":   func(input map[string]any) { input["target"].(map[string]any)["positionKnown"] = false },
		"foreign_map":    func(input map[string]any) { input["target"].(map[string]any)["mapId"] = 1 },
		"text_position":  func(input map[string]any) { input["target"].(map[string]any)["x"] = "20" },
		"missing_route":  func(input map[string]any) { delete(input, "route") },
		"unknown_route":  func(input map[string]any) { input["route"].(map[string]any)["known"] = "false" },
		"negative_remaining": func(input map[string]any) {
			input["route"] = map[string]any{"known": true, "remainingYards": -1, "direct": true}
		},
		"unknown_direct": func(input map[string]any) {
			input["route"] = map[string]any{"known": true, "remainingYards": 20}
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := starterApproachInput(8, 20)
			change(input)
			assertStarterApproachChoice(t, runtime, input, "wait", 0)
		})
	}
}

func TestStarterApproachLuaActualMovementTuningCASChangesUnchangedFrame(t *testing.T) {
	source := behaviors.StarterSpellSource
	runtime := starterLuaRuntime(t, source)
	input := starterApproachInput(8, 20)
	before, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	assertStarterApproachChoice(t, runtime, input, "arrived", 25)
	initial := runtime.Status().Revision
	edited := editPracticeApproachTuning(t, source, "caster_yards=25", "caster_yards=15")
	revision, err := runtime.Reload(edited, initial)
	if err != nil || revision == initial {
		t.Fatal(revision, err)
	}
	assertStarterApproachChoice(t, runtime, input, "approach", 15)
	if got, err := runtime.Reload(source, initial); err == nil || got != revision {
		t.Fatal("stale CAS replaced accepted movement tuning", got, err)
	}
	assertStarterApproachChoice(t, runtime, input, "approach", 15)
	after, err := json.Marshal(input)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("movement policy mutated observed poses or route facts", err)
	}
	invalid := editPracticeApproachTuning(t, edited, "caster_yards=15", "caster_yards='15'")
	if _, err := runtime.Reload(invalid, revision); err != nil {
		t.Fatal(err)
	}
	assertStarterApproachChoice(t, runtime, input, "wait", 0)
}

func TestStarterApproachLuaActualEncounterBundlePassesLateSharedMovementModule(t *testing.T) {
	bundle, err := exec.Command("python3", "-B", filepath.Join("..", "..", "dev", "encounter-bundle")).Output()
	if err != nil {
		t.Fatal(err)
	}
	fullSource := string(bundle)
	starterAt := strings.Index(fullSource, "local encounter_starter_spells = (function()")
	movementAt := strings.Index(fullSource, "local encounter_movement = (function()")
	if starterAt < 0 || movementAt <= starterAt {
		t.Fatal("actual generator no longer places starter before the shared movement module")
	}
	// Exercise the real session export, whose explicit module argument must
	// bridge the generator's lexical order. No embedded-source substitute is used.
	source := "local policy=(function()\n" + fullSource + "\nend)()\nreturn {decide=policy.starter_spell}"
	runtime := starterLuaRuntime(t, source)
	input := starterApproachInput(8, 20)
	input["route"] = map[string]any{"known": true, "remainingYards": 20, "direct": true}
	before, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	assertStarterApproachChoice(t, runtime, input, "arrived", 25)
	initial := runtime.Status().Revision
	edited := editPracticeApproachTuning(t, source, "caster_yards=25", "caster_yards=15")
	revision, err := runtime.Reload(edited, initial)
	if err != nil || revision == initial {
		t.Fatal("actual generated bundle rejected shared movement tuning CAS", revision, err)
	}
	assertStarterApproachChoice(t, runtime, input, "approach", 15)
	after, err := json.Marshal(input)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("generated session export mutated the unchanged movement frame", err)
	}
}
