package buddyinventory

import (
	"context"
	"encoding/binary"
	"testing"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func starterBootMetadata(t *testing.T, entry uint32) itemMetadata {
	t.Helper()
	p := metadataFixture(entry, 4, 1, 8, 0)
	at := 16 + len("Fixture") + 4
	binary.LittleEndian.PutUint32(p[at+4*7:], ^uint32(0))
	binary.LittleEndian.PutUint32(p[at+4*8:], ^uint32(0))
	binary.LittleEndian.PutUint32(p[at+4*19:], 1)
	m, err := parseItemMetadata(p)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func starterBootGear(t *testing.T, entry uint32) (*Service, *fakeInventory, *inventoryv1.EquipGearRequest) {
	t.Helper()
	s, f := fixtureGear(t, false)
	f.w.Self.ClassId = 5
	f.w.SelfSkills[0] = &worldv1.SkillState{Slot: 0, SkillId: 415, Value: 1, MaxValue: 1}
	f.w.SelfEquipment[7] = &worldv1.EquipmentSlotState{Slot: 7, SlotKnown: true, ItemGuid: oldPantsGUID,
		MetadataKnown: true, Entry: entry, StackCount: 1, DurabilityKnown: true, EnhancementsKnown: true}
	s.metadata[entry] = starterBootMetadata(t, entry)
	s.queried[entry] = true
	p := gearMetadataFixture(80, 7)
	binary.LittleEndian.PutUint32(p[8:], 1) // Cloth shoes, not leather.
	at := 16 + len("Fixture") + 4
	binary.LittleEndian.PutUint32(p[at+4*6:], 8)
	m, err := parseItemMetadata(p)
	if err != nil {
		t.Fatal(err)
	}
	s.metadata[80] = m
	s.queried[80] = true
	f.w.InventoryBags[0].Slots[2].Entry = 80
	st := stateInventory(t, s)
	return s, f, &inventoryv1.EquipGearRequest{SessionId: "body", RequestId: "starter-boots",
		ExpectedRevision: st.Revision, ExpectedInventoryRevision: st.InventoryRevision,
		Source:        &inventoryv1.ItemRef{Slot: 2, ItemGuid: breechesGUID, Entry: 80, StackCount: 1},
		EquipmentSlot: 7, ExpectedEquipmentRevision: st.EquipmentRevision,
		ExpectedEquippedItemGuid: oldPantsGUID, RequireConservativeUpgrade: true}
}

func TestStarterArmorBootUpgradeRequiresNormalDisplacedItemProof(t *testing.T) {
	for _, entry := range []uint32{51, 55} { // Actual Neophyte's/Apprentice's zero-armor subclass0 boots.
		t.Run(map[uint32]string{51: "priest", 55: "mage"}[entry], func(t *testing.T) {
			s, f, r := starterBootGear(t, entry)
			if entry == 55 {
				f.w.Self.ClassId = 8
			}
			st := stateInventory(t, s)
			var eligible bool
			for _, i := range st.Items {
				if i.Item.Entry == 80 {
					eligible = i.GearEquipEligible && i.ConservativeUpgrade && i.GearEquipmentSlot == 7 && i.ExpectedEquippedItemGuid == oldPantsGUID
				}
			}
			if !eligible || s.metadata[entry].ordinaryArmor() {
				t.Fatal("real cloth upgrade over plain starter clothing not selected", st.Items)
			}
			gearProof(s, f)
			r.ExpectedRevision = st.Revision
			if _, err := s.EquipGear(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			st = waitInventory(t, s)
			if st.Operation.Status != "CONFIRMED" || !st.Operation.InventoryConfirmed || !st.Operation.EquipmentConfirmed || !st.Operation.LeaseReleased ||
				st.Equipment[7].ItemGuid != breechesGUID || st.InventoryBags[0].Slots[2].ItemGuid != oldPantsGUID || len(f.packets) != 1 {
				t.Fatal("normal exact swap/displaced-item proof missing", st)
			}
			if f.packets[0].Opcode != opcode.CMSGAutoequipItemSlot || len(f.packets[0].Payload) != 9 || f.packets[0].Payload[8] != 7 {
				t.Fatal("unexpected equip packet", f.packets)
			}
		})
	}
}

func TestStarterArmorBaselineRefusesFunctionalOrUnknownClothing(t *testing.T) {
	base := starterBootMetadata(t, 51)
	if !comparableArmorBaseline(base) {
		t.Fatal("plain full-frame subclass0 baseline not comparable", base)
	}
	for name, change := range map[string]func(*itemMetadata){
		"unknown":              func(m *itemMetadata) { m.known = false },
		"no entry":             func(m *itemMetadata) { m.entry = 0 },
		"weapon":               func(m *itemMetadata) { m.class = 2 },
		"jewelry":              func(m *itemMetadata) { m.inventoryType = 2 },
		"uncommon":             func(m *itemMetadata) { m.quality = 2 },
		"armor":                func(m *itemMetadata) { m.armor = 1 },
		"stat":                 func(m *itemMetadata) { m.statCount, m.stats[0] = 1, itemStat{7, 1} },
		"resistance":           func(m *itemMetadata) { m.resistances[0] = 1 },
		"durable":              func(m *itemMetadata) { m.maxDurability = 1 },
		"bound":                func(m *itemMetadata) { m.bonding = 1 },
		"level":                func(m *itemMetadata) { m.requiredLevel = 1 },
		"requirement":          func(m *itemMetadata) { m.hasRequirement = true },
		"skill":                func(m *itemMetadata) { m.requiredSkill = 415 },
		"skill rank":           func(m *itemMetadata) { m.requiredSkillRank = 1 },
		"spell requirement":    func(m *itemMetadata) { m.requiredSpell = 1 },
		"honor":                func(m *itemMetadata) { m.requiredHonorRank = 1 },
		"city":                 func(m *itemMetadata) { m.requiredCityRank = 1 },
		"reputation faction":   func(m *itemMetadata) { m.requiredReputationFaction = 1 },
		"reputation rank":      func(m *itemMetadata) { m.requiredReputationRank = 1 },
		"restricted class":     func(m *itemMetadata) { m.allowableClass = 1 },
		"restricted race":      func(m *itemMetadata) { m.allowableRace = 1 },
		"flags":                func(m *itemMetadata) { m.flags = 1 },
		"flags2":               func(m *itemMetadata) { m.flags2 = 1 },
		"item spell":           func(m *itemMetadata) { m.hasSpell = true },
		"bag":                  func(m *itemMetadata) { m.containerSlots = 1 },
		"quest":                func(m *itemMetadata) { m.startQuest = 1 },
		"lock":                 func(m *itemMetadata) { m.lockID = 1 },
		"page":                 func(m *itemMetadata) { m.pageText = 1 },
		"bag family":           func(m *itemMetadata) { m.bagFamily = 1 },
		"duration":             func(m *itemMetadata) { m.duration = 1 },
		"unique":               func(m *itemMetadata) { m.maxCount = 1 },
		"stack":                func(m *itemMetadata) { m.stackable = 2 },
		"limit":                func(m *itemMetadata) { m.itemLimitCategory = 1 },
		"set":                  func(m *itemMetadata) { m.itemSet = 1 },
		"scaling distribution": func(m *itemMetadata) { m.scalingDistribution = 1 },
		"scaling value":        func(m *itemMetadata) { m.scalingValue = 1 },
		"special":              func(m *itemMetadata) { m.specialArmor = true },
	} {
		t.Run(name, func(t *testing.T) {
			m := base
			change(&m)
			if comparableArmorBaseline(m) {
				t.Fatal("functional/unknown baseline admitted", m)
			}
		})
	}
}

func TestStarterArmorKeepsCandidateAndEnhancementGates(t *testing.T) {
	for _, mode := range []string{"candidate subclass0", "no proficiency", "candidate enchanted", "baseline enchanted", "baseline enhancements unknown", "wrong slot", "no improvement"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r := starterBootGear(t, 51)
			m := s.metadata[80]
			switch mode {
			case "candidate subclass0":
				m.subclass = 0
			case "no proficiency":
				f.w.SelfSkills[0] = &worldv1.SkillState{Slot: 0}
			case "candidate enchanted":
				f.w.InventoryBags[0].Slots[2].HasEnhancements = true
			case "baseline enchanted":
				f.w.SelfEquipment[7].HasEnhancements = true
			case "baseline enhancements unknown":
				f.w.SelfEquipment[7].EnhancementsKnown = false
			case "wrong slot":
				old := s.metadata[51]
				old.inventoryType = 7
				s.metadata[51] = old
			case "no improvement":
				m.armor = 0
			}
			s.metadata[80] = m
			r.ExpectedRevision = stateInventory(t, s).Revision
			if _, err := s.EquipGear(context.Background(), r); err == nil {
				st := waitInventory(t, s)
				if st.Operation.Status != "BLOCKED" {
					t.Fatal("unsafe automatic equip accepted", st)
				}
			}
			if len(f.packets) != 0 {
				t.Fatal("rejected candidate emitted equip", f.packets)
			}
		})
	}
}
