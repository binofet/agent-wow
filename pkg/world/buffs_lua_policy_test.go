package world

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
)

func buffsPolicySource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "buffs.lua"))
	if err != nil {
		t.Fatal(err)
	}
	return "local module=(function()\n" + string(data) + "\nend)()\n" +
		`return {decide=function(frame)return {action=module.choose(frame.input,{clock=frame.input.clock})} end,
        on_event=module.on_event,export_state=module.export_state,import_state=module.import_state}`
}
func buffPolicyObservation() map[string]any {
	player := func(x float64) map[string]any {
		return map[string]any{"present": true, "aliveKnown": true, "alive": true,
			"mapKnown": true, "mapId": float64(0), "positionKnown": true, "x": x, "healthKnown": true, "health": float64(100), "maxHealth": float64(100),
			"combatKnown": true, "inCombat": false, "powerKnown": true, "powerType": float64(0), "power": float64(100), "maxPower": float64(100)}
	}
	spell := func(rank float64) map[string]any {
		return map[string]any{"capability": "buff", "known": true, "ready": true, "available": true,
			"metadataKnown": true, "targetRule": "ally", "familyId": float64(1243), "rank": rank, "level": rank * 10, "powerType": float64(0),
			"cost": "10", "maxRange": float64(30), "auraEffects": []any{map[string]any{"kind": float64(6), "aura": float64(29), "misc": float64(2)}}}
	}
	return map[string]any{"sessionId": "buffs-lua", "selfGuid": "4", "mapId": float64(0), "hostOwned": true, "spellbookKnown": true,
		"clock": map[string]any{"monotonicMs": float64(0)}, "players": map[string]any{"4": player(0), "3": player(5), "9": player(5)},
		"party":  map[string]any{"group": map[string]any{"known": true, "inGroup": true, "members": []any{map[string]any{"guid": "3", "online": true}}}},
		"spells": map[string]any{"1243": spell(1), "1244": spell(2)}, "support": map[string]any{"playerAuras": []any{}, "auraSpells": map[string]any{}}}
}
func newBuffPolicy(t *testing.T, source string) *behavior.Session {
	t.Helper()
	host, err := behavior.NewSession(context.Background(), "buffs-lua", source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	return host
}
func decideBuff(t *testing.T, host *behavior.Session, obs map[string]any) map[string]any {
	t.Helper()
	out, err := host.Decide(context.Background(), obs)
	if err != nil {
		t.Fatal(err)
	}
	action, _ := out.Output["action"].(map[string]any)
	return action
}
func installBuffAura(obs map[string]any, target string, spell int, remaining float64, water bool) {
	support := obs["support"].(map[string]any)
	support["playerAuras"] = []any{map[string]any{"targetGuid": target, "complete": true, "revision": "2", "auras": []any{
		map[string]any{"spellId": float64(spell), "positive": true, "durationKnown": true, "remainingMs": remaining}}}}
	known := map[string]any{"metadataKnown": true, "familyId": float64(1243), "rank": float64(2)}
	if water {
		known["auraEffects"] = []any{map[string]any{"kind": float64(6), "aura": float64(82)}}
	}
	support["auraSpells"] = map[string]any{"1244": known, "5697": known}
}
func TestLuaBuffPolicyChoosesHighestRankAndActualPartyRecipients(t *testing.T) {
	source := buffsPolicySource(t)
	host := newBuffPolicy(t, source)
	obs := buffPolicyObservation()
	if a := decideBuff(t, host, obs); a["spellId"] != float64(1244) || a["targetGuid"] != "4" {
		t.Fatal("rank policy not shared Lua", a)
	}
	installBuffAura(obs, "4", 1244, 600000, false)
	if a := decideBuff(t, host, obs); a["targetGuid"] != "3" {
		t.Fatal("owner not buffed after self", a)
	}
	obs["party"].(map[string]any)["group"].(map[string]any)["members"].([]any)[0].(map[string]any)["online"] = false
	if a := decideBuff(t, host, obs); a != nil {
		t.Fatal("buffed stranger/offline teammate", a)
	}
}
func TestLuaBuffPolicySuppressesManaWasteAndStrongerArmor(t *testing.T) {
	for _, mode := range []string{"mana_reserve", "injured", "combat", "intellect_energy", "learned_armor", "observed_armor"} {
		t.Run(mode, func(t *testing.T) {
			host := newBuffPolicy(t, buffsPolicySource(t))
			obs := buffPolicyObservation()
			self := obs["players"].(map[string]any)["4"].(map[string]any)
			spells := obs["spells"].(map[string]any)
			switch mode {
			case "mana_reserve":
				self["power"] = float64(44)
			case "injured":
				obs["players"].(map[string]any)["3"].(map[string]any)["health"] = float64(40)
			case "combat":
				self["inCombat"] = true
			case "intellect_energy":
				installBuffAura(obs, "4", 1244, 600000, false)
				for _, v := range spells {
					v.(map[string]any)["auraEffects"] = []any{map[string]any{"kind": float64(6), "aura": float64(29), "misc": float64(3)}}
				}
				obs["players"].(map[string]any)["3"].(map[string]any)["powerType"] = float64(3)
			case "learned_armor", "observed_armor":
				for _, v := range spells {
					v.(map[string]any)["targetRule"] = "self"
					v.(map[string]any)["familySet"] = float64(3)
					v.(map[string]any)["familyFlags"] = []any{float64(0x10000000)}
				}
				stronger := map[string]any{"known": true, "metadataKnown": true, "familyId": float64(7302), "familySet": float64(3), "familyFlags": []any{float64(0x10000000)}, "level": float64(30)}
				if mode == "learned_armor" {
					spells["7302"] = stronger
				} else {
					installBuffAura(obs, "4", 7302, 600000, false)
					obs["support"].(map[string]any)["auraSpells"].(map[string]any)["7302"] = stronger
				}
			}
			if a := decideBuff(t, host, obs); a != nil {
				t.Fatal("Lua ignored policy context", mode, a)
			}
		})
	}
}
func TestLuaWaterBreathingRefreshRetryAndSameSessionTuning(t *testing.T) {
	source := buffsPolicySource(t)
	host := newBuffPolicy(t, source)
	obs := buffPolicyObservation()
	ranks := obs["spells"].(map[string]any)
	spell := ranks["1243"].(map[string]any)
	spell["familyId"] = float64(5697)
	spell["auraEffects"] = []any{map[string]any{"kind": float64(6), "aura": float64(82)}}
	obs["spells"] = map[string]any{"5697": spell}
	installBuffAura(obs, "4", 5697, 600000, true)
	install := obs["support"].(map[string]any)["playerAuras"].([]any)
	install = append(install, map[string]any{"targetGuid": "3", "auras": []any{map[string]any{"spellId": float64(5697), "positive": true, "durationKnown": true, "remainingMs": float64(62000)}}})
	obs["support"].(map[string]any)["playerAuras"] = install
	if a := decideBuff(t, host, obs); a != nil {
		t.Fatal("premature water refresh", a)
	}
	obs["clock"].(map[string]any)["monotonicMs"] = float64(3000)
	install[1].(map[string]any)["auras"].([]any)[0].(map[string]any)["remainingMs"] = float64(59000)
	decision, err := host.Decide(context.Background(), obs)
	if err != nil {
		t.Fatal(err)
	}
	a := decision.Output["action"].(map[string]any)
	if a["targetGuid"] != "3" || a["spellId"] != float64(5697) {
		t.Fatal("water not refreshed", a)
	}
	id, err := host.ReserveAction(decision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "buffs-lua", ActionID: id, Outcome: behavior.ActionSettledUnverified,
		Payload: map[string]any{"capability": "buff", "target_guid": "3", "spell_id": float64(5697)}}})
	if err != nil {
		t.Fatal(err)
	}
	if a := decideBuff(t, host, obs); a != nil {
		t.Fatal("unverified water replayed immediately", a)
	}
	before := host.Status()
	changed := strings.Replace(source, "retry_ms=5000", "retry_ms=1", 1)
	if _, err := host.Reload(context.Background(), changed, host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	// Existing retry deadline survives reload; tuning applies to later attempts.
	obs["clock"].(map[string]any)["monotonicMs"] = float64(8001)
	if a := decideBuff(t, host, obs); a["targetGuid"] != "3" || host.Status().SessionID != before.SessionID {
		t.Fatal("policy repair required reconnect or lost retry", a)
	}
}
func TestEncounterBuffPolicyYieldsToHealingAndApproachesSelectedAlly(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	obs := positionedHealObservation()
	self := obs.Players[fallbackGUID(fallbackSelf)]
	self.CombatKnown = true
	self.PowerKnown = true
	self.PowerType = 0
	self.Power = 100
	self.MaxPower = 100
	self.HealthKnown = true
	self.Health = 100
	self.MaxHealth = 100
	buff := &actionsv1.EncounterSpell{Known: true, Ready: true, Available: true, MetadataKnown: true, Capability: "buff", TargetRule: "ally", FamilyId: 1243, Rank: 2, Cost: 10, MaxRange: 30}
	obs.Spells["1244"] = buff
	out := p.decide(observeSparseEncounter(t, p, obs)).Output
	if out["action"].(map[string]any)["capability"] != "heal" {
		t.Fatal("buff displaced real triage", out)
	}
	obs.Healing.Patients, obs.Healing.Spells = nil, nil
	obs.Spells["6063"].Ready = false
	for _, patient := range obs.Players {
		patient.HealthKnown = true
		patient.Health = 100
		patient.MaxHealth = 100
	}
	obs.Support = &actionsv1.EncounterSupport{PlayerAuras: []*actionsv1.PlayerAuras{{TargetGuid: fallbackSelf, Auras: []*actionsv1.ObservedAura{{SpellId: 1244, Positive: true}}}}, AuraSpells: map[uint32]*actionsv1.EncounterSpell{1244: proto.Clone(buff).(*actionsv1.EncounterSpell)}}
	owner := obs.Players["3"]
	owner.PositionKnown = true
	owner.X = 100
	owner.Y = self.Y
	owner.Z = self.Z
	// Only owner3 needs the buff; ally8 already has the same stronger aura.
	obs.Support.PlayerAuras = append(obs.Support.PlayerAuras, &actionsv1.PlayerAuras{TargetGuid: 8, Auras: []*actionsv1.ObservedAura{{SpellId: 1244, Positive: true}}})
	out = p.decide(observeSparseEncounter(t, p, obs)).Output
	if a := out["action"].(map[string]any); a["capability"] != "approach_target" || a["targetGuid"] != "3" {
		t.Fatal("buff gave up instead of positioning", out)
	}
}

func TestEncounterCommandedControlPrecedesIdleBuffMaintenance(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	obs := positionedHealObservation()
	obs.Healing.Patients, obs.Healing.Spells = nil, nil
	self := obs.Players[fallbackGUID(fallbackSelf)]
	self.CombatKnown, self.PowerKnown, self.HealthKnown = true, true, true
	self.Power, self.MaxPower, self.Health, self.MaxHealth = 100, 100, 100, 100
	obs.Spells = map[string]*actionsv1.EncounterSpell{
		"1244": {Known: true, Ready: true, Available: true, MetadataKnown: true, Capability: "buff", TargetRule: "ally", FamilyId: 1243, Rank: 2, Cost: 10, MaxRange: 30},
		"118":  {Known: true, Ready: true, Available: true, MetadataKnown: true, Capability: "polymorph", FamilyId: 118, Rank: 1, MaxRange: 30}}
	obs.MeleeKnown = true
	addFallbackTarget(obs, fallbackNPC, 0, 100)
	_, err := p.host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"task": "control", "technique": "polymorph", "targetGuid": fallbackGUID(fallbackNPC)}})
	if err != nil {
		t.Fatal(err)
	}
	out := p.decide(observeSparseEncounter(t, p, obs)).Output
	action, _ := out["action"].(map[string]any)
	if action["targetGuid"] != fallbackGUID(fallbackNPC) || (action["capability"] != "approach_target" && action["capability"] != "polymorph") {
		t.Fatal("idle buffs displaced the player's explicit control", out)
	}
}
