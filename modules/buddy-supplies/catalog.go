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

func catalogLookup(entry uint32) (catalogItem, bool) {
	var skill uint32
	once := true
	switch entry {
	case 6256: // Fishing Pole
		skill = 356
	case 7005: // Skinning Knife
		skill = 393
	case 2901: // Mining Pick
		skill = 186
	case 6217: // Copper Rod
		skill = 333
	case 4470: // Flint and Tinder
		skill = 185
	case 4471: // Simple Wood
		skill, once = 185, false
	case 2320: // Coarse Thread
		skill, once = 197, false
	default:
		return catalogItem{}, false
	}
	return catalogItem{entry: entry, skillID: skill, oncePerCharacter: once}, true
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
	if spec.entry == 6256 && (item.ItemClass != 2 || item.Subclass != 20 || item.InventoryType != 17) {
		return "server_item_is_not_supported_fishing_pole"
	}
	return ""
}
