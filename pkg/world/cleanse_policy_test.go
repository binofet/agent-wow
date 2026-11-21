package world

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func cleansePolicySource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "support.lua"))
	if err != nil {
		t.Fatal(err)
	}
	return "local module=(function()\n" + string(data) + "\nend)()\n" +
		`return {decide=function(frame)return {action=module.cleanse(frame.input)} end,
 on_event=module.on_event,export_state=module.export_state,import_state=module.import_state}`
}

func cleansePolicyObservation() map[string]any {
	player := func(x float64) map[string]any {
		return map[string]any{"present": true, "aliveKnown": true, "alive": true, "mapKnown": true, "mapId": float64(0), "positionKnown": true, "x": x}
	}
	aura := func(slot, spell, kind float64, revision string) map[string]any {
		return map[string]any{"slot": slot, "spellId": spell, "revision": revision, "negative": true, "metadataKnown": true,
			"dispelType": float64(1), "auraTypes": []any{kind}, "durationKnown": true, "remainingMs": float64(10000)}
	}
	return map[string]any{"sessionId": "cleanse-lua", "selfGuid": "4", "mapId": float64(0), "hostOwned": true,
		"players":    map[string]any{"4": player(0), "3": player(5), "9": player(5)},
		"party":      map[string]any{"group": map[string]any{"known": true, "inGroup": true, "members": []any{map[string]any{"guid": "3", "online": true}}}},
		"navigation": map[string]any{"ownerGuid": "3"},
		"spells": map[string]any{
			"527": map[string]any{"known": true, "ready": true, "metadataKnown": true, "capability": "cleanse", "rank": float64(1), "cost": float64(14), "maxRange": float64(40), "friendlyDispelMask": float64(2)},
			"988": map[string]any{"known": true, "ready": true, "metadataKnown": true, "capability": "cleanse", "rank": float64(2), "cost": float64(14), "maxRange": float64(40), "friendlyDispelMask": float64(2)}},
		"support": map[string]any{"playerAuras": []any{map[string]any{"targetGuid": "3", "complete": true,
			"auras": []any{aura(0, 172, 3, "1"), aura(1, 5782, 7, "2")}}}},
	}
}

func TestLuaCleanseOwnsAuraAndRankPriorityAndReloadsWithoutReconnect(t *testing.T) {
	source := cleansePolicySource(t)
	host, err := behavior.NewSession(context.Background(), "cleanse-lua", source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	obs := cleansePolicyObservation()
	first, err := host.Decide(context.Background(), obs)
	if err != nil {
		t.Fatal(err)
	}
	action := first.Output["action"].(map[string]any)
	if action["targetGuid"] != "3" || action["spellId"] != float64(988) || action["auraSlot"] != float64(1) || action["auraRevision"] != "2" {
		t.Fatal("Lua did not prefer observed fear and highest same-cost rank", first.Output)
	}
	before := host.Status()
	changed := strings.Replace(source, "[7]=5", "[7]=0", 1)
	if changed == source {
		t.Fatal("tuning fixture did not edit Lua")
	}
	if _, err := host.Reload(context.Background(), changed, before.Revision); err != nil {
		t.Fatal(err)
	}
	second, err := host.Decide(context.Background(), obs)
	if err != nil || second.Output["action"].(map[string]any)["auraSlot"] != float64(0) || host.Status().SessionID != before.SessionID {
		t.Fatal("cleanse priority still depended on Go or a replacement session", second.Output, err)
	}
}

func TestLuaCleanseRequiresPositiveHarmfulMatchingAuraFacts(t *testing.T) {
	for _, mode := range []string{"positive", "expired", "metadata unknown", "wrong mask", "offline", "dead", "unknown position", "nonmember"} {
		t.Run(mode, func(t *testing.T) {
			host, err := behavior.NewSession(context.Background(), "cleanse-lua", cleansePolicySource(t))
			if err != nil {
				t.Fatal(err)
			}
			defer host.Close()
			obs := cleansePolicyObservation()
			set := obs["support"].(map[string]any)["playerAuras"].([]any)[0].(map[string]any)
			set["auras"] = set["auras"].([]any)[1:]
			aura := set["auras"].([]any)[0].(map[string]any)
			patient := obs["players"].(map[string]any)["3"].(map[string]any)
			switch mode {
			case "positive":
				aura["negative"], aura["positive"] = false, true
			case "expired":
				aura["remainingMs"] = float64(0)
			case "metadata unknown":
				aura["metadataKnown"] = false
			case "wrong mask":
				aura["dispelType"] = float64(2)
			case "offline":
				obs["party"].(map[string]any)["group"].(map[string]any)["members"].([]any)[0].(map[string]any)["online"] = false
			case "dead":
				patient["alive"] = false
			case "unknown position":
				patient["positionKnown"] = false
			case "nonmember":
				set["targetGuid"] = "9"
			}
			out, err := host.Decide(context.Background(), obs)
			if err != nil || out.Output["action"] != nil {
				t.Fatal("unsupported aura became a cleanse choice", mode, out.Output, err)
			}
		})
	}
}

func TestLuaCleanseRetainsNoReplayBindingAcrossReloadUntilNewAuraFact(t *testing.T) {
	source := cleansePolicySource(t)
	host, err := behavior.NewSession(context.Background(), "cleanse-lua", source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	obs := cleansePolicyObservation()
	first, err := host.Decide(context.Background(), obs)
	if err != nil {
		t.Fatal(err)
	}
	id, err := host.ReserveAction(first)
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "cleanse-lua", ActionID: id, Outcome: behavior.ActionSettledUnverified,
		Payload: map[string]any{"capability": "cleanse", "target_guid": "3", "aura_slot": float64(1), "aura_revision": "2"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Reload(context.Background(), source+"\n-- preserve attempt", host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	out, err := host.Decide(context.Background(), obs)
	if err != nil || out.Output["action"].(map[string]any)["auraRevision"] == "2" {
		t.Fatal("uncertain cleanse replayed after source reload", out.Output, err)
	}
	set := obs["support"].(map[string]any)["playerAuras"].([]any)[0].(map[string]any)
	set["auras"].([]any)[1].(map[string]any)["revision"] = "9"
	if _, err := host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: obs}); err != nil {
		t.Fatal(err)
	}
	out, err = host.Decide(context.Background(), obs)
	if err != nil || out.Output["action"].(map[string]any)["auraRevision"] != "9" {
		t.Fatal("new observed aura application could not be cleansed", out.Output, err)
	}
}

func TestEncounterLuaCleansingYieldsToHealingAndApproachesOnlyWhenNeeded(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	obs := positionedHealObservation()
	owner := obs.Players["3"]
	owner.PositionKnown = true
	owner.X, owner.Y, owner.Z = 15, 10, 10
	obs.Spells["527"] = &actionsv1.EncounterSpell{Known: true, Ready: true, MetadataKnown: true, Capability: "cleanse", FamilyId: 527, Rank: 1, MaxRange: 40, FriendlyDispelMask: 2}
	obs.Support = &actionsv1.EncounterSupport{PlayerAuras: []*actionsv1.PlayerAuras{{TargetGuid: 3, Auras: []*actionsv1.ObservedAura{{Slot: 0, SpellId: 172, Revision: 1, Negative: true, MetadataKnown: true, DispelType: 1, AuraTypes: []uint32{3}}}}}}
	out := p.decide(observeSparseEncounter(t, p, obs)).Output
	if out["action"].(map[string]any)["capability"] != "heal" {
		t.Fatal("cleansing displaced selected real healing", out)
	}
	obs.Healing.Patients, obs.Healing.Spells = nil, nil
	obs.Spells["6063"].Ready = false
	out = p.decide(observeSparseEncounter(t, p, obs)).Output
	action := out["action"].(map[string]any)
	if action["capability"] != "cleanse" || action["auraSlot"] != float64(0) || action["auraRevision"] != "1" {
		t.Fatal("missing exact Lua cleanse after triage", out)
	}
	owner.X = 100
	out = p.decide(observeSparseEncounter(t, p, obs)).Output
	if action := out["action"].(map[string]any); action["capability"] != "approach_target" || action["targetGuid"] != "3" {
		t.Fatal("Lua did not choose approach for out-of-range cleanse", out)
	}
}
