package buddyinventory

import (
	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
	"math"
)

type slotKey struct{ bag, slot uint32 }
type view struct {
	w     *worldv1.SnapshotResponse
	bags  [5]*worldv1.InventoryBag
	slots map[slotKey]*worldv1.BackpackSlot
}

func finite(x float32) bool {
	return !math.IsNaN(float64(x)) && !math.IsInf(float64(x), 0) && math.Abs(float64(x)) < 40000
}
func validateWorld(w *worldv1.SnapshotResponse, self uint64) (view, error) {
	v := view{w: w, slots: map[slotKey]*worldv1.BackpackSlot{}}
	if w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.Present || !w.InventoryKnown || w.InventoryRevision == 0 || len(w.InventoryBags) != 5 {
		return v, fail("coherent_owned_inventory_required")
	}
	guids := map[uint64]bool{}
	total := 0
	for _, b := range w.InventoryBags {
		if b == nil || b.Bag > 4 || v.bags[b.Bag] != nil || !b.SlotKnown || !b.ContentsKnown || b.SlotCount > 36 || len(b.Slots) != int(b.SlotCount) {
			return v, fail("complete_unique_bag_layout_required")
		}
		if b.Bag == 0 {
			if b.ContainerGuid != 0 || b.Entry != 0 || b.SlotCount != 16 {
				return v, fail("built_in_backpack_layout_invalid")
			}
		} else if b.ContainerGuid == 0 {
			if b.Entry != 0 || b.SlotCount != 0 || b.MetadataKnown {
				return v, fail("unknown_empty_bag_slot")
			}
		} else {
			if !itemGUID(b.ContainerGuid) || guids[b.ContainerGuid] || !b.MetadataKnown || b.Entry == 0 || b.SlotCount == 0 {
				return v, fail("observed_container_metadata_required")
			}
			guids[b.ContainerGuid] = true
		}
		v.bags[b.Bag] = b
		total += len(b.Slots)
	}
	if total > 160 {
		return v, fail("inventory_slot_bound_exceeded")
	}
	for _, b := range v.bags {
		for _, i := range b.Slots {
			if i == nil || i.Bag != b.Bag || i.Slot >= b.SlotCount || v.slots[slotKey{i.Bag, i.Slot}] != nil {
				return v, fail("inventory_slot_identity_invalid")
			}
			if i.ItemGuid == 0 {
				if i.Entry != 0 || i.StackCount != 0 || i.MetadataKnown {
					return v, fail("empty_slot_metadata_inconsistent")
				}
			} else {
				if !itemGUID(i.ItemGuid) || guids[i.ItemGuid] || i.MetadataKnown && (i.Entry == 0 || i.StackCount == 0) {
					return v, fail("owned_unique_item_required")
				}
				guids[i.ItemGuid] = true
			}
			v.slots[slotKey{i.Bag, i.Slot}] = i
		}
	}
	return v, nil
}
func ref(i *worldv1.BackpackSlot) *inventoryv1.ItemRef {
	return &inventoryv1.ItemRef{Bag: i.Bag, Slot: i.Slot, ItemGuid: i.ItemGuid, Entry: i.Entry, StackCount: i.StackCount}
}
func (v view) source(r *inventoryv1.ItemRef) (*worldv1.BackpackSlot, bool) {
	if r == nil || !itemGUID(r.ItemGuid) || r.Entry == 0 || r.StackCount == 0 {
		return nil, false
	}
	i := v.slots[slotKey{r.Bag, r.Slot}]
	return i, i != nil && i.MetadataKnown && proto.Equal(ref(i), r)
}
func normalBag(m itemMetadata) bool {
	return m.known && m.entry == 41599 && m.class == 1 && m.subclass == 0 && m.inventoryType == 18 && m.containerSlots == 20 && m.bagFamily == 0
}
func sameSlot(a, b *worldv1.BackpackSlot) bool {
	return a != nil && b != nil && a.Bag == b.Bag && a.Slot == b.Slot && a.ItemGuid == b.ItemGuid && a.Entry == b.Entry && a.StackCount == b.StackCount && (a.ItemGuid == 0 || a.MetadataKnown == b.MetadataKnown)
}
func exactVector(expected, actual view) bool {
	if len(expected.slots) != len(actual.slots) {
		return false
	}
	for n, a := range expected.bags {
		b := actual.bags[n]
		if b == nil || !b.SlotKnown || !b.ContentsKnown || a.ContainerGuid != b.ContainerGuid || a.Entry != b.Entry || a.SlotCount != b.SlotCount || a.ContainerGuid != 0 && !b.MetadataKnown {
			return false
		}
	}
	for k, a := range expected.slots {
		if !sameSlot(a, actual.slots[k]) {
			return false
		}
	}
	return true
}
func expectedAfter(before view, r *inventoryv1.Operation) (view, error) {
	if r.Kind == "equip_gear" {
		return expectedGearAfter(before, r)
	}
	w := proto.Clone(before.w).(*worldv1.SnapshotResponse)
	v, e := validateWorld(w, w.Self.Guid)
	if e != nil {
		return v, e
	}
	source, ok := v.source(r.Source)
	if !ok {
		return v, fail("baseline_source_missing")
	}
	saved := proto.Clone(source).(*worldv1.BackpackSlot)
	source.ItemGuid = 0
	source.Entry = 0
	source.StackCount = 0
	source.MetadataKnown = false
	if r.Kind == "move" {
		d := v.slots[slotKey{r.DestinationBag, r.DestinationSlot}]
		if d == nil || d.ItemGuid != 0 {
			return v, fail("baseline_destination_not_empty")
		}
		d.Bag = r.DestinationBag
		d.Slot = r.DestinationSlot
		d.ItemGuid = saved.ItemGuid
		d.Entry = saved.Entry
		d.StackCount = saved.StackCount
		d.MetadataKnown = saved.MetadataKnown
	} else if r.Kind == "equip" {
		n := r.EquipmentSlot - 18
		if n < 1 || n > 4 {
			return v, fail("bag_equipment_slot_invalid")
		}
		b := v.bags[n]
		if b.ContainerGuid != 0 {
			return v, fail("bag_equipment_not_empty")
		}
		b.ContainerGuid = saved.ItemGuid
		b.Entry = 41599
		b.MetadataKnown = true
		b.SlotCount = 20
		b.Slots = nil
		for i := uint32(0); i < 20; i++ {
			b.Slots = append(b.Slots, &worldv1.BackpackSlot{Bag: n, Slot: i})
		}
	}
	return validateWorld(w, w.Self.Guid)
}
