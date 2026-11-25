package buddyactions

import worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"

// A goal stays bound to its selected quest for its whole practice run. Counts
// are high-water marks from complete own quest-log observations, not kill or
// transport counters. Needed is refreshed on every observation and fails closed.
type coldridgePracticeGoal struct {
	questID       uint32
	counts        [2]uint32
	needed        bool
	neededEntries [2]uint32
}

// Validate the complete own frame and preserve factual counter high-water marks.
// Whether this goal or an objective still needs work belongs to Lua.
func (g *coldridgePracticeGoal) observationCounts(w *worldv1.SnapshotResponse, selfGUID uint64) ([2]uint32, bool) {
	previous := g.counts
	if selfGUID == 0 || selfGUID>>48 != 0 || w == nil || w.Self == nil || w.Self.Guid != selfGUID || !w.Self.Present ||
		!w.Self.ClassKnown || w.Self.ClassId != 9 || !w.Self.LevelKnown || w.Self.Level < 1 ||
		!w.SelfQuestLogKnown || len(w.SelfQuestLog) != 25 {
		return previous, false
	}
	var slots [25]bool
	quests := make(map[uint32]bool)
	var own *worldv1.QuestLogEntry
	for _, q := range w.SelfQuestLog {
		if q == nil || q.Slot >= 25 || slots[q.Slot] || q.QuestId != 0 && quests[q.QuestId] {
			return previous, false
		}
		slots[q.Slot] = true
		if q.QuestId != 0 {
			quests[q.QuestId] = true
		}
		if q.QuestId == 170 && !validColdridgeThreatCounts(q.ObjectiveCounts) || q.QuestId == 183 && !validColdridge183Counts(q.ObjectiveCounts) ||
			g.questID == 182 && q.QuestId == 182 && !validColdridge182Counts(q.ObjectiveCounts) {
			return previous, false
		}
		if q.QuestId == g.questID {
			own = q
		}
	}
	if g.questID == 0 {
		return previous, true
	}
	if g.questID != 179 && g.questID != 170 && g.questID != 183 && g.questID != 182 {
		return previous, false
	}
	if own == nil {
		return previous, true
	}
	if g.questID == 179 {
		return previous, true
	}
	if len(own.ObjectiveCounts) != 4 {
		return previous, false
	}
	for i := range previous {
		if own.ObjectiveCounts[i] < previous[i] {
			return previous, false
		}
	}
	return [2]uint32{own.ObjectiveCounts[0], own.ObjectiveCounts[1]}, true
}

func validColdridgeThreatCounts(counts []uint32) bool {
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

func validColdridge183Counts(counts []uint32) bool {
	return len(counts) == 4 && counts[0] <= 12 && counts[1] == 0 && counts[2] == 0 && counts[3] == 0
}

func validColdridge182Counts(counts []uint32) bool {
	return len(counts) == 4 && counts[0] <= 10 && counts[1] == 0 && counts[2] == 0 && counts[3] == 0
}

func (g coldridgePracticeGoal) targetNeeded(entry uint32) bool {
	return g.needed && entry != 0 && (g.neededEntries[0] == entry || g.neededEntries[1] == entry)
}

// Actual packet availability is independent of the Lua scenario profile.
func coldridgeCreatureAvailable(c *worldv1.CreatureState, self *worldv1.PlayerState) bool {
	return self != nil && self.MapKnown && self.LevelKnown && practiceCreatureAvailable(c, self.MapId) &&
		c.Guid&0xffffff != 0 && uint32(c.Guid>>24&0xffffff) == c.Entry && c.Health <= c.MaxHealth
}
