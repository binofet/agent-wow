package world

import (
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"os"
	"testing"
)

func TestEncounterAutomaticEquipmentWorksSoloAndArlenLedWithoutTownControls(t *testing.T) {
	for _, party := range []bool{false, true} {
		t.Run(map[bool]string{false: "solo", true: "Arlen-led"}[party], func(t *testing.T) {
			gear, err := os.ReadFile("../../behaviors/gear.lua")
			if err != nil {
				t.Fatal(err)
			}
			auto, err := os.ReadFile("../../behaviors/auto-equipment.lua")
			if err != nil {
				t.Fatal(err)
			}
			p := newFallbackPolicyTest(t, "local encounter_gear=(function()\n"+string(gear)+"\nend)()\nlocal encounter_auto_equipment=(function()\n"+string(auto)+"\nend)()\n")
			obs := fallbackObservation()
			self := obs.Players["4"]
			self.CombatKnown = true
			self.MovementKnown = true
			if party {
				obs.Party = &actionsv1.EncounterParty{Group: &partyv1.Group{Known: true, InGroup: true, GroupGuid: 7, LeaderGuid: 1, Members: []*partyv1.Member{{Guid: 1, Online: true}, {Guid: 4, Self: true, Online: true}, {Guid: 3, Online: true}}}}
			}
			inventory := map[string]any{"sessionId": fallbackSession, "selfGuid": "4", "revision": "1", "inventoryKnown": true, "inventoryRevision": "2", "equipmentKnown": true, "equipmentRevision": "3",
				"equipment":   []any{map[string]any{"slot": 6, "slotKnown": true, "enhancementsKnown": true}},
				"gearContext": map[string]any{"templates": map[string]any{"100": map[string]any{"itemId": 100, "available": true, "armorComparisonKnown": true, "itemClass": 4, "inventoryType": 7}}},
				"items":       []any{map[string]any{"metadataKnown": true, "item": map[string]any{"itemGuid": "4611686018427387905", "entry": 100, "slot": 2, "stackCount": 1}, "gearFacts": map[string]any{"enhancementsKnown": true, "destinations": []any{map[string]any{"eligible": true, "equipmentSlot": 6}}}}}}
			if _, err = p.host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: inventory}); err != nil {
				t.Fatal(err)
			}
			d := p.decide(p.observe(obs))
			a, _ := d.Output["action"].(map[string]any)
			if a["capability"] != "equip_gear" || a["selfGuid"] != "4" || a["equipmentSlot"] != float64(6) {
				t.Fatal(d.Output)
			}
			// Stale item/observations cannot cause another decision while its reserved
			// journal entry remains pending, even with a newer encounter observation.
			id, err := p.host.ReserveAction(d)
			if err != nil {
				t.Fatal(err)
			}
			again := p.decide(p.observe(obs))
			if again.Output["action"] != nil {
				t.Fatal(again.Output)
			}
			_ = id
		})
	}
}
