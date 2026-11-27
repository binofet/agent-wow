package harvestguard

import worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"

// CompleteOwnItemCounts accepts only a complete, non-overlapping carried inventory.
func CompleteOwnItemCounts(w *worldv1.SnapshotResponse, self uint64) (map[uint32]uint64, bool) {
	if w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.Present || !w.InventoryKnown || w.InventoryRevision == 0 || len(w.InventoryBags) != 5 {
		return nil, false
	}
	validGUID := func(g uint64) bool { return g>>48 == 0x4000 && uint32(g) != 0 }
	bags, guids := [5]bool{}, map[uint64]bool{}
	counts, total := map[uint32]uint64{}, 0
	for _, b := range w.InventoryBags {
		if b == nil || b.Bag > 4 || bags[b.Bag] || !b.SlotKnown || !b.ContentsKnown || b.SlotCount > 36 || len(b.Slots) != int(b.SlotCount) {
			return nil, false
		}
		bags[b.Bag] = true
		if b.Bag == 0 {
			if b.ContainerGuid != 0 || b.Entry != 0 || b.MetadataKnown || b.SlotCount != 16 {
				return nil, false
			}
		} else if b.ContainerGuid == 0 {
			if b.Entry != 0 || b.MetadataKnown || b.SlotCount != 0 {
				return nil, false
			}
		} else {
			if !validGUID(b.ContainerGuid) || guids[b.ContainerGuid] || !b.MetadataKnown || b.Entry == 0 || b.SlotCount == 0 || b.Entry == 41599 && b.SlotCount != 20 {
				return nil, false
			}
			guids[b.ContainerGuid] = true
		}
		total += len(b.Slots)
		if total > 160 {
			return nil, false
		}
		seen := [36]bool{}
		for _, slot := range b.Slots {
			if slot == nil || slot.Bag != b.Bag || slot.Slot >= b.SlotCount || seen[slot.Slot] {
				return nil, false
			}
			seen[slot.Slot] = true
			if slot.ItemGuid == 0 {
				if slot.Entry != 0 || slot.StackCount != 0 || slot.MetadataKnown {
					return nil, false
				}
			} else {
				if !validGUID(slot.ItemGuid) || guids[slot.ItemGuid] || !slot.MetadataKnown || slot.Entry == 0 || slot.StackCount == 0 {
					return nil, false
				}
				guids[slot.ItemGuid] = true
				counts[slot.Entry] += uint64(slot.StackCount)
			}
		}
	}
	return counts, true
}
