package buddyloot

import (
	"context"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestAutomaticLootReservesLastSlotForLiveQuestObjectives(t *testing.T) {
	const targorrHead = 3630
	for _, tc := range []struct {
		name                             string
		questDrop, carried, unknownCount bool
		wantOpcode                       uint32
		wantSlot                         byte
	}{
		{"quest before ordinary roll winner", true, false, false, opcode.CMSGAutostoreLootItem, 1},
		{"reserve for future corpse", false, false, false, opcode.CMSGLootRelease, 0},
		{"objective already in bags", false, true, false, opcode.CMSGAutostoreLootItem, 0},
		{"unknown counts do not create a reserve", false, false, true, opcode.CMSGAutostoreLootItem, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			_, f, _ := rig(t)
			world := f.world
			world.SelfQuestLogKnown = true
			world.SelfQuestLog = []*worldv1.QuestLogEntry{{QuestId: 386}}
			for i, slot := range world.Backpack[:15] {
				slot.ItemGuid = 0x4000000000000100 + uint64(i)
				slot.Entry, slot.StackCount, slot.MetadataKnown = 25, 1, true
			}
			if tc.carried {
				world.Backpack[0].Entry = targorrHead
			}
			if tc.unknownCount {
				world.Backpack[0].Entry, world.Backpack[0].StackCount, world.Backpack[0].MetadataKnown = 0, 0, false
			}
			items := []lootItem{{slot: 0, id: 123, count: 1, slotType: 4}}
			if tc.questDrop {
				// AzerothCore presents needed quest items as slot type 0 in a party.
				items = append(items, lootItem{slot: 1, id: targorrHead, count: 1})
			}
			s := NewAutomaticService(f, 1, "session")
			now := time.Unix(1000, 0)
			s.now = func() time.Time { return now }
			tickCorpseLua(t, s)
			emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 0, items...))
			tickCorpseLua(t, s)
			if len(f.packets) != 2 || f.packets[1].Opcode != tc.wantOpcode {
				t.Fatalf("unexpected loot request: %+v, state=%v", f.packets, current(t, s))
			}
			if tc.wantOpcode == opcode.CMSGAutostoreLootItem && (len(f.packets[1].Payload) != 1 || f.packets[1].Payload[0] != tc.wantSlot) {
				t.Fatalf("autostore picked the wrong loot slot: %v", f.packets[1].Payload)
			}
			if tc.wantOpcode == opcode.CMSGLootRelease {
				emit(s, opcode.SMSGLootReleaseResponse, releasePacket())
				now = now.Add(time.Minute)
				tickCorpseLua(t, s)
				if len(f.packets) != 2 {
					t.Fatalf("reopened the ordinary corpse while holding the reserve: %+v", f.packets)
				}
			}
		})
	}
}
