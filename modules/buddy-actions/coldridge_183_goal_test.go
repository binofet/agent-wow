package buddyactions

import (
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

const cold183GUID uint64 = uint64(0xf130)<<48 | uint64(708)<<24 | 0x183

func cold183World(progress uint32) *worldv1.SnapshotResponse {
	w := &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{Guid: 8, Present: true, ClassKnown: true, ClassId: 9, LevelKnown: true, Level: 3, MapKnown: true, PositionKnown: true, PositionRevision: 1, X: -6240, Y: 331, Z: 383, HealthKnown: true, Health: 100, MaxHealth: 100, PowerKnown: true, Power: 223, MaxPower: 223, BaseManaKnown: true, BaseMana: 98, AliveKnown: true, Alive: true, UnitFlagsKnown: true, MovementFlagsKnown: true, SpeedsKnown: true, WalkSpeed: 2.5, RunSpeed: 7}, SelfQuestLogKnown: true, SelfQuestLogRevision: 1}
	for i := range 25 {
		w.SelfQuestLog = append(w.SelfQuestLog, &worldv1.QuestLogEntry{Slot: uint32(i), ObjectiveCounts: []uint32{0, 0, 0, 0}})
	}
	w.SelfQuestLog[3].QuestId = 183
	w.SelfQuestLog[3].ObjectiveCounts[0] = progress
	return w
}

func cold183Quest(w *worldv1.SnapshotResponse) *worldv1.QuestLogEntry {
	for _, q := range w.SelfQuestLog {
		if q != nil && q.QuestId == 183 {
			return q
		}
	}
	return nil
}

func cold183Creature(x float32) *worldv1.CreatureState {
	// The profile comes from the saved public proof; geometry and low GUID bits
	// here are mock inputs, not additional live creature observations.
	return &worldv1.CreatureState{Guid: cold183GUID, Entry: 708, Present: true, MapKnown: true, PositionKnown: true, PositionRevision: 1, X: x, Y: 331, Z: 383, LevelKnown: true, Level: 3, HealthKnown: true, Health: 71, MaxHealth: 71, AliveKnown: true, Alive: true, FactionTemplate: 189, UnitFlags_2: 2048, TargetKnown: true}
}

func TestColdridge183GoalPriorityAndNoMidrunSubstitution(t *testing.T) {
	w := cold183World(4)
	w.SelfQuestLog[10].QuestId = 170
	w.SelfQuestLog[20].QuestId = 179
	for _, wanted := range []uint32{179, 170, 183} {
		g, ok := selectColdridgePracticeGoal(w, 8)
		if !ok || g.questID != wanted {
			t.Fatal("start priority changed", wanted, g, ok)
		}
		if wanted == 179 {
			w.SelfQuestLog[20].Complete = true
		} else if wanted == 170 {
			w.SelfQuestLog[10].ObjectiveCounts = []uint32{6, 6, 0, 0}
		}
	}
	g, _ := selectColdridgePracticeGoal(w, 8)
	w.SelfQuestLog[20].Complete = false
	w.SelfQuestLog[10].ObjectiveCounts[0] = 0
	if active, needed := g.observe(w, 8); !active || !needed || g.questID != 183 || !g.targetNeeded(708) || g.targetNeeded(707) || g.supportsEntry(706) {
		t.Fatal("new older quests replaced the selected183 goal", g, active, needed)
	}
	cold183Quest(w).QuestId = 182
	if active, needed := g.observe(w, 8); active || needed || g.questID != 183 || g.targetNeeded(708) {
		t.Fatal("missing183 adopted another actual quest", g, active, needed)
	}
}

func TestColdridge183GoalRescansSlotsAndCommitsOnlyMonotonicCounts(t *testing.T) {
	w := cold183World(4)
	g, _ := selectColdridgePracticeGoal(w, 8)
	w.SelfQuestLog[3].QuestId = 233
	w.SelfQuestLog[3].ObjectiveCounts[0] = 0
	w.SelfQuestLog[19].QuestId = 183
	w.SelfQuestLog[19].ObjectiveCounts[0] = 5
	w.SelfQuestLog[19], w.SelfQuestLog[8] = w.SelfQuestLog[8], w.SelfQuestLog[19]
	if active, needed := g.observe(w, 8); !active || !needed || g.counts != [2]uint32{5, 0} {
		t.Fatal("old slot decoy replaced bound quest progress", g, active, needed)
	}
	for _, counts := range [][]uint32{{4, 0, 0, 0}, {6, 1, 0, 0}, {6, 0, 1, 0}, {6, 0, 0, 1}, {13, 0, 0, 0}, {65536, 0, 0, 0}, {6, 0, 0}, {6, 0, 0, 0, 0}} {
		cold183Quest(w).ObjectiveCounts = counts
		if active, needed := g.observe(w, 8); active || needed || g.counts != [2]uint32{5, 0} || g.targetNeeded(708) {
			t.Fatal("invalid counter snapshot authorized or partly committed progress", counts, g)
		}
	}
	cold183Quest(w).ObjectiveCounts = []uint32{12, 0, 0, 0}
	if active, needed := g.observe(w, 8); !active || needed || g.targetNeeded(708) || cold183Quest(w).Complete || !g.eligible(cold183Creature(-6237), w.Self) {
		t.Fatal("last objective count failed to stop new pulls while retaining finish eligibility", g, active, needed)
	}
}

func TestColdridge183GoalRequiresActualLevelAndCompleteUnambiguousOwnLog(t *testing.T) {
	cases := map[string]func(*worldv1.SnapshotResponse){
		"foreign_self":    func(w *worldv1.SnapshotResponse) { w.Self.Guid = 7 },
		"wrong_class":     func(w *worldv1.SnapshotResponse) { w.Self.ClassId = 8 },
		"unknown_class":   func(w *worldv1.SnapshotResponse) { w.Self.ClassKnown = false },
		"unknown_level":   func(w *worldv1.SnapshotResponse) { w.Self.LevelKnown = false },
		"level2":          func(w *worldv1.SnapshotResponse) { w.Self.Level = 2 },
		"level6":          func(w *worldv1.SnapshotResponse) { w.Self.Level = 6 },
		"unknown_log":     func(w *worldv1.SnapshotResponse) { w.SelfQuestLogKnown = false },
		"partial_log":     func(w *worldv1.SnapshotResponse) { w.SelfQuestLog = w.SelfQuestLog[:24] },
		"nil_slot":        func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24] = nil },
		"duplicate_slot":  func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24].Slot = 3 },
		"duplicate_quest": func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24].QuestId = 183 },
		"failed":          func(w *worldv1.SnapshotResponse) { cold183Quest(w).Failed = true },
		"failed_state":    func(w *worldv1.SnapshotResponse) { cold183Quest(w).State = 2 },
		"unsupported182":  func(w *worldv1.SnapshotResponse) { cold183Quest(w).QuestId = 182 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w := cold183World(4)
			g, _ := selectColdridgePracticeGoal(w, 8)
			mutate(w)
			if active, needed := g.observe(w, 8); active || needed || g.counts != [2]uint32{4, 0} {
				t.Fatal("invalid own quest observation accepted", g, active, needed)
			}
			if _, ok := selectColdridgePracticeGoal(w, 8); ok {
				t.Fatal("invalid observation authorized a fresh goal")
			}
		})
	}
}

func TestColdridge183GoalRequiresExact708ProfileAndKeepsCompletionSeparate(t *testing.T) {
	w := cold183World(0)
	g, _ := selectColdridgePracticeGoal(w, 8)
	cases := map[string]func(*worldv1.CreatureState){
		"entry706":          func(c *worldv1.CreatureState) { c.Entry, c.Guid = 706, coldThreatGUID(706) },
		"entry707":          func(c *worldv1.CreatureState) { c.Entry, c.Guid = 707, coldThreatGUID(707) },
		"unknown_level":     func(c *worldv1.CreatureState) { c.LevelKnown = false },
		"other_level":       func(c *worldv1.CreatureState) { c.Level = 2 },
		"other_health":      func(c *worldv1.CreatureState) { c.MaxHealth = 72 },
		"incoherent_health": func(c *worldv1.CreatureState) { c.Health = 72 },
		"other_faction":     func(c *worldv1.CreatureState) { c.FactionTemplate = 36 },
		"other_map":         func(c *worldv1.CreatureState) { c.MapId = 1 },
		"encoded_entry":     func(c *worldv1.CreatureState) { c.Guid = coldThreatGUID(724) },
		"other_type":        func(c *worldv1.CreatureState) { c.Guid &^= 0xffff000000000000 },
		"zero_counter":      func(c *worldv1.CreatureState) { c.Guid &^= 0xffffff },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := cold183Creature(-6237)
			mutate(c)
			if g.eligible(c, w.Self) {
				t.Fatal("unverified708 target profile admitted", c)
			}
		})
	}
	for _, completeState := range []bool{false, true} {
		q := cold183Quest(w)
		q.Complete, q.State = !completeState, 0
		if completeState {
			q.State = 1
		}
		if active, needed := g.observe(w, 8); !active || needed || g.targetNeeded(708) || !g.supportsEntry(708) {
			t.Fatal("server completion failed to stop new pulls", g, active, needed)
		}
	}
}
