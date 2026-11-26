package buddyinventory

import worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"

const (
	firstFingerSlot    = 10
	secondFingerSlot   = 11
	mainhandSlot       = 15
	offhandSlot        = 16
	dualWieldSkill     = 118
	shieldSkill        = 433
	uniqueEquippedFlag = 0x00080000
)

func (m itemMetadata) ordinaryRing() bool {
	return m.known && m.entry != 0 && m.class == 4 && m.subclass == 0 && m.inventoryType == 11 &&
		m.quality <= 4 && m.flags&^uniqueEquippedFlag == 0 && m.flags2 == 0 &&
		m.containerSlots == 0 && !m.hasSpell && m.bonding <= 2 && m.startQuest == 0 &&
		m.lockID == 0 && m.pageText == 0 && m.bagFamily == 0 && m.duration == 0 &&
		m.maxCount <= 1 && m.stackable == 1 && m.itemLimitCategory == 0 && m.itemSet == 0 &&
		m.scalingDistribution == 0 && m.scalingValue == 0 && !m.specialArmor
}

func (m itemMetadata) oneHandDagger() bool {
	return m.ordinaryMainhandWeapon() && m.subclass == 15 && m.inventoryType == 13
}

func (m itemMetadata) shield() bool {
	return m.class == 4 && m.subclass == 6 && m.inventoryType == 14
}

// No ordering preference: enumerate legal physical destinations in slot order.
func gearDestinationSlots(w *worldv1.SnapshotResponse, m itemMetadata, equipment [19]*worldv1.EquipmentSlotState, templates map[uint32]itemMetadata) []uint32 {
	if !m.known {
		return nil
	}
	if m.shield() {
		return []uint32{offhandSlot}
	}
	if m.class == 4 && m.inventoryType == 11 {
		if m.flags&uniqueEquippedFlag != 0 || m.maxCount == 1 {
			for _, slot := range []uint32{firstFingerSlot, secondFingerSlot} {
				old := equipment[slot]
				if old.ItemGuid != 0 && (!old.MetadataKnown || old.Entry == m.entry) {
					return nil
				}
			}
		}
		return []uint32{firstFingerSlot, secondFingerSlot}
	}
	if m.class == 2 && m.weaponProficiency() != 0 {
		if m.subclass == 15 && m.inventoryType == 13 {
			main := equipment[mainhandSlot]
			base := templates[main.Entry]
			skills, _ := ownedSkills(w)
			off := equipment[offhandSlot]
			old := templates[off.Entry]
			observedOffhandWeapon := off.MetadataKnown && old.known && old.class == 2 && old.subclass == 15 && old.inventoryType == 13
			if (main.ItemGuid == 0 || main.MetadataKnown && base.known && base.inventoryType != 17) && (skills[dualWieldSkill] != 0 || observedOffhandWeapon) {
				return []uint32{mainhandSlot, offhandSlot}
			}
		}
		return []uint32{mainhandSlot}
	}
	if m.class == 4 && m.subclass >= 1 && m.subclass <= 4 {
		if slot, ok := armorSlot(m); ok {
			return []uint32{slot}
		}
	}
	return nil
}
