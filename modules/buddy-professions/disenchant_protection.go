package buddyprofessions

import (
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

// Idle junk intent is safe under the exclusive professions lease: normal junk
// disposal must acquire its own trade lease and only destroys poor-quality
// items. Actual unresolved disposal or trade receipts remain blocking.
func settledDisenchantTrade(t *tradev1.State, session string) bool {
	if t == nil || t.SessionId != session || t.LastJunkDisposal != nil && t.LastJunkDisposal.Status != "SERVER_SLOT_REMOVAL_CONFIRMED" {
		return false
	}
	if t.Phase == "COMPLETE" {
		return t.PartnerGuid == 3 && t.LastServerStatus == 8 && t.Reason == "server_confirmed_trade_complete" && t.CompletedTrades > 0
	}
	return t.Phase == "IDLE" && t.PartnerGuid == 0 && !t.PartnerAccepted && !t.AcceptTransportWritten && len(t.OwnRequestedItems) == 0 && len(t.PartnerItems) == 0 && t.OwnRequestedCopper == 0 && t.PartnerCopper == 0
}

// Complete owned equipment prevents a carried-item observation from granting
// authority when another private view still identifies that GUID as equipped.
// No nearby player's equipment or inferred empty slots participate.
func disenchantEquipment(w *worldv1.SnapshotResponse) (map[uint64]bool, string) {
	const unknown = "complete_actual_owned_nineteen_equipment_slots_required"
	if w == nil || !w.SelfEquipmentKnown || w.SelfEquipmentRevision == 0 || len(w.SelfEquipment) != 19 {
		return nil, unknown
	}
	var slots [19]bool
	guids := make(map[uint64]bool, 19)
	for _, row := range w.SelfEquipment {
		if row == nil || row.Slot >= 19 || slots[row.Slot] || !row.SlotKnown {
			return nil, unknown
		}
		slots[row.Slot] = true
		if row.ItemGuid == 0 {
			if row.Entry != 0 || row.StackCount != 0 || row.MetadataKnown {
				return nil, unknown
			}
			continue
		}
		if !carriedItemGUID(row.ItemGuid) || guids[row.ItemGuid] || !row.MetadataKnown || row.Entry == 0 || row.StackCount != 1 {
			return nil, unknown
		}
		guids[row.ItemGuid] = true
	}
	for _, bag := range w.InventoryBags {
		if bag == nil {
			return nil, unknown
		}
		if guids[bag.ContainerGuid] {
			return nil, "owned_equipment_and_carried_container_guid_conflict"
		}
		for _, row := range bag.Slots {
			if row == nil {
				return nil, unknown
			}
			if guids[row.ItemGuid] {
				return nil, "owned_equipment_and_carried_item_guid_conflict"
			}
		}
	}
	return guids, ""
}
