package buddyloot

import (
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

// This is read-only preflight for an own observed quest. It does not authorize
// any corpse: every operation must select and retain a matched loot goal below.
func ownColdridgeQuest(w *worldv1.SnapshotResponse, self uint64) bool {
	for _, questID := range []uint32{179, 170, 183} {
		if _, ok := coldridgeLootQuest(w, self, questID); ok {
			return true
		}
	}
	return false
}

// Quest and corpse identity remain fixed throughout one loot operation. The
// counter highwaters belong to that quest, never to whichever slot it used to
// occupy or to another newly observed quest. An authoritative position correction
// invalidates the operation; ordinary client estimates keep the selected base.
type coldridgeLootGoal struct {
	questID          uint32
	corpseGUID       uint64
	positionRevision uint64
	counts           [2]uint32
}

func coldridgeLootQuest(w *worldv1.SnapshotResponse, self uint64, questID uint32) (*worldv1.QuestLogEntry, bool) {
	if questID != 179 && questID != 170 && questID != 183 || self == 0 || self>>48 != 0 || w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.Present || !w.Self.ClassKnown || w.Self.ClassId != 9 || !w.Self.LevelKnown || w.Self.Level < 1 || w.Self.Level > 5 || questID == 170 && w.Self.Level < 2 || questID == 183 && w.Self.Level < 3 || !w.SelfQuestLogKnown || len(w.SelfQuestLog) != 25 {
		return nil, false
	}
	var slots [25]bool
	quests := make(map[uint32]bool)
	var own *worldv1.QuestLogEntry
	for _, q := range w.SelfQuestLog {
		if q == nil || q.Slot >= 25 || slots[q.Slot] || q.QuestId != 0 && quests[q.QuestId] {
			return nil, false
		}
		slots[q.Slot] = true
		if q.QuestId != 0 {
			quests[q.QuestId] = true
		}
		if q.QuestId == 170 && !validColdridgeLootCounts(q.ObjectiveCounts) {
			return nil, false
		}
		if q.QuestId == 183 && !validColdridge183LootCounts(q.ObjectiveCounts) {
			return nil, false
		}
		if q.QuestId == questID {
			own = q
		}
	}
	return own, own != nil && !own.Failed && own.State&2 == 0
}

func validColdridgeLootCounts(counts []uint32) bool {
	if len(counts) != 4 {
		return false
	}
	for i, count := range counts {
		if count > 0xffff || i < 2 && count > 6 || i >= 2 && count != 0 {
			return false
		}
	}
	return true
}

func validColdridge183LootCounts(counts []uint32) bool {
	return len(counts) == 4 && counts[0] <= 12 && counts[1] == 0 && counts[2] == 0 && counts[3] == 0
}

func selectColdridgeLootGoal(w *worldv1.SnapshotResponse, self uint64, c *worldv1.CreatureState) (coldridgeLootGoal, bool) {
	if c == nil {
		return coldridgeLootGoal{}, false
	}
	for _, questID := range []uint32{179, 170, 183} {
		goal := coldridgeLootGoal{questID: questID, corpseGUID: c.Guid}
		if !goal.matchesCorpse(c) {
			continue
		}
		if q, ok := coldridgeLootQuest(w, self, questID); ok {
			goal.positionRevision = w.Self.PositionRevision
			if questID == 170 || questID == 183 {
				copy(goal.counts[:], q.ObjectiveCounts)
			}
			return goal, true
		}
	}
	return coldridgeLootGoal{}, false
}

func (g *coldridgeLootGoal) context(w *worldv1.SnapshotResponse, p *actionsv1.ClientPosition, self uint64, pendingGUID uint64) bool {
	if g == nil || g.corpseGUID == 0 || pendingGUID != g.corpseGUID {
		return false
	}
	entry := uint32(g.corpseGUID >> 24 & 0xffffff)
	if g.corpseGUID>>48 != 0xf130 || g.corpseGUID&0xffffff == 0 || g.questID == 179 && entry != 705 || g.questID == 170 && entry != 707 && entry != 724 || g.questID == 183 && entry != 708 {
		return false
	}
	q, ok := coldridgeLootQuest(w, self, g.questID)
	if !ok || w.Self.PositionRevision != g.positionRevision || !safeOwnQuestLootContext(w, p) || !coldridgePoint(w.Self.X, w.Self.Y, w.Self.Z) || !coldridgePoint(p.X, p.Y, p.Z) {
		return false
	}
	if g.questID == 170 || g.questID == 183 {
		for i := range g.counts {
			if q.ObjectiveCounts[i] < g.counts[i] {
				return false
			}
		}
		copy(g.counts[:], q.ObjectiveCounts)
	}
	// Completion is valid for the last corpse. Item receipts remain independent
	// of these quest counters, and the caller still owns its exact pending GUID.
	return true
}

// Scope is based on the local public-client receipts cited in the frozen
// buddy-actions/coldridge_goal.go: own quest170 requires707x6 then724x6, and
// both actual creature profiles are level2/max health55/faction36 on map0.
// Quest183 public template: data/autonomy/uther/roster/rpc-20261030T180639716920Z.json,
// SHA256 5ec46cdf99f6aa6b91ea754943968345510e45102f3b834b77a2b68f2126efbd:
// objective0 requires708x12. The actual level3/max71/faction189/map0 profile is
// in roster/lake-exploration-20261030T1936/rpc-20261030T193635119420Z.json,
// SHA256 e6cc24cbbe8df941fecc0e65d76a1dc86fab2e3907980d2803d1197437432409.
// Existing Coldridge bounds still exclude observed profiles outside the region.
// This is structural matching only. Service.eligibleWithin separately requires
// a present, dead, receiving-client-lootable corpse at a safe actual range.
// No killer or victim field is inferred from the body's death observation.
func (g coldridgeLootGoal) matchesCorpse(c *worldv1.CreatureState) bool {
	if c == nil || g.corpseGUID == 0 || c.Guid != g.corpseGUID || !c.MapKnown || c.MapId != 0 {
		return false
	}
	if g.questID == 179 {
		return coldridgeCorpse(c)
	}
	switch g.questID {
	case 170:
		if c.Entry != 707 && c.Entry != 724 || c.Level != 2 || c.MaxHealth != 55 || c.FactionTemplate != 36 {
			return false
		}
	case 183:
		if c.Entry != 708 || c.Level != 3 || c.MaxHealth != 71 || c.FactionTemplate != 189 {
			return false
		}
	default:
		return false
	}
	if c.Guid>>48 != 0xf130 || c.Guid&0xffffff == 0 || uint32(c.Guid>>24&0xffffff) != c.Entry || !c.LevelKnown {
		return false
	}
	x, y, z, known := corpsePosition(c)
	return known && coldridgePoint(x, y, z)
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
