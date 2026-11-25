package buddyinventory

import (
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
)

// before is observe's detached comparison snapshot, never the published state.
// A decreasing, still-positive timer at the same observed aura revision is
// elapsed time, not a new inventory operation precondition. Keep the current
// wire countdown exact. Refreshes, expiry, unknown duration and all other facts
// remain visible to the complete protobuf equality check.
func normalizeElapsedAuraCountdown(before, current *inventoryv1.State) {
	old, next := before.GetConsumableContext().GetSelfAuras(), current.GetConsumableContext().GetSelfAuras()
	if old == nil || next == nil || old.TargetGuid != next.TargetGuid || old.Revision == 0 || old.Revision != next.Revision || len(old.Auras) != len(next.Auras) {
		return
	}
	// Native aura rows originate in a slot map. Ordering alone is not a change
	// to those facts; duplicates or missing slots are not normalized.
	bySlot := make(map[uint32]*actionsv1.ObservedAura, len(old.Auras))
	for _, aura := range old.Auras {
		if aura == nil || bySlot[aura.Slot] != nil {
			return
		}
		bySlot[aura.Slot] = aura
	}
	ordered := make([]*actionsv1.ObservedAura, 0, len(next.Auras))
	for _, aura := range next.Auras {
		if aura == nil || bySlot[aura.Slot] == nil {
			return
		}
		ordered = append(ordered, bySlot[aura.Slot])
		delete(bySlot, aura.Slot)
	}
	for index, aura := range ordered {
		now := next.Auras[index]
		if aura.Revision != 0 && aura.Revision == now.Revision && aura.DurationKnown && now.DurationKnown && now.RemainingMs > 0 && now.RemainingMs < aura.RemainingMs {
			aura.RemainingMs = now.RemainingMs
		}
	}
	old.Auras = ordered
}
