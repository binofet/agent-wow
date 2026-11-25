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

// CMSG_SET_AMMO carries one little-endian item ENTRY, not an equipment slot or
// item GUID. The source GUID/stack and bow are our admission facts; the server
// PRIVATE PLAYER_AMMO_ID update is the independent selection receipt.
func (s *Service) ammoEligibilityLocked(source *worldv1.BackpackSlot) string {
	if source == nil || !source.MetadataKnown || source.StackCount == 0 {
		return "exact_owned_ammo_stack_required"
	}
	m := s.metadata[source.Entry]
	if !m.known || m.class != 6 || (m.subclass != 2 && m.subclass != 3) || m.inventoryType != 24 || m.hasSpell || m.containerSlots != 0 || m.stackable == 0 {
		return "normal_projectile_metadata_required"
	}
	if source.StackCount > m.stackable {
		return "ammo_stack_exceeds_template_limit"
	}
	if !s.view.w.SelfAmmoKnown || s.view.w.SelfAmmoRevision == 0 {
		return "own_private_ammo_observation_required"
	}
	equipment, known := equipmentView(s.view)
	if !known || equipment[17].ItemGuid == 0 || !equipment[17].MetadataKnown {
		return "exact_owned_ranged_weapon_required"
	}
	weapon := s.metadata[equipment[17].Entry]
	skill := map[uint32]uint32{2: 45, 3: 46, 18: 226}[weapon.subclass]
	if !weapon.known || weapon.class != 2 || skill == 0 || weapon.ammoType != m.subclass {
		return "ammo_incompatible_with_observed_ranged_weapon"
	}
	if reason := gearUsability(s.view.w, weapon, skill); reason != "" {
		return reason
	}
	return gearUsability(s.view.w, m, 0)
}
func (s *Service) SelectAmmo(ctx context.Context, r *inventoryv1.SelectAmmoRequest) (*inventoryv1.State, error) {
	if r == nil || r.Source == nil || r.Source.ItemGuid == 0 || r.Source.Entry == 0 || r.Source.StackCount == 0 || r.ExpectedAmmoRevision == 0 || r.ExpectedEquipmentRevision == 0 || r.ExpectedRangedItemGuid == 0 {
		return nil, status.Error(codes.InvalidArgument, "exact_owned_ammo_weapon_and_revisions_required")
	}
	return s.submit(ctx, "select_ammo", r.RequestId, r.SessionId, r.ExpectedRevision, r.ExpectedInventoryRevision, r,
		&inventoryv1.Operation{Source: proto.Clone(r.Source).(*inventoryv1.ItemRef), AmmoEntry: r.Source.Entry, BaselineAmmoEntry: r.ExpectedAmmoEntry, BaselineAmmoRevision: r.ExpectedAmmoRevision, BaselineEquipmentRevision: r.ExpectedEquipmentRevision, DisplacedItemGuid: r.ExpectedRangedItemGuid})
}
func (s *Service) ammoStateLocked(v view) {
	s.state.AmmoKnown = v.w.SelfAmmoKnown
	s.state.AmmoEntry = v.w.SelfAmmoEntry
	s.state.AmmoRevision = v.w.SelfAmmoRevision
}
func (s *Service) ammoGateLocked(r *inventoryv1.Operation, source *worldv1.BackpackSlot) string {
	if op := s.actions.GetLastSpellOperation(); op != nil {
		if op.CancelOutcomeUnknown {
			return "prior_spell_cancel_outcome_unconfirmed"
		}
		switch op.Phase {
		case "waiting_server", "waiting_channel_observation", "channeling", "waiting_cleanse_effect", "waiting_created_item", "transport_unconfirmed_no_retry", "outcome_unconfirmed_no_retry":
			return "prior_spell_operation_unsettled"
		}
	}
	if op := s.actions.GetLastBuff(); op != nil && (op.Phase == "waiting_server" || op.Phase == "outcome_unconfirmed") {
		return "prior_buff_operation_unsettled"
	}
	w := s.view.w
	if !w.SelfAmmoKnown || w.SelfAmmoRevision != r.BaselineAmmoRevision || w.SelfAmmoEntry != r.BaselineAmmoEntry {
		return "ammo_selection_revision_changed"
	}
	if w.SelfEquipmentRevision != r.BaselineEquipmentRevision {
		return "ranged_equipment_revision_changed"
	}
	equipment, known := equipmentView(s.view)
	if !known || equipment[17].ItemGuid != r.DisplacedItemGuid {
		return "exact_ranged_weapon_changed"
	}
	if w.SelfAmmoEntry == r.AmmoEntry {
		return "ammo_already_selected"
	}
	return s.ammoEligibilityLocked(source)
}
func ammoPacket(entry uint32) []byte {
	if entry == 0 {
		return nil
	}
	return binary.LittleEndian.AppendUint32(nil, entry)
}
func passiveInventoryOperation(kind string) bool {
	return kind == "equip_gear" || kind == "select_ammo"
}
