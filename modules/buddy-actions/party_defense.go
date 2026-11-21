package buddyactions

import (
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

// An actual attack relation may precede combat flags briefly. Older relations
// require both players' current combat state, and after thirty seconds also the
// creature's current selected victim. A selected target alone grants nothing.
func ongoingAttack(c *worldv1.CreatureState, victim *worldv1.PlayerState) bool {
	if c == nil || victim == nil || !c.AttackVictimKnown || c.AttackVictimGuid != victim.Guid {
		return false
	}
	if c.TargetKnown && c.TargetGuid != victim.Guid {
		return false
	}
	if c.AttackAgeMs <= 5000 {
		return true
	}
	if !victim.UnitFlagsKnown || victim.UnitFlags&0x80000 == 0 {
		return false
	}
	if c.AttackAgeMs <= 30000 {
		return true
	}
	return c.TargetKnown && c.TargetGuid == victim.Guid && c.UnitFlagsKnown && c.UnitFlags&0x80000 != 0
}

func partyDefenseCreature(w *worldv1.SnapshotResponse, guid uint64) *worldv1.CreatureState {
	if w == nil || len(w.NearbyCreatures) > 32 {
		return nil
	}
	var found *worldv1.CreatureState
	for _, view := range [][]*worldv1.CreatureState{w.NearbyCreatures, {w.OwnerTarget, w.RequestedCreature}} {
		for _, c := range view {
			if c != nil && c.Guid == guid {
				found = c
			}
		}
	}
	return found
}
