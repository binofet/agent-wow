package world

import (
	"context"
	"fmt"
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

func TestLuaBuffPolicyPreservesActorClassReserveScopes(t *testing.T) {
	type reserveCase struct {
		name           string
		class          float64
		known, omitted bool
		self, party    float64
	}
	classes := []reserveCase{
		{"paladin", 2, true, false, 5, 35},
		{"priest", 5, true, false, 30, 30},
		{"mage", 8, true, false, 30, 30},
		{"warlock", 9, true, false, 30, 30},
		{"unknown_paladin", 2, false, false, 35, 35},
		{"unknown_priest", 5, false, false, 35, 35},
		{"unknown_mage", 8, false, false, 35, 35},
		{"unknown_warlock", 9, false, false, 35, 35},
		{"omitted_class_known", 5, false, true, 35, 35},
		{"unknown_id", 0, true, false, 35, 35},
		{"unmapped_id", 999, true, false, 35, 35},
	}
	for _, class := range []float64{1, 3, 4, 6, 7, 11} {
		classes = append(classes, reserveCase{fmt.Sprintf("other_class_%g", class), class, true, false, 35, 35})
	}
	source := buffsPolicySource(t)
	for _, class := range classes {
		for _, target := range []string{"4", "3"} {
			reserve := class.self
			if target == "3" {
				reserve = class.party
			}
			for _, delta := range []float64{-1, 0} {
				t.Run(fmt.Sprintf("%s/target%s/boundary%g", class.name, target, delta), func(t *testing.T) {
					host, obs := newBuffPolicy(t, source), buffPolicyObservation()
					self := obs["players"].(map[string]any)["4"].(map[string]any)
					self["classId"], self["power"] = class.class, 10+reserve+delta
					if !class.omitted {
						self["classKnown"] = class.known
					}
					if target == "3" {
						installBuffAura(obs, "4", 1244, 600000, false)
					}
					action := decideBuff(t, host, obs)
					if delta < 0 && action != nil || delta == 0 && (action["targetGuid"] != target || action["spellId"] != float64(1244)) {
						t.Fatal("class/recipient reserve or exact cost boundary changed", reserve, self["power"], action)
					}
				})
			}
		}
	}
}

func TestLuaActorBuffReserveKeepsWaterAndObservedResourceChecks(t *testing.T) {
	source := buffsPolicySource(t)
	for _, class := range []float64{2, 5, 8, 9} {
		for _, mode := range []string{"water_exact_cost", "water_insufficient_power", "unknown_power", "other_resource"} {
			t.Run(fmt.Sprintf("class%g/%s", class, mode), func(t *testing.T) {
				host, obs := newBuffPolicy(t, source), buffPolicyObservation()
				self := obs["players"].(map[string]any)["4"].(map[string]any)
				self["classKnown"], self["classId"], self["power"] = true, class, float64(10)
				if mode == "water_exact_cost" || mode == "water_insufficient_power" {
					for _, spell := range obs["spells"].(map[string]any) {
						spell.(map[string]any)["auraEffects"] = []any{map[string]any{"kind": float64(6), "aura": float64(82)}}
					}
					if mode == "water_insufficient_power" {
						self["power"] = float64(9)
					}
				} else if mode == "unknown_power" {
					self["powerKnown"], self["power"] = false, float64(100)
				} else {
					self["powerType"], self["power"] = float64(3), float64(100)
				}
				action := decideBuff(t, host, obs)
				if mode == "water_exact_cost" && action["spellId"] != float64(1244) || mode != "water_exact_cost" && action != nil {
					t.Fatal("reserve tuning changed water cost or observed-resource admission", mode, action)
				}
			})
		}
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
func TestEncounterOptionalBuffYieldsToDefaultTravelAndMount(t *testing.T) {
	formation, err := os.ReadFile("../../behaviors/follow-formation.lua")
	if err != nil {
		t.Fatal(err)
	}
	p := newFallbackPolicyTest(t, "local encounter_formation=(function()\n"+string(formation)+"\nend)()\n")
	obs := positionedHealObservation()
	obs.Healing = nil
	self, owner := obs.Players[fallbackGUID(fallbackSelf)], obs.Players["3"]
	for _, player := range obs.Players {
		player.HealthKnown, player.Health, player.MaxHealth = true, 100, 100
		player.PowerKnown, player.Power, player.MaxPower = true, 100, 100
		player.CombatKnown, player.MovementKnown, player.PositionKnown = true, true, true
		player.PositionRevision = 205
	}
	owner.X, owner.Y, owner.Z = self.X+25, self.Y, self.Z
	obs.Navigation = &actionsv1.NavigationObservation{OwnerGuid: 3, FollowOwner: owner, FollowFormationSupported: true, GroundNavigationConfigured: true}
	buff := &actionsv1.EncounterSpell{Known: true, Ready: true, Available: true, MetadataKnown: true, Capability: "buff", FamilyId: 1243, Rank: 4, TargetRule: "ally", MaxRange: 30}
	obs.Spells = map[string]*actionsv1.EncounterSpell{
		"2791": buff, "23229": {Known: true, Available: true, GroundMountSpeedPercent: 100},
	}
	obs.Support = &actionsv1.EncounterSupport{AuraSpells: map[uint32]*actionsv1.EncounterSpell{2791: buff}}
	for _, id := range []uint64{fallbackSelf, 3} {
		obs.Support.PlayerAuras = append(obs.Support.PlayerAuras, &actionsv1.PlayerAuras{TargetGuid: id, Auras: []*actionsv1.ObservedAura{{SpellId: 2791, Positive: true}}})
	}
	next := func() behavior.Decision { return p.decide(observeRanged(t, p, obs, false)) }
	first := next().Output
	goal, _ := first["followFormation"].(map[string]any)
	if goal["ownerGuid"] != "3" || first["action"] != nil {
		t.Fatalf("default formation must make progress while a buff is available: %v", first["action"])
	}
	self.X, self.Y, self.Z = float32(goal["x"].(float64)), float32(goal["y"].(float64)), float32(goal["z"].(float64))
	self.PositionRevision++
	obs.Navigation.GroundMount = &actionsv1.FollowMountObservation{Ready: true, OwnerMounted: proto.Bool(true), OwnerGroundSpell: 458, SelfMounted: proto.Bool(false), OwnerMountRevision: 1}
	action, _ := next().Output["action"].(map[string]any)
	if action["capability"] != "cast_spell" || action["spellId"] != float64(23229) || action["followMount"] == nil {
		t.Fatal("genuinely ready mount lost to optional buff", action)
	}
	obs.Navigation.GroundMount = nil
	decision := next()
	action, _ = decision.Output["action"].(map[string]any)
	if action["capability"] != "buff" || action["targetGuid"] != "8" {
		t.Fatal("settled in-range companion buff unavailable", action)
	}
	id, err := p.host.ReserveAction(decision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
		SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionNotDispatched,
		Payload: map[string]any{"capability": "buff", "target_guid": "8", "spell_id": 2791, "phase": "admission_rejected", "grpc_status": "FailedPrecondition", "grpc_message": "current living observed friendly target required"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	obs.WorldObservationRevision++
	action, _ = next().Output["action"].(map[string]any)
	if action["capability"] == "buff" {
		t.Fatal("unchanged refused recipient was immediately retried", action)
	}
	obs.Players["8"].PositionRevision++
	action, _ = next().Output["action"].(map[string]any)
	if action["capability"] != "buff" || action["targetGuid"] != "8" {
		t.Fatal("fresh observed recipient could not be considered", action)
	}
}

func TestLuaBuffDefiniteRecipientRefusalExpiresInPlace(t *testing.T) {
	host, obs := newBuffPolicy(t, buffsPolicySource(t)), buffPolicyObservation()
	installBuffAura(obs, "4", 1244, 600000, false)
	decision, err := host.Decide(context.Background(), obs)
	if err != nil {
		t.Fatal(err)
	}
	id, err := host.ReserveAction(decision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
		SessionID: "buffs-lua", ActionID: id, Outcome: behavior.ActionNotDispatched,
		Payload: map[string]any{"capability": "buff", "target_guid": "3", "spell_id": 1244, "phase": "admission_rejected", "grpc_status": "FailedPrecondition", "grpc_message": "current living observed friendly target required"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if a := decideBuff(t, host, obs); a != nil {
		t.Fatal("immediate retry", a)
	}
	obs["clock"].(map[string]any)["monotonicMs"] = float64(5000)
	if a := decideBuff(t, host, obs); a["targetGuid"] != "3" || a["spellId"] != float64(1244) {
		t.Fatal("stationary recipient remained excluded at the ordinary retry deadline", a)
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
	decision := p.decide(observeSparseEncounter(t, p, obs))
	out := decision.Output
	if out["action"].(map[string]any)["capability"] != "heal" {
		t.Fatal("buff displaced real triage", out)
	}
	id, err := p.host.ReserveAction(decision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.host.Enqueue(behavior.Event{Kind: "buff_request", Value: map[string]any{"source": "buffs"}}); err != nil {
		t.Fatal(err)
	}
	out = p.decide(observeSparseEncounter(t, p, obs)).Output
	if out["action"] != nil || p.host.Status().Pending != 1 {
		t.Fatal("ensure request bypassed pending healing custody", out, p.host.Status())
	}
	if _, err = p.host.Reload(context.Background(), readEncounterBundle(t)+"\n-- same-session buff request", p.host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if p.host.Status().Pending != 1 {
		t.Fatal("reload lost the outstanding heal")
	}
	if _, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
		SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionNotDispatched}}); err != nil {
		t.Fatal(err)
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

func TestLuaBuffFollowRangeFiltersBeforeRecipientPreference(t *testing.T) {
	for _, following := range []bool{false, true} {
		t.Run(fmt.Sprint(following), func(t *testing.T) {
			host := newBuffPolicy(t, buffsPolicySource(t))
			obs := buffPolicyObservation()
			obs["navigation"] = map[string]any{"following": following}
			obs["players"].(map[string]any)["3"].(map[string]any)["x"] = float64(140)
			installBuffAura(obs, "4", 1244, 600000, false)
			obs["party"].(map[string]any)["group"].(map[string]any)["members"] = []any{map[string]any{"guid": "3", "online": true}, map[string]any{"guid": "9", "online": true}}
			a := decideBuff(t, host, obs)
			want := "3"
			if following {
				want = "9"
			}
			if a["targetGuid"] != want || a["needsApproach"] != !following {
				t.Fatal("follow filter did not preserve viable alternative/nonfollow", a)
			}
		})
	}
	t.Run("self remains eligible", func(t *testing.T) {
		host := newBuffPolicy(t, buffsPolicySource(t))
		obs := buffPolicyObservation()
		obs["navigation"] = map[string]any{"following": true}
		obs["players"].(map[string]any)["3"].(map[string]any)["x"] = float64(140)
		if a := decideBuff(t, host, obs); a["targetGuid"] != "4" {
			t.Fatal(a)
		}
	})
}

func TestEncounterBuffFollowDoesNotApproachDistantOptionalRecipient(t *testing.T) {
	for _, following := range []bool{false, true} {
		t.Run(fmt.Sprint(following), func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			obs := positionedHealObservation()
			obs.Healing.Patients = nil
			obs.Healing.Spells = nil
			buff := &actionsv1.EncounterSpell{Known: true, Ready: true, Available: true, MetadataKnown: true, Capability: "buff", TargetRule: "ally", FamilyId: 5697, Rank: 1, Cost: 10, MaxRange: 30, AuraEffects: []*actionsv1.EncounterAuraEffect{{Kind: 6, Aura: 82}}}
			obs.Spells = map[string]*actionsv1.EncounterSpell{"5697": buff}
			for _, player := range obs.Players {
				player.HealthKnown = true
				player.Health = 100
				player.MaxHealth = 100
				player.PowerKnown = true
				player.PowerType = 0
				player.Power = 100
				player.MaxPower = 100
				player.CombatKnown = true
				player.InCombat = false
			}
			obs.Navigation = &actionsv1.NavigationObservation{Following: following}
			self := obs.Players[fallbackGUID(fallbackSelf)]
			obs.Players["3"].PositionKnown = true
			obs.Players["3"].X = self.X + 140
			obs.Players["3"].Y = self.Y
			obs.Players["3"].Z = self.Z
			obs.Support = &actionsv1.EncounterSupport{AuraSpells: map[uint32]*actionsv1.EncounterSpell{5697: proto.Clone(buff).(*actionsv1.EncounterSpell)}}
			for guid := range obs.Players {
				if guid != "3" {
					var n uint64
					fmt.Sscan(guid, &n)
					obs.Support.PlayerAuras = append(obs.Support.PlayerAuras, &actionsv1.PlayerAuras{TargetGuid: n, Auras: []*actionsv1.ObservedAura{{SpellId: 5697, Positive: true}}})
				}
			}
			out := p.decide(observeRanged(t, p, obs, false)).Output
			a, _ := out["action"].(map[string]any)
			if following {
				if a["capability"] == "approach_target" || a["capability"] == "buff" {
					t.Fatal("optional distant buff fought follow", out)
				}
			} else if a["capability"] != "approach_target" || a["targetGuid"] != "3" {
				t.Fatal("nonfollow approach changed", out)
			}
		})
	}
}

func TestFollowingInRangeBuffDoesNotBorrowFailedHealApproach(t *testing.T) {
	for _, following := range []bool{false, true} {
		t.Run(fmt.Sprint(following), func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			obs := positionedHealObservation()
			obs.Navigation = &actionsv1.NavigationObservation{Following: following}
			d := p.decide(observeRanged(t, p, obs, false))
			a, _ := d.Output["action"].(map[string]any)
			if a["capability"] != "heal" || a["targetGuid"] != "8" {
				t.Fatal("seed not an actual heal", d.Output)
			}
			id, err := p.host.ReserveAction(d)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionFailed, Payload: map[string]any{"capability": "heal", "target_guid": "8", "spell_id": 6063}}})
			if err != nil {
				t.Fatal(err)
			}
			obs.Healing.Patients = nil
			obs.Healing.Spells = nil
			buff := &actionsv1.EncounterSpell{Known: true, Ready: true, Available: true, MetadataKnown: true, Capability: "buff", TargetRule: "ally", FamilyId: 5697, Rank: 1, Cost: 10, MaxRange: 30, AuraEffects: []*actionsv1.EncounterAuraEffect{{Kind: 6, Aura: 82}}}
			obs.Spells = map[string]*actionsv1.EncounterSpell{"5697": buff}
			obs.Support = &actionsv1.EncounterSupport{AuraSpells: map[uint32]*actionsv1.EncounterSpell{5697: proto.Clone(buff).(*actionsv1.EncounterSpell)}}
			for guid, player := range obs.Players {
				player.HealthKnown = true
				player.Health = 100
				player.MaxHealth = 100
				player.PowerKnown = true
				player.PowerType = 0
				player.Power = 100
				player.MaxPower = 100
				player.CombatKnown = true
				player.InCombat = false
				if guid != "8" {
					var n uint64
					fmt.Sscan(guid, &n)
					obs.Support.PlayerAuras = append(obs.Support.PlayerAuras, &actionsv1.PlayerAuras{TargetGuid: n, Auras: []*actionsv1.ObservedAura{{SpellId: 5697, Positive: true}}})
				}
			}
			out := p.decide(observeRanged(t, p, obs, false)).Output
			a, _ = out["action"].(map[string]any)
			if following {
				if a["capability"] != "buff" || a["targetGuid"] != "8" {
					t.Fatal("in-range optional buff borrowed failed heal movement", out)
				}
			} else if a["capability"] != "approach_target" || a["targetGuid"] != "8" {
				t.Fatal("nonfollow cached heal positioning changed", out)
			}
		})
	}
}
