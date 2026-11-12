package buddyprofessions

import "math"

// Item-push coordinates use physical bag-equip slots19..22 for carried bags.
// Existing-stack pushes omit the slot, but still require an observed container.
func carriedPushLocation(v worldView, bag byte, slot uint32) bool {
	if v.raw == nil {
		return false
	}
	var logical uint32
	if bag == 255 {
		if slot != math.MaxUint32 && (slot < 23 || slot >= 39) {
			return false
		}
		return true
	}
	if bag < 19 || bag > 22 {
		return false
	}
	logical = uint32(bag) - 18
	for _, b := range v.raw.InventoryBags {
		if b != nil && b.Bag == logical && b.SlotKnown && b.ContentsKnown && b.ContainerGuid != 0 && b.MetadataKnown && b.SlotCount > 0 {
			return slot == math.MaxUint32 || slot < b.SlotCount
		}
	}
	return false
}
