package buddyinventory

import (
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestGearFactsPreserveRawEnhancementDetailsWithoutValuation(t *testing.T) {
	s, f := fixtureGear(t, true)
	detail := &worldv1.ItemEnhancementState{PropertySeedKnown: true, PropertySeed: 123, RandomPropertyIdKnown: true, RandomPropertyId: -7,
		Enchantments: []*worldv1.ItemEnchantmentSlot{{Slot: 2, IdKnown: true, Id: 3210}}}
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
