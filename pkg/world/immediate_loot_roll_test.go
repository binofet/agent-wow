package world

import (
	"context"
	"encoding/json"
	"fmt"
	"google.golang.org/protobuf/encoding/protojson"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func immediateRollBundle(t testing.TB) string {
	t.Helper()
	data, err := exec.Command("python3", "-B", filepath.Join("..", "..", "dev", "encounter-bundle")).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Actual reported IDs/shape, with no claim that random property775 or worn
// enhancements have been realized into a verified upgrade score.
func immediateRollView(actor string) map[string]any {
	equipment := []any{}
	for i := 0; i < 19; i++ {
		equipment = append(equipment, map[string]any{"slot": float64(i), "slotKnown": true})
	}
	equipment[7] = map[string]any{"slot": float64(7), "slotKnown": true, "itemGuid": "4611686018427387905", "entry": float64(20095), "stackCount": float64(1), "metadataKnown": true, "enhancementsKnown": true, "hasEnhancements": true, "durabilityKnown": true, "durability": float64(20), "maxDurability": float64(20)}
	return map[string]any{"sessionId": "immediate-roll", "selfGuid": actor, "visitId": "1", "player": map[string]any{"guid": actor, "present": true, "mapKnown": true, "mapId": float64(349), "classKnown": true, "classId": float64(8), "raceKnown": true, "raceId": float64(1), "levelKnown": true, "level": float64(40)}, "equipmentKnown": true, "equipmentRevision": "10", "equipment": equipment, "skillsKnown": true, "skillsComplete": true, "skillsRevision": "1", "skills": []any{map[string]any{"skillId": float64(415), "value": float64(150), "maxValue": float64(300)}, map[string]any{"skillId": float64(333), "value": float64(150), "maxValue": float64(225)}}, "metadata": []any{}, "offers": []any{map[string]any{"rollGuid": "4611686018427388000", "revision": "1", "visitId": "1", "mapId": float64(349), "lootSlot": float64(2), "itemEntry": float64(7522), "itemCount": float64(1), "randomProperty": float64(775), "offeredMask": float64(15), "remainingMs": "60000", "available": true, "phase": "pending"}}}
}
func immediateEnqueue(t testing.TB, h *behavior.Session, kind string, value map[string]any) string {
	t.Helper()
	n, e := h.Enqueue(behavior.Event{Kind: kind, Value: value})
	if e != nil {
		t.Fatal(e)
	}
	return fmt.Sprint(n)
}
func immediateDecide(t testing.TB, h *behavior.Session, input map[string]any) behavior.Decision {
	t.Helper()
	d, e := h.Decide(context.Background(), input)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func immediateReceive(t testing.TB, h *behavior.Session, d behavior.Decision, outcome behavior.ActionOutcome, payload map[string]any) uint64 {
	t.Helper()
	id, e := h.ReserveAction(d)
	if e != nil {
		t.Fatal(e)
	}
	_, e = h.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "immediate-roll", ActionID: id, Outcome: outcome, Payload: payload}})
	if e != nil {
		t.Fatal(e)
	}
	return id
}
func TestImmediateFullRollPhaseUsesRoleFallbackAndPreservesUnknown(t *testing.T) {
	source := immediateRollBundle(t)
	for _, actor := range []string{"1", "4", "5", "11"} {
		t.Run(actor, func(t *testing.T) {
			h, e := behavior.NewSession(context.Background(), "immediate-roll", source)
			if e != nil {
				t.Fatal(e)
			}
			defer h.Close()
			view := immediateRollView(actor)
			seq := immediateEnqueue(t, h, "loot_roll_observation", view)
			d := immediateDecide(t, h, map[string]any{"kind": "loot_roll", "observationSequence": seq})
			a, _ := d.Output["action"].(map[string]any)
			want := "pass"
			if actor == "4" {
				want = "disenchant"
			}
			if a["capability"] != "loot_roll" || a["choice"] != want || a["randomProperty"] != float64(775) {
				t.Fatal("not immediate exact role vote", d.Output)
			}
			if hint, _ := d.Output["observationWakeKinds"].(map[string]any); hint["loot_roll"] != true {
				t.Fatal("no revision-bound wake declaration", d.Output)
			}
			if facts, _ := d.Output["lootRoll"].(map[string]any); facts["classification"] != "unknown" || facts["unclassifiedFallback"] != true {
				t.Fatal("invented upgrade proof", d.Output)
			}
			id := immediateReceive(t, h, d, behavior.ActionUnknown, map[string]any{"capability": "loot_roll", "roll_guid": a["rollGuid"], "transport_written": true})
			if _, e = h.Reload(context.Background(), source+"\n-- preserve unresolved vote", h.Status().Revision); e != nil {
				t.Fatal(e)
			}
			for i := 0; i < 2; i++ {
				seq = immediateEnqueue(t, h, "loot_roll_observation", view)
				out := immediateDecide(t, h, map[string]any{"kind": "loot_roll", "observationSequence": seq}).Output
				if out["action"] != nil {
					t.Fatal("UNKNOWN vote replay", out)
				}
			}
			pending := h.Outstanding()
			if len(pending) != 1 || pending[0].ActionID != id || pending[0].Outcome != behavior.ActionUnknown {
				t.Fatal("original UNKNOWN changed", pending)
			}
		})
	}
}

func TestImmediateRollDuringQuiescentDrinkSurvivesFinalMovementFilters(t *testing.T) {
	source := immediateRollBundle(t)
	h, e := behavior.NewSession(context.Background(), "immediate-roll", source)
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	inventory, obs := consumablePolicyFixture()
	inventory["sessionId"], inventory["selfGuid"] = "immediate-roll", "4"
	obs["sessionId"], obs["selfGuid"], obs["mapId"], obs["hostOwned"], obs["spellbookKnown"], obs["worldObservationRevision"] = "immediate-roll", "4", float64(349), true, true, "1"
	self := obs["players"].(map[string]any)["6"].(map[string]any)
	self["mapKnown"], self["mapId"], self["positionKnown"], self["movementKnown"], self["moving"], self["combatKnown"], self["inCombat"], self["classKnown"], self["classId"] = true, float64(349), true, true, false, true, false, true, float64(8)
	obs["players"] = map[string]any{"4": self}
	obs["targets"] = map[string]any{}
	obs["spells"] = map[string]any{}
	nav := obs["navigation"].(map[string]any)
	step := func(roll bool) behavior.Decision {
		inv := immediateEnqueue(t, h, "inventory_observation", inventory)
		seqs := map[string]any{"inventory": inv}
		if roll {
			seqs["loot_roll"] = immediateEnqueue(t, h, "loot_roll_observation", immediateRollView("4"))
		}
		enc := immediateEnqueue(t, h, "encounter_observation", obs)
		return immediateDecide(t, h, map[string]any{"kind": "encounter", "observationSequence": enc, "observationSequences": seqs})
	}
	d := step(false)
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] != "movement_owner" || a["mode"] != "acquire" {
		t.Fatal("did not enter real drink ownership", d.Output)
	}
	owner := a["owner"]
	immediateReceive(t, h, d, behavior.ActionConfirmed, map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": owner, "result": map[string]any{"token": "drink-token"}})
	nav["movementOwner"] = map[string]any{"active": true, "owner": owner, "remainingMs": "2000"}
	d = step(false)
	a, _ = d.Output["action"].(map[string]any)
	if a["capability"] != "consume" {
		t.Fatal("did not use exact drink", d.Output)
	}
	immediateReceive(t, h, d, behavior.ActionConfirmed, map[string]any{"capability": "consume", "requestId": a["requestId"], "transport_written": true})
	d = step(false)
	rest, _ := d.Output["consumable"].(map[string]any)
	if rest["hold"] != true || d.Output["action"] != nil {
		t.Fatal("not a real quiescent drink hold", d.Output)
	}
	before, e := h.State(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	// A distant learned hazard causes the old final ranged filter to suppress
	// ordinary actions when the drink owns movement. A vote must remain sideband.
	obs["targets"] = map[string]any{"17379391151924051969": map[string]any{"entry": float64(12222), "present": true, "aliveKnown": true, "alive": true, "mapKnown": true, "mapId": float64(349), "positionKnown": true, "x": float64(80), "attackObservation": map[string]any{}}}
	d = step(true)
	a, _ = d.Output["action"].(map[string]any)
	rest, _ = d.Output["consumable"].(map[string]any)
	if a["capability"] != "loot_roll" || a["choice"] != "disenchant" || rest["hold"] != true || d.Output["followFormation"] != nil {
		t.Fatal("rest or final movement filter blocked vote/interrupted drink", d.Output)
	}
	after, e := h.State(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	old := before.Checkpoint["data"].(map[string]any)["consumables"]
	next := after.Checkpoint["data"].(map[string]any)["consumables"]
	if !reflect.DeepEqual(old, next) {
		t.Fatal("vote changed drink intent/lease", old, next)
	}
	immediateReceive(t, h, d, behavior.ActionConfirmed, map[string]any{"capability": "loot_roll", "transport_written": true})
	if again := step(true); again.Output["action"] != nil {
		t.Fatal("duplicate vote or drink", again.Output)
	}
}

func immediateUpgradeRollView(actor string) map[string]any {
	v := immediateRollView(actor)
	v["equipment"].([]any)[7] = map[string]any{"slot": float64(7), "slotKnown": true}
	offer := v["offers"].([]any)[0].(map[string]any)
	offer["itemEntry"] = float64(5000)
	offer["randomProperty"] = float64(0)
	v["metadata"] = []any{map[string]any{"itemId": float64(5000), "available": true, "itemClass": float64(4), "subclass": float64(1), "inventoryType": float64(20), "quality": float64(2), "armorComparisonKnown": true, "armor": float64(10), "itemLevel": float64(10), "requiredLevel": float64(10), "allowableClass": float64(4294967295), "allowableRace": float64(4294967295), "stats": []any{map[string]any{"type": float64(5), "value": float64(2)}}}}
	return v
}
func TestImmediateFastRollDoesNotReuseCachedHealingOrBypassPending(t *testing.T) {
	h, e := behavior.NewSession(context.Background(), "immediate-roll", immediateRollBundle(t))
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	obs := positionedHealObservation()
	obs.SessionId = "immediate-roll"
	obs.Healing.SessionId = "immediate-roll"
	raw, e := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(obs)
	if e != nil {
		t.Fatal(e)
	}
	value := map[string]any{}
	if e = json.Unmarshal(raw, &value); e != nil {
		t.Fatal(e)
	}
	seq := immediateEnqueue(t, h, "encounter_observation", value)
	heal := immediateDecide(t, h, map[string]any{"kind": "encounter", "observationSequence": seq})
	if a, _ := heal.Output["action"].(map[string]any); a["capability"] != "heal" {
		t.Fatal("no real heal fixture", heal.Output)
	}
	id, e := h.ReserveAction(heal)
	if e != nil {
		t.Fatal(e)
	}
	seq = immediateEnqueue(t, h, "loot_roll_observation", immediateUpgradeRollView("4"))
	out := immediateDecide(t, h, map[string]any{"kind": "loot_roll", "observationSequence": seq}).Output
	if out["action"] != nil || h.Status().Pending != 1 {
		t.Fatal("vote interrupted pending heal", out)
	}
	_, e = h.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "immediate-roll", ActionID: id, Outcome: behavior.ActionFailed, Payload: map[string]any{"capability": "heal", "target_guid": "8", "spell_id": 6063}}})
	if e != nil {
		t.Fatal(e)
	}
	seq = immediateEnqueue(t, h, "loot_roll_observation", immediateUpgradeRollView("4"))
	begin := time.Now()
	d := immediateDecide(t, h, map[string]any{"kind": "loot_roll", "observationSequence": seq})
	elapsed := time.Since(begin)
	if a, _ := d.Output["action"].(map[string]any); a["capability"] != "loot_roll" || a["choice"] != "need" {
		t.Fatal("cached heal or repaired position displaced verified upgrade Need", d.Output)
	}
	t.Logf("loaded full-VM event-drain/decision/checkpoint elapsed: %s (not native packet latency)", elapsed)
}

// Loaded-VM decision latency includes real event drain, Lua decision, host
// conversion/checkpoint. Source parsing/setup is excluded, as in a persistent
// accepted policy; native observation/IPC/network time is not measured here.
func BenchmarkImmediateFullBundleLootDecision(b *testing.B) {
	source := immediateRollBundle(b)
	for _, kind := range []string{"randomized_fallback", "verified_upgrade"} {
		b.Run(kind, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				h, e := behavior.NewSession(context.Background(), "immediate-roll", source)
				if e != nil {
					b.Fatal(e)
				}
				view := immediateRollView("4")
				want := "disenchant"
				if kind == "verified_upgrade" {
					view = immediateUpgradeRollView("4")
					want = "need"
				}
				seq := immediateEnqueue(b, h, "loot_roll_observation", view)
				b.StartTimer()
				d := immediateDecide(b, h, map[string]any{"kind": "loot_roll", "observationSequence": seq})
				b.StopTimer()
				if a, _ := d.Output["action"].(map[string]any); a["choice"] != want {
					b.Fatal(d.Output)
				}
				h.Close()
			}
		})
	}
}
