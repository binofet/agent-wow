package buddyinventory

// Plain starter clothing uses armor subclass0 despite occupying an armor slot.
// It contributes no armor, resistances, stats or special effect to preserve.
// This only qualifies the displaced baseline; candidate usability is unchanged.
func comparableArmorBaseline(m itemMetadata) bool {
	if m.ordinaryArmor() {
		return true
	}
	if m.entry == 0 || m.subclass != 0 || m.quality > 1 || m.armor != 0 || m.statCount != 0 ||
		m.resistances != [6]uint32{} || m.maxDurability != 0 || m.bonding != 0 ||
		m.requiredLevel != 0 || m.hasRequirement || m.requiredSkill != 0 || m.requiredSkillRank != 0 ||
		m.requiredSpell != 0 || m.requiredHonorRank != 0 || m.requiredCityRank != 0 ||
		m.requiredReputationFaction != 0 || m.requiredReputationRank != 0 ||
		m.allowableClass != ^uint32(0) || m.allowableRace != ^uint32(0) {
		return false
	}
	// Reuse every ordinary-template restriction without admitting subclass0
	// candidates or changing the actual observed template.
	plain := m
	plain.subclass = 1
	return plain.ordinaryArmor()
}
