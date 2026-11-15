package buddyinventory

import (
	"context"
	"testing"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
)

func TestGearEquipsOwnedRingIntoEmptySecondFinger(t *testing.T) {
	for _, ring := range []struct {
		name      string
		id, level uint32
		stats     []itemStat
	}{
		{"Defias Renegade Ring", 1076, 20, []itemStat{{7, 3}, {3, 3}}},
		{"Blood Ring", 4998, 19, []itemStat{{7, 5}}},
	} {
		t.Run(ring.name, func(t *testing.T) {
			s, f := fixtureGear(t, false)
			f.w.Self.Level = ring.level
			f.w.SelfSkillsKnown, f.w.SelfSkills, f.w.SelfSkillsRevision = false, nil, 0
			carried := f.w.InventoryBags[0].Slots[2]
			carried.Entry, carried.Durability, carried.MaxDurability = ring.id, 0, 0
			m := itemMetadata{known: true, entry: ring.id, class: 4, inventoryType: 11,
				quality: 2, stackable: 1, bonding: 2, requiredLevel: ring.level,
				allowableClass: ^uint32(0), allowableRace: ^uint32(0), statCount: uint32(len(ring.stats))}
			copy(m.stats[:], ring.stats)
			s.metadata[ring.id], s.queried[ring.id] = m, true
			f.w.SelfEquipment[firstFingerSlot] = &worldv1.EquipmentSlotState{Slot: firstFingerSlot, SlotKnown: true, ItemGuid: oldPantsGUID, Entry: 12006, StackCount: 1, MetadataKnown: true, EnhancementsKnown: true, HasEnhancements: true}
			before := proto.Clone(f.w.SelfEquipment[firstFingerSlot]).(*worldv1.EquipmentSlotState)
			verifyPairedGearSwap(t, s, f, stateInventory(t, s), ring.id, secondFingerSlot, 0)
			st := waitInventory(t, s)
			if st.Operation.Status != "CONFIRMED" || !proto.Equal(st.Equipment[firstFingerSlot], before) || st.Equipment[secondFingerSlot].ItemGuid != breechesGUID {
				t.Fatal("empty second ring did not equip or overwrote existing ring", st)
			}
		})
	}
}

const doomspike = 3413

func fixtureDoomspikeGear(t *testing.T) (*Service, *fakeInventory) {
	t.Helper()
	s, f, _ := fixtureWeapon(t, false)
	const buzzer = 2169
	for id, damage := range map[uint32][2]float32{buzzer: {14, 28}, doomspike: {17, 32}} {
		m, err := parseItemMetadata(weaponMetadataFrame(id, 15, 13, damage[0], damage[1], 1500))
		if err != nil {
			t.Fatal(err)
		}
		if id == buzzer {
			m.statCount, m.stats[0] = 1, itemStat{32, 5}
		} else {
			m.statCount, m.stats[0], m.stats[1], m.requiredLevel = 2, itemStat{4, 3}, itemStat{7, 3}, 20
		}
		s.metadata[id], s.queried[id] = m, true
	}
	f.w.Self.Level = 20
	f.w.InventoryBags[0].Slots[2].Entry = doomspike
	f.w.SelfEquipment[mainhandSlot].Entry = buzzer
	f.w.SelfEquipment[mainhandSlot].HasEnhancements = true
	f.w.SelfEquipment[offhandSlot].Entry = buzzer
	return s, f
}

func TestGearUsesDoomspikeInOffhandAndPreservesEnchantedMainhand(t *testing.T) {
	s, f := fixtureDoomspikeGear(t)
	before := proto.Clone(f.w.SelfEquipment[mainhandSlot]).(*worldv1.EquipmentSlotState)
	st := stateInventory(t, s)
	verifyPairedGearSwap(t, s, f, st, doomspike, offhandSlot, otherHandGUID)
	st = waitInventory(t, s)
	if st.Operation.Status != "CONFIRMED" || !proto.Equal(st.Equipment[mainhandSlot], before) || st.Equipment[offhandSlot].ItemGuid != breechesGUID || st.InventoryBags[0].Slots[2].ItemGuid != otherHandGUID {
		t.Fatal("offhand upgrade did not preserve enchanted mainhand or displaced weapon", st)
	}
}

func verifyPairedGearSwap(t *testing.T, s *Service, f *fakeInventory, st *inventoryv1.State, entry, slot uint32, displaced uint64) {
	t.Helper()
	for _, item := range st.Items {
		if item.Item.ItemGuid == breechesGUID {
			if !item.GearEquipEligible || !item.ConservativeUpgrade || item.GearEquipmentSlot != slot || item.ExpectedEquippedItemGuid != displaced {
				t.Fatal("unexpected equipment candidate", item)
			}
			gearProof(s, f)
			r := &inventoryv1.EquipGearRequest{SessionId: "body", RequestId: "paired-gear", ExpectedRevision: st.Revision, ExpectedInventoryRevision: st.InventoryRevision,
				Source: &inventoryv1.ItemRef{Slot: 2, ItemGuid: breechesGUID, Entry: entry, StackCount: 1}, EquipmentSlot: slot,
				ExpectedEquipmentRevision: st.EquipmentRevision, ExpectedEquippedItemGuid: displaced, RequireConservativeUpgrade: true}
			if _, err := s.EquipGear(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("owned candidate was not advertised", st.Items)
}
