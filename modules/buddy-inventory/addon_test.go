package buddyinventory

import (
	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestInventoryAddonGivePreservesSelectedWholeStackAndTradeSession(t *testing.T) {
	item := &worldv1.BackpackSlot{Bag: 0, Slot: 2, ItemGuid: 0x4000000000000001, Entry: 707, StackCount: 3, MetadataKnown: true}
	valid := &tradev1.State{SessionId: "body", Phase: "OPEN", PartnerGuid: 3, BackpackKnown: true, BackpackRevision: 7, Backpack: []*tradev1.Item{{Slot: 2, ItemGuid: item.ItemGuid, Entry: 707, Count: 3, MetadataKnown: true}}}
	if !addonTradeItemBound(valid, "body", item) {
		t.Fatal("valid exact observed stack rejected")
	}
	for _, mode := range []string{"count_grew", "count_shrank", "guid", "entry", "slot", "metadata", "session", "recipient", "revision", "unknown", "accepted"} {
		t.Run(mode, func(t *testing.T) {
			s := proto.Clone(valid).(*tradev1.State)
			switch mode {
			case "count_grew":
				s.Backpack[0].Count++
			case "count_shrank":
				s.Backpack[0].Count--
			case "guid":
				s.Backpack[0].ItemGuid++
			case "entry":
				s.Backpack[0].Entry++
			case "slot":
				s.Backpack[0].Slot++
			case "metadata":
				s.Backpack[0].MetadataKnown = false
			case "session":
				s.SessionId = "newbody"
			case "recipient":
				s.PartnerGuid = 4
			case "revision":
				s.BackpackRevision = 0
			case "unknown":
				s.BackpackKnown = false
			case "accepted":
				s.AcceptTransportWritten = true
			}
			if addonTradeItemBound(s, "body", item) {
				t.Fatal("changed selection authorized trade", mode)
			}
		})
	}
}
func TestInventoryAddonGiveNeverCallsAnUnconfirmedOfferConfirmed(t *testing.T) {
	item := &worldv1.BackpackSlot{Bag: 0, Slot: 2, ItemGuid: 0x4000000000000001, Entry: 707, StackCount: 3, MetadataKnown: true}
	offered := &tradev1.State{SessionId: "body", Phase: "OPEN", PartnerGuid: 3, OwnRequestedItems: []*tradev1.Item{{Slot: 0, ItemGuid: item.ItemGuid, Entry: 707, Count: 3}}}
	if !addonTradeOfferBound(offered, "body", item) {
		t.Fatal("verified offer intent not recognized")
	}
	for _, mode := range []string{"callback_lost", "count", "entry", "recipient", "session", "offer_absent", "accepted"} {
		s := proto.Clone(offered).(*tradev1.State)
		switch mode {
		case "callback_lost":
			s.Reason = "item_offer_write_unconfirmed"
		case "count":
			s.OwnRequestedItems[0].Count++
		case "entry":
			s.OwnRequestedItems[0].Entry++
		case "recipient":
			s.PartnerGuid = 4
		case "session":
			s.SessionId = "newbody"
		case "offer_absent":
			s.OwnRequestedItems = nil
		case "accepted":
			s.AcceptTransportWritten = true
		}
		if addonTradeOfferBound(s, "body", item) {
			t.Fatal("unverified offer intent accepted", mode)
		}
	}
}
func TestInventoryAddonAllPriorUnresolvedOrCleanupOperationsBlockGive(t *testing.T) {
	for _, status := range []string{"REQUESTED", "OUTCOME_UNCONFIRMED", "UNCONFIRMED", "WRITE_UNCONFIRMED"} {
		if !addonUnresolved(&inventoryv1.State{Operation: &inventoryv1.Operation{Status: status, LeaseReleased: true}}) {
			t.Fatal("unresolved operation accepted", status)
		}
	}
	for _, status := range []string{"CONFIRMED", "BLOCKED", "SERVER_REJECTED"} {
		s := &inventoryv1.State{Operation: &inventoryv1.Operation{Status: status, LeaseReleased: false}}
		if !addonUnresolved(s) {
			t.Fatal("unresolved lease cleanup accepted", status)
		}
		s.Operation.LeaseReleased = true
		if addonUnresolved(s) {
			t.Fatal("terminal released operation blocked", status)
		}
	}
}
func TestInventoryAddonUnknownOccupiedBagCannotImplyEmptySlots(t *testing.T) {
	s := addonFixture()
	s.InventoryBags[0].Slots[0] = &worldv1.BackpackSlot{Bag: 0, Slot: 0, ItemGuid: 0x4000000000000001}
	parts, e := inventoryAddonParts("0123456789ab", "abcdef012345", s)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, p := range parts {
		for i := 0; i+11 <= len(p); i++ {
			if p[i:i+11] == "B=0,16,0,0;" || p[i:i+10] == "B=0,16,0,0" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("unknown occupied item advertised as empty", parts)
	}
}
