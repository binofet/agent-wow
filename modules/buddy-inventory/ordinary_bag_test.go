package buddyinventory

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func ordinaryBagFixture(t *testing.T, entry, size uint32) (*Service, *fakeInventory, *inventoryv1.EquipRequest) {
	t.Helper()
	s, f := fixtureInventory()
	s.selfGUID, s.state.SelfGuid, f.w.Self.Guid = 8, 8, 8
	f.prof = &professionsv1.State{SessionId: "body", SelfGuid: 8}
	f.w.InventoryBags[0].Slots[1].Entry = entry
	m, err := parseItemMetadata(metadataFixture(entry, 1, 1, 18, size))
	if err != nil {
		t.Fatal(err)
	}
	s.metadata[entry], s.queried[entry] = m, true
	v := stateInventory(t, s)
	return s, f, &inventoryv1.EquipRequest{SessionId: "body", RequestId: "ordinarybag", ExpectedRevision: v.Revision,
		ExpectedInventoryRevision: v.InventoryRevision, Source: &inventoryv1.ItemRef{Slot: 1, ItemGuid: bagGUID, Entry: entry, StackCount: 1}, EquipmentSlot: 19}
}

func TestOrdinaryBagEquipUsesActualEntryCapacityAndExactNormalPacket(t *testing.T) {
	for _, tc := range []struct{ entry, size uint32 }{{4498, 8}, {41599, 20}, {5571, 6}} {
		t.Run(fmt.Sprintf("%d_%d", tc.entry, tc.size), func(t *testing.T) {
			s, f, request := ordinaryBagFixture(t, tc.entry, tc.size)
			actualProof(s, f, false)
			if _, err := s.Equip(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			state := waitInventory(t, s)
			bag := state.InventoryBags[1]
			if state.Operation.Status != "CONFIRMED" || !state.Operation.InventoryConfirmed || !state.Operation.EquipmentConfirmed || !state.Operation.LeaseReleased ||
				bag.Entry != tc.entry || bag.ContainerGuid != bagGUID || bag.SlotCount != tc.size || len(bag.Slots) != int(tc.size) ||
				state.InventoryBags[0].Slots[1].ItemGuid != 0 || state.InventoryBags[0].Slots[0].ItemGuid != junkGUID {
				t.Fatal(state)
			}
			packet := f.packets[0]
			if len(f.packets) != 1 || packet.Opcode != opcode.CMSGAutoequipItemSlot || len(packet.Payload) != 9 ||
				binary.LittleEndian.Uint64(packet.Payload) != bagGUID || packet.Payload[8] != 19 {
				t.Fatal(f.packets)
			}
		})
	}
}

func TestOrdinaryBagStructuralAndKnownRequirementGuards(t *testing.T) {
	base, err := parseItemMetadata(metadataFixture(4498, 1, 1, 18, 8))
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*itemMetadata){
		"unknown": func(m *itemMetadata) { m.known = false }, "class": func(m *itemMetadata) { m.class = 4 },
		"specialty": func(m *itemMetadata) { m.subclass = 1 }, "family": func(m *itemMetadata) { m.bagFamily = 1 },
		"slot": func(m *itemMetadata) { m.inventoryType = 27 }, "empty": func(m *itemMetadata) { m.containerSlots = 0 },
		"oversized": func(m *itemMetadata) { m.containerSlots = 37 }, "flag": func(m *itemMetadata) { m.flags = 1 },
		"flag2": func(m *itemMetadata) { m.flags2 = 1 }, "quest": func(m *itemMetadata) { m.startQuest = 1 },
		"bond": func(m *itemMetadata) { m.bonding = 4 }, "spell": func(m *itemMetadata) { m.hasSpell = true },
		"stack": func(m *itemMetadata) { m.stackable = 2 }, "duration": func(m *itemMetadata) { m.duration = 1 },
		"class_unknown": func(m *itemMetadata) { m.allowableClass = 1 }, "race_unknown": func(m *itemMetadata) { m.allowableRace = 1 },
		"level_unknown": func(m *itemMetadata) { m.requiredLevel = 1 }, "skill_unknown": func(m *itemMetadata) { m.requiredSkill = 197 },
		"spell_requirement": func(m *itemMetadata) { m.requiredSpell = 1 }, "reputation": func(m *itemMetadata) { m.requiredReputationRank = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			m := base
			change(&m)
			if bagUsability(&worldv1.SnapshotResponse{Self: &worldv1.PlayerState{}}, m) == "" {
				t.Fatal("unsupported or unproven bag accepted", m)
			}
		})
	}
	w := &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{ClassKnown: true, ClassId: 9, RaceKnown: true, RaceId: 1, LevelKnown: true, Level: 5}}
	base.allowableClass, base.allowableRace, base.requiredLevel = 1<<8, 1, 5
	if reason := bagUsability(w, base); reason != "" {
		t.Fatal(reason)
	}
	w.Self.Level = 4
	if bagUsability(w, base) == "" {
		t.Fatal("unmet level accepted")
	}
}

func TestOrdinaryBagFinalRenewRechecksRequirementsAndEmptyDestination(t *testing.T) {
	for _, change := range []func(*Service, *fakeInventory){
		func(s *Service, _ *fakeInventory) {
			s.mu.Lock()
			m := s.metadata[4498]
			m.requiredLevel = 20
			s.metadata[4498] = m
			s.mu.Unlock()
		},
		func(_ *Service, f *fakeInventory) {
			f.mu.Lock()
			f.w.InventoryBags[1] = &worldv1.InventoryBag{Bag: 1, SlotKnown: true, ContentsKnown: true, ContainerGuid: bagGUID + 9, Entry: 4498, MetadataKnown: true, SlotCount: 1, Slots: []*worldv1.BackpackSlot{{Bag: 1}}}
			f.mu.Unlock()
		},
	} {
		s, f, request := ordinaryBagFixture(t, 4498, 8)
		f.afterRenew = func() { change(s, f) }
		if _, err := s.Equip(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		state := waitInventory(t, s)
		if state.Operation.Status != "BLOCKED" || len(f.packets) != 0 || !state.Operation.LeaseReleased {
			t.Fatal(state, f.packets)
		}
	}
}

func TestOrdinaryBagWrongServerCapacityOrSourceConservationCannotConfirm(t *testing.T) {
	for _, mode := range []string{"capacity", "entry", "retained_source", "unrelated_item"} {
		t.Run(mode, func(t *testing.T) {
			s, f, request := ordinaryBagFixture(t, 4498, 8)
			f.afterSend = func(_ *modv1.SendPacketRequest) {
				s.mu.Lock()
				w := proto.Clone(s.last.expected.w).(*worldv1.SnapshotResponse)
				w.InventoryRevision++
				s.mu.Unlock()
				switch mode {
				case "capacity":
					w.InventoryBags[1].Slots = w.InventoryBags[1].Slots[:7]
					w.InventoryBags[1].SlotCount = 7
				case "entry":
					w.InventoryBags[1].Entry = 41599
				case "retained_source":
					w.InventoryBags[0].Slots[1] = &worldv1.BackpackSlot{Slot: 1, ItemGuid: bagGUID, Entry: 4498, StackCount: 1, MetadataKnown: true}
				case "unrelated_item":
					w.InventoryBags[0].Slots[0] = &worldv1.BackpackSlot{}
				}
				f.mu.Lock()
				f.w = w
				f.mu.Unlock()
				s.mu.Lock()
				s.last.cancel()
				s.mu.Unlock()
			}
			s.Equip(context.Background(), request)
			state := waitInventory(t, s)
			if state.Operation.Status == "CONFIRMED" || len(f.packets) != 1 {
				t.Fatal(state, f.packets)
			}
		})
	}
}
