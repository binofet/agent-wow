package buddyactions

// Verified player rank chains: public AzerothCore spell_ranks.sql. Instant,
// reagent-free mana costs and target ranges come from client build12340
// Spell.dbc SHA d5cce1a83550dcfa9eb2f0251dbb11fd24c272534b2b1a9b230924a44d817ab3;
// fields28/42/43/46/52..67/86..88/136/153/204, SpellRange4=30yd.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/shared/DataStores/DBCStructure.h
// NPC-only same-name spells and reagent-based raid buffs are not inferred.
type buffSpell struct {
	id, family, rank, class, manaPercent uint32
	name                                 string
	party                                bool
}

var buffCatalog = func() []buffSpell {
	var out []buffSpell
	add := func(family, class, cost uint32, name string, party bool, ids ...uint32) {
		for i, id := range ids {
			out = append(out, buffSpell{id: id, family: family, rank: uint32(i + 1), class: class, manaPercent: cost, name: name, party: party})
		}
	}
	add(1243, 5, 27, "Power Word: Fortitude", true, 1243, 1244, 1245, 2791, 10937, 10938, 25389, 48161)
	add(588, 5, 14, "Inner Fire", false, 588, 7128, 602, 1006, 10951, 10952, 25431, 48040, 48168)
	add(1459, 8, 31, "Arcane Intellect", true, 1459, 1460, 1461, 10156, 10157, 27126, 42995)
	add(168, 8, 24, "Frost Armor", false, 168, 7300, 7301)
	add(687, 9, 31, "Demon Skin", false, 687, 696)
	return out
}()

func buffMetadata(id uint32) buffSpell {
	for _, r := range buffCatalog {
		if r.id == id {
			return r
		}
	}
	return buffSpell{}
}
func highestBuff(known map[uint32]bool, family uint32) buffSpell {
	var best buffSpell
	for _, r := range buffCatalog {
		if r.family == family && known[r.id] && r.rank > best.rank {
			best = r
		}
	}
	return best
}

// A stronger, mutually exclusive learned armor aura must never be overwritten
// by the starter armor controller, even though its automation is not exposed.
func strongerArmorAura(id, family uint32) bool {
	var ids []uint32
	switch family {
	case 168:
		ids = []uint32{7302, 7320, 10219, 10220, 27124, 43008, 30482, 43045, 43046}
	case 687:
		ids = []uint32{706, 1086, 11733, 11734, 11735, 27260, 47793, 47889, 28176, 28189, 47892, 47893}
	}
	for _, v := range ids {
		if id == v {
			return true
		}
	}
	return false
}
