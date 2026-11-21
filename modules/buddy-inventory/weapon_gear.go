package buddyinventory

import (
	"math"
)

// These ordinary weapon families have one supported destination: mainhand.
// Staff eligibility additionally requires the actual offhand to be empty, so
// the existing single displaced-item receipt remains complete.
func (m itemMetadata) weaponProficiency() uint32 {
	if m.class == 2 && m.subclass == 4 && (m.inventoryType == 13 || m.inventoryType == 21) {
		return 54 // Item::GetSkill / SharedDefines: one-handed maces.
	}
	if m.class == 2 && m.subclass == 15 && (m.inventoryType == 13 || m.inventoryType == 21) {
		return 173 // Item::GetSkill / SharedDefines: daggers.
	}
	if m.class == 2 && m.subclass == 10 && m.inventoryType == 17 {
		return 136 // Staves.
	}
	return 0
}

func (m itemMetadata) ordinaryMainhandWeapon() bool {
	_, damageKnown := m.physicalWeaponDPS()
	return m.known && m.entry != 0 && m.weaponProficiency() != 0 && damageKnown && m.quality <= 4 &&
		m.flags == 0 && m.flags2 == 0 && m.containerSlots == 0 && !m.hasSpell && m.bonding <= 2 &&
		m.startQuest == 0 && m.lockID == 0 && m.pageText == 0 && m.bagFamily == 0 && m.duration == 0 &&
		m.maxCount == 0 && m.stackable == 1 && m.itemLimitCategory == 0 && m.itemSet == 0 &&
		m.scalingDistribution == 0 && m.scalingValue == 0 && !m.specialArmor
}

func (m itemMetadata) physicalWeaponDPS() (float64, bool) {
	if !m.known || m.delay == 0 {
		return 0, false
	}
	var damage float64
	for _, d := range m.damages {
		lo, hi := float64(d.minimum), float64(d.maximum)
		if math.IsNaN(lo) || math.IsNaN(hi) || math.IsInf(lo, 0) || math.IsInf(hi, 0) || lo < 0 || hi < lo || d.school != 0 {
			return 0, false
		}
		damage += (lo + hi) / 2
	}
	dps := damage * 1000 / float64(m.delay)
	return dps, dps > 0 && !math.IsInf(dps, 0) && !math.IsNaN(dps)
}
