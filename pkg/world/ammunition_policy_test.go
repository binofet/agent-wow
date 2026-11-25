package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"os"
	"testing"
)

func TestNormalEncounterLuaSelectsAmmoAndKeepsUnknownAcrossReload(t *testing.T) {
	for _, group := range []bool{false, true} {
		t.Run(map[bool]string{false: "solo", true: "party"}[group], func(t *testing.T) {
			read := func(name string) string {
				b, err := os.ReadFile("../../behaviors/" + name)
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			source := "local encounter_gear=(function()\n" + read("gear.lua") + "\nend)()\nlocal encounter_auto_equipment=(function()\n" + read("auto-equipment.lua") + "\nend)()\n"
			p := newFallbackPolicyTest(t, source)
			obs := fallbackObservation()
			obs.Players["4"].CombatKnown = true
			obs.Players["4"].MovementKnown = true
			if group {
				obs.Party = &actionsv1.EncounterParty{Group: &partyv1.Group{Known: true, InGroup: true, GroupGuid: 7, LeaderGuid: 1, Members: []*partyv1.Member{{Guid: 1, Online: true}, {Guid: 4, Online: true, Self: true}, {Guid: 3, Online: true}}}}
			}
			v := map[string]any{"sessionId": fallbackSession, "selfGuid": "4", "revision": "1", "inventoryKnown": true, "inventoryRevision": "2", "equipmentKnown": true, "equipmentRevision": "3", "ammoKnown": true, "ammoRevision": "1",
				"equipment": []any{map[string]any{"slot": 17, "slotKnown": true, "itemGuid": "4611686018427387955", "entry": 9400, "stackCount": 1, "metadataKnown": true}},
				"items":     []any{map[string]any{"metadataKnown": true, "ammoSelectEligible": true, "item": map[string]any{"bag": 0, "slot": 2, "itemGuid": "4611686018427387953", "entry": 11285, "stackCount": 200}}}}
			if _, err := p.host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: v}); err != nil {
				t.Fatal(err)
			}
			d := p.decide(p.observe(obs))
			a, _ := d.Output["action"].(map[string]any)
			if a["capability"] != "select_ammo" || a["expectedAmmoEntry"] != float64(0) || a["expectedRangedItemGuid"] != "4611686018427387955" {
				t.Fatal(d.Output)
			}
			if _, err := p.host.ReserveAction(d); err != nil {
				t.Fatal(err)
			}
			before := p.host.Status()
			if _, err := p.host.Reload(context.Background(), source+readEncounterBundle(t)+"\n-- retained ammo attempt", before.Revision); err != nil {
				t.Fatal(err)
			}
			again := p.decide(p.observe(obs))
			if again.Output["action"] != nil {
				t.Fatal("pending ammo repeated", again.Output)
			}
		})
	}
}
