package buddysupplies

import tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"

// This is purchase policy, not item metadata or a price list. Skill IDs are
// AzerothCore SharedDefines.h SkillType values. Actual templates and prices
// must come from the normal client item query and current vendor offer.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/shared/SharedDefines.h
type catalogItem struct {
	entry, skillID   uint32
	oncePerCharacter bool
}

var purchaseCatalog = []catalogItem{
	{6256, 356, true},  // Fishing Pole.
	{6365, 356, true},  // Strong Fishing Pole; level/rank come from actual metadata.
	{7005, 393, true},  // Skinning Knife.
	{2901, 186, true},  // Mining Pick.
	{6217, 333, true},  // Copper Rod.
	{4470, 185, true},  // Flint and Tinder.
	{4471, 185, false}, // Simple Wood.
	{2320, 197, false}, // Coarse Thread.
}

func catalogLookup(entry uint32) (catalogItem, bool) {
	for _, item := range purchaseCatalog {
		if item.entry == entry {
			return item, true
		}
	}
	return catalogItem{}, false
}

// Pole equipment uses the actual complete template, independently of the
// bounded shopping catalog. A carried item ID or a name is not type evidence.
func fishingPole(item *tradev1.ItemDetails) bool {
	return item != nil && item.ItemId != 0 && item.Available && !item.Pending &&
		item.ItemClass == 2 && item.Subclass == 20 && item.InventoryType == 17
}

// A catalog match grants no ownership or character eligibility. Those checks
// belong to the service using the current body, skills and backpack. Apart
// from the observed pole shape, no non-pole template fields are guessed.
func validateCatalogMetadata(item *tradev1.ItemDetails, spec catalogItem) string {
	canonical, ok := catalogLookup(spec.entry)
	if !ok || canonical != spec {
		return "item_outside_supplies_catalog"
	}
	if item == nil || item.ItemId != spec.entry || !item.Available || item.Pending {
		return "complete_server_item_metadata_required"
	}
	if spec.skillID == 356 && !fishingPole(item) {
		return "server_item_is_not_supported_fishing_pole"
	}
	return ""
}
