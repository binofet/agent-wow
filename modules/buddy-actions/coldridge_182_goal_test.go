package buddyactions

import (
	"fmt"
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

const cold182GoalGUID uint64 = uint64(0xf130)<<48 | uint64(706)<<24 | 0x182

func cold182GoalWorld(progress uint32) *worldv1.SnapshotResponse {
	w := &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{Guid: 8, Present: true, ClassKnown: true, ClassId: 9, LevelKnown: true, Level: 4, MapKnown: true}, SelfQuestLogKnown: true}
	for i := range 25 {
		w.SelfQuestLog = append(w.SelfQuestLog, &worldv1.QuestLogEntry{Slot: uint32(i), ObjectiveCounts: []uint32{0, 0, 0, 0}})
	}
	w.SelfQuestLog[5].QuestId = 182
	w.SelfQuestLog[5].ObjectiveCounts[0] = progress
	return w
}

func cold182GoalQuest(w *worldv1.SnapshotResponse) *worldv1.QuestLogEntry {
	for _, q := range w.SelfQuestLog {
		if q != nil && q.QuestId == 182 {
			return q
		}
	}
	return nil
}

func cold182GoalCreature(level uint32) *worldv1.CreatureState {
	health := uint32(71)
	if level == 4 {
		health = 86
	}
	// Only the profile tuple comes from saved public evidence. Low GUID bits are
	// synthetic; this goal fixture supplies no position or route qualification.
	return &worldv1.CreatureState{Guid: cold182GoalGUID, Entry: 706, Present: true, MapKnown: true, LevelKnown: true, Level: level, HealthKnown: true, Health: health, MaxHealth: health, AliveKnown: true, Alive: true, FactionTemplate: 190, UnitFlags_2: 2048}
}

func TestColdridge182GoalHasLastPriorityAndCannotAdoptOtherQuests(t *testing.T) {
	w := cold182GoalWorld(1)
	w.SelfQuestLog[1].QuestId = 179
	w.SelfQuestLog[2].QuestId = 170
	w.SelfQuestLog[3].QuestId = 183
	var goal coldridgePracticeGoal
	for _, wanted := range []uint32{179, 170, 183, 182} {
		g, ok := selectColdridgePracticeGoal(w, 8)
		if !ok || g.questID != wanted {
			t.Fatal("182 changed existing goal priority", wanted, g, ok)
		}
		goal = g
		if wanted != 182 {
			for _, q := range w.SelfQuestLog {
				if q.QuestId == wanted {
					q.Complete = true
				}
			}
		}
	}
	for _, entry := range []uint32{705, 707, 708, 724} {
		if goal.targetNeeded(entry) || goal.supportsEntry(entry) {
			t.Fatal("182 borrowed another quest's target", entry, goal)
		}
	}
	if !goal.targetNeeded(706) || !goal.supportsEntry(706) {
		t.Fatal("182 lost its exact target", goal)
	}
	for _, q := range w.SelfQuestLog {
		q.Complete = false
	}
	if active, needed := goal.observe(w, 8); !active || !needed || goal.questID != 182 {
		t.Fatal("older quests replaced the pinned182", goal, active, needed)
	}
	cold182GoalQuest(w).QuestId = 0
	if active, needed := goal.observe(w, 8); active || needed || goal.questID != 182 || goal.targetNeeded(706) {
		t.Fatal("missing182 was rescued by another accepted quest", goal, active, needed)
	}
}

func TestColdridge182GoalOwnLevelRangeDoesNotRaiseLegacyCaps(t *testing.T) {
	for _, level := range []uint32{0, 1, 2, 3, 4, 5, 6, 7, 80} {
		t.Run(fmt.Sprint(level), func(t *testing.T) {
			w := cold182GoalWorld(1)
			w.Self.Level = level
			goal, ok := selectColdridgePracticeGoal(w, 8)
			want := level >= 4 && level <= 6
			if ok != want || ok && (goal.questID != 182 || !goal.targetNeeded(706)) {
				t.Fatal("182 did not enforce its own4..6 range", level, goal, ok)
			}
		})
	}
	for _, test := range []struct{ quest, minimum uint32 }{{179, 1}, {170, 2}, {183, 3}} {
		for _, level := range []uint32{test.minimum - 1, test.minimum, 5, 6, 7} {
			w := cold182GoalWorld(1)
			w.Self.Level = level
			w.SelfQuestLog[1].QuestId = test.quest
			g := coldridgePracticeGoal{questID: test.quest}
			active, needed := g.observe(w, 8)
			want := level >= test.minimum && level <= 5
			if active != want || needed != want {
				t.Fatal("182 raised an old goal's level cap or minimum", test.quest, level, g, active, needed)
			}
		}
	}
}

func TestColdridge182GoalRequiresOwnCompleteUnambiguousLog(t *testing.T) {
	cases := map[string]func(*worldv1.SnapshotResponse){
		"foreign_self":    func(w *worldv1.SnapshotResponse) { w.Self.Guid = 7 },
		"missing_self":    func(w *worldv1.SnapshotResponse) { w.Self = nil },
		"absent_self":     func(w *worldv1.SnapshotResponse) { w.Self.Present = false },
		"unknown_class":   func(w *worldv1.SnapshotResponse) { w.Self.ClassKnown = false },
		"other_class":     func(w *worldv1.SnapshotResponse) { w.Self.ClassId = 8 },
		"unknown_level":   func(w *worldv1.SnapshotResponse) { w.Self.LevelKnown = false },
		"unknown_log":     func(w *worldv1.SnapshotResponse) { w.SelfQuestLogKnown = false },
		"partial_log":     func(w *worldv1.SnapshotResponse) { w.SelfQuestLog = w.SelfQuestLog[:24] },
		"nil_slot":        func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24] = nil },
		"invalid_slot":    func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24].Slot = 25 },
		"duplicate_slot":  func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24].Slot = 5 },
		"duplicate_quest": func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24].QuestId = 182 },
		"foreign_duplicate_quest": func(w *worldv1.SnapshotResponse) {
			w.SelfQuestLog[23].QuestId, w.SelfQuestLog[24].QuestId = 3115, 3115
		},
		"long_log": func(w *worldv1.SnapshotResponse) {
			w.SelfQuestLog = append(w.SelfQuestLog, &worldv1.QuestLogEntry{Slot: 25})
		},
		"removed":      func(w *worldv1.SnapshotResponse) { cold182GoalQuest(w).QuestId = 0 },
		"failed":       func(w *worldv1.SnapshotResponse) { cold182GoalQuest(w).Failed = true },
		"failed_state": func(w *worldv1.SnapshotResponse) { cold182GoalQuest(w).State = 2 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w := cold182GoalWorld(2)
			g, _ := selectColdridgePracticeGoal(w, 8)
			mutate(w)
			if active, needed := g.observe(w, 8); active || needed || g.counts != [2]uint32{2, 0} || g.targetNeeded(706) {
				t.Fatal("invalid own observation authorized182 or changed highwater", g, active, needed)
			}
			if _, ok := selectColdridgePracticeGoal(w, 8); ok {
				t.Fatal("invalid observation authorized a fresh goal")
			}
		})
	}
	for _, self := range []uint64{0, 7, uint64(0xf130)<<48 | 8} {
		if _, ok := selectColdridgePracticeGoal(cold182GoalWorld(0), self); ok {
			t.Fatal("invalid player identity authorized182", self)
		}
	}
	g := coldridgePracticeGoal{questID: 182, counts: [2]uint32{2, 0}, needed: true}
	if active, needed := g.observe(nil, 8); active || needed || g.targetNeeded(706) || g.counts[0] != 2 {
		t.Fatal("missing snapshot reused earlier authority", g, active, needed)
	}
}

func TestColdridge182GoalRescansSlotsAndCommitsFourCountersAtomically(t *testing.T) {
	w := cold182GoalWorld(1)
	g, _ := selectColdridgePracticeGoal(w, 8)
	cold182GoalQuest(w).QuestId = 233
	w.SelfQuestLog[19].QuestId = 182
	w.SelfQuestLog[19].ObjectiveCounts[0] = 3
	w.SelfQuestLog[19], w.SelfQuestLog[8] = w.SelfQuestLog[8], w.SelfQuestLog[19]
	if active, needed := g.observe(w, 8); !active || !needed || g.counts != [2]uint32{3, 0} {
		t.Fatal("182 followed the old slot instead of its own quest", g, active, needed)
	}
	for _, counts := range [][]uint32{nil, {2, 0, 0, 0}, {11, 0, 0, 0}, {65536, 0, 0, 0}, {4, 1, 0, 0}, {4, 0, 1, 0}, {4, 0, 0, 1}, {4, 0, 0}, {4, 0, 0, 0, 0}} {
		cold182GoalQuest(w).ObjectiveCounts = counts
		if active, needed := g.observe(w, 8); active || needed || g.counts != [2]uint32{3, 0} || g.targetNeeded(706) {
			t.Fatal("regression or invalid four-counter vector partly committed", counts, g, active, needed)
		}
	}
	cold182GoalQuest(w).ObjectiveCounts = []uint32{4, 0, 0, 0}
	w.SelfQuestLog[24].Slot = 5
	if active, needed := g.observe(w, 8); active || needed || g.counts != [2]uint32{3, 0} {
		t.Fatal("a later invalid log row allowed an early counter commit", g, active, needed)
	}
	w.SelfQuestLog[24].Slot = 24
	if active, needed := g.observe(w, 8); !active || !needed || g.counts != [2]uint32{4, 0} {
		t.Fatal("a repaired current observation failed to advance atomically", g, active, needed)
	}
}

func TestColdridge182GoalCompletionRetainsOnlyStructuralFinishEligibility(t *testing.T) {
	for _, mode := range []string{"count10", "complete_flag", "complete_state"} {
		t.Run(mode, func(t *testing.T) {
			w := cold182GoalWorld(4)
			g, _ := selectColdridgePracticeGoal(w, 8)
			q := cold182GoalQuest(w)
			switch mode {
			case "count10":
				q.ObjectiveCounts[0] = 10
			case "complete_flag":
				q.Complete = true
			case "complete_state":
				q.State = 1
			}
			if active, needed := g.observe(w, 8); !active || needed || g.targetNeeded(706) || !g.supportsEntry(706) || !g.eligible(cold182GoalCreature(3), w.Self) || !g.eligible(cold182GoalCreature(4), w.Self) {
				t.Fatal("completion authorized a new pull or discarded the existing fight's profile", g, active, needed)
			}
			q.Failed = true
			if active, needed := g.observe(w, 8); active || needed || g.targetNeeded(706) {
				t.Fatal("failed quest remained an active finish goal", g, active, needed)
			}
		})
	}
}

func TestColdridge182ProfileMatchesOnlyTheTwoObservedTuples(t *testing.T) {
	for _, level := range []uint32{3, 4} {
		c := cold182GoalCreature(level)
		if !matches182Profile(c) {
			t.Fatal("actual observed tuple rejected", level)
		}
		c.Health, c.Alive = 0, false
		if !matches182Profile(c) || (coldridgePracticeGoal{questID: 182}).eligible(c, cold182GoalWorld(1).Self) {
			t.Fatal("retained corpse profile was confused with living pull eligibility", level)
		}
	}
	cases := map[string]func(*worldv1.CreatureState){
		"other_entry":   func(c *worldv1.CreatureState) { c.Entry = 708 },
		"unknown_map":   func(c *worldv1.CreatureState) { c.MapKnown = false },
		"other_map":     func(c *worldv1.CreatureState) { c.MapId = 1 },
		"unknown_level": func(c *worldv1.CreatureState) { c.LevelKnown = false },
		"level2":        func(c *worldv1.CreatureState) { c.Level = 2 },
		"level5":        func(c *worldv1.CreatureState) { c.Level = 5 },
		"level3_hp86":   func(c *worldv1.CreatureState) { c.MaxHealth = 86 },
		"level4_hp71":   func(c *worldv1.CreatureState) { c.Level = 4 },
		"wrong_health":  func(c *worldv1.CreatureState) { c.MaxHealth = 72 },
		"other_faction": func(c *worldv1.CreatureState) { c.FactionTemplate = 189 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := cold182GoalCreature(3)
			mutate(c)
			if matches182Profile(c) {
				t.Fatal("unobserved or mixed profile tuple admitted", c)
			}
		})
	}
	if matches182Profile(nil) {
		t.Fatal("missing creature matched the182 profile")
	}
}

func TestColdridge182EligibilityKeepsExistingIdentityAndLivingSafetyGates(t *testing.T) {
	g := coldridgePracticeGoal{questID: 182}
	for _, level := range []uint32{3, 4} {
		if !g.eligible(cold182GoalCreature(level), cold182GoalWorld(1).Self) {
			t.Fatal("supported living body rejected", level)
		}
	}
	for name, mutate := range map[string]func(*worldv1.CreatureState){
		"encoded_entry":  func(c *worldv1.CreatureState) { c.Guid = uint64(0xf130)<<48 | uint64(708)<<24 | 1 },
		"other_type":     func(c *worldv1.CreatureState) { c.Guid &^= 0xffff000000000000 },
		"zero_low":       func(c *worldv1.CreatureState) { c.Guid &^= 0xffffff },
		"not_present":    func(c *worldv1.CreatureState) { c.Present = false },
		"unknown_health": func(c *worldv1.CreatureState) { c.HealthKnown = false },
		"zero_health":    func(c *worldv1.CreatureState) { c.Health = 0 },
		"excess_health":  func(c *worldv1.CreatureState) { c.Health = c.MaxHealth + 1 },
		"unknown_alive":  func(c *worldv1.CreatureState) { c.AliveKnown = false },
		"not_alive":      func(c *worldv1.CreatureState) { c.Alive = false },
		"npc_flags":      func(c *worldv1.CreatureState) { c.NpcFlags = 1 },
		"not_attackable": func(c *worldv1.CreatureState) { c.UnitFlags = 2 },
		"unit_flags2":    func(c *worldv1.CreatureState) { c.UnitFlags_2 |= 1 },
	} {
		t.Run(name, func(t *testing.T) {
			c := cold182GoalCreature(3)
			mutate(c)
			if g.eligible(c, cold182GoalWorld(1).Self) {
				t.Fatal("182 waived a common living safety gate", c)
			}
		})
	}
	for name, mutate := range map[string]func(*worldv1.PlayerState){
		"unknown_map":   func(p *worldv1.PlayerState) { p.MapKnown = false },
		"other_map":     func(p *worldv1.PlayerState) { p.MapId = 1 },
		"unknown_level": func(p *worldv1.PlayerState) { p.LevelKnown = false },
		"below_body":    func(p *worldv1.PlayerState) { p.Level = 3 },
	} {
		t.Run(name, func(t *testing.T) {
			self := cold182GoalWorld(1).Self
			mutate(self)
			if g.eligible(cold182GoalCreature(4), self) {
				t.Fatal("182 waived own map or body-level gate", self)
			}
		})
	}
	if g.eligible(nil, cold182GoalWorld(1).Self) || g.eligible(cold182GoalCreature(3), nil) {
		t.Fatal("missing body or own player admitted")
	}
}

func TestColdridge182DoesNotChangeLegacyScalarProfilesOrLogRules(t *testing.T) {
	for _, test := range []struct{ quest, level, health, faction uint32 }{{179, 0, 0, 0}, {170, 2, 55, 36}, {183, 3, 71, 189}, {182, 0, 0, 0}} {
		g := coldridgePracticeGoal{questID: test.quest}
		level, health, faction := g.creatureProfile()
		if level != test.level || health != test.health || faction != test.faction {
			t.Fatal("legacy scalar profile changed or hid182's dual tuples", test, level, health, faction)
		}
	}
	for _, quest := range []uint32{179, 170, 183} {
		w := cold182GoalWorld(1)
		w.SelfQuestLog[1].QuestId = quest
		cold182GoalQuest(w).ObjectiveCounts = []uint32{11, 9, 9, 9}
		g := coldridgePracticeGoal{questID: quest}
		if active, needed := g.observe(w, 8); !active || !needed {
			t.Fatal("new182-only validation altered a legacy goal", quest, active, needed)
		}
	}
}
