package buddyinventory

import (
	"context"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// The public Wrath spell contract identifies both the item target and the field
// the server can change. The caller never supplies a desired effect as evidence.
func itemEnchantEffect(spell spellcatalog.Spell) (slot, id uint32, ok bool) {
	if spell.TargetMask != 0x10 || (spell.ItemClass != 2 && spell.ItemClass != 4) || len(spell.Effects) != 1 {
		return 0, 0, false
	}
	e := spell.Effects[0]
	if e.Misc <= 0 || e.A != 0 || e.B != 0 {
		return 0, 0, false
	}
	switch e.Kind {
	case 53: // SPELL_EFFECT_ENCHANT_ITEM
		return 0, uint32(e.Misc), true
	case 54: // SPELL_EFFECT_ENCHANT_ITEM_TEMPORARY
		return 1, uint32(e.Misc), true
	}
	return 0, 0, false
}

func (m itemMetadata) enhancementUse() (*inventoryv1.ItemEnhancementUse, int32) {
	if !m.known || m.class != 0 || m.inventoryType != 0 {
		return nil, 0
	}
	var use *inventoryv1.ItemEnhancementUse
	var charges int32
	for n, id := range m.onUseSpells {
		if id == 0 {
			continue
		}
		spell, known := spellcatalog.Default().Lookup(id)
		slot, enchant, supported := itemEnchantEffect(spell)
		if use != nil || !known || !supported || m.onUseCharges[n] >= 0 {
			return nil, 0
		}
		use = &inventoryv1.ItemEnhancementUse{SpellId: id, EnchantmentSlot: slot, EnchantmentId: enchant}
		charges = m.onUseCharges[n]
	}
	return use, charges
}

func enchantmentAt(d *worldv1.ItemEnhancementState, slot uint32) (*worldv1.ItemEnchantmentSlot, bool) {
	if d == nil || slot > 1 || d.IdKnownMask&(1<<slot) == 0 {
		return nil, false
	}
	for _, row := range d.Enchantments {
		if row != nil && row.Slot == slot {
			return row, true
		}
	}
	return &worldv1.ItemEnchantmentSlot{Slot: slot}, true // Known zero is sparse on the wire.
}

func (s *Service) ApplyItemEnhancement(ctx context.Context, q *inventoryv1.ApplyItemEnhancementRequest) (*inventoryv1.State, error) {
	if q.GetSelfGuid() == 0 || q.SelfGuid != s.selfGUID || q.VisitRevision == 0 || q.ExpectedEquipmentRevision == 0 ||
		q.ExpectedInventoryRevision == 0 || q.ExpectedEnchantmentId == nil || q.EquipmentSlot >= 19 || q.EquippedEntry == 0 ||
		!itemGUID(q.EquippedItemGuid) || equippedItemUsePayload(q.Source, q.SpellId, 0, q.EquippedItemGuid) == nil {
		return nil, status.Error(codes.InvalidArgument, "exact body, visit, carried source, equipped target, previous enchantment and revisions required")
	}
	spell, known := spellcatalog.Default().Lookup(q.SpellId)
	slot, id, ok := itemEnchantEffect(spell)
	if !known || !ok {
		return nil, status.Error(codes.InvalidArgument, "direct permanent or temporary item-enchantment use spell required")
	}
	e := &inventoryv1.ItemEnhancementApplication{VisitRevision: q.VisitRevision, EquippedItemGuid: q.EquippedItemGuid,
		EquippedEntry: q.EquippedEntry, SpellId: q.SpellId, EnchantmentSlot: slot, EnchantmentId: id, PreviousEnchantmentId: *q.ExpectedEnchantmentId}
	return s.submit(ctx, "apply_item_enhancement", q.RequestId, q.SessionId, q.ExpectedRevision, q.ExpectedInventoryRevision, q,
		&inventoryv1.Operation{Source: proto.Clone(q.Source).(*inventoryv1.ItemRef), EquipmentSlot: q.EquipmentSlot,
			BaselineEquipmentRevision: q.ExpectedEquipmentRevision, Enhancement: e})
}

func (s *Service) enhancementGateLocked(op *operation, source *worldv1.BackpackSlot) string {
	r, w := op.receipt, s.view.w
	e := r.Enhancement
	if s.actions.GetSessionId() != s.state.SessionId || s.actions.GetSelfGuid() != s.selfGUID {
		return "enhancement_controller_body_changed"
	}
	if w.VisitRevision != e.VisitRevision || w.SelfEquipmentRevision != r.BaselineEquipmentRevision {
		return "enhancement_visit_or_equipment_changed"
	}
	if reason := s.unsettledSpellLocked(); reason != "" {
		return reason
	}
	if last := s.lastConsumable; last != nil && last.view.TransportAttempted && !last.view.ServerRejected && !last.view.InventoryConfirmed {
		return "prior_consumable_outcome_unconfirmed"
	}
	equipped, known := equipmentView(s.view)
	target := equipped[r.EquipmentSlot]
	if !known || !target.GetMetadataKnown() || target.ItemGuid != e.EquippedItemGuid || target.Entry != e.EquippedEntry || target.StackCount != 1 {
		return "exact_equipped_enhancement_target_changed"
	}
	modifier, known := enchantmentAt(target.EnhancementDetails, e.EnchantmentSlot)
	if !known || modifier.Id != e.PreviousEnchantmentId {
		return "expected_enchantment_unknown_or_changed"
	}
	if _, known := enchantmentAt(target.EnhancementDetails, 1-e.EnchantmentSlot); !known {
		return "other_equipment_enchantment_unknown"
	}
	if modifier.Id == e.EnchantmentId {
		if e.EnchantmentSlot == 0 {
			return "permanent_enchantment_already_present"
		}
		if (target.EnhancementDetails.DurationKnownMask|target.EnhancementDetails.ChargesKnownMask)&2 == 0 {
			return "temporary_enchantment_refresh_baseline_unknown"
		}
	}
	m, gear := s.metadata[source.Entry], s.metadata[target.Entry]
	use, charges := m.enhancementUse()
	if source.StackCount > m.stackable || !gear.known || use.GetSpellId() != e.SpellId {
		return "observed_consumable_and_equipped_item_templates_required"
	}
	if reason := gearUsability(w, m, 0); reason != "" {
		return reason
	}
	op.multiCharge = charges < -1
	spell, _ := spellcatalog.Default().Lookup(e.SpellId)
	if uint32(spell.ItemClass) != gear.class || gear.subclass >= 32 || gear.inventoryType >= 32 ||
		spell.SubclassMask != 0 && spell.SubclassMask&(1<<gear.subclass) == 0 ||
		spell.InventoryMask != 0 && spell.InventoryMask&(1<<gear.inventoryType) == 0 {
		return "enchantment_incompatible_with_equipped_target"
	}
	return ""
}

func (s *Service) enhancementEvidenceLocked(op *operation, current view) {
	r, e := op.receipt, op.receipt.Enhancement
	if current.w.VisitRevision != e.VisitRevision || current.w.SelfEquipmentRevision <= r.BaselineEquipmentRevision ||
		current.w.InventoryRevision < r.BaselineInventoryRevision || !exactEquipmentVector(op.baseline, current) {
		return
	}
	equipment, _ := equipmentView(current)
	original, _ := equipmentView(op.baseline)
	currentDetails, previousDetails := equipment[r.EquipmentSlot].EnhancementDetails, original[r.EquipmentSlot].EnhancementDetails
	after, known := enchantmentAt(currentDetails, e.EnchantmentSlot)
	before, _ := enchantmentAt(previousDetails, e.EnchantmentSlot)
	other, otherKnown := enchantmentAt(currentDetails, 1-e.EnchantmentSlot)
	previousOther, _ := enchantmentAt(previousDetails, 1-e.EnchantmentSlot)
	if !known || !otherKnown || other.Id != previousOther.Id {
		return
	}
	e.ObservedEnchantmentId = proto.Uint32(after.Id)
	effect := after.Id == e.EnchantmentId && (after.Id != before.Id ||
		currentDetails.DurationKnownMask&previousDetails.DurationKnownMask&2 != 0 && after.DurationRaw > before.DurationRaw ||
		currentDetails.ChargesKnownMask&previousDetails.ChargesKnownMask&2 != 0 && after.Charges > before.Charges)
	if !effect {
		return
	}
	r.InventoryConfirmed = current.w.InventoryRevision > r.BaselineInventoryRevision && exactVector(op.expected, current)
	// A multi-use oil can spend a charge without changing stack count or bag
	// revision. Only its new enchant on the same equipped GUID proves the effect.
	if !r.InventoryConfirmed && !(op.multiCharge && exactVector(op.baseline, current)) {
		return
	}
	e.EffectConfirmed, r.EquipmentConfirmed = true, true
	r.Status, r.Reason = "CONFIRMED", "newer_exact_equipped_enchantment_observed"
}
