package buddyinventory

import (
	"context"
	"encoding/binary"
	"testing"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func fixtureMace(t *testing.T) (*Service, *fakeInventory, *inventoryv1.EquipGearRequest) {
	t.Helper()
	s, f, r := fixtureWeapon(t, true)
	f.w.Self.ClassId, f.w.Self.Level = 5, 13
	f.w.SelfSkills[0] = &worldv1.SkillState{Slot: 0, SkillId: 54, Value: 1, MaxValue: 65}
	f.w.SelfSkills[1] = &worldv1.SkillState{Slot: 1, SkillId: 136, Value: 1, MaxValue: 65}
	f.w.InventoryBags[0].Slots[2].Entry = 1158
	// Actual observed ordinary Solid Metal Club damage/delay versus Bent Staff
	// from the normal public item-query responses; no role-score inference.
	m, err := parseItemMetadata(weaponMetadataFrame(1158, 4, 21, 8, 16, 2700))
	if err != nil {
		t.Fatal(err)
	}
	s.metadata[1158], s.queried[1158] = m, true
	r.RequestId, r.Source.Entry = "mace", 1158
	r.ExpectedRevision = stateInventory(t, s).Revision
	return s, f, r
}

func TestMainhandMaceKnownPriestNormalSwapAndDisplacedStaffProof(t *testing.T) {
	s, f, r := fixtureMace(t)
	st := stateInventory(t, s)
	var qualified bool
	for _, item := range st.Items {
		if item.Item.ItemGuid == r.Source.ItemGuid {
			qualified = item.GearEquipEligible && item.ConservativeUpgrade && item.GearEquipmentSlot == 15
		}
	}
	if !qualified {
		t.Fatal("actual learned mace54 and strictly higher physical DPS did not qualify", st.Items)
	}
	beforeOH := proto.Clone(f.w.SelfEquipment[16]).(*worldv1.EquipmentSlotState)
	gearProof(s, f)
	if _, err := s.EquipGear(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	st = waitInventory(t, s)
	if st.Operation.Status != "CONFIRMED" || !st.Operation.EquipmentConfirmed || !st.Operation.InventoryConfirmed || !st.Operation.LeaseReleased || st.Equipment[15].Entry != 1158 || st.InventoryBags[0].Slots[2].Entry != 35 || st.InventoryBags[0].Slots[2].ItemGuid != oldPantsGUID || !proto.Equal(st.Equipment[16], beforeOH) {
		t.Fatal("mace/staff conservation not proven", st)
	}
	if len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGAutoequipItemSlot || len(f.packets[0].Payload) != 9 || binary.LittleEndian.Uint64(f.packets[0].Payload) != r.Source.ItemGuid || f.packets[0].Payload[8] != 15 {
		t.Fatal("not the normal one-slot equip packet", f.packets)
	}
	if _, err := s.EquipGear(context.Background(), r); err != nil || len(f.packets) != 1 {
		t.Fatal("mace request replayed", err)
	}
}

func TestMainhandMaceKeepsKnownProficiencyUsabilityAndComparisonGates(t *testing.T) {
	for _, mode := range []string{"unknown skills", "wrong skill", "zero rank", "class", "race", "level", "required rank", "offhand mace", "two hand mace", "enchanted", "broken", "lost stat", "lost resistance", "lower DPS", "spell effect", "two hand baseline conflict", "skill lost after renew"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r := fixtureMace(t)
			m := s.metadata[1158]
			switch mode {
			case "unknown skills":
				f.w.SelfSkillsKnown = false
			case "wrong skill":
				f.w.SelfSkills[0].SkillId = 173
			case "zero rank":
				f.w.SelfSkills[0].Value = 0
			case "class":
				m.allowableClass = 1
			case "race":
				m.allowableRace = 2
			case "level":
				m.requiredLevel = 14
			case "required rank":
				m.requiredSkill, m.requiredSkillRank = 54, 2
			case "offhand mace":
				m.inventoryType = 22
			case "two hand mace":
				m.subclass, m.inventoryType = 5, 17
			case "enchanted":
				f.w.InventoryBags[0].Slots[2].HasEnhancements = true
			case "broken":
				f.w.InventoryBags[0].Slots[2].Durability = 0
			case "lost stat":
				old := s.metadata[35]
				old.statCount, old.stats[0] = 1, itemStat{6, 1}
				s.metadata[35] = old
			case "lost resistance":
				old := s.metadata[35]
				old.resistances[0] = 1
				s.metadata[35] = old
			case "lower DPS":
				m.damages, m.delay = s.metadata[35].damages, s.metadata[35].delay
			case "spell effect":
				m.hasSpell = true
			case "two hand baseline conflict":
				f.w.SelfEquipment[16] = &worldv1.EquipmentSlotState{Slot: 16, SlotKnown: true, ItemGuid: otherHandGUID, Entry: 15944, StackCount: 1, MetadataKnown: true, EnhancementsKnown: true}
				s.queried[15944] = true
			case "skill lost after renew":
				f.afterRenew = func() { f.mu.Lock(); f.w.SelfSkills[0].Value = 0; f.w.SelfSkillsRevision++; f.mu.Unlock() }
			}
			s.metadata[1158] = m
			r.ExpectedRevision = stateInventory(t, s).Revision
			if _, err := s.EquipGear(context.Background(), r); err == nil {
				if st := waitInventory(t, s); st.Operation.Status != "BLOCKED" {
					t.Fatal("unsafe mace accepted", st)
				}
			}
			if len(f.packets) != 0 {
				t.Fatal("unsafe mace wrote", f.packets)
			}
		})
	}
}

func TestMainhandMaceRetainsAlreadyEquippedOffhandWithOneHandBaseline(t *testing.T) {
	s, f, r := fixtureMace(t)
	old, err := parseItemMetadata(weaponMetadataFrame(5580, 4, 21, 3, 6, 2300))
	if err != nil {
		t.Fatal(err)
	}
	s.metadata[5580], s.queried[5580] = old, true
	f.w.SelfEquipment[15].Entry = 5580
	f.w.SelfEquipment[16] = &worldv1.EquipmentSlotState{Slot: 16, SlotKnown: true, ItemGuid: otherHandGUID, Entry: 15944, StackCount: 1, MetadataKnown: true, DurabilityKnown: true, EnhancementsKnown: true}
	offhand, err := parseItemMetadata(metadataFixture(15944, 4, 1, 23, 0))
	if err != nil {
		t.Fatal(err)
	}
	s.metadata[15944], s.queried[15944] = offhand, true
	r.ExpectedRevision = stateInventory(t, s).Revision
	before := proto.Clone(f.w.SelfEquipment[16]).(*worldv1.EquipmentSlotState)
	gearProof(s, f)
	if _, err := s.EquipGear(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	st := waitInventory(t, s)
	if st.Operation.Status != "CONFIRMED" || !proto.Equal(st.Equipment[16], before) || st.InventoryBags[0].Slots[2].Entry != 5580 {
		t.Fatal("single hand swap moved/discarded offhand", st)
	}
}
