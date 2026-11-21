package gamecatalog

import (
	_ "embed"
	"encoding/json"
	"sync"
)

//go:embed fear-break-items.json
var fearBreakItemData []byte
var fearBreakItemsOnce sync.Once
var fearBreakItems map[uint32]ItemUseCooldown

// ItemUseCooldown is a raw item-template override; negative values retain the
// DBC fallback semantics in Player::AddSpellAndCategoryCooldowns.
type ItemUseCooldown struct {
	CooldownMs         int32  `json:"cooldownMs"`
	Category           uint32 `json:"category"`
	CategoryCooldownMs int32  `json:"categoryCooldownMs"`
}

// FearBreakItemCooldown returns exact source facts, not an item preference.
func FearBreakItemCooldown(entry uint32) (ItemUseCooldown, bool) {
	fearBreakItemsOnce.Do(func() {
		var source struct {
			Items map[uint32]ItemUseCooldown `json:"items"`
		}
		if err := json.Unmarshal(fearBreakItemData, &source); err != nil {
			panic("invalid pinned fear-break item cooldowns: " + err.Error())
		}
		fearBreakItems = source.Items
	})
	row, ok := fearBreakItems[entry]
	return row, ok && TrinketHasUseSpell(entry, 42292)
}
