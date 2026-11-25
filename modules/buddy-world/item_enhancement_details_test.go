package buddyworld

import (
	"context"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"testing"
)

func TestEnhancementDetailsPartialKnownZeroAndSignedProperty(t *testing.T) {
	var p observed
	p.known[0x16] = true // known empty permanent enchant; next words unknown.
	p.known[0x1c] = true
	p.fields[0x1c] = 3210 // native slot2 enchantment reference, not a gem item ID.
	p.known[0x3a] = true
	p.fields[0x3a] = 123456
	p.known[0x3b] = true
	p.fields[0x3b] = 0xfffffff9
	v := observedEnhancementDetails(&p).message()
	if len(v.Enchantments) != 12 || !v.Enchantments[0].IdKnown || v.Enchantments[0].Id != 0 || v.Enchantments[0].DurationKnown || v.Enchantments[1].IdKnown {
		t.Fatal(v)
	}
	if v.Enchantments[2].Id != 3210 || !v.PropertySeedKnown || v.PropertySeed != 123456 || !v.RandomPropertyIdKnown || v.RandomPropertyId != -7 {
		t.Fatal(v)
	}
	if known, _ := itemEnhancements(&p); known {
		t.Fatal("partial fields changed legacy boolean semantics")
	}
	if (itemEnhancementView{}).message() != nil {
		t.Fatal("absent metadata fabricated details")
	}
}

func TestEnhancementDetailsOwnedPacketUpdatesRemovalAndVisitIsolation(t *testing.T) {
	s := NewService(1)
	fields := equipmentFields(6, armorItem)
	fields[0x172], fields[0x173] = uint32((armorItem+1)&0xffffffff), uint32((armorItem+1)>>32)
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, fields))
	partial := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(armorItem, map[int]uint32{0x16: 3210}))
	if partial.SelfEquipment[6].EnhancementDetails != nil {
		t.Fatal("unowned partial delta promoted")
	}
	packet(t, s, opcode.SMSGUpdateObject, objectFixture(armorItem, 1, map[int]uint32{3: 2962, 6: 1, 0xe: 1, 0x16: 3210, 0x17: 9000, 0x18: 3, 0x3a: 123, 0x3b: 0xfffffff9}))
	r := packet(t, s, opcode.SMSGUpdateObject, objectFixture(armorItem+1, 1, map[int]uint32{3: 2962, 6: 1, 0xe: 1, 0x1c: 3310}))
	d := r.SelfEquipment[6].EnhancementDetails
	if !r.SelfEquipment[6].EnhancementsKnown || !r.SelfEquipment[6].HasEnhancements || d.Enchantments[0].Id != 3210 || d.Enchantments[0].DurationRaw != 9000 || d.Enchantments[0].Charges != 3 || d.RandomPropertyId != -7 {
		t.Fatal(d)
	}
	if r.Backpack[0].EnhancementDetails.Enchantments[2].Id != 3310 || r.InventoryBags[0].Slots[0].EnhancementDetails.Enchantments[2].Id != 3310 {
		t.Fatal("carried projections disagree")
	}
	er, ir := r.SelfEquipmentRevision, r.InventoryRevision
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(armorItem, map[int]uint32{0x16: 3211}))
	if r.SelfEquipmentRevision != er+1 || !r.SelfEquipment[6].HasEnhancements {
		t.Fatal("changed nonzero ID hidden behind same boolean")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(armorItem+1, map[int]uint32{0x1c: 3311}))
	if r.InventoryRevision != ir+1 || r.Backpack[0].EnhancementDetails.Enchantments[2].Id != 3311 {
		t.Fatal("carried revision did not include exact change")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(armorItem+1, map[int]uint32{0x1c: 0}))
	if r.Backpack[0].HasEnhancements || !r.Backpack[0].EnhancementDetails.Enchantments[2].IdKnown || r.Backpack[0].EnhancementDetails.Enchantments[2].Id != 0 {
		t.Fatal("known removal lost")
	}
	r.Backpack[0].EnhancementDetails.Enchantments[2].Id = 999
	r, _ = s.Snapshot(context.Background(), nil)
	if r.Backpack[0].EnhancementDetails.Enchantments[2].Id != 0 {
		t.Fatal("snapshot alias")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(armorItem+1, map[int]uint32{6: 3}))
	if r.Backpack[0].EnhancementDetails != nil || r.InventoryBags[0].Slots[0].EnhancementDetails != nil {
		t.Fatal("foreign ownership detail leaked")
	}
	packet(t, s, opcode.SMSGNewWorld, login())
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, fields))
	if r.SelfEquipment[6].EnhancementDetails != nil || r.Backpack[0].EnhancementDetails != nil {
		t.Fatal("previous visit reused")
	}
}

func TestEnhancementDetailsDifferentBodyNeverAdoptsForeignItem(t *testing.T) {
	s := NewService(3)
	packet(t, s, opcode.SMSGUpdateObject, createFixture(3, equipmentFields(6, armorItem)))
	r := packet(t, s, opcode.SMSGUpdateObject, objectFixture(armorItem, 1, map[int]uint32{3: 2962, 6: 1, 0xe: 1, 0x16: 3210}))
	if r.SelfEquipment[6].EnhancementDetails != nil || r.SelfEquipment[6].MetadataKnown {
		t.Fatal("different body adopted foreign owned details")
	}
}
