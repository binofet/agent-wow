package buddyprofessions

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestDisenchantProtectsCompleteOwnedEquipmentAndDynamicItemFacts(t *testing.T) {
	for _, mode := range []string{"plain", "unknown_equipment", "missing_slot", "duplicate_slot", "unknown_occupied", "enhancements_unknown", "enhanced"} {
		t.Run(mode, func(t *testing.T) {
			s, f := disenchantBehaviorFixture(t)
			f.mu.Lock()
			w := f.world
			switch mode {
			case "unknown_equipment":
				w.SelfEquipmentKnown = false
			case "missing_slot":
				w.SelfEquipment = w.SelfEquipment[:18]
			case "duplicate_slot":
				w.SelfEquipment[18].Slot = 0
			case "unknown_occupied":
				w.SelfEquipment[0].ItemGuid = 0x4000000000001234
			case "enhancements_unknown":
				w.InventoryBags[1].Slots[0].EnhancementsKnown = false
			case "enhanced":
				w.InventoryBags[1].Slots[0].HasEnhancements = true
			}
			f.mu.Unlock()
			st, err := s.InspectDisenchant(context.Background(), &professionsv1.DisenchantInspectRequest{SessionId: "body", TargetGuid: disenchantBehaviorTarget})
			if err != nil || st.DisenchantItem == nil || st.DisenchantItem.Ready != (mode == "plain") || len(s.requests) != 0 || len(f.packets) != 0 {
				t.Fatal("inspection granted unsafe item-loss authority or mutated", st, err, f.packets)
			}
		})
	}
	for slot := range 19 {
		t.Run(fmt.Sprintf("equipped_alias_slot_%d", slot), func(t *testing.T) {
			s, f := disenchantBehaviorFixture(t)
			f.mu.Lock()
			f.world.SelfEquipment[slot] = &worldv1.EquipmentSlotState{Slot: uint32(slot), SlotKnown: true, ItemGuid: disenchantBehaviorTarget, MetadataKnown: true, Entry: 1001, StackCount: 1}
			f.mu.Unlock()
			st, err := s.InspectDisenchant(context.Background(), &professionsv1.DisenchantInspectRequest{SessionId: "body", TargetGuid: disenchantBehaviorTarget})
			if err != nil || st.DisenchantItem == nil || st.DisenchantItem.Ready || len(f.packets) != 0 {
				t.Fatal("equipped item admitted as carried", st, err)
			}
		})
	}
}

func TestDisenchantRetainsFullProtectedTemplateFields(t *testing.T) {
	for _, mode := range []string{"plain", "bop", "boe", "quest_binding", "start_quest", "no_disenchant", "account_bound", "flags2", "bag_family", "duration", "page", "lock", "container", "spell_first", "spell_last", "scaling", "item_set", "socket", "map_restriction"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := disenchantBehaviorFixture(t)
			p, at, tail := disenchantMetadataFixture(1001, 2, 2, 75)
			put := func(offset int, value uint32) { binary.LittleEndian.PutUint32(p[offset:], value) }
			bonding := tail - 1 - len("Selected deliberately by the operator") - 4
			spells := at + 22*4 + 2*8 + 8 + 2*12 + 7*4 + 3*4
			switch mode {
			case "bop":
				put(bonding, 1)
			case "boe":
				put(bonding, 2)
			case "quest_binding":
				put(bonding, 4)
			case "start_quest":
				put(tail+3*4, 184)
			case "no_disenchant":
				put(at+2*4, 0x8000)
			case "account_bound":
				put(at+2*4, 0x08000000)
			case "flags2":
				put(at+3*4, 1)
			case "bag_family":
				put(tail+14*4, 64)
			case "duration":
				put(tail+26*4, 3600)
			case "page":
				put(tail, 12)
			case "lock":
				put(tail+4*4, 43)
			case "container":
				put(at+20*4, 20)
			case "spell_first":
				put(spells, 133)
			case "spell_last":
				put(spells+4*24, 133)
			case "scaling":
				put(at+22*4+2*8, 1)
			case "item_set":
				put(tail+10*4, 1)
			case "socket":
				put(tail+16*4, 1)
			case "map_restriction":
				put(tail+13*4, 1)
			}
			m, err := decodeDisenchantTemplate(p)
			if err != nil || !m.known || m.requiredSkill != 75 {
				t.Fatal("complete protected metadata lost framing", m, err)
			}
			professionPacket(t, s, opcode.SMSGItemQuerySingleResponse, p)
			s.mu.Lock()
			_, reason := s.disenchantTargetLocked(disenchantBehaviorTarget, nil)
			s.mu.Unlock()
			wantReady := mode == "plain" || mode == "bop" || mode == "boe"
			if (reason == "") != wantReady {
				t.Fatal("protected metadata authorized consumption", mode, reason, m)
			}
		})
	}
}

func TestDisenchantFinalDispatchRefreshesOtherItemWritersAndOwnedProtection(t *testing.T) {
	for _, mode := range []string{"plain", "settled_complete_junk_intent", "trade_opened", "junk_unconfirmed", "foreign_trade", "loot_enabled", "foreign_loot", "buffing_enabled", "buff_lease", "enhancement_arrived", "equipped_during_final_world", "equipment_unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, f := disenchantBehaviorFixture(t)
			op := disenchantBehaviorDirect(t, s, f)
			f.mu.Lock()
			switch mode {
			case "settled_complete_junk_intent":
				f.trade = &tradev1.State{SessionId: "body", Phase: "COMPLETE", PartnerGuid: 3, LastServerStatus: 8, Reason: "server_confirmed_trade_complete", CompletedTrades: 1, JunkDisposalEnabled: true}
			case "trade_opened":
				f.trade.Phase, f.trade.PartnerGuid = "OPEN", 3
			case "junk_unconfirmed":
				f.trade.LastJunkDisposal = &tradev1.JunkDisposal{Status: "UNCONFIRMED_NO_RETRY"}
			case "foreign_trade":
				f.trade.SessionId = "other-body"
			case "loot_enabled":
				f.loot.Enabled, f.loot.Phase = true, "idle"
			case "foreign_loot":
				f.loot.SessionId = "other-body"
			case "buffing_enabled":
				f.actions.Buffing = true
			case "buff_lease":
				f.actions.BuffLeaseRemainingMs = 1000
			}
			f.worldHook = func(*worldv1.SnapshotRequest) {
				switch mode {
				case "enhancement_arrived":
					f.world.InventoryBags[1].Slots[0].HasEnhancements = true
				case "equipped_during_final_world":
					f.world.SelfEquipment[6] = &worldv1.EquipmentSlotState{Slot: 6, SlotKnown: true, ItemGuid: disenchantBehaviorTarget, MetadataKnown: true, Entry: 1001, StackCount: 1}
				case "equipment_unknown":
					f.world.SelfEquipmentKnown = false
				}
			}
			f.mu.Unlock()
			attempted, err := s.disenchantWrite(op.ctx, op, opcode.CMSGCastSpell, disenchantCastPacket(9, disenchantBehaviorTarget))
			wantWrite := mode == "plain" || mode == "settled_complete_junk_intent"
			if attempted != wantWrite || (err == nil) != wantWrite || len(f.packets) != boolCount(wantWrite) || s.deAttempted[disenchantBehaviorTarget] != wantWrite {
				t.Fatal("final protection change was not respected", attempted, err, f.packets)
			}
			s.mu.Lock()
			s.active = nil
			s.mu.Unlock()
		})
	}
}

func TestDisenchantRequiresOptedOutBuffingAndExpiredBuffLease(t *testing.T) {
	for _, mode := range []string{"buffing", "lease_only"} {
		t.Run(mode, func(t *testing.T) {
			s, f := disenchantBehaviorFixture(t)
			f.mu.Lock()
			f.actions.Buffing = mode == "buffing"
			if mode == "lease_only" {
				f.actions.BuffLeaseRemainingMs = 1000
			}
			f.mu.Unlock()
			st, err := s.InspectDisenchant(context.Background(), &professionsv1.DisenchantInspectRequest{SessionId: "body", TargetGuid: disenchantBehaviorTarget})
			if err != nil || st.DisenchantItem == nil || st.DisenchantItem.Ready || len(s.requests) != 0 || len(f.packets) != 0 {
				t.Fatal("buff intent or its held lease authorized destructive work", st, err)
			}
		})
	}
}

func boolCount(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestDisenchantEquippedArmorCannotBecomeFalseConsumptionReceipt(t *testing.T) {
	s, f := disenchantBehaviorFixture(t)
	op := disenchantBehaviorDirect(t, s, f)
	disenchantBehaviorOwnWindow(t, s, op, 1)
	op.stored[0] = true
	disenchantBehaviorOutput(f, true)
	f.mu.Lock()
	f.world.SelfEquipment[6] = &worldv1.EquipmentSlotState{Slot: 6, SlotKnown: true, ItemGuid: disenchantBehaviorTarget, MetadataKnown: true, Entry: 1001, StackCount: 1}
	f.mu.Unlock()
	disenchantBehaviorMaterialReceipts(t, s, true)
	if err := s.observe(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.reconcileDisenchantLocked(op)
	got := s.operationSnapshotLocked(op)
	s.active = nil
	s.mu.Unlock()
	if got.InputConsumedObserved || got.OutputInventoryObserved || got.Status == professionsv1.AttemptStatus_COMPLETED {
		t.Fatal("moved equipment was mislabeled destroyed", got)
	}
}

func TestDisenchantMaterialPickupRechecksProtectionWithoutClosingTheWindow(t *testing.T) {
	for _, mode := range []string{"enhanced", "equipped", "trade_open"} {
		t.Run(mode, func(t *testing.T) {
			s, f := disenchantBehaviorFixture(t)
			op := disenchantBehaviorDirect(t, s, f)
			disenchantBehaviorOwnWindow(t, s, op, 1)
			f.mu.Lock()
			if mode == "trade_open" {
				f.trade.Phase, f.trade.PartnerGuid = "OPEN", 3
			}
			f.worldHook = func(*worldv1.SnapshotRequest) {
				if mode == "enhanced" {
					f.world.InventoryBags[1].Slots[0].HasEnhancements = true
				} else if mode == "equipped" {
					f.world.SelfEquipment[10] = &worldv1.EquipmentSlotState{Slot: 10, SlotKnown: true, ItemGuid: disenchantBehaviorTarget, Entry: 1001, StackCount: 1, MetadataKnown: true}
				}
			}
			f.mu.Unlock()
			if !s.stepDisenchant(op) || op.receipt.Status != professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED || len(f.packets) != 0 || op.receipt.LootReleaseObserved || op.receipt.InputConsumedObserved || op.receipt.OutputInventoryObserved {
				t.Fatal("changed material-pickup authority wrote or claimed cleanup", op.receipt, f.packets)
			}
			s.mu.Lock()
			s.active = nil
			s.mu.Unlock()
		})
	}
}

func TestDisenchantOrdinaryUnusedGreenCompletesWithSettledTradeAndJunkIntent(t *testing.T) {
	s, f := disenchantBehaviorFixture(t)
	f.mu.Lock()
	f.trade = &tradev1.State{SessionId: "body", Phase: "COMPLETE", PartnerGuid: 3, LastServerStatus: 8, Reason: "server_confirmed_trade_complete", CompletedTrades: 1, JunkDisposalEnabled: true}
	f.mu.Unlock()
	f.beforeSend = func(p *modv1.SendPacketRequest) {
		s.mu.Lock()
		count := byte(s.active.receipt.CastCount)
		s.mu.Unlock()
		switch p.Opcode {
		case opcode.CMSGCastSpell:
			professionPacket(t, s, opcode.SMSGSpellGo, disenchantBehaviorCast(true, 7, disenchantBehaviorTarget, count))
			professionPacket(t, s, opcode.SMSGLootResponse, disenchantBehaviorLoot(1))
		case opcode.CMSGAutostoreLootItem:
			disenchantBehaviorOutput(f, true)
			disenchantBehaviorMaterialReceipts(t, s, true)
		default:
			t.Errorf("unexpected item-window packet %x", p.Opcode)
		}
	}
	r := disenchantBehaviorRequest(s, "auto-worker-selected-unused-green")
	if _, err := s.Attempt(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	op := s.last
	s.mu.Unlock()
	professionWaitDone(t, op)
	got := professionReceipt(s, op)
	if got.Status != professionsv1.AttemptStatus_COMPLETED || !got.InputConsumedObserved || !got.OutputInventoryObserved || !got.OwnGoObserved || !got.LootReleaseObserved || !got.LeaseReleased || got.CleanupUnconfirmed || len(f.packets) != 2 {
		t.Fatal("ordinary green did not complete exact normal transaction", got, f.packets)
	}
}
