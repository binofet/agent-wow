package buddyactions

import "github.com/agent-wow/agent-wow/pkg/spellcatalog"

// Compatibility view of a generated spell contract; no per-class rank lists.
type buffSpell struct {
	id, family, rank, class, manaPercent uint32
	name                                 string
	party                                bool
}

func buffMetadata(id uint32) buffSpell {
	r, ok := spellcatalog.Default().Lookup(id)
	if !ok || r.Capability().Kind != spellcatalog.Buff || r.CastMs != 0 || r.Cost != 0 || r.CostPerLevel != 0 || r.Power != 0 {
		return buffSpell{}
	}
	return buffSpell{id: r.ID, family: r.Family, rank: r.Rank, manaPercent: r.CostPercent, name: r.Name, party: r.Capability().Target == "ally"}
}
func highestBuff(known map[uint32]bool, family uint32) buffSpell {
	var best buffSpell
	for id, on := range known {
		if on {
			r := buffMetadata(id)
			if r.family == family && r.rank > best.rank {
				best = r
			}
		}
	}
	return best
}
func strongerArmorAura(id, family uint32) bool {
	a, ok := spellcatalog.Default().Lookup(id)
	if !ok {
		return false
	}
	b, ok := spellcatalog.Default().Lookup(family)
	if !ok || a.Family == b.Family || a.Level < b.Level {
		return false
	}
	// Published SpellInfo family masks group alternative armors, without ID lists.
	category := func(r spellcatalog.Spell) uint32 {
		if r.FamilySet == 3 && r.FamilyFlags[0]&0x12040000 != 0 {
			return 1
		}
		if r.FamilySet == 5 && (r.FamilyFlags[1]&0x20000020 != 0 || r.FamilyFlags[2]&0x10 != 0) {
			return 2
		}
		return 0
	}
	if group := category(a); group != 0 && group == category(b) {
		return true
	}
	for _, x := range a.Effects {
		for _, y := range b.Effects {
			if x.Kind == 6 && y.Kind == 6 && x.A == 1 && y.A == 1 && x.Aura == 22 && y.Aura == 22 && x.Misc == y.Misc && x.Misc == 1 {
				return true
			}
		}
	}
	return false
}

func intellectBuff(id uint32) bool {
	r, _ := spellcatalog.Default().Lookup(id)
	for _, e := range r.Effects {
		if e.Kind == 6 && e.Aura == 29 && e.Misc == 3 {
			return true
		}
	}
	return false
}
