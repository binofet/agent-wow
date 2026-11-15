package buddyinventory

import worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"

const (
	firstFingerSlot    = 10
	secondFingerSlot   = 11
	mainhandSlot       = 15
	offhandSlot        = 16
	dualWieldSkill     = 118
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

func equipmentSlots(w *worldv1.SnapshotResponse, m itemMetadata, equipment [19]*worldv1.EquipmentSlotState, templates map[uint32]itemMetadata) []uint32 {
	if m.ordinaryRing() {
		if m.flags&uniqueEquippedFlag != 0 || m.maxCount == 1 {
			for _, slot := range []uint32{firstFingerSlot, secondFingerSlot} {
				if old := equipment[slot]; old.ItemGuid != 0 && (!old.MetadataKnown || old.Entry == m.entry) {
					return nil
				}
			}
		}
		return preferEmptySlot(equipment, firstFingerSlot, secondFingerSlot)
	}
	if m.ordinaryMainhandWeapon() {
		if m.oneHandDagger() && w.GetSelf().GetClassId() == 4 {
			main := equipment[mainhandSlot]
			mainTemplate := templates[main.Entry]
			// An unknown or two-handed main weapon cannot prove an offhand fit.
			if main.ItemGuid == 0 || main.MetadataKnown && mainTemplate.known && mainTemplate.inventoryType != 17 {
				off := equipment[offhandSlot]
				skills, _ := ownedSkills(w)
				if skills[dualWieldSkill] != 0 || off.MetadataKnown && templates[off.Entry].oneHandDagger() {
					return preferEmptySlot(equipment, mainhandSlot, offhandSlot)
				}
			}
		}
		return []uint32{mainhandSlot}
	}
	if slot, ok := armorSlot(m); ok {
		return []uint32{slot}
	}
	return nil
}

func preferEmptySlot(equipment [19]*worldv1.EquipmentSlotState, first, second uint32) []uint32 {
	if equipment[first].ItemGuid != 0 && equipment[second].ItemGuid == 0 {
		return []uint32{second, first}
	}
	return []uint32{first, second}
}
