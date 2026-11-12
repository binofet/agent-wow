package buddyinventory

import (
	"context"
	"encoding/binary"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Ordinary armor has a single unambiguous equipment destination. The separately
// bounded weapon policy adds mainhand daggers/maces and empty-offhand staves.
func armorSlot(m itemMetadata) (uint32, bool) {
	switch m.inventoryType {
	case 1:
		return 0, true
	case 3:
		return 2, true
	case 5, 20:
		return 4, true
	case 6:
		return 5, true
	case 7:
		return 6, true
	case 8:
		return 7, true
	case 9:
		return 8, true
	case 10:
		return 9, true
	case 16:
		return 14, true
	}
	return 0, false
}
func (m itemMetadata) ordinaryArmor() bool {
	_, slot := armorSlot(m)
	return m.known && slot && m.class == 4 && m.subclass >= 1 && m.subclass <= 4 && m.quality <= 4 &&
		m.flags == 0 && m.flags2 == 0 && m.containerSlots == 0 && !m.hasSpell && m.bonding <= 2 &&
		m.startQuest == 0 && m.lockID == 0 && m.pageText == 0 && m.bagFamily == 0 && m.duration == 0 &&
		m.maxCount == 0 && m.stackable == 1 && m.itemLimitCategory == 0 && m.itemSet == 0 &&
		m.scalingDistribution == 0 && m.scalingValue == 0 && !m.specialArmor
}

func equipmentView(v view) ([19]*worldv1.EquipmentSlotState, bool) {
	var out [19]*worldv1.EquipmentSlotState
	w := v.w
	if w == nil || !w.SelfEquipmentKnown || w.SelfEquipmentRevision == 0 || len(w.SelfEquipment) != len(out) {
		return out, false
	}
	guids := map[uint64]bool{}
	for _, i := range v.slots {
		if i.ItemGuid != 0 {
			guids[i.ItemGuid] = true
		}
	}
	for _, b := range v.bags {
		if b != nil && b.ContainerGuid != 0 {
			guids[b.ContainerGuid] = true
		}
	}
	for _, i := range w.SelfEquipment {
		if i == nil || i.Slot >= uint32(len(out)) || out[i.Slot] != nil || !i.SlotKnown {
			return out, false
		}
		if i.ItemGuid == 0 {
			if i.Entry != 0 || i.StackCount != 0 || i.MetadataKnown {
				return out, false
			}
		} else {
			if !itemGUID(i.ItemGuid) || guids[i.ItemGuid] || i.MetadataKnown && (i.Entry == 0 || i.StackCount != 1) {
				return out, false
			}
			guids[i.ItemGuid] = true
		}
		out[i.Slot] = i
	}
	return out, true
}

func (s *Service) equipmentStateLocked(v view) {
	s.state.EquipmentKnown = false
	s.state.Equipment = nil
	if _, known := equipmentView(v); known {
		s.state.EquipmentKnown = true
		s.state.EquipmentRevision = v.w.SelfEquipmentRevision
		s.state.Equipment = v.w.SelfEquipment
	}
}

// Validate all128 private slots before using a proficiency. Public player
// observations and a partial list cannot establish that an armor skill exists.
func ownedSkills(w *worldv1.SnapshotResponse) (map[uint32]uint32, bool) {
	if w == nil || !w.SelfSkillsKnown || w.SelfSkillsRevision == 0 || len(w.SelfSkills) != 128 {
		return nil, false
	}
	out := map[uint32]uint32{}
	var seen [128]bool
	for _, k := range w.SelfSkills {
		if k == nil || k.Slot >= 128 || seen[k.Slot] || k.SkillId > 65535 || k.Value > k.MaxValue || k.MaxValue > 65535 {
			return nil, false
		}
		seen[k.Slot] = true
		if k.SkillId == 0 {
			if k.Value != 0 || k.MaxValue != 0 {
				return nil, false
			}
			continue
		}
		if _, exists := out[k.SkillId]; exists {
			return nil, false
		}
		out[k.SkillId] = k.Value // Ignore transient bonuses for a conservative proficiency gate.
	}
	return out, true
}

func armorUsability(w *worldv1.SnapshotResponse, m itemMetadata) string {
	if !m.ordinaryArmor() {
		return "only_ordinary_single_slot_armor_supported"
	}
	// SharedDefines.h / Item::GetSkill: cloth415, leather414, mail413, plate293.
	return gearUsability(w, m, [5]uint32{0, 415, 414, 413, 293}[m.subclass])
}

func gearUsability(w *worldv1.SnapshotResponse, m itemMetadata, proficiency uint32) string {
	p := w.GetSelf()
	if p == nil || !p.ClassKnown || p.ClassId == 0 || p.ClassId > 11 || !p.RaceKnown || p.RaceId == 0 || p.RaceId > 11 || !p.LevelKnown || p.Level == 0 {
		return "actual_class_race_level_required"
	}
	if m.allowableClass&(uint32(1)<<(p.ClassId-1)) == 0 || m.allowableRace&(uint32(1)<<(p.RaceId-1)) == 0 {
		return "item_class_or_race_restricted"
	}
	if p.Level < m.requiredLevel {
		return "item_required_level_not_met"
	}
	if m.requiredSpell != 0 || m.requiredHonorRank != 0 || m.requiredCityRank != 0 || m.requiredReputationFaction != 0 || m.requiredReputationRank != 0 {
		return "unverified_special_item_requirement"
	}
	skills, ok := ownedSkills(w)
	if !ok {
		return "complete_own_skill_baseline_required"
	}
	if skills[proficiency] == 0 {
		if m.class == 2 {
			return "actual_weapon_proficiency_required"
		}
		return "actual_armor_proficiency_required"
	}
	if m.requiredSkill != 0 && (skills[m.requiredSkill] == 0 || skills[m.requiredSkill] < m.requiredSkillRank) {
		return "item_required_skill_not_met"
	}
	return ""
}

func (s *Service) gearEligibilityLocked(i *worldv1.BackpackSlot) (uint32, uint64, bool, string) {
	m := s.metadata[i.Entry]
	slot, supported := armorSlot(m)
	weapon := m.ordinaryMainhandWeapon()
	if weapon {
		slot, supported = 15, true
	}
	if !i.MetadataKnown || !m.known {
		return 0, 0, false, "complete_owned_item_metadata_pending"
	}
	if !supported || i.StackCount != 1 {
		return 0, 0, false, "only_ordinary_single_slot_armor_supported"
	}
	var reason string
	if weapon {
		reason = gearUsability(s.view.w, m, m.weaponProficiency())
	} else {
		reason = armorUsability(s.view.w, m)
	}
	if reason != "" {
		return slot, 0, false, reason
	}
	if m.maxDurability != 0 && (!i.DurabilityKnown || i.MaxDurability != m.maxDurability || i.Durability == 0 || i.Durability > i.MaxDurability) {
		return slot, 0, false, "known_unbroken_owned_armor_required"
	}
	equipment, known := equipmentView(s.view)
	if !known {
		return slot, 0, false, "complete_own_equipment_baseline_required"
	}
	current := equipment[slot]
	if weapon && equipment[16].ItemGuid != 0 && (m.inventoryType == 17 || current.ItemGuid != 0 && s.metadata[current.Entry].inventoryType == 17) {
		return slot, equipment[15].ItemGuid, false, "two_hand_weapon_requires_known_empty_offhand"
	}
	if current.ItemGuid != 0 && (!current.MetadataKnown || current.StackCount != 1) {
		return slot, current.ItemGuid, false, "displaced_owned_item_metadata_required"
	}
	if current.ItemGuid != 0 && !s.metadata[current.Entry].known {
		return slot, current.ItemGuid, false, "displaced_item_template_pending"
	}
	if !i.EnhancementsKnown || current.ItemGuid != 0 && !current.EnhancementsKnown {
		return slot, current.ItemGuid, false, "enhancement_context_pending"
	}
	if weapon {
		return slot, current.ItemGuid, conservativeWeaponUpgrade(m, i, s.metadata[current.Entry], current), ""
	}
	return slot, current.ItemGuid, conservativeArmorUpgrade(m, i, s.metadata[current.Entry], current), ""
}

func conservativeArmorUpgrade(candidate itemMetadata, carried *worldv1.BackpackSlot, current itemMetadata, equipped *worldv1.EquipmentSlotState) bool {
	// Dynamic enhancements must also be excluded; a template-only comparison
	// must never downgrade enchanted or randomly enhanced equipment.
	if !carried.EnhancementsKnown || carried.HasEnhancements {
		return false
	}
	if candidate.statCount > uint32(len(candidate.stats)) {
		return false
	}
	for n := uint32(0); n < candidate.statCount; n++ {
		if !knownUpgradeStat(candidate.stats[n].kind) || candidate.stats[n].value < 0 {
			return false
		}
	}
	if equipped.ItemGuid == 0 {
		return true
	}
	if !equipped.EnhancementsKnown || equipped.HasEnhancements || !comparableArmorBaseline(current) {
		return false
	}
	a, aok := armorSlot(candidate)
	b, bok := armorSlot(current)
	if !aok || !bok || a != b || candidate.armor < current.armor {
		return false
	}
	better := candidate.armor > current.armor
	for i := range candidate.resistances {
		if candidate.resistances[i] < current.resistances[i] {
			return false
		}
		better = better || candidate.resistances[i] > current.resistances[i]
	}
	var next, old [49]int32
	for _, pair := range []struct {
		m   itemMetadata
		out *[49]int32
	}{{candidate, &next}, {current, &old}} {
		for n := uint32(0); n < pair.m.statCount; n++ {
			stat := pair.m.stats[n]
			if !knownUpgradeStat(stat.kind) || stat.value < 0 {
				return false
			}
			pair.out[stat.kind] = stat.value
		}
	}
	for i := range next {
		if next[i] < old[i] {
			return false
		}
		better = better || next[i] > old[i]
	}
	return better
}

// ItemTemplate.h ItemModType omits2,8..11,40;41/42 are deprecated.
// Unknown or deprecated modifiers are not evidence of an automatic upgrade.
func knownUpgradeStat(kind uint32) bool {
	return kind <= 1 || kind >= 3 && kind <= 7 || kind >= 12 && kind <= 39 || kind >= 43 && kind <= 48
}

// ItemPackets::AutoEquipItemSlot carries a raw item GUID followed by one slot.
// ItemHandler routes it through Player::SwapItem, returning displaced armor to
// the exact vacated source slot. Two-handed weapons require an empty offhand,
// so this packet never relies on automatic stowage of a second displaced item.
func equipGearPacket(g uint64, slot uint32) []byte {
	if !itemGUID(g) {
		return nil
	}
	switch slot {
	case 0, 2, 4, 5, 6, 7, 8, 9, 14, 15:
		return append(binary.LittleEndian.AppendUint64(nil, g), byte(slot))
	}
	return nil
}
func (s *Service) EquipGear(ctx context.Context, r *inventoryv1.EquipGearRequest) (*inventoryv1.State, error) {
	if r == nil || r.Source == nil || r.ExpectedEquipmentRevision == 0 || len(equipGearPacket(r.Source.ItemGuid, r.EquipmentSlot)) != 9 {
		return nil, status.Error(codes.InvalidArgument, "exact_owned_gear_and_equipment_binding_required")
	}
	return s.submit(ctx, "equip_gear", r.RequestId, r.SessionId, r.ExpectedRevision, r.ExpectedInventoryRevision, r, &inventoryv1.Operation{Source: proto.Clone(r.Source).(*inventoryv1.ItemRef), EquipmentSlot: r.EquipmentSlot, BaselineEquipmentRevision: r.ExpectedEquipmentRevision, DisplacedItemGuid: r.ExpectedEquippedItemGuid})
}

func exactEquipmentVector(expected, actual view) bool {
	a, ok := equipmentView(expected)
	if !ok {
		return false
	}
	b, ok := equipmentView(actual)
	if !ok {
		return false
	}
	for i, e := range a {
		v := b[i]
		if e.ItemGuid != v.ItemGuid || e.MetadataKnown && (!v.MetadataKnown || e.Entry != v.Entry || e.StackCount != v.StackCount) {
			return false
		}
	}
	return true
}

func expectedGearAfter(before view, r *inventoryv1.Operation) (view, error) {
	w := proto.Clone(before.w).(*worldv1.SnapshotResponse)
	v, err := validateWorld(w, w.Self.Guid)
	if err != nil {
		return v, err
	}
	equipment, ok := equipmentView(v)
	if !ok || r.EquipmentSlot >= 19 {
		return v, fail("baseline_equipment_missing")
	}
	source, ok := v.source(r.Source)
	if !ok {
		return v, fail("baseline_source_missing")
	}
	destination := equipment[r.EquipmentSlot]
	if destination.ItemGuid != r.DisplacedItemGuid || destination.ItemGuid != 0 && (!destination.MetadataKnown || destination.StackCount != 1) {
		return v, fail("baseline_displaced_item_changed")
	}
	saved := proto.Clone(source).(*worldv1.BackpackSlot)
	source.ItemGuid, source.Entry, source.StackCount, source.MetadataKnown = destination.ItemGuid, destination.Entry, destination.StackCount, destination.MetadataKnown
	source.DurabilityKnown, source.Durability, source.MaxDurability = destination.DurabilityKnown, destination.Durability, destination.MaxDurability
	source.EnhancementsKnown, source.HasEnhancements = destination.EnhancementsKnown, destination.HasEnhancements
	destination.ItemGuid, destination.Entry, destination.StackCount, destination.MetadataKnown = saved.ItemGuid, saved.Entry, saved.StackCount, saved.MetadataKnown
	destination.DurabilityKnown, destination.Durability, destination.MaxDurability = saved.DurabilityKnown, saved.Durability, saved.MaxDurability
	destination.EnhancementsKnown, destination.HasEnhancements = saved.EnhancementsKnown, saved.HasEnhancements
	out, err := validateWorld(w, w.Self.Guid)
	if err != nil {
		return out, err
	}
	if _, known := equipmentView(out); !known {
		return out, fail("expected_equipment_vector_invalid")
	}
	return out, nil
}
