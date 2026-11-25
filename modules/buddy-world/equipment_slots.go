package buddyworld

import worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"

// Build12340 UpdateFields.h: UNIT_END0x94 + private INV_SLOT_HEAD0xb0.
// Player.h EquipmentSlots ends at19, before the four equipped bag references.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Object/Updates/UpdateFields.h
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Player/Player.h
const (
	playerEquipmentFirst = 0x144
	equipmentSlots       = 19
	itemEnhancementFirst = 0x16
	itemEnhancementEnd   = 0x3c // 12x3 enchantment words, property seed and random-properties ID.
)

type equipmentItemView struct {
	guid                                 uint64
	slotKnown, metadata, durabilityKnown bool
	entry, count, durability, maximum    uint32
	enhancementsKnown, enhancements      bool
	enhancementDetails                   itemEnhancementView
}

type equipmentView struct {
	baseline, known bool
	slots           [equipmentSlots]equipmentItemView
}

// Comparable, age-free projection: revisions change only after whole-packet
// validation and include actual slot, ownership, metadata and durability changes.
func (s *observations) equipmentView() equipmentView {
	p := s.players[0]
	r := equipmentView{}
	if !p.isSelf || !p.present || !p.equipmentKnown {
		return r
	}
	r.baseline, r.known = true, true
	seen := make(map[uint64]bool, equipmentSlots)
	for slot, guid := range p.equipment {
		v := &r.slots[slot]
		v.guid, v.slotKnown = guid, guid == 0 || itemGUID(guid)
		if !v.slotKnown || guid != 0 && seen[guid] {
			r.known = false
		}
		seen[guid] = guid != 0
		item := s.ownedItem(guid)
		if guid == 0 || !v.slotKnown || item == nil || item.isContainer || item.inventoryEpoch != p.inventoryEpoch {
			continue
		}
		if item.known[objectEntry] && item.fields[objectEntry] != 0 && item.known[itemStack] && item.fields[itemStack] != 0 {
			v.entry, v.count, v.metadata = item.fields[objectEntry], item.fields[itemStack], true
			v.durability, v.maximum = item.fields[0x3c], item.fields[0x3d]
			v.durabilityKnown = item.known[0x3c] && item.known[0x3d] && v.durability <= v.maximum
			v.enhancementsKnown, v.enhancements = itemEnhancements(item)
			v.enhancementDetails = observedEnhancementDetails(item)
		}
	}
	return r
}

func (s *observations) appendEquipment(r *worldv1.SnapshotResponse) {
	v := s.equipmentView()
	r.SelfEquipmentKnown, r.SelfEquipmentRevision = v.known, s.equipmentRevision
	if !v.baseline {
		return
	}
	for slot, p := range v.slots {
		r.SelfEquipment = append(r.SelfEquipment, &worldv1.EquipmentSlotState{Slot: uint32(slot), SlotKnown: p.slotKnown, ItemGuid: p.guid, MetadataKnown: p.metadata, Entry: p.entry, StackCount: p.count, DurabilityKnown: p.durabilityKnown, Durability: p.durability, MaxDurability: p.maximum, EnhancementsKnown: p.enhancementsKnown, HasEnhancements: p.enhancements, EnhancementDetails: p.enhancementDetails.message()})
	}
}

func itemEnhancements(p *observed) (known, present bool) {
	for f := itemEnhancementFirst; f < itemEnhancementEnd; f++ {
		if !p.known[f] {
			return false, false
		}
		present = present || p.fields[f] != 0
	}
	return true, present
}

// Fixed comparable facts participate in the existing age-free revision view.
// Missing fields remain unknown independently; zero is a known native value.
type itemEnchantmentView struct {
	idKnown, durationKnown, chargesKnown bool
	id, duration, charges                uint32
}
type itemEnhancementView struct {
	slots                  [12]itemEnchantmentView
	seedKnown, randomKnown bool
	seed                   uint32
	random                 int32
}

func observedEnhancementDetails(p *observed) itemEnhancementView {
	var v itemEnhancementView
	for i := range v.slots {
		at := itemEnhancementFirst + i*3
		v.slots[i] = itemEnchantmentView{p.known[at], p.known[at+1], p.known[at+2], p.fields[at], p.fields[at+1], p.fields[at+2]}
	}
	v.seedKnown, v.randomKnown = p.known[0x3a], p.known[0x3b]
	v.seed, v.random = p.fields[0x3a], int32(p.fields[0x3b])
	return v
}
func (v itemEnhancementView) message() *worldv1.ItemEnhancementState {
	out := &worldv1.ItemEnhancementState{PropertySeedKnown: v.seedKnown, PropertySeed: v.seed, RandomPropertyIdKnown: v.randomKnown, RandomPropertyId: v.random}
	// No details means no current owned metadata, not a complete empty item.
	any := v.seedKnown || v.randomKnown
	for _, s := range v.slots {
		any = any || s.idKnown || s.durationKnown || s.chargesKnown
	}
	if !any {
		return nil
	}
	for i, s := range v.slots {
		out.Enchantments = append(out.Enchantments, &worldv1.ItemEnchantmentSlot{Slot: uint32(i), IdKnown: s.idKnown, Id: s.id, DurationKnown: s.durationKnown, DurationRaw: s.duration, ChargesKnown: s.chargesKnown, Charges: s.charges})
	}
	return out
}
