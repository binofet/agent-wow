package buddyprofessions

import (
	"math"
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
)

func professionCarriedBag(w *worldv1.SnapshotResponse, bag, entry, size uint32) *worldv1.InventoryBag {
	b := &worldv1.InventoryBag{Bag: bag, SlotKnown: true, ContentsKnown: true, ContainerGuid: 0x4000000000000100 + uint64(bag), MetadataKnown: true, Entry: entry, SlotCount: size}
	for slot := uint32(0); slot < size; slot++ {
		b.Slots = append(b.Slots, &worldv1.BackpackSlot{Bag: bag, Slot: slot})
	}
	w.InventoryBags[bag] = b
	return b
}

func TestProfessionInventoryCountsAllFiveBagsAndOnlyGeneralFreeSlots(t *testing.T) {
	w := professionObservationFixture()
	for bag := uint32(1); bag < 5; bag++ {
		b := professionCarriedBag(w, bag, 41599, 20)
		b.Slots[bag] = &worldv1.BackpackSlot{Bag: bag, Slot: bag, ItemGuid: 0x4000000000000200 + uint64(bag), MetadataKnown: true, Entry: 2589, StackCount: bag}
	}
	// The legacy backpack can be absent or contradictory without replacing the
	// newer authoritative carried vector or its revision.
	w.BackpackKnown, w.BackpackRevision, w.Backpack = false, 1000, nil
	w.InventoryRevision = 27
	before := proto.Clone(w)
	v, err := validateWorld(w, 7)
	if err != nil || v.counts[2589] != 17 || v.freeSlots != 90 || v.inventoryRevision != 27 || v.backpackRevision != 1000 {
		t.Fatal(v, err)
	}
	if !proto.Equal(w, before) {
		t.Fatal("inventory validation mutated public source")
	}
	// Layout and slot order carry no meaning beyond their explicit identities.
	w.InventoryBags[0], w.InventoryBags[4] = w.InventoryBags[4], w.InventoryBags[0]
	w.InventoryBags[0].Slots[0], w.InventoryBags[0].Slots[19] = w.InventoryBags[0].Slots[19], w.InventoryBags[0].Slots[0]
	v, err = validateWorld(w, 7)
	if err != nil || v.counts[2589] != 17 || v.freeSlots != 90 {
		t.Fatal("reordered carried topology rejected", v, err)
	}
}

func TestProfessionSpecialtyBagMaterialsDoNotCreateGenericCapacity(t *testing.T) {
	w := professionObservationFixture()
	for slot := uint32(0); slot < 16; slot++ {
		w.InventoryBags[0].Slots[slot] = &worldv1.BackpackSlot{Slot: slot, ItemGuid: 0x4000000000000300 + uint64(slot), Entry: 2589, StackCount: 1, MetadataKnown: true}
	}
	b := professionCarriedBag(w, 1, 22248, 20)
	b.Slots[3] = &worldv1.BackpackSlot{Bag: 1, Slot: 3, ItemGuid: 0x4000000000000400, Entry: 2589, StackCount: 8, MetadataKnown: true}
	v, err := validateWorld(w, 7)
	if err != nil || v.counts[2589] != 24 || v.freeSlots != 0 {
		t.Fatal("specialty materials or output capacity incorrect", v, err)
	}
	professionCarriedBag(w, 2, 41599, 20)
	v, err = validateWorld(w, 7)
	if err != nil || v.freeSlots != 20 {
		t.Fatal("verified normal bag capacity unavailable", v, err)
	}
}

func TestProfessionInventoryRejectsPartialUnknownAndAliasedTopology(t *testing.T) {
	cases := map[string]func(*worldv1.SnapshotResponse){
		"inventory_unknown": func(w *worldv1.SnapshotResponse) { w.InventoryKnown = false },
		"revision_zero":     func(w *worldv1.SnapshotResponse) { w.InventoryRevision = 0 },
		"missing_bag":       func(w *worldv1.SnapshotResponse) { w.InventoryBags = w.InventoryBags[:4] },
		"extra_bag": func(w *worldv1.SnapshotResponse) {
			w.InventoryBags = append(w.InventoryBags, &worldv1.InventoryBag{Bag: 5})
		},
		"nil_bag":                    func(w *worldv1.SnapshotResponse) { w.InventoryBags[4] = nil },
		"duplicate_bag":              func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].Bag = 3 },
		"bag_out_of_range":           func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].Bag = 5 },
		"bag_slot_unknown":           func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].SlotKnown = false },
		"contents_unknown":           func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].ContentsKnown = false },
		"container_metadata_unknown": func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].MetadataKnown = false },
		"container_entry_unknown":    func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Entry = 0 },
		"container_invalid_guid":     func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].ContainerGuid = 17 },
		"duplicate_container_guid":   func(w *worldv1.SnapshotResponse) { w.InventoryBags[2].ContainerGuid = w.InventoryBags[1].ContainerGuid },
		"container_aliases_backpack_item": func(w *worldv1.SnapshotResponse) {
			w.InventoryBags[2].ContainerGuid = w.InventoryBags[0].Slots[0].ItemGuid
		},
		"item_aliases_container": func(w *worldv1.SnapshotResponse) {
			w.InventoryBags[1].Slots[0].ItemGuid = w.InventoryBags[2].ContainerGuid
		},
		"item_duplicate_across_bags": func(w *worldv1.SnapshotResponse) {
			w.InventoryBags[2].Slots[0] = proto.Clone(w.InventoryBags[1].Slots[0]).(*worldv1.BackpackSlot)
			w.InventoryBags[2].Slots[0].Bag = 2
		},
		"normal_bag_not_twenty": func(w *worldv1.SnapshotResponse) {
			w.InventoryBags[1].SlotCount = 19
			w.InventoryBags[1].Slots = w.InventoryBags[1].Slots[:19]
		},
		"bag_over_thirty_six": func(w *worldv1.SnapshotResponse) { professionCarriedBag(w, 3, 22248, 37) },
		"full_layout_over_160": func(w *worldv1.SnapshotResponse) {
			for b := uint32(1); b < 5; b++ {
				professionCarriedBag(w, b, 22248, 36)
			}
			w.InventoryBags[0].SlotCount = 17
			w.InventoryBags[0].Slots = append(w.InventoryBags[0].Slots, &worldv1.BackpackSlot{Slot: 16})
		},
		"empty_container_metadata": func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].MetadataKnown = true },
		"empty_container_entry":    func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].Entry = 41599 },
		"empty_container_contents": func(w *worldv1.SnapshotResponse) {
			w.InventoryBags[4].SlotCount = 1
			w.InventoryBags[4].Slots = []*worldv1.BackpackSlot{{Bag: 4}}
		},
		"backpack_container_guid":         func(w *worldv1.SnapshotResponse) { w.InventoryBags[0].ContainerGuid = 0x4000000000000999 },
		"backpack_container_metadata":     func(w *worldv1.SnapshotResponse) { w.InventoryBags[0].MetadataKnown = true },
		"occupied_metadata_unknown":       func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[0].MetadataKnown = false },
		"occupied_entry_unknown":          func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[0].Entry = 0 },
		"occupied_stack_unknown":          func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[0].StackCount = 0 },
		"occupied_guid_invalid":           func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[0].ItemGuid = 0x4000000000000000 },
		"nil_carried_slot":                func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[5] = nil },
		"missing_carried_slot":            func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots = w.InventoryBags[1].Slots[:19] },
		"duplicate_empty_carried_slot":    func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[6].Slot = 5 },
		"misaddressed_carried_slot":       func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[5].Bag = 2 },
		"out_of_range_carried_slot":       func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[5].Slot = 20 },
		"empty_carried_metadata":          func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[5].MetadataKnown = true },
		"empty_carried_entry":             func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[5].Entry = 2589 },
		"empty_carried_stack":             func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[5].StackCount = 1 },
		"aggregate_across_bags_overflows": func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Slots[0].StackCount = math.MaxUint32 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w := professionObservationFixture()
			b := professionCarriedBag(w, 1, 41599, 20)
			b.Slots[0] = &worldv1.BackpackSlot{Bag: 1, ItemGuid: 0x4000000000000500, Entry: 2589, StackCount: 4, MetadataKnown: true}
			professionCarriedBag(w, 2, 41599, 20)
			mutate(w)
			assertProfessionObservationRejected(t, w, 7)
		})
	}
}

func TestProfessionInventoryMaximumCapacityAndExactCountBounds(t *testing.T) {
	w := professionObservationFixture()
	for b := uint32(1); b < 5; b++ {
		professionCarriedBag(w, b, 22248, 36)
	}
	b := w.InventoryBags[4]
	b.Slots[35] = &worldv1.BackpackSlot{Bag: 4, Slot: 35, ItemGuid: 0x4000000000000600, Entry: 2589, StackCount: math.MaxUint32 - 7, MetadataKnown: true}
	v, err := validateWorld(w, 7)
	if err != nil || v.counts[2589] != math.MaxUint32 || v.freeSlots != 14 {
		t.Fatal("160 carried slots or exact count bound rejected", v, err)
	}
}
