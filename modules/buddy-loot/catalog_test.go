package buddyloot

import (
	"context"
	"encoding/binary"
	"fmt"
	"github.com/agent-wow/agent-wow/pkg/gamecatalog"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"testing"
)

func TestOpeningMechanicComesFromCatalog(t *testing.T) {
	for _, spell := range []uint32{3365, 6477, 6478} {
		s, f, _ := harvestRig(t)
		data := fmt.Sprintf(`{"objects":{"161557":{"type":3,"action":"open_and_loot","openSpell":%d,"questItems":[{"questId":3904,"itemId":11119,"count":8}]}}}`, spell)
		var err error
		s.catalog, err = gamecatalog.Decode([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		enable(t, s)
		s.Tick(context.Background())
		if len(f.packets) != 1 || binary.LittleEndian.Uint32(f.packets[0].Payload[1:]) != spell {
			t.Fatal("wrong opening mechanic", spell, current(t, s))
		}
	}
}

// These IDs exist only in the injected data, never in a production allowlist.
func TestUnseenCatalogContainerUsesOwnQuestAndExactSource(t *testing.T) {
	for _, mode := range []string{"ordinary", "other_map", "unrelated_quest", "different_lock", "not_catalogued"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := harvestRig(t)
			data := []byte(`{"objects":{"987654":{"name":"Unfamiliar supply cache","type":3,"action":"open_and_loot","openSpell":6478,"questItems":[{"questId":81234,"itemId":91234,"count":20},{"questId":81235,"itemId":91235,"count":1}]}}}`)
			c, err := gamecatalog.Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			s.catalog = c
			g := f.world.QuestObjects[0]
			g.Entry = 987654
			g.Guid = 0xf110000000000000 | uint64(g.Entry)<<24 | 99
			f.world.SelfQuestLog[0].QuestId = 81235
			if mode == "other_map" {
				f.world.Self.MapId = 1
				f.actions.Position.MapId = 1
				g.MapId = 1
			}
			if mode == "unrelated_quest" {
				f.world.SelfQuestLog[0].QuestId = 123
			}
			if mode == "different_lock" {
				s.catalog, _ = gamecatalog.Decode([]byte(`{"objects":{"987654":{"type":3,"action":"inspect_lock","questItems":[{"questId":81235,"itemId":91235,"count":1}]}}}`))
			}
			if mode == "not_catalogued" {
				s.catalog, _ = gamecatalog.Decode([]byte(`{"objects":{}}`))
			}
			enable(t, s)
			s.Tick(context.Background())
			if mode != "ordinary" && mode != "other_map" {
				if len(f.packets) != 0 {
					t.Fatal("unqualified source opened")
				}
				return
			}
			r := current(t, s).LastGather
			if r == nil || r.QuestId != 81235 || r.ItemId != 91235 || r.ObjectGuid != g.Guid || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGCastSpell || binary.LittleEndian.Uint32(f.packets[0].Payload[1:]) != 6478 {
				t.Fatal(current(t, s))
			}
			emit(s, opcode.SMSGLootResponse, harvestWindow(g.Guid, lootItem{slot: 1, id: 91235, count: 12, slotType: 4}))
			s.Tick(context.Background())
			if f.packets[len(f.packets)-1].Opcode != opcode.CMSGAutostoreLootItem {
				t.Fatal("catalog pickup retained old count-eight restriction")
			}
			// Another quest on the same container cannot replace a pending binding.
			f.world.SelfQuestLog[0].QuestId = 81234
			if b := s.boundHarvestRecipe(); b.questID != 81235 || b.itemID != 91235 {
				t.Fatal("pending source rebound", b)
			}
		})
	}
}
