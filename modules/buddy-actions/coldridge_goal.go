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

func (g coldridgePracticeGoal) supportsEntry(entry uint32) bool {
	return g.questID == 179 && entry == 705 || g.questID == 170 && (entry == 707 || entry == 724) || g.questID == 183 && entry == 708 || g.questID == 182 && entry == 706
}

func (g coldridgePracticeGoal) creatureProfile() (level, maxHealth, faction uint32) {
	switch g.questID {
	case 170:
		return 2, 55, 36
	case 183:
		return 3, 71, 189
	}
	return 0, 0, 0
}

// Saved public proof mekkatorque-coldridge-182-public-proof.json, SHA256
// 84e76d3570162b8fabc05a98bd3cf8d7991293d3d433fb6d18a740cd628821bb,
// binds quest182 objective0 to706x10 and records these two actual map0 profiles.
// This structural matcher also accepts the retained profile on a corpse;
// current health, alive/loot state, GUID, position and claims remain caller gates.
func matches182Profile(c *worldv1.CreatureState) bool {
	return c != nil && c.Entry == 706 && c.MapKnown && c.MapId == 0 && c.LevelKnown && c.FactionTemplate == 190 && (c.Level == 3 && c.MaxHealth == 71 || c.Level == 4 && c.MaxHealth == 86)
}

// Local public-client receipts under data/autonomy/uther/roster establish the
// scope; no actor, entry location or quest requirement is inferred at runtime.
// rpc-20261030T173922654417Z.json: quest170 objectives707x6,724x6 (indices0,1).
// SHA256 e7daf4152a21157438c304a5c57b3a986f37fe46d1ed06e34ef8f49607ac693f.
// rpc-20261030T175229037808Z.json and rpc-20261030T175557519449Z.json:
// actual724 and707 observations, respectively, both level2, max health55,
// faction36, ordinary creature type, map0. Their SHA256 values are
// f63a45043ec5a13631fefb6a57a6f7ad8d1e3cdf2d44d7730c1533da3e64dcad and
// 146bf374a7355d88d64a628c98cab93f931b49eba45c5b365a943f65f444e2b5.
// rpc-20261030T180639716920Z.json establishes quest183 objective0=708x12;
// SHA256 5ec46cdf99f6aa6b91ea754943968345510e45102f3b834b77a2b68f2126efbd.
// lake-exploration-20261030T1936/rpc-20261030T193635119420Z.json establishes
// actual708 level3/max health71/faction189/map0; SHA256
// e6cc24cbbe8df941fecc0e65d76a1dc86fab2e3907980d2803d1197437432409.
// Some observed708 positions exceed the sealed region; observation never
// widens the controller's existing position or navigation bounds.
// Eligibility is structural: a completed objective can still have the current
// own fight to finish. Fresh position, ownership, pull and route gates remain
// the controller's responsibility; only targetNeeded authorizes a new pull.
func (g coldridgePracticeGoal) eligible(c *worldv1.CreatureState, self *worldv1.PlayerState) bool {
	if g.questID == 179 {
		return eligibleColdridgeWolf(c)
	}
	level, maxHealth, faction := g.creatureProfile()
	if g.questID == 182 {
		if !matches182Profile(c) {
			return false
		}
		level, maxHealth, faction = c.Level, c.MaxHealth, c.FactionTemplate
	}
	return level != 0 && c != nil && g.supportsEntry(c.Entry) && c.Guid>>48 == 0xf130 && c.Guid&0xffffff != 0 && uint32(c.Guid>>24&0xffffff) == c.Entry && c.Present && c.MapKnown && c.MapId == 0 && c.LevelKnown && c.Level == level && c.HealthKnown && c.MaxHealth == maxHealth && c.Health > 0 && c.Health <= c.MaxHealth && c.AliveKnown && c.Alive && c.FactionTemplate == faction && c.NpcFlags == 0 && c.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) == 0 && c.UnitFlags_2&1 == 0 && self != nil && self.MapKnown && self.MapId == 0 && self.LevelKnown && self.Level >= c.Level
}
