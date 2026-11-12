package buddysupplies

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	suppliesv1 "github.com/agent-wow/agent-wow/modules/buddy-supplies/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func strongPoleFixture(t *testing.T) (*Service, *supplyFixture) {
	t.Helper()
	s, f := newFixture(t)
	f.items[6365] = proto.Clone(f.items[6256]).(*tradev1.ItemDetails)
	i := f.items[6365]
	i.ItemId, i.RequiredLevel, i.RequiredSkill, i.RequiredSkillRank, i.HasSpell = 6365, 5, 356, 10, true
	f.world.SelfSkills[0].Value = 10
	return s, f
}

func quotedPoleBuy(t *testing.T, s *Service, f *supplyFixture, entry, price uint32) *suppliesv1.BuyRequest {
	t.Helper()
	f.onSend = func(r *modv1.SendPacketRequest) {
		if r.Opcode == opcode.CMSGListInventory {
			sendPacket(s, opcode.SMSGListInventory, vendorPacket(entry, price))
		}
	}
	st, e := s.List(context.Background(), &suppliesv1.ListRequest{SessionId: "body", NpcGuid: vendorGUID})
	if e != nil || st.Offer == nil {
		t.Fatal(st, e)
	}
	st, e = s.GetState(context.Background(), &emptypb.Empty{})
	if e != nil {
		t.Fatal(e)
	}
	return &suppliesv1.BuyRequest{SessionId: "body", RequestId: "buy-pole", ExpectedRevision: st.Revision, NpcGuid: vendorGUID, OfferRevision: st.Offer.Revision, VendorSlot: 1, ItemId: entry, Count: 1, ExpectedPriceCopper: price, MaxSpendCopper: price, ExpectedBackpackRevision: st.BackpackRevision, ExpectedCopperRevision: st.CopperRevision, ExpectedSkillsRevision: st.SkillsRevision}
}

func TestFishingPolePurchaseBeforeTrainingAndStrongPurchaseProof(t *testing.T) {
	for _, entry := range []uint32{6256, 6365} {
		t.Run(fmt.Sprint(entry), func(t *testing.T) {
			s, f := strongPoleFixture(t)
			price := uint32(902)
			if entry == 6256 {
				// The complete basic-pole template has no skill requirement.
				f.world.SelfSkills[0] = &worldv1.SkillState{Slot: 0}
				price = 23
			}
			r := quotedPoleBuy(t, s, f, entry, price)
			if _, e := s.Buy(context.Background(), r); e != nil {
				t.Fatal(e)
			}
			waitReceipt(t, s, func(st *suppliesv1.State) bool { return st.Operation.TransportWritten })
			sendPacket(s, opcode.SMSGBuyItem, buyAck(vendorGUID, 1, 1))
			if s.snapshot().Operation.Status == "CONFIRMED" {
				t.Fatal("purchase acknowledgement invented inventory and debit")
			}
			f.change(func() {
				f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: poleGUID, Entry: entry, StackCount: 1, MetadataKnown: true}
				f.world.BackpackRevision++
				f.world.SelfCopper -= price
				f.world.SelfCopperRevision++
			})
			_, _ = s.GetState(context.Background(), &emptypb.Empty{})
			st := waitReceipt(t, s, func(st *suppliesv1.State) bool { return !st.Active })
			o := st.Operation
			if o.Status != "CONFIRMED" || o.ItemId != entry || o.CostCopper != price || !o.ServerSuccessObserved || !o.InventoryConfirmed || !o.CopperConfirmed || !o.LeaseReleased || f.count(opcode.CMSGBuyItem) != 1 {
				t.Fatal(st)
			}
			if _, e := s.Buy(context.Background(), r); e != nil || f.count(opcode.CMSGBuyItem) != 1 {
				t.Fatal("replayed purchase", e)
			}
		})
	}
}

func TestStrongFishingPoleRequirementsBeforeBuyAndEquip(t *testing.T) {
	cases := map[string]func(*supplyFixture){
		"level4":        func(f *supplyFixture) { f.world.Self.Level = 4 },
		"rank9":         func(f *supplyFixture) { f.world.SelfSkills[0].Value = 9 },
		"missing_skill": func(f *supplyFixture) { f.world.SelfSkills[0] = &worldv1.SkillState{Slot: 0} },
		"pending":       func(f *supplyFixture) { f.items[6365].Pending = true },
		"missing":       func(f *supplyFixture) { delete(f.items, 6365) },
		"wrong_type":    func(f *supplyFixture) { f.items[6365].Subclass = 10 },
		"wrong_class":   func(f *supplyFixture) { f.items[6365].AllowableClass = 1 },
		"extra_spell":   func(f *supplyFixture) { f.items[6365].RequiredSpell = 99 },
	}
	for name, mutate := range cases {
		for _, equip := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/equip=%v", name, equip), func(t *testing.T) {
				s, f := strongPoleFixture(t)
				mutate(f)
				var st *suppliesv1.State
				var e error
				if equip {
					f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: poleGUID, Entry: 6365, StackCount: 1, MetadataKnown: true}
					r := equipRequest(t, s, "equip-strong", poleGUID)
					st, e = s.Equip(context.Background(), r)
				} else {
					r := quotedPoleBuy(t, s, f, 6365, 902)
					st, e = s.Buy(context.Background(), r)
				}
				if e != nil || st.Operation.Status != "BLOCKED" || !st.Operation.LeaseReleased || f.count(opcode.CMSGBuyItem) != 0 || f.count(opcode.CMSGAutoequipItemSlot) != 0 {
					t.Fatal(st, e)
				}
			})
		}
	}
}

func applyPoleSwap(t *testing.T, s *Service, f *supplyFixture, request string, slot uint32, guid uint64, entry uint32) *suppliesv1.State {
	t.Helper()
	before := proto.Clone(f.world.Self.Mainhand).(*worldv1.EquippedWeapon)
	waitReceipt(t, s, func(st *suppliesv1.State) bool {
		return st.Operation.RequestId == request && st.Operation.TransportWritten
	})
	f.mu.Lock()
	last := f.packets[len(f.packets)-1]
	if last.Opcode != opcode.CMSGAutoequipItemSlot || len(last.Payload) != 9 || binary.LittleEndian.Uint64(last.Payload) != guid || last.Payload[8] != 15 {
		f.mu.Unlock()
		t.Fatal("not the exact normal nine-byte equip packet", last)
	}
	f.mu.Unlock()
	f.change(func() {
		f.world.Self.Mainhand = &worldv1.EquippedWeapon{SlotKnown: true, ItemGuid: guid, Entry: entry, MetadataKnown: true}
		f.world.Backpack[slot] = &worldv1.BackpackSlot{Slot: slot, ItemGuid: before.ItemGuid, Entry: before.Entry, StackCount: 1, MetadataKnown: true}
		f.world.BackpackRevision++
	})
	_, _ = s.GetState(context.Background(), &emptypb.Empty{})
	return waitReceipt(t, s, func(st *suppliesv1.State) bool { return !st.Active })
}

func TestFishingPoleMetadataDrivenExactEquipAndRestore(t *testing.T) {
	for _, entry := range []uint32{6256, 6365, 987654} {
		t.Run(fmt.Sprint(entry), func(t *testing.T) {
			s, f := strongPoleFixture(t)
			if entry == 987654 {
				// Owned equipment is typed by its complete server template;
				// this ID remains outside the bounded purchasing catalog.
				f.items[entry] = proto.Clone(f.items[6365]).(*tradev1.ItemDetails)
				f.items[entry].ItemId = entry
				if _, ok := catalogLookup(entry); ok {
					t.Fatal("unexpected shopping authorization")
				}
			}
			if entry == 6256 {
				f.world.SelfSkills[0] = &worldv1.SkillState{Slot: 0}
			}
			f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: poleGUID, Entry: entry, StackCount: 1, MetadataKnown: true}
			unrelated := &worldv1.BackpackSlot{Slot: 4, ItemGuid: poleGUID + 10, Entry: 2320, StackCount: 3, MetadataKnown: true}
			f.world.Backpack[4] = proto.Clone(unrelated).(*worldv1.BackpackSlot)
			r := equipRequest(t, s, "equip-pole", poleGUID)
			if _, e := s.Equip(context.Background(), r); e != nil {
				t.Fatal(e)
			}
			st := applyPoleSwap(t, s, f, r.RequestId, 2, poleGUID, entry)
			if st.Operation.Status != "CONFIRMED" || st.Operation.ItemId != entry || !st.Operation.EquipmentConfirmed || !st.Operation.LeaseReleased || !st.RestoreRequired || st.OriginalWeaponGuid != weaponGUID || !proto.Equal(f.world.Backpack[4], unrelated) {
				t.Fatal(st)
			}
			restore := equipRequest(t, s, "restore-weapon", weaponGUID)
			if _, e := s.Restore(context.Background(), restore); e != nil {
				t.Fatal(e)
			}
			st = applyPoleSwap(t, s, f, restore.RequestId, 2, weaponGUID, 35)
			if st.Operation.Status != "CONFIRMED" || !st.Operation.EquipmentConfirmed || !st.Operation.LeaseReleased || st.RestoreRequired || st.EquippedPoleGuid != 0 || f.world.Backpack[2].ItemGuid != poleGUID || f.world.Backpack[2].Entry != entry || !proto.Equal(f.world.Backpack[4], unrelated) || f.count(opcode.CMSGAutoequipItemSlot) != 2 {
				t.Fatal(st)
			}
		})
	}
}

func TestFishingPoleUpgradePreservesOriginalWeapon(t *testing.T) {
	s, f := strongPoleFixture(t)
	f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: poleGUID, Entry: 6256, StackCount: 1, MetadataKnown: true}
	f.world.Backpack[3] = &worldv1.BackpackSlot{Slot: 3, ItemGuid: poleGUID + 1, Entry: 6365, StackCount: 1, MetadataKnown: true}
	r := equipRequest(t, s, "basic", poleGUID)
	_, _ = s.Equip(context.Background(), r)
	applyPoleSwap(t, s, f, r.RequestId, 2, poleGUID, 6256)
	r = equipRequest(t, s, "strong", poleGUID+1)
	r.BackpackSlot = 3
	_, e := s.Equip(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	st := applyPoleSwap(t, s, f, r.RequestId, 3, poleGUID+1, 6365)
	if st.Operation.Status != "CONFIRMED" || st.Operation.OriginalWeaponEntry != 35 || st.OriginalWeaponGuid != weaponGUID || st.OriginalWeaponEntry != 35 || st.EquippedPoleGuid != poleGUID+1 || f.world.Backpack[2].ItemGuid != weaponGUID || f.world.Backpack[3].ItemGuid != poleGUID {
		t.Fatal("original weapon history changed", st)
	}
	r = equipRequest(t, s, "restore", weaponGUID)
	_, _ = s.Restore(context.Background(), r)
	st = applyPoleSwap(t, s, f, r.RequestId, 2, weaponGUID, 35)
	if st.Operation.Status != "CONFIRMED" || st.RestoreRequired || f.world.Backpack[2].ItemGuid != poleGUID+1 || f.world.Backpack[3].ItemGuid != poleGUID || f.count(opcode.CMSGAutoequipItemSlot) != 3 {
		t.Fatal(st)
	}
}

func TestStrongFishingPoleRestoreAfterFishingUnlearned(t *testing.T) {
	s, f := strongPoleFixture(t)
	f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: poleGUID, Entry: 6365, StackCount: 1, MetadataKnown: true}
	r := equipRequest(t, s, "equip-strong", poleGUID)
	if _, e := s.Equip(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	st := applyPoleSwap(t, s, f, r.RequestId, 2, poleGUID, 6365)
	if st.Operation.Status != "CONFIRMED" {
		t.Fatal(st)
	}
	f.change(func() {
		f.world.SelfSkills[0] = &worldv1.SkillState{Slot: 0}
		f.world.SelfSkillsRevision++
	})
	r = equipRequest(t, s, "restore-without-fishing", weaponGUID)
	if _, e := s.Restore(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	st = applyPoleSwap(t, s, f, r.RequestId, 2, weaponGUID, 35)
	if st.Operation.Status != "CONFIRMED" || !st.Operation.EquipmentConfirmed || !st.Operation.LeaseReleased || st.RestoreRequired || f.world.Backpack[2].ItemGuid != poleGUID || f.count(opcode.CMSGAutoequipItemSlot) != 2 {
		t.Fatal(st)
	}
}

func TestStrongFishingPoleFinalLeaseRequirementAndConservation(t *testing.T) {
	for _, missingOriginal := range []bool{false, true} {
		t.Run(fmt.Sprintf("missingOriginal=%v", missingOriginal), func(t *testing.T) {
			s, f := strongPoleFixture(t)
			f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: poleGUID, Entry: 6365, StackCount: 1, MetadataKnown: true}
			r := equipRequest(t, s, "strong", poleGUID)
			if missingOriginal {
				// A retained history cannot authorize a new pole if the
				// original weapon is no longer actually owned.
				s.state.RestoreRequired, s.state.OriginalWeaponGuid, s.state.OriginalWeaponEntry, s.state.EquippedPoleGuid = true, weaponGUID+5, 35, weaponGUID
				f.items[35].ItemClass, f.items[35].Subclass, f.items[35].InventoryType = 2, 20, 17
				s.metadata[35] = proto.Clone(f.items[35]).(*tradev1.ItemDetails)
				r.OriginalWeaponGuid = weaponGUID + 5
			} else {
				f.onAcquire = func() { f.change(func() { f.world.SelfSkills[0].Value = 9; f.world.SelfSkillsRevision++ }) }
			}
			st, e := s.Equip(context.Background(), r)
			if e != nil || st.Operation.Status != "BLOCKED" || !st.Operation.LeaseReleased || f.count(opcode.CMSGAutoequipItemSlot) != 0 {
				t.Fatal(st, e)
			}
		})
	}
}
