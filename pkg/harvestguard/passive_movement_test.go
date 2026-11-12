package harvestguard

import "testing"

func TestPassiveModifiersPreserveHarvestSafetyAndHealingPriority(t *testing.T) {
	w := guardFixture()
	w.Self.MovementFlags = 0x70000000
	w.Owner.MovementFlags = 0x70000000
	if got := WorldReason(w, 1, []uint64{1, 3, 4}); got != "" {
		t.Fatal(got)
	}
	w.Owner.Health = 50
	if got := WorldReason(w, 1, []uint64{1, 3, 4}); got != "harvest_observed_healing_need_takes_priority" {
		t.Fatal("passive owner injury lost priority", got)
	}
	w.Owner.Health = 100
	for _, bad := range []uint32{0x200, 0x400, 0x1000, 0x200000, 0x2000000, 0x80000000} {
		w.Self.MovementFlags = 0x70000000 | bad
		if got := WorldReason(w, 1, []uint64{1, 3, 4}); got != "harvest_own_ordinary_ground_movement_required" {
			t.Fatal("actual unsupported mode became harvesting", bad, got)
		}
	}
}
