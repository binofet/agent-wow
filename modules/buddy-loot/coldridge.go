package buddyloot

import (
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

// This is an own actual quest-log exception, not authority to discover targets
// from public spawn tables or to loot another character's unavailable items.
func ownColdridgeQuest(w *worldv1.SnapshotResponse, self uint64) bool {
	if w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.ClassKnown || w.Self.ClassId != 9 || !w.Self.LevelKnown || w.Self.Level < 1 || w.Self.Level > 5 || !w.SelfQuestLogKnown || len(w.SelfQuestLog) != 25 {
		return false
	}
	var slots [25]bool
	found := false
	for _, q := range w.SelfQuestLog {
		if q == nil || q.Slot >= 25 || slots[q.Slot] {
			return false
		}
		slots[q.Slot] = true
		if q.QuestId == 179 && !q.Failed && q.State&2 == 0 {
			found = true
		}
	}
	return found
}

func coldridgePoint(x, y, z float32) bool {
	return x >= -6399 && x <= -6000 && y >= 0 && y <= 700 && z >= 320 && z <= 450
}

func coldridgeQuestContext(w *worldv1.SnapshotResponse, p *actionsv1.ClientPosition, self uint64) bool {
	return ownColdridgeQuest(w, self) && safeOwnQuestLootContext(w, p) && coldridgePoint(w.Self.X, w.Self.Y, w.Self.Z) && coldridgePoint(p.X, p.Y, p.Z)
}

func coldridgeCorpse(c *worldv1.CreatureState) bool {
	if c == nil || c.Entry != 705 || c.Guid>>48 != 0xf130 || c.Guid&0xffffff == 0 || uint32(c.Guid>>24&0xffffff) != 705 {
		return false
	}
	x, y, z, known := corpsePosition(c)
	return known && coldridgePoint(x, y, z)
}
