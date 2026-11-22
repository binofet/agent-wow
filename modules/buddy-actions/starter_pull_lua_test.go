package buddyactions

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-wow/agent-wow/behaviors"
)

func starterPullInput(health, maximum, mana, base uint32) map[string]any {
	return map[string]any{"selection": "coldridge_pull_readiness", "sessionId": "pull-lua", "selfGuid": "8", "autoAttackKnown": false,
		"observation": map[string]any{"sessionId": "pull-lua", "selfGuid": "8", "players": map[string]any{
			"8": map[string]any{"guid": "8", "classKnown": true, "classId": 9,
				"healthKnown": true, "health": health, "maxHealth": maximum,
				"powerKnown": true, "powerType": 0, "power": mana, "baseManaKnown": true, "baseMana": base}}}}
}

func starterPullPlayer(input map[string]any) map[string]any {
	return input["observation"].(map[string]any)["players"].(map[string]any)["8"].(map[string]any)
}

func editColdridgePullTuning(t *testing.T, source, old, replacement string) string {
	t.Helper()
	const prefix = "M.coldridge_pull = {"
	start := strings.Index(source, prefix)
	if start < 0 {
		t.Fatal("missing actual Coldridge tuning block")
	}
	end := strings.IndexByte(source[start:], '\n')
	if end < 0 {
		t.Fatal("missing Coldridge tuning boundary")
	}
	tuning := source[start : start+end]
	if strings.Count(tuning, old) != 1 {
		t.Fatalf("actual shared pull tuning is no longer uniquely editable: %q", old)
	}
	return source[:start] + strings.Replace(tuning, old, replacement, 1) + source[start+end:]
}

func TestStarterPullLuaTargetFreeInclusiveHealthAndCeilingManaBoundaries(t *testing.T) {
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	for _, test := range []struct {
		health, maximum, mana, base uint32
		want                        string
	}{
		{95, 100, 59, 98, "ready"}, {94, 100, 59, 98, "wait"}, {95, 100, 58, 98, "wait"},
		{94, 98, 62, 102, "ready"}, {93, 98, 62, 102, "wait"}, {94, 98, 61, 102, "wait"},
		{4080218931, 4294967295, 2576980377, 4294967295, "ready"},
		{4080218930, 4294967295, 2576980377, 4294967295, "wait"},
		{4080218931, 4294967295, 2576980376, 4294967295, "wait"},
	} {
		t.Run(fmt.Sprintf("%d/%d/%d/%d", test.health, test.maximum, test.mana, test.base), func(t *testing.T) {
			input := starterPullInput(test.health, test.maximum, test.mana, test.base)
			if input["target"] != nil || input["spells"] != nil || starterPullPlayer(input)["positionKnown"] != nil {
				t.Fatal("pull readiness fixture invented a target, spells or a pose")
			}
			assertStarterLuaChoice(t, runtime, input, test.want, 0)
		})
	}
}

func TestStarterPullLuaRejectsUnknownFactsAndSharesPredicateWithColdCast(t *testing.T) {
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	for field, value := range map[string]any{"classKnown": false, "classId": 8, "healthKnown": false,
		"health": -1, "maxHealth": 0, "powerKnown": false, "power": 1.5, "powerType": 3,
		"baseManaKnown": false, "baseMana": "98"} {
		t.Run(field, func(t *testing.T) {
			input := starterPullInput(95, 100, 59, 98)
			starterPullPlayer(input)[field] = value
			assertStarterLuaChoice(t, runtime, input, "wait", 0)
		})
	}
	for _, field := range []string{"guid", "healthKnown", "health", "maxHealth", "powerKnown", "power", "powerType", "baseManaKnown", "baseMana"} {
		t.Run("missing/"+field, func(t *testing.T) {
			input := starterPullInput(95, 100, 59, 98)
			delete(starterPullPlayer(input), field)
			assertStarterLuaChoice(t, runtime, input, "wait", 0)
		})
	}
	for name, change := range map[string]func(map[string]any){
		"overflow":          func(input map[string]any) { starterPullPlayer(input)["baseMana"] = float64(4294967296) },
		"zero_base":         func(input map[string]any) { starterPullPlayer(input)["baseMana"] = 0 },
		"incoherent_health": func(input map[string]any) { starterPullPlayer(input)["health"] = 101 },
		"foreign_session":   func(input map[string]any) { input["sessionId"] = "replacement" },
		"foreign_player":    func(input map[string]any) { starterPullPlayer(input)["guid"] = "9" },
	} {
		t.Run(name, func(t *testing.T) {
			input := starterPullInput(95, 100, 59, 98)
			change(input)
			assertStarterLuaChoice(t, runtime, input, "wait", 0)
		})
	}
	// Actual spell mechanics are identical. Only a new pull needs the shared
	// readiness predicate; an ongoing affordable own fight retains its cast.
	input := starterLuaInput(map[string]any{"686": starterLuaDamage(686, 14, 1700)})
	self := starterLuaCaster(input)
	self["classId"], self["healthKnown"], self["health"], self["maxHealth"] = 9, true, 94, 100
	self["baseManaKnown"], self["baseMana"], self["power"] = true, 98, 59
	input["selection"], input["pullRequired"] = "coldridge_cast", true
	assertStarterLuaChoice(t, runtime, input, "pull_wait", 0)
	self["health"] = 95
	assertStarterLuaChoice(t, runtime, input, "cast", shadowBolt)
	self["health"], input["pullRequired"] = 50, false
	assertStarterLuaChoice(t, runtime, input, "cast", shadowBolt)
	delete(input, "pullRequired")
	assertStarterLuaChoice(t, runtime, input, "wait", 0)
	delete(input, "selection")
	assertStarterLuaChoice(t, runtime, input, "cast", shadowBolt) // Generic starter behavior is unchanged.
}

func TestStarterPullLuaRealTuningCASChangesSameObservedFrameWithoutMutation(t *testing.T) {
	source := behaviors.StarterSpellSource
	runtime := starterLuaRuntime(t, source)
	input := starterPullInput(94, 100, 58, 98)
	before, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	assertStarterLuaChoice(t, runtime, input, "wait", 0)
	initial := runtime.Status().Revision
	healthEdit := editColdridgePullTuning(t, source, "health_percent=95", "health_percent=94")
	healthRevision, err := runtime.Reload(healthEdit, initial)
	if err != nil || healthRevision == initial {
		t.Fatal(healthRevision, err)
	}
	assertStarterLuaChoice(t, runtime, input, "wait", 0) // Mana still needs the same predicate's edit.
	manaEdit := editColdridgePullTuning(t, healthEdit, "base_mana_percent=60", "base_mana_percent=59")
	revision, err := runtime.Reload(manaEdit, healthRevision)
	if err != nil || revision == healthRevision {
		t.Fatal(revision, err)
	}
	assertStarterLuaChoice(t, runtime, input, "ready", 0)
	if got, err := runtime.Reload(source, initial); err == nil || got != revision {
		t.Fatal("stale tuning CAS replaced accepted source", got, err)
	}
	assertStarterLuaChoice(t, runtime, input, "ready", 0)
	after, err := json.Marshal(input)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("policy tuning mutated the observed resource frame", err)
	}
	malformed := editColdridgePullTuning(t, manaEdit, "health_percent=94", "health_percent='94'")
	if _, err := runtime.Reload(malformed, revision); err != nil {
		t.Fatal(err)
	}
	assertStarterLuaChoice(t, runtime, input, "wait", 0)
}
