package gamecatalog

import (
	"encoding/json"
	"slices"
	"sync"
)

type usableEquipment struct {
	ItemClass     uint32   `json:"itemClass"`
	InventoryType uint32   `json:"inventoryType"`
	UseSpells     []uint32 `json:"useSpells"`
}

var equipmentUsesOnce sync.Once
var equipmentUses map[uint32]usableEquipment

// TrinketHasUseSpell checks the pinned item template. The caller still needs
// the exact item GUID and equipped slot observed in the character's own view.
func TrinketHasUseSpell(entry, spell uint32) bool {
	equipmentUsesOnce.Do(func() {
		var source struct {
			Items map[uint32]usableEquipment `json:"items"`
		}
		if err := json.Unmarshal(contentCatalog, &source); err != nil {
			panic("invalid generated item catalog: " + err.Error())
		}
		equipmentUses = source.Items
	})
	item := equipmentUses[entry]
	return item.ItemClass == 4 && item.InventoryType == 12 && slices.Contains(item.UseSpells, spell)
}
