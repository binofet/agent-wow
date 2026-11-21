package buddyactions

import "github.com/agent-wow/agent-wow/pkg/spellcatalog"

// Compatibility view of a generated spell contract; no per-class rank lists.
type buffSpell struct {
	id, family, rank, class, manaPercent uint32
	name                                 string
	party, waterBreathing                bool
	rangeYards                           float64
}

func buffMetadata(id uint32) buffSpell {
	r, ok := spellcatalog.Default().Lookup(id)
	if !ok || r.Capability().Kind != spellcatalog.Buff || r.CastMs != 0 || r.Cost != 0 || r.CostPerLevel != 0 || r.Power != 0 {
		return buffSpell{}
	}
	return buffSpell{id: r.ID, family: r.Family, rank: r.Rank, manaPercent: r.CostPercent, name: r.Name, party: r.Capability().Target == "ally", waterBreathing: r.ProvidesWaterBreathing(), rangeYards: r.MaxFriendly}
}
