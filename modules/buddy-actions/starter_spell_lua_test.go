package buddyactions

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/agent-wow/agent-wow/behaviors"
	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func starterLuaDamage(id uint32, damage, castMS float64) map[string]any {
	return map[string]any{"known": true, "metadataKnown": true, "ready": true, "available": true,
		"capability": "offense", "familyId": id, "rank": 1, "level": 1, "powerType": 0,
		"cost": "10", "directDamage": damage, "castTimeMs": castMS, "globalCooldownMs": 1500,
		"minRange": 0, "maxRange": 30}
}

func starterLuaInput(rows map[string]any) map[string]any {
	return map[string]any{"sessionId": "starter-lua", "selfGuid": "4", "autoAttackKnown": false,
		"observation": map[string]any{"sessionId": "starter-lua", "selfGuid": "4", "spells": rows,
			"players": map[string]any{"4": map[string]any{"guid": "4", "classKnown": true, "classId": 8,
				"powerKnown": true, "powerType": 0, "power": 500, "maxPower": 500,
				"positionKnown": true, "x": 0, "y": 0, "z": 0}}},
		"target": map[string]any{"guid": "17379390974694817793", "positionKnown": true, "x": 5, "y": 0, "z": 0,
			"healthKnown": true, "health": 100, "maxHealth": 100, "ownDamageFamilies": []any{}}}
}

func starterLuaRuntime(t *testing.T, source string) *behavior.Runtime {
	t.Helper()
	runtime := newStarterSemanticRuntime(t)
	if _, err := runtime.Reload(source, ""); err != nil {
		t.Fatal(err)
	}
	return runtime
}

func assertStarterLuaChoice(t *testing.T, runtime *behavior.Runtime, input map[string]any, kind string, spell uint32) {
	t.Helper()
	out, err := runtime.Evaluate(input)
	if err != nil || len(out) != 2 || out["kind"] != kind || out["spellId"] != float64(spell) {
		t.Fatalf("want %s/%d, got %+v (%v), runtime %+v", kind, spell, out, err, runtime.Status())
	}
}

func starterLuaCaster(input map[string]any) map[string]any {
	return input["observation"].(map[string]any)["players"].(map[string]any)["4"].(map[string]any)
}

func TestStarterSpellLuaAllLearnedRanksAndLevelRankIDTies(t *testing.T) {
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	low, high := starterLuaDamage(100, 60, 1500), starterLuaDamage(200, 30, 1500)
	high["familyId"], high["rank"], high["level"] = 100, 2, 2
	input := starterLuaInput(map[string]any{"100": low, "200": high})
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
	high["directDamage"] = 60
	assertStarterLuaChoice(t, runtime, input, "cast", 200)
	low["level"], low["rank"], high["rank"] = 3, 1, 9
	assertStarterLuaChoice(t, runtime, input, "cast", 100) // Level precedes rank.
	high["level"] = 3
	assertStarterLuaChoice(t, runtime, input, "cast", 200)
	low["rank"] = 9
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
}

func TestStarterSpellLuaFinishUsesDirectDamageAndRawCastTime(t *testing.T) {
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	strong, instant := starterLuaDamage(100, 100, 1000), starterLuaDamage(200, 30, 0)
	input := starterLuaInput(map[string]any{"100": strong, "200": instant})
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
	target := input["target"].(map[string]any)
	target["health"] = 25 // Inclusive quarter-health finish boundary.
	assertStarterLuaChoice(t, runtime, input, "cast", 200)
	target["health"] = 25.001
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
	strong["directDamage"], instant["directDamage"] = 0, 15
	strong["periodicDamage"] = []any{map[string]any{"damagePerTick": 100, "periodMs": 2000}}
	target["health"] = 25
	assertStarterLuaChoice(t, runtime, input, "cast", 200)
	target["healthKnown"] = false
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
}

func TestStarterSpellLuaFixedPeriodicWindowAndWholeOwnedHybridSkip(t *testing.T) {
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	dot, direct := starterLuaDamage(100, 0, 0), starterLuaDamage(200, 30, 0)
	dot["periodicDamage"] = []any{map[string]any{"damagePerTick": 50, "periodMs": 12000}}
	input := starterLuaInput(map[string]any{"100": dot, "200": direct})
	assertStarterLuaChoice(t, runtime, input, "cast", 100) // At least one tick, even past eight seconds.
	dot["periodicDamage"] = []any{map[string]any{"damagePerTick": 10, "periodMs": 2000}}
	assertStarterLuaChoice(t, runtime, input, "cast", 100) // Fixed four ticks, no health-based shortening.
	dot["directDamage"] = 100
	input["target"].(map[string]any)["ownDamageFamilies"] = []any{100}
	assertStarterLuaChoice(t, runtime, input, "cast", 200) // Even the hybrid's direct component is skipped.
	input["target"].(map[string]any)["ownDamageFamilies"] = []any{}
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
}

func TestStarterSpellLuaInterruptionPenaltyBindsExactTargetAndIgnoresOrdinaryPressure(t *testing.T) {
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	cast, instant := starterLuaDamage(100, 100, 2000), starterLuaDamage(200, 60, 0)
	cast["globalCooldownMs"] = 10000 // The starter denominator ignores GCD.
	input := starterLuaInput(map[string]any{"100": cast, "200": instant})
	obs := input["observation"].(map[string]any)
	target := input["target"].(map[string]any)["guid"]
	obs["engagement"] = map[string]any{target.(string): map[string]any{"victimGuid": "4"}}
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
	for _, failure := range []int{40, 41} {
		obs["lastCast"] = map[string]any{"targetGuid": target, "failure": failure}
		assertStarterLuaChoice(t, runtime, input, "cast", 200)
	}
	obs["lastCast"] = map[string]any{"targetGuid": "99", "failure": 40}
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
	obs["lastCast"] = map[string]any{"targetGuid": target, "failure": 17}
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
}

func TestStarterSpellLuaReserveIncludesUnavailableUnaffordableFutureHeal(t *testing.T) {
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	damage := starterLuaDamage(100, 100, 1500)
	damage["cost"] = "20"
	heal := map[string]any{"known": true, "metadataKnown": true, "capability": "heal", "powerType": 0,
		"cost": "100", "ready": false, "available": false, "maxRange": 0, "cooldownMs": 60000}
	input := starterLuaInput(map[string]any{"100": damage, "200": heal})
	starterLuaCaster(input)["power"] = 119
	assertStarterLuaChoice(t, runtime, input, "wait", 0)
	starterLuaCaster(input)["power"] = 120
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
	starterLuaCaster(input)["power"] = 50
	assertStarterLuaChoice(t, runtime, input, "wait", 0) // Unaffordable heal still reserves its future cost.
	heal["known"] = false
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
	heal["known"], heal["capability"] = true, "offense"
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
}

func TestStarterSpellLuaLethalCooldownIsNotSuppressedByOrdinaryConservation(t *testing.T) {
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	strong, weak := starterLuaDamage(100, 50, 0), starterLuaDamage(200, 10, 0)
	strong["cooldownMs"] = 30000
	input := starterLuaInput(map[string]any{"100": strong, "200": weak})
	input["target"].(map[string]any)["health"] = 12
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
}

func TestStarterSpellLuaZeroScoreRemainsEligibleExceptPurePeriodicFinish(t *testing.T) {
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	zero := starterLuaDamage(100, 0, 0)
	input := starterLuaInput(map[string]any{"100": zero})
	input["autoAttackKnown"] = true
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
	zero["periodicDamage"] = []any{map[string]any{"damagePerTick": 10, "periodMs": 2000}}
	input["target"].(map[string]any)["health"] = 25
	assertStarterLuaChoice(t, runtime, input, "melee", 0) // Old finish path requires immediate damage.
}

func TestStarterSpellLuaMalformedKnownOffenseWaitsBeforeMeleeFallback(t *testing.T) {
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	changes := []struct {
		field string
		value any
	}{
		{"metadataKnown", false}, {"cost", nil}, {"cost", false}, {"cost", "1e1"}, {"cost", "01"},
		{"cost", "1.5"}, {"cost", "18446744073709551616"}, {"cost", 1.5},
		{"maxRange", nil}, {"maxRange", -1}, {"maxRange", "30"},
		{"minRange", -1}, {"minRange", "0"}, {"minRange", 31},
		{"castTimeMs", "0"}, {"castTimeMs", -1}, {"castTimeMs", 0.5},
		{"globalCooldownMs", -1}, {"cooldownMs", 4294967296}, {"categoryCooldownMs", false},
		{"rank", 1.5}, {"level", "1"}, {"familyId", -1}, {"powerType", false},
		{"directDamage", -1}, {"directDamage", "100"}, {"periodicDamage", false},
		{"periodicDamage", []any{false}},
		{"periodicDamage", []any{map[string]any{"damagePerTick": "10", "periodMs": 2000}}},
		{"periodicDamage", []any{map[string]any{"damagePerTick": 10, "periodMs": -1}}},
		{"periodicDamage", []any{map[string]any{"damagePerTick": 10, "periodMs": 1.5}}},
	}
	for _, change := range changes {
		t.Run(fmt.Sprintf("%s_%v", change.field, change.value), func(t *testing.T) {
			spell := starterLuaDamage(100, 100, 1500)
			spell[change.field] = change.value
			input := starterLuaInput(map[string]any{"100": spell})
			input["autoAttackKnown"] = true
			assertStarterLuaChoice(t, runtime, input, "wait", 0)
		})
	}
	bad := starterLuaDamage(200, 100, 1500)
	bad["cost"], bad["ready"] = "bad", false
	input := starterLuaInput(map[string]any{"100": starterLuaDamage(100, 100, 1500), "200": bad})
	input["autoAttackKnown"] = true
	assertStarterLuaChoice(t, runtime, input, "wait", 0) // Known unavailable rows still need valid mechanics.
	bad["known"] = false
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
	bad["known"], bad["powerType"] = true, 1
	assertStarterLuaChoice(t, runtime, input, "cast", 100) // Non-mana offense is outside this profile.
}

func TestStarterSpellLuaAcceptsNumericCostsAndOmittedScalarZeros(t *testing.T) {
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	spell := starterLuaDamage(100, 0, 0)
	for _, field := range []string{"familyId", "rank", "level", "castTimeMs", "globalCooldownMs", "cooldownMs", "categoryCooldownMs", "minRange", "directDamage", "periodicDamage"} {
		delete(spell, field)
	}
	input := starterLuaInput(map[string]any{"100": spell})
	spell["cost"] = 10
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
	spell["cost"] = "0"
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
	spell["maxRange"], input["autoAttackKnown"] = 0, true
	assertStarterLuaChoice(t, runtime, input, "melee", 0) // Explicit zero range stays bounded.
}

func TestStarterSpellLuaInvalidKnownHealCostWaitsWithoutMeleeFallback(t *testing.T) {
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	heal := map[string]any{"known": true, "metadataKnown": true, "capability": "heal", "powerType": 0}
	input := starterLuaInput(map[string]any{"100": starterLuaDamage(100, 100, 0), "200": heal})
	input["autoAttackKnown"] = true
	for _, cost := range []any{nil, false, -1, 1.5, "", "01", "bad", "18446744073709551616"} {
		t.Run(fmt.Sprintf("cost_%v", cost), func(t *testing.T) {
			heal["cost"] = cost
			assertStarterLuaChoice(t, runtime, input, "wait", 0)
		})
	}
	heal["cost"], heal["metadataKnown"] = "1", false
	assertStarterLuaChoice(t, runtime, input, "wait", 0)
	heal["known"] = false
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
	heal["known"], heal["metadataKnown"] = true, true
	starterLuaCaster(input)["power"] = 4294967295
	for _, cost := range []any{"4294967296", "18446744073709551615", float64(4294967296)} {
		heal["cost"] = cost
		assertStarterLuaChoice(t, runtime, input, "melee", 0) // Valid reserve exceeds every caster mana value.
	}
}

func TestStarterSpellLuaInclusiveCatalogRangeAndLegacyRoleFallback(t *testing.T) {
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	spell := starterLuaDamage(100, 100, 0)
	spell["minRange"], spell["maxRange"] = 2, 7
	input := starterLuaInput(map[string]any{"100": spell})
	target := input["target"].(map[string]any)
	for _, x := range []float64{2, 5} {
		target["x"] = x
		assertStarterLuaChoice(t, runtime, input, "cast", 100)
	}
	for _, x := range []float64{1.999, 5.001} {
		target["x"] = x
		assertStarterLuaChoice(t, runtime, input, "wait", 0)
	}
	input["autoAttackKnown"] = true
	assertStarterLuaChoice(t, runtime, input, "melee", 0)
	target["x"] = 5
	starterLuaCaster(input)["powerType"] = 1
	assertStarterLuaChoice(t, runtime, input, "melee", 0)
	starterLuaCaster(input)["powerType"] = 0
	starterLuaCaster(input)["classId"] = 4
	assertStarterLuaChoice(t, runtime, input, "melee", 0)
	input["autoAttackKnown"] = false
	assertStarterLuaChoice(t, runtime, input, "melee", 0)
	for _, field := range []string{"classKnown", "powerKnown", "positionKnown"} {
		starterLuaCaster(input)[field] = false
		assertStarterLuaChoice(t, runtime, input, "wait", 0)
		starterLuaCaster(input)[field] = true
	}
	input["sessionId"] = "replacement"
	assertStarterLuaChoice(t, runtime, input, "wait", 0)
}

func TestStarterSpellLuaSourceEditChangesSharedRankProfileInSameRuntime(t *testing.T) {
	source := behaviors.StarterSpellSource
	runtime := starterLuaRuntime(t, source)
	low, high := starterLuaDamage(100, 60, 1500), starterLuaDamage(200, 30, 1500)
	high["familyId"], high["rank"], high["level"] = 100, 2, 2
	input := starterLuaInput(map[string]any{"100": low, "200": high})
	assertStarterLuaChoice(t, runtime, input, "cast", 100)
	before := runtime.Status().Revision
	edited := strings.Replace(source, "all_ranks=true", "all_ranks=false", 1)
	if edited == source {
		t.Fatal("test did not edit the actual shared starter profile")
	}
	after, err := runtime.Reload(edited, before)
	if err != nil || after == before {
		t.Fatal("same-runtime source edit was not accepted", after, err)
	}
	assertStarterLuaChoice(t, runtime, input, "cast", 200)
}

func TestStarterSpellLuaOptionsLeaveOrdinarySelectorDefaultsUnchanged(t *testing.T) {
	source, err := os.ReadFile("../../behaviors/encounter-spells.lua")
	if err != nil {
		t.Fatal(err)
	}
	runtime := starterLuaRuntime(t, "local spells=(function()\n"+string(source)+
		"\nend)()\nreturn {decide=function(input)return {spellId=spells.damage_spell(input.observation,input.target,nil,input.options) or 0} end}")
	choose := func(input map[string]any, want float64) {
		t.Helper()
		out, err := runtime.Evaluate(input)
		if err != nil || out["spellId"] != want {
			t.Fatal("ordinary selector defaults changed", out, err, want)
		}
	}
	low, high := starterLuaDamage(100, 60, 1500), starterLuaDamage(200, 30, 1500)
	high["familyId"], high["rank"] = 100, 2
	choose(starterLuaInput(map[string]any{"100": low, "200": high}), 200) // One highest rank per family.
	cast, instant := starterLuaDamage(100, 100, 2000), starterLuaDamage(200, 60, 0)
	input := starterLuaInput(map[string]any{"100": cast, "200": instant})
	input["target"].(map[string]any)["health"], input["target"].(map[string]any)["maxHealth"] = 1000, 1000
	input["observation"].(map[string]any)["engagement"] = map[string]any{
		input["target"].(map[string]any)["guid"].(string): map[string]any{"victimGuid": "4"}}
	choose(input, 200) // Ordinary cast pressure still applies.
	cast["castTimeMs"], cast["globalCooldownMs"] = 1000, 10000
	delete(input["observation"].(map[string]any), "engagement")
	choose(input, 200) // Ordinary GCD remains part of the denominator.
	input["options"] = map[string]any{"max_cost": 0}
	choose(input, 200) // Known caster power historically overrides authored max_cost.
	starterLuaCaster(input)["powerKnown"] = false
	choose(input, 0) // Without known power, the authored cap still applies.
	starterLuaCaster(input)["powerKnown"] = true
	delete(input, "options")
	starterLuaCaster(input)["classId"], starterLuaCaster(input)["power"], starterLuaCaster(input)["maxPower"] = 5, 10, 100
	choose(input, 0) // Ordinary healer-class percentage reserve remains enabled.
	zero := starterLuaInput(map[string]any{"100": starterLuaDamage(100, 0, 0)})
	choose(zero, 0) // Ordinary zero-score ranks remain ineligible.
}
