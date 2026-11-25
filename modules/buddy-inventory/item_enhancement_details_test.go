package buddyinventory

import (
	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestGearFactsPreserveRawEnhancementDetailsWithoutValuation(t *testing.T) {
	s, f := fixtureGear(t, true)
	detail := &worldv1.ItemEnhancementState{PropertySeedKnown: true, PropertySeed: 123, RandomPropertyIdKnown: true, RandomPropertyId: -7,
		IdKnownMask: 4, Enchantments: []*worldv1.ItemEnchantmentSlot{{Slot: 2, Id: 3210}}}
	i := f.w.InventoryBags[0].Slots[2]
	i.EnhancementDetails = detail
	i.HasEnhancements = true
	st := stateGear(t, s)
	var found bool
	for _, row := range st.Items {
		if row.Item.ItemGuid != breechesGUID {
			continue
		}
		found = true
		if !proto.Equal(row.GearFacts.EnhancementDetails, detail) || !row.GearFacts.HasEnhancements {
			t.Fatal(row.GearFacts)
		}
		if row.ConservativeUpgrade || row.GearEquipEligible {
			t.Fatal("raw IDs relaxed enhancement safety")
		}
		row.GearFacts.EnhancementDetails.Enchantments[0].Id = 999
	}
	if !found || detail.Enchantments[0].Id != 3210 {
		t.Fatal("missing facts or alias")
	}
}

// A populated 32-item carried inventory plus all nineteen equipment slots must
// not inflate the ordinary bounded Lua input solely with pristine modifiers.
func TestPristineEnhancementWireStaysSparseForPopulatedInventory(t *testing.T) {
	detail := &worldv1.ItemEnhancementState{IdKnownMask: 4095, DurationKnownMask: 4095, ChargesKnownMask: 4095, PropertySeedKnown: true, RandomPropertyIdKnown: true}
	encoded, err := protojson.Marshal(detail)
	if err != nil || len(encoded) > 160 || len(detail.Enchantments) != 0 {
		t.Fatalf("pristine details inflated: %d %v", len(encoded), err)
	}
	state := &inventoryv1.State{}
	for i := 0; i < 32; i++ {
		state.Items = append(state.Items, &inventoryv1.ItemEligibility{Item: &inventoryv1.ItemRef{ItemGuid: uint64(i + 1), Entry: 2962, StackCount: 1}, GearFacts: &inventoryv1.GearItemFacts{EnhancementsKnown: true, EnhancementDetails: cloneEnhancementDetails(detail)}})
	}
	for i := 0; i < 19; i++ {
		state.Equipment = append(state.Equipment, &worldv1.EquipmentSlotState{Slot: uint32(i), SlotKnown: true, ItemGuid: uint64(i + 100), Entry: 2962, StackCount: 1, MetadataKnown: true, EnhancementsKnown: true, EnhancementDetails: cloneEnhancementDetails(detail)})
	}
	wire, err := protojson.Marshal(state)
	if err != nil || len(wire) > 24*1024 {
		t.Fatalf("representative clean inventory exceeds unchanged 32KiB input allowance: %d %v", len(wire), err)
	}
	t.Logf("pristine enhancement=%d bytes; 32-carried+19-equipped State=%d bytes", len(encoded), len(wire))
}
