package buddytrade

import (
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

type carriedJunkProof struct {
	revision        uint64
	presentRevision uint64
}

func carriedJunkItemGUID(guid uint64) bool {
	return guid>>48 == 0x4000 && guid&0x0000ffffffffffff != 0
}

// Validate the complete carried layout before using either an occupied source
// or an empty receipt slot. Container references share the same GUID namespace.
func knownCarriedJunkSlots(w *worldv1.SnapshotResponse) ([]*worldv1.BackpackSlot, bool) {
	if w == nil || !w.InventoryKnown || w.InventoryRevision == 0 || len(w.InventoryBags) != 5 {
		return nil, false
	}
	var bags [5]*worldv1.InventoryBag
	guids := make(map[uint64]bool)
	total := 0
	for _, bag := range w.InventoryBags {
		if bag == nil || bag.Bag > 4 || bags[bag.Bag] != nil || !bag.SlotKnown || !bag.ContentsKnown ||
			bag.SlotCount > 36 || len(bag.Slots) != int(bag.SlotCount) {
			return nil, false
		}
		switch {
		case bag.Bag == 0:
			if bag.ContainerGuid != 0 || bag.Entry != 0 || bag.MetadataKnown || bag.SlotCount != 16 {
				return nil, false
			}
		case bag.ContainerGuid == 0:
			if bag.Entry != 0 || bag.MetadataKnown || bag.SlotCount != 0 {
				return nil, false
			}
		default:
			if !carriedJunkItemGUID(bag.ContainerGuid) || guids[bag.ContainerGuid] ||
				!bag.MetadataKnown || bag.Entry == 0 || bag.SlotCount == 0 {
				return nil, false
			}
			guids[bag.ContainerGuid] = true
		}
		bags[bag.Bag] = bag
		total += len(bag.Slots)
	}
	if total > 160 {
		return nil, false
	}
	slots := make([]*worldv1.BackpackSlot, 0, total)
	for _, bag := range bags {
		var seen [36]bool
		for _, item := range bag.Slots {
			if item == nil || item.Bag != bag.Bag || item.Slot >= bag.SlotCount || seen[item.Slot] {
				return nil, false
			}
			seen[item.Slot] = true
			if item.ItemGuid == 0 {
				if item.Entry != 0 || item.StackCount != 0 || item.MetadataKnown {
					return nil, false
				}
			} else {
				if !carriedJunkItemGUID(item.ItemGuid) || guids[item.ItemGuid] ||
					!item.MetadataKnown || item.Entry == 0 || item.StackCount == 0 {
					return nil, false
				}
				guids[item.ItemGuid] = true
			}
			slots = append(slots, item)
		}
	}
	return slots, true
}

func junkSource(w *worldv1.SnapshotResponse, request *tradev1.JunkRequest) *worldv1.BackpackSlot {
	if w == nil || request == nil {
		return nil
	}
	slots, bag, slot := w.Backpack, uint32(0), request.BackpackSlot
	if request.InventoryRevision != 0 {
		if request.SourceBag < 1 || request.SourceBag > 4 || request.SourceSlot >= 36 ||
			request.BackpackRevision != 0 || request.BackpackSlot != 0 || w.InventoryRevision != request.InventoryRevision {
			return nil
		}
		var known bool
		slots, known = knownCarriedJunkSlots(w)
		if !known {
			return nil
		}
		for _, item := range slots {
			if item.ItemGuid == 0 {
				return nil
			}
		}
		bag, slot = request.SourceBag, request.SourceSlot
	} else if request.SourceBag != 0 || request.SourceSlot != 0 || request.BackpackSlot >= 16 ||
		!fullBackpack(w) || w.BackpackRevision != request.BackpackRevision {
		return nil
	}
	for _, item := range slots {
		if item.Bag == bag && item.Slot == slot && item.ItemGuid == request.ExpectedItemGuid &&
			item.MetadataKnown && item.Entry != 0 && item.StackCount != 0 {
			if request.InventoryRevision != 0 {
				for _, worn := range w.SelfEquipment {
					if worn.GetItemGuid() == item.ItemGuid {
						return nil
					}
				}
			}
			return item
		}
	}
	return nil
}

func carriedJunkRemovalConfirmed(w *worldv1.SnapshotResponse, pending *tradev1.JunkDisposal) bool {
	if pending == nil || pending.InventoryRevision == 0 || pending.SourceBag < 1 ||
		pending.SourceBag > 4 || pending.SourceSlot >= 36 {
		return false
	}
	slots, known := knownCarriedJunkSlots(w)
	if !known || w.InventoryRevision <= pending.InventoryRevision {
		return false
	}
	for _, bag := range w.InventoryBags {
		if bag.ContainerGuid == pending.ItemGuid {
			return false
		}
	}
	empty := false
	for _, item := range slots {
		if item.ItemGuid == pending.ItemGuid {
			return false
		}
		if item.Bag == pending.SourceBag && item.Slot == pending.SourceSlot {
			empty = item.ItemGuid == 0
		}
	}
	if !empty || !w.SelfEquipmentKnown || w.SelfEquipmentRevision == 0 || len(w.SelfEquipment) != 19 {
		return false
	}
	guids := make(map[uint64]bool)
	for _, bag := range w.InventoryBags {
		if bag.ContainerGuid != 0 {
			guids[bag.ContainerGuid] = true
		}
	}
	for _, item := range slots {
		if item.ItemGuid != 0 {
			guids[item.ItemGuid] = true
		}
	}
	var seen [19]bool
	for _, item := range w.SelfEquipment {
		if item == nil || item.Slot >= 19 || seen[item.Slot] || !item.SlotKnown {
			return false
		}
		seen[item.Slot] = true
		if item.ItemGuid == 0 {
			if item.Entry != 0 || item.StackCount != 0 || item.MetadataKnown {
				return false
			}
		} else {
			if !carriedJunkItemGUID(item.ItemGuid) || item.ItemGuid == pending.ItemGuid || guids[item.ItemGuid] ||
				!item.MetadataKnown || item.Entry == 0 || item.StackCount != 1 {
				return false
			}
			guids[item.ItemGuid] = true
		}
	}
	return true
}

// The public receipt retains its original revision. Later reads can establish
// a higher floor without converting an out-of-order absence into removal.
func (proof *carriedJunkProof) observe(w *worldv1.SnapshotResponse, pending *tradev1.JunkDisposal) {
	if proof == nil || w == nil || pending == nil || w.InventoryRevision == 0 {
		return
	}
	if w.InventoryRevision > proof.revision {
		proof.revision = w.InventoryRevision
	}
	present := false
	for _, bag := range w.InventoryBags {
		if bag == nil {
			continue
		}
		present = present || bag.ContainerGuid == pending.ItemGuid
		for _, item := range bag.Slots {
			present = present || item.GetItemGuid() == pending.ItemGuid
		}
	}
	for _, item := range w.Backpack {
		present = present || item.GetItemGuid() == pending.ItemGuid
	}
	for _, item := range w.SelfEquipment {
		present = present || item.GetItemGuid() == pending.ItemGuid
	}
	if present && w.InventoryRevision > proof.presentRevision {
		proof.presentRevision = w.InventoryRevision
	}
}

func (proof *carriedJunkProof) removalConfirmed(w *worldv1.SnapshotResponse, pending *tradev1.JunkDisposal) bool {
	if proof == nil || w == nil || pending == nil || w.InventoryRevision == 0 {
		return false
	}
	proof.observe(w, pending)
	return w.InventoryRevision >= proof.revision && w.InventoryRevision > proof.presentRevision &&
		carriedJunkRemovalConfirmed(w, pending)
}
