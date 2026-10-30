package buddyprofessions

import (
	"fmt"
	"math"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

func carriedItemGUID(guid uint64) bool {
	return guid>>48 == 0x4000 && guid&0x0000ffffffffffff != 0
}

// All carried locations must be coherent before any material count is usable.
// Only the built-in backpack and verified 20-slot bag41599 provide general
// output capacity; contents of other observed bags still count as materials.
func carriedInventory(w *worldv1.SnapshotResponse) (map[uint32]uint32, uint32, error) {
	bad := func(reason string) (map[uint32]uint32, uint32, error) {
		return nil, 0, fmt.Errorf("actual own carried inventory: %s", reason)
	}
	if w == nil || !w.InventoryKnown || w.InventoryRevision == 0 || len(w.InventoryBags) != 5 {
		return bad("complete five-bag topology and revision are required")
	}
	var bags [5]*worldv1.InventoryBag
	guids := make(map[uint64]bool, 164)
	total := uint32(0)
	for _, bag := range w.InventoryBags {
		if bag == nil || bag.Bag > 4 || bags[bag.Bag] != nil || !bag.SlotKnown || !bag.ContentsKnown || bag.SlotCount > 36 || len(bag.Slots) != int(bag.SlotCount) {
			return bad("bag slot, contents or bounded layout is unknown or duplicated")
		}
		bags[bag.Bag] = bag
		if bag.Bag == 0 {
			if bag.ContainerGuid != 0 || bag.Entry != 0 || bag.MetadataKnown || bag.SlotCount != 16 {
				return bad("built-in backpack must contain exactly sixteen slots")
			}
		} else if bag.ContainerGuid == 0 {
			if bag.Entry != 0 || bag.MetadataKnown || bag.SlotCount != 0 {
				return bad("empty equipped bag slot has container metadata or contents")
			}
		} else {
			if !carriedItemGUID(bag.ContainerGuid) || guids[bag.ContainerGuid] || !bag.MetadataKnown || bag.Entry == 0 || bag.SlotCount == 0 || bag.Entry == 41599 && bag.SlotCount != 20 {
				return bad("occupied container metadata or identity is invalid")
			}
			guids[bag.ContainerGuid] = true
		}
		total += bag.SlotCount
		if total > 160 {
			return bad("carried slot capacity exceeds 160")
		}
	}
	counts := make(map[uint32]uint32)
	free := uint32(0)
	for _, bag := range bags {
		if bag == nil {
			return bad("carried bag identity is missing")
		}
		var seen [36]bool
		general := bag.Bag == 0 || bag.Entry == 41599 && bag.SlotCount == 20
		for _, row := range bag.Slots {
			if row == nil || row.Bag != bag.Bag || row.Slot >= bag.SlotCount || seen[row.Slot] {
				return bad("carried item slot is missing, duplicated or misaddressed")
			}
			seen[row.Slot] = true
			if row.ItemGuid == 0 {
				if row.Entry != 0 || row.StackCount != 0 || row.MetadataKnown {
					return bad("empty carried slot has item metadata")
				}
				if general {
					free++
				}
				continue
			}
			if !carriedItemGUID(row.ItemGuid) || guids[row.ItemGuid] || !row.MetadataKnown || row.Entry == 0 || row.StackCount == 0 {
				return bad("occupied carried item metadata is unknown or identity is duplicated")
			}
			guids[row.ItemGuid] = true
			if row.StackCount > math.MaxUint32-counts[row.Entry] {
				return bad("aggregate carried material count overflows")
			}
			counts[row.Entry] += row.StackCount
		}
	}
	return counts, free, nil
}
