package buddyinventory

import (
	"context"
	"encoding/binary"
	"testing"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

func grayArmorFixture(entry, subclass, inventoryType uint32) []byte {
	p := metadataFixture(entry, 4, 0, inventoryType, 0)
	binary.LittleEndian.PutUint32(p[8:], subclass)
	binary.LittleEndian.PutUint32(p[len(p)-29*4+11*4:], 22) // Actual ordinary armor durability.
	return p
}

func TestGrayArmorSaleUsesCompleteOrdinaryArmorTemplate(t *testing.T) {
	for _, subclass := range []uint32{1, 2, 3, 4} {
		for _, inventoryType := range []uint32{1, 3, 5, 6, 7, 8, 9, 10, 16, 20} {
			m, err := parseItemMetadata(grayArmorFixture(1376, subclass, inventoryType))
			if err != nil || !m.disposable() || m.maxDurability != 22 {
				t.Fatal(subclass, inventoryType, m, err)
			}
		}
	}
	p := grayArmorFixture(1376, 1, 16)
	for n := range len(p) {
		m, err := parseItemMetadata(p[:n])
		if err == nil && m.disposable() {
			t.Fatalf("partial armor response authorized sale at %d", n)
		}
	}
	if _, err := parseItemMetadata(append(p, 0)); err == nil {
		t.Fatal("trailing armor bytes accepted")
	}
	for _, field := range []int{11, 12, 13, 14, 15, 16, 17} {
		q := append([]byte(nil), p...)
		binary.LittleEndian.PutUint32(q[16+len("Fixture")+4+field*4:], 1)
		m, err := parseItemMetadata(q)
		if err != nil || !m.hasRequirement || m.disposable() {
			t.Fatal("special requirement accepted", field, m, err)
		}
	}
}

func TestGrayArmorSaleExcludesQualityQuestFunctionalItemsAndTools(t *testing.T) {
	m, err := parseItemMetadata(grayArmorFixture(1376, 1, 16))
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*itemMetadata){
		"white":              func(p *itemMetadata) { p.quality = 1 },
		"quest_bound":        func(p *itemMetadata) { p.bonding = 4 },
		"soulbound_template": func(p *itemMetadata) { p.bonding = 1 },
		"starts_quest":       func(p *itemMetadata) { p.startQuest = 7 },
		"spell":              func(p *itemMetadata) { p.hasSpell = true },
		"flags":              func(p *itemMetadata) { p.flags = 1 },
		"flags2":             func(p *itemMetadata) { p.flags2 = 1 },
		"page":               func(p *itemMetadata) { p.pageText = 1 },
		"lock":               func(p *itemMetadata) { p.lockID = 1 },
		"bag_family":         func(p *itemMetadata) { p.bagFamily = 1 },
		"duration":           func(p *itemMetadata) { p.duration = 1 },
		"container":          func(p *itemMetadata) { p.containerSlots = 1 },
		"fishing_pole":       func(p *itemMetadata) { p.class = 2; p.subclass = 20; p.inventoryType = 17 },
		"weapon":             func(p *itemMetadata) { p.class = 2; p.subclass = 7; p.inventoryType = 13 },
		"shield":             func(p *itemMetadata) { p.subclass = 6; p.inventoryType = 14 },
		"relic":              func(p *itemMetadata) { p.subclass = 8; p.inventoryType = 28 },
		"shirt":              func(p *itemMetadata) { p.inventoryType = 4 },
		"unknown":            func(p *itemMetadata) { p.known = false },
	} {
		t.Run(name, func(t *testing.T) {
			q := m
			change(&q)
			if q.disposable() {
				t.Fatal(q)
			}
		})
	}
	for inventoryType := uint32(0); inventoryType <= 28; inventoryType++ {
		q := m
		q.inventoryType = inventoryType
		allowed := inventoryType == 1 || inventoryType == 3 || inventoryType >= 5 && inventoryType <= 10 || inventoryType == 16 || inventoryType == 20
		if q.disposable() != allowed {
			t.Fatal("inventory type", inventoryType, q)
		}
	}
}

func TestGrayArmorSaleStillRequiresExactCarriedSlotAndActualReceipt(t *testing.T) {
	s, f := fixtureInventory()
	m, err := parseItemMetadata(grayArmorFixture(4865, 1, 5))
	if err != nil {
		t.Fatal(err)
	}
	s.metadata[4865] = m
	state := stateInventory(t, s)
	if !state.Items[0].SafeJunkSaleEligible {
		t.Fatal(state.Items[0])
	}
	request := sellRequest(t, s)
	actualProof(s, f, true)
	if _, err := s.Sell(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	result := waitInventory(t, s)
	if result.Operation.Status != "CONFIRMED" || !result.Operation.InventoryConfirmed || !result.Operation.CopperConfirmed || !result.Operation.LeaseReleased || len(f.packets) != 1 {
		t.Fatal(result)
	}
	// An equipment-slot claim cannot stand in for an observed carried slot.
	s, f = fixtureInventory()
	s.metadata[4865] = m
	f.w.InventoryBags[0].Slots[0] = &worldv1.BackpackSlot{Slot: 0}
	request = sellRequest(t, s)
	request.Source = &inventoryv1.ItemRef{Bag: 0, Slot: 5, ItemGuid: junkGUID, Entry: 4865, StackCount: 3}
	if _, err := s.Sell(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	result = waitInventory(t, s)
	if result.Operation.Status != "BLOCKED" || len(f.packets) != 0 {
		t.Fatal(result, f.packets)
	}
}
