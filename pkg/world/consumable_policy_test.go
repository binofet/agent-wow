package world

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func consumablePolicySource(t *testing.T, choose bool) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "consumables.lua"))
	if err != nil {
		t.Fatal(err)
	}
	decide := `decide=function(f)return m.decide(f,f.input.encounter,f.input.available~=false) end,`
	if choose {
		decide = `decide=function(f)return {choice=m.choose(f.input.inventory,f.input.encounter,f.input.requested)}end,`
	}
	return "local m=(function()\n" + string(raw) + "\nend)()\nreturn {" + decide + `on_event=m.on_event,export_state=m.export_state,import_state=m.import_state}`
}

func consumablePolicyFixture() (map[string]any, map[string]any) {
	self := map[string]any{"present": true, "aliveKnown": true, "alive": true, "powerKnown": true, "powerType": float64(0), "power": float64(60), "maxPower": float64(100)}
	obs := map[string]any{"sessionId": "consumable-body", "selfGuid": "6", "visitId": "1", "players": map[string]any{"6": self}, "navigation": map[string]any{"movementOwner": map[string]any{}}}
	view := map[string]any{"sessionId": "consumable-body", "selfGuid": "6", "inventoryKnown": true, "inventoryRevision": "10", "manaDrinkCount": float64(6),
		"consumableContext": map[string]any{"bodyReady": true, "foodBuffKnown": true, "dungeon": true, "selfAuras": map[string]any{"complete": true, "revision": "1", "auras": []any{}}}}
	var items []any
	for index, power := range []float64{21, 40} {
		items = append(items, map[string]any{"metadataKnown": true, "item": map[string]any{"slot": float64(index), "bag": float64(0), "itemGuid": []string{"4611686018427387905", "4611686018427387906"}[index], "entry": float64(60001 + index), "stackCount": float64(3)}, "consumable": map[string]any{"usable": true, "spells": []any{map[string]any{"spellId": float64(430 + index), "drinkSupported": true, "manaPerTick": power}}}})
	}
	view["items"] = items
	return view, obs
}

func consumableBuffFoodFixture() (map[string]any, map[string]any) {
	view, obs := consumablePolicyFixture()
	obs["players"].(map[string]any)["6"].(map[string]any)["power"] = float64(100)
	view["items"].([]any)[0].(map[string]any)["consumable"] = map[string]any{"usable": true, "spells": []any{
		map[string]any{"spellId": float64(5004), "foodSupported": true, "foodAuraSpellId": float64(19705), "foodEffects": []any{map[string]any{"points": float64(4)}}}}}
	return view, obs
}

func TestConsumableLuaKeepsOutdoorFoodSpecificToKnownRogues(t *testing.T) {
	source := consumablePolicySource(t, true)
	for _, class := range []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 11, 999} {
		for _, known := range []bool{false, true} {
			for _, dungeon := range []any{nil, false, true} {
				t.Run(fmt.Sprintf("class%g/known%t/dungeon%v", class, known, dungeon), func(t *testing.T) {
					view, obs := consumableBuffFoodFixture()
					self := obs["players"].(map[string]any)["6"].(map[string]any)
					self["classKnown"], self["classId"] = known, class
					view["consumableContext"].(map[string]any)["dungeon"] = dungeon
					host, err := behavior.NewSession(context.Background(), "consumable-body", source)
					if err != nil {
						t.Fatal(err)
					}
					defer host.Close()
					out, err := host.Decide(context.Background(), map[string]any{"inventory": view, "encounter": obs})
					if err != nil {
						t.Fatal(err)
					}
					choice, _ := out.Output["choice"].(map[string]any)
					allowed := dungeon == true || known && class == 4
					if allowed && (choice["kind"] != "eat_buff_food" || choice["spellId"] != float64(5004)) || !allowed && choice != nil {
						t.Fatal("explicit false class override or dungeon-only fallback changed", choice)
					}
				})
			}
		}
	}
}

func TestRogueOutdoorFoodStillRequiresUsefulObservedFoodAndQuietBody(t *testing.T) {
	source := consumablePolicySource(t, true)
	for _, mode := range []string{"buff_present", "aura_present", "aura_unknown", "food_buff_unknown", "combat", "metadata_unknown", "class_unknown", "explicit_other_class"} {
		t.Run(mode, func(t *testing.T) {
			view, obs := consumableBuffFoodFixture()
			self := obs["players"].(map[string]any)["6"].(map[string]any)
			self["classKnown"], self["classId"] = true, float64(4)
			facts := view["consumableContext"].(map[string]any)
			facts["dungeon"] = false
			input := map[string]any{"inventory": view, "encounter": obs}
			switch mode {
			case "buff_present":
				facts["foodBuffPresent"] = true
			case "aura_present":
				facts["selfAuras"].(map[string]any)["auras"] = []any{map[string]any{"spellId": float64(19705), "positive": true}}
			case "aura_unknown":
				facts["selfAuras"].(map[string]any)["complete"] = false
			case "food_buff_unknown":
				facts["foodBuffKnown"] = false
			case "combat":
				self["inCombat"] = true
			case "metadata_unknown":
				view["items"].([]any)[0].(map[string]any)["metadataKnown"] = false
			case "class_unknown":
				delete(self, "classKnown")
			case "explicit_other_class":
				self["classId"], input["requested"] = float64(8), "eat_buff_food"
			}
			host, err := behavior.NewSession(context.Background(), "consumable-body", source)
			if err != nil {
				t.Fatal(err)
			}
			defer host.Close()
			out, err := host.Decide(context.Background(), input)
			if err != nil || mode == "explicit_other_class" && out.Output["choice"] == nil || mode != "explicit_other_class" && out.Output["choice"] != nil {
				t.Fatal("outdoor override bypassed facts/combat or changed existing explicit requests", out.Output, err)
			}
		})
	}
}

func TestConsumableLuaChoosesItemsAndThresholdsThroughSameSessionReload(t *testing.T) {
	source := consumablePolicySource(t, true)
	host, err := behavior.NewSessionWithOptions(context.Background(), "consumable-body", source, behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	view, obs := consumablePolicyFixture()
	input := map[string]any{"inventory": view, "encounter": obs}
	out, err := host.Decide(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if choice := out.Output["choice"].(map[string]any); choice["spellId"] != float64(431) {
		t.Fatal("Lua did not rank actual item-use metadata", choice)
	}
	changed := strings.Replace(source, "drink_below_percent=70", "drink_below_percent=40", 1)
	if _, err = host.Reload(context.Background(), changed, host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	out, err = host.Decide(context.Background(), input)
	if err != nil || out.Output["choice"] != nil {
		t.Fatal("Go still owned consumption threshold", out.Output, err)
	}
	input["requested"] = "drink"
	out, err = host.Decide(context.Background(), input)
	if err != nil || out.Output["choice"].(map[string]any)["spellId"] != float64(431) || host.Status().SessionID != "consumable-body" {
		t.Fatal("explicit request did not share edited selector/body", out.Output, err)
	}
}

func TestConsumableLuaRetainsUnknownItemAcrossRestInterruptionAndReload(t *testing.T) {
	t.Run("drink", func(t *testing.T) { testConsumableUnknownAcrossReload(t, false) })
	t.Run("rogue_outdoor_food", func(t *testing.T) { testConsumableUnknownAcrossReload(t, true) })
}

func testConsumableUnknownAcrossReload(t *testing.T, food bool) {
	t.Helper()
	source := consumablePolicySource(t, false)
	host, err := behavior.NewSessionWithOptions(context.Background(), "consumable-body", source, behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	view, obs := consumablePolicyFixture()
	wantSpell := float64(431)
	if food {
		view, obs = consumableBuffFoodFixture()
		self := obs["players"].(map[string]any)["6"].(map[string]any)
		self["classKnown"], self["classId"], self["powerType"] = true, float64(4), float64(3)
		view["consumableContext"].(map[string]any)["dungeon"] = false
		wantSpell = 5004
	}
	step := func(available bool) behavior.Decision {
		t.Helper()
		sequence, err := host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: view})
		if err != nil {
			t.Fatal(err)
		}
		out, err := host.Decide(context.Background(), map[string]any{"encounter": obs, "available": available, "observationSequences": map[string]any{"inventory": fmt.Sprint(sequence)}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	receive := func(d behavior.Decision, payload map[string]any, outcome behavior.ActionOutcome) {
		t.Helper()
		id, err := host.ReserveAction(d)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "consumable-body", ActionID: id, Outcome: outcome, Payload: payload}}); err != nil {
			t.Fatal(err)
		}
	}
	first := step(true)
	lease := first.Output["action"].(map[string]any)
	if lease["capability"] != "movement_owner" || lease["mode"] != "acquire" {
		t.Fatal("rest did not take finite movement ownership", first.Output)
	}
	owner := lease["owner"].(string)
	receive(first, map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": owner, "result": map[string]any{"token": "rest-token"}}, behavior.ActionConfirmed)
	obs["navigation"].(map[string]any)["movementOwner"] = map[string]any{"active": true, "owner": owner, "remainingMs": "2000"}
	use := step(true)
	action := use.Output["action"].(map[string]any)
	if action["capability"] != "consume" || action["spellId"] != wantSpell {
		t.Fatal("selected item was not used", use.Output)
	}
	receive(use, map[string]any{"capability": "consume", "requestId": action["requestId"], "reconciliationRequired": true}, behavior.ActionSettledUnverified)
	if busy := step(false); busy.Output["action"] != nil {
		t.Fatal("rest displaced primary healing/defense", busy.Output)
	}
	obs["players"].(map[string]any)["6"].(map[string]any)["inCombat"] = true
	stop := step(true)
	release := stop.Output["action"].(map[string]any)
	if release["mode"] != "release" || release["token"] != "rest-token" {
		t.Fatal("incoming combat failed to release only owned rest custody", stop.Output)
	}
	receive(stop, map[string]any{"capability": "movement_owner", "mode": "release", "owner": owner}, behavior.ActionConfirmed)
	if _, err = host.Reload(context.Background(), source+"\n-- same-session edit", host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	obs["players"].(map[string]any)["6"].(map[string]any)["inCombat"] = false
	obs["navigation"].(map[string]any)["movementOwner"] = map[string]any{}
	out := step(true)
	if out.Output["action"] != nil || out.Output["hold"] == true || host.Status().Pending != 0 {
		t.Fatal("unknown use replayed or blocked ordinary policy after reload", out.Output, host.Status())
	}
}

func TestConsumableLuaRequiresClockLeaseAndAvoidsActiveFoodBuff(t *testing.T) {
	view, obs := consumablePolicyFixture()
	self := obs["players"].(map[string]any)["6"].(map[string]any)
	self["power"] = float64(100)
	view["items"].([]any)[0].(map[string]any)["consumable"] = map[string]any{"usable": true, "spells": []any{map[string]any{"spellId": float64(5004), "foodSupported": true, "foodAuraSpellId": float64(19705), "foodEffects": []any{map[string]any{"points": float64(4)}}}}}
	view["consumableContext"].(map[string]any)["foodBuffPresent"] = true
	host, err := behavior.NewSession(context.Background(), "consumable-body", consumablePolicySource(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	out, err := host.Decide(context.Background(), map[string]any{"inventory": view, "encounter": obs, "requested": "eat_buff_food"})
	if err != nil || out.Output["choice"] != nil {
		t.Fatal("Lua selected redundant food", out.Output, err)
	}
	// The module reports a missing mechanical capability instead of toggling Follow.
	vm, err := behavior.NewSession(context.Background(), "consumable-body", consumablePolicySource(t, false))
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Close()
	delete(obs["navigation"].(map[string]any), "movementOwner")
	if _, err = vm.Enqueue(behavior.Event{Kind: "inventory_observation", Value: view}); err != nil {
		t.Fatal(err)
	}
	out, err = vm.Decide(context.Background(), map[string]any{"encounter": obs})
	if err != nil || out.Output["phase"] != "clock_or_movement_lease_unavailable" || out.Output["action"] != nil {
		t.Fatal("missing lease silently changed native follow", out.Output, err)
	}
}

func TestConsumableLuaStopsWalkingFollowerBeforeOneExactUse(t *testing.T) {
	host, err := behavior.NewSessionWithOptions(context.Background(), "consumable-body", consumablePolicySource(t, false), behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	view, obs := consumablePolicyFixture()
	contextView := view["consumableContext"].(map[string]any)
	contextView["bodyReady"] = false
	self := obs["players"].(map[string]any)["6"].(map[string]any)
	self["moving"] = true
	step := func() behavior.Decision {
		t.Helper()
		seq, err := host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: view})
		if err != nil {
			t.Fatal(err)
		}
		out, err := host.Decide(context.Background(), map[string]any{"encounter": obs, "observationSequences": map[string]any{"inventory": fmt.Sprint(seq)}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := step()
	request := first.Output["action"].(map[string]any)
	if request["mode"] != "acquire" {
		t.Fatal("walking follower could not request finite stop", first.Output)
	}
	id, err := host.ReserveAction(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "consumable-body", ActionID: id, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": request["owner"], "result": map[string]any{"token": "rest-token"}}}}); err != nil {
		t.Fatal(err)
	}
	obs["navigation"].(map[string]any)["movementOwner"] = map[string]any{"active": true, "owner": request["owner"], "remainingMs": "2000"}
	if moving := step(); moving.Output["action"] != nil || moving.Output["phase"] != "awaiting_stationary_body" {
		t.Fatal("item used while still moving", moving.Output)
	}
	self["moving"], contextView["bodyReady"] = false, true
	view["inventoryRevision"] = "11" // Unrelated loot arrived during the stop.
	ready := step()
	if action := ready.Output["action"].(map[string]any); action["capability"] != "consume" || action["spellId"] != float64(431) || action["expectedInventoryRevision"] != "11" {
		t.Fatal("observed stop did not reach chosen exact use", ready.Output)
	}
}
