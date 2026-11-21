package buddyactions

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/encoding/protojson"
)

// Real catalog/weapon reflection, serialized without EmitUnpopulated so zero
// cast time, orientation, minimum range and false flags remain genuinely absent.
func starterWeaponLuaInput(t *testing.T, s *Service, fake *fakeSession) map[string]any {
	t.Helper()
	obs := &actionsv1.EncounterObservationResponse{SessionId: "starter-weapon-lua", SelfGuid: s.selfGUID,
		Players: map[string]*actionsv1.EncounterPlayer{}, Spells: map[string]*actionsv1.EncounterSpell{}}
	s.encounterPlayers(fake.world, nil, obs)
	s.encounterSpells(fake.world, nil, obs)
	raw, err := protojson.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	var observation map[string]any
	if err := json.Unmarshal(raw, &observation); err != nil {
		t.Fatal(err)
	}
	target := &actionsv1.EncounterCreature{Guid: enemyGUID, PositionKnown: true, X: 3,
		HealthKnown: true, Health: 1000, MaxHealth: 1000}
	raw, err = protojson.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	var creature map[string]any
	if err := json.Unmarshal(raw, &creature); err != nil {
		t.Fatal(err)
	}
	return map[string]any{"selection": "weapon_strike", "sessionId": obs.SessionId,
		"selfGuid": encounterGUID(s.selfGUID), "observation": observation, "target": creature, "autoAttackKnown": false}
}

func starterWeaponLuaPlayer(input map[string]any) map[string]any {
	return input["observation"].(map[string]any)["players"].(map[string]any)[input["selfGuid"].(string)].(map[string]any)
}

func starterWeaponLuaSpells(input map[string]any) map[string]any {
	return input["observation"].(map[string]any)["spells"].(map[string]any)
}

func TestStarterWeaponLuaUsesReflectedModelAndSparseProtoZeros(t *testing.T) {
	s, fake, _ := instantStrikeFixture(t, 1760)
	input := starterWeaponLuaInput(t, s, fake)
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	backstab := starterWeaponLuaSpells(input)["53"].(map[string]any)
	if backstab["directDamage"] != 52.5 || backstab["weaponDamageMultiplier"] != 1.5 || backstab["damageModelKnown"] != true {
		t.Fatal("fixture did not reflect actual base weapon damage/effect order", backstab)
	}
	for _, field := range []string{"castTimeMs", "minRange", "requiresStealth"} {
		if _, present := backstab[field]; present {
			t.Fatal("fixture masked an omitted protobuf zero", field, backstab)
		}
	}
	if _, present := starterWeaponLuaPlayer(input)["orientation"]; present {
		t.Fatal("fixture masked the omitted zero facing angle")
	}
	assertStarterLuaChoice(t, runtime, input, "cast", 53)
	delete(input, "selection")
	assertStarterLuaChoice(t, runtime, input, "melee", 0) // Initial role dispatch is unchanged.
}

func TestStarterWeaponLuaComboFinisherUsesExactTargetAndObservedFivePoints(t *testing.T) {
	s, fake, _ := instantStrikeFixture(t, 1760)
	input := starterWeaponLuaInput(t, s, fake)
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	input["target"].(map[string]any)["orientation"] = math.Pi // Exclude positional Backstab.
	player := starterWeaponLuaPlayer(input)
	finisher := starterWeaponLuaSpells(input)["6762"].(map[string]any)
	if finisher["requiresComboPoints"] != true || finisher["damageModelKnown"] == true {
		t.Fatal("fixture invented combo damage", finisher)
	}
	assertStarterLuaChoice(t, runtime, input, "cast", 1760)
	player["comboKnown"], player["comboPoints"], player["comboTargetGuid"] = true, 4, encounterGUID(enemyGUID)
	assertStarterLuaChoice(t, runtime, input, "cast", 1760)
	player["comboPoints"] = 5
	assertStarterLuaChoice(t, runtime, input, "cast", 6762)
	player["comboTargetGuid"] = encounterGUID(enemyGUID + 1)
	assertStarterLuaChoice(t, runtime, input, "cast", 1760)
	player["comboKnown"], player["comboPoints"], player["comboTargetGuid"] = false, "unused", false
	assertStarterLuaChoice(t, runtime, input, "cast", 1760)
}

func TestStarterWeaponLuaUsesAdjustedEnergyAndMechanicalReadiness(t *testing.T) {
	s, fake, _ := instantStrikeFixture(t, 1760)
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	fake.world.Self.Power = 40
	s.improvedSinister2 = true
	input := starterWeaponLuaInput(t, s, fake)
	input["target"].(map[string]any)["orientation"] = math.Pi
	strike := starterWeaponLuaSpells(input)["1760"].(map[string]any)
	if strike["cost"] != "40" || strike["ready"] != true {
		t.Fatal("fixture did not reflect actual talent-adjusted energy cost", strike)
	}
	assertStarterLuaChoice(t, runtime, input, "cast", 1760)
	fake.world.Self.Power = 39
	input = starterWeaponLuaInput(t, s, fake)
	assertStarterLuaChoice(t, runtime, input, "melee", 0)
	fake.world.Self.Power, s.improvedSinister2 = 44, false
	input = starterWeaponLuaInput(t, s, fake)
	assertStarterLuaChoice(t, runtime, input, "melee", 0)
	fake.world.Self.Power = 100
	input = starterWeaponLuaInput(t, s, fake)
	for _, value := range starterWeaponLuaSpells(input) {
		value.(map[string]any)["ready"] = false
	}
	assertStarterLuaChoice(t, runtime, input, "melee", 0) // Ready owns cooldown, weapon and stealth availability.
	opener := starterWeaponLuaSpells(input)["53"].(map[string]any)
	opener["requiresStealth"] = true
	assertStarterLuaChoice(t, runtime, input, "melee", 0)
	opener["ready"] = true
	assertStarterLuaChoice(t, runtime, input, "cast", 53) // Native Ready supplies the actual stealth proof.
}

func TestStarterWeaponLuaUsesSharedFacingBehindAndCatalogRange(t *testing.T) {
	s, fake, _ := instantStrikeFixture(t, 1760)
	input := starterWeaponLuaInput(t, s, fake)
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	target := input["target"].(map[string]any)
	assertStarterLuaChoice(t, runtime, input, "cast", 53)
	target["orientation"] = math.Pi
	assertStarterLuaChoice(t, runtime, input, "cast", 1760)
	starterWeaponLuaPlayer(input)["orientation"] = math.Pi
	assertStarterLuaChoice(t, runtime, input, "melee", 0)
	starterWeaponLuaPlayer(input)["orientation"] = 0
	target["x"] = 5
	assertStarterLuaChoice(t, runtime, input, "cast", 1760)
	target["x"] = 5.001
	assertStarterLuaChoice(t, runtime, input, "melee", 0)
	target["positionKnown"] = false
	assertStarterLuaChoice(t, runtime, input, "wait", 0)
}

func TestStarterWeaponLuaKeepsFamilyRanksAndExcludesUnknownBaseWeaponModel(t *testing.T) {
	s, fake, _ := instantStrikeFixture(t, 1760)
	learnSpells(s, 53, 1752, 1760, 6762)
	input := starterWeaponLuaInput(t, s, fake)
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	input["target"].(map[string]any)["orientation"] = math.Pi
	starterWeaponLuaSpells(input)["1752"].(map[string]any)["directDamage"] = 10000
	assertStarterLuaChoice(t, runtime, input, "cast", 1760) // Highest shared family rank, not starter all-ranks profile.
	proof := s.weapons[2092]
	proof.damageKnown = false
	s.weapons[2092] = proof
	input = starterWeaponLuaInput(t, s, fake)
	assertStarterLuaChoice(t, runtime, input, "melee", 0)
	player := starterWeaponLuaPlayer(input)
	player["comboKnown"], player["comboPoints"], player["comboTargetGuid"] = true, 5, encounterGUID(enemyGUID)
	assertStarterLuaChoice(t, runtime, input, "cast", 6762) // Existing shared finisher rule, no invented weapon model.
}

func TestStarterWeaponLuaBypassesManaReserveAndIgnoresOtherOffenseKinds(t *testing.T) {
	s, fake, _ := instantStrikeFixture(t, 1760)
	input := starterWeaponLuaInput(t, s, fake)
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	rows := starterWeaponLuaSpells(input)
	rows["900"] = map[string]any{"known": true, "capability": "heal", "powerType": 0, "cost": "malformed"}
	other := starterLuaDamage(901, 10000, 0)
	other["powerType"] = 3
	rows["901"] = other // An energy offense row without WeaponStrike is not a strike candidate.
	assertStarterLuaChoice(t, runtime, input, "cast", 53)
	for _, id := range []string{"53", "1760", "6762"} {
		rows[id].(map[string]any)["known"] = false
	}
	assertStarterLuaChoice(t, runtime, input, "melee", 0)
}

func TestStarterWeaponLuaMalformedKnownFactsWaitWithoutFallback(t *testing.T) {
	s, fake, _ := instantStrikeFixture(t, 1760)
	runtime := starterLuaRuntime(t, behaviors.StarterSpellSource)
	changes := []struct {
		field string
		value any
	}{
		{"weaponStrike", "true"}, {"damageModelKnown", "true"}, {"requiresComboPoints", 1},
		{"requiresBehind", "false"}, {"requiresFacing", 1}, {"requiresStealth", 0},
		{"ready", "true"}, {"available", 1}, {"cost", "1e1"}, {"maxRange", nil},
		{"castTimeMs", -1}, {"weaponDamageMultiplier", -1}, {"weaponBonusDamage", "10"},
		{"comboPointsGenerated", 1.5},
	}
	for _, change := range changes {
		t.Run(fmt.Sprintf("%s_%v", change.field, change.value), func(t *testing.T) {
			input := starterWeaponLuaInput(t, s, fake)
			input["autoAttackKnown"] = true
			starterWeaponLuaSpells(input)["53"].(map[string]any)[change.field] = change.value
			assertStarterLuaChoice(t, runtime, input, "wait", 0)
		})
	}
	for _, bad := range []map[string]any{{"comboKnown": "true"}, {"comboKnown": true, "comboPoints": 6},
		{"comboKnown": true, "comboPoints": 5, "comboTargetGuid": "03"}, {"orientation": "0"}} {
		input := starterWeaponLuaInput(t, s, fake)
		for key, value := range bad {
			starterWeaponLuaPlayer(input)[key] = value
		}
		assertStarterLuaChoice(t, runtime, input, "wait", 0)
	}
	for _, change := range []struct {
		field string
		value any
	}{{"classId", 8}, {"powerType", 0}, {"powerKnown", false}} {
		input := starterWeaponLuaInput(t, s, fake)
		starterWeaponLuaPlayer(input)[change.field] = change.value
		assertStarterLuaChoice(t, runtime, input, "wait", 0)
	}
	input := starterWeaponLuaInput(t, s, fake)
	input["selection"] = "unknown"
	assertStarterLuaChoice(t, runtime, input, "wait", 0)
	input["selection"], input["sessionId"] = "weapon_strike", "replacement"
	assertStarterLuaChoice(t, runtime, input, "wait", 0)
}

func TestStarterWeaponLuaSourceEditChangesSharedFamilyWeightsInSameRuntime(t *testing.T) {
	s, fake, _ := instantStrikeFixture(t, 1760)
	input := starterWeaponLuaInput(t, s, fake)
	source := behaviors.StarterSpellSource
	runtime := starterLuaRuntime(t, source)
	assertStarterLuaChoice(t, runtime, input, "cast", 53)
	before := runtime.Status().Revision
	edited := strings.Replace(source, "finish_at_combo=5}", "finish_at_combo=5, family_weights={[53]=0}}", 1)
	if edited == source {
		t.Fatal("test did not edit the actual shared offense profile")
	}
	after, err := runtime.Reload(edited, before)
	if err != nil || after == before {
		t.Fatal("same-runtime shared source edit failed", after, err)
	}
	assertStarterLuaChoice(t, runtime, input, "cast", 1760)
}
