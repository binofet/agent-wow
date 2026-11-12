package buddyloot

import (
	"fmt"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

func cold183GoalFixture() (*worldv1.SnapshotResponse, *actionsv1.ClientPosition, *worldv1.CreatureState) {
	w, p := coldridgeLootGoalSnapshot()
	w.Self.Level, w.Self.PositionRevision = 3, 7
	w.SelfQuestLog[2].QuestId, w.SelfQuestLog[2].ObjectiveCounts = 183, []uint32{1, 0, 0, 0}
	c := coldridgeLootGoalCorpse(708)
	c.Level, c.MaxHealth, c.FactionTemplate = 3, 71, 189
	return w, p, c
}

func TestColdridge183LootGoalUsesOnlyItsActualQuestCorpsePair(t *testing.T) {
	for _, quest := range []uint32{179, 170, 182, 183} {
		for _, entry := range []uint32{705, 706, 707, 708, 724} {
			t.Run(fmt.Sprintf("%d/%d", quest, entry), func(t *testing.T) {
				w, p, actual := cold183GoalFixture()
				w.SelfQuestLog[2].QuestId = quest
				c := coldridgeLootGoalCorpse(entry)
				if entry == 708 {
					c = actual
				}
				g, ok := selectColdridgeLootGoal(w, 8, c)
				want := quest == 179 && entry == 705 || quest == 170 && (entry == 707 || entry == 724) || quest == 183 && entry == 708
				if ok != want || ok && (g.questID != quest || g.corpseGUID != c.Guid || !g.context(w, p, 8, c.Guid)) {
					t.Fatal("quest or corpse authorized a different pair", g, ok)
				}
			})
		}
	}
}

func TestColdridge183LootGoalRequiresOwnLevelAndVerifiedProfile(t *testing.T) {
	for _, level := range []uint32{2, 3, 5, 6} {
		w, _, c := cold183GoalFixture()
		w.Self.Level = level
		if _, ok := selectColdridgeLootGoal(w, 8, c); ok != (level >= 3 && level <= 5) {
			t.Fatal("wrong own level admission", level, ok)
		}
	}
	for name, change := range map[string]func(*worldv1.CreatureState){
		"unknown_level": func(c *worldv1.CreatureState) { c.LevelKnown = false },
		"level2":        func(c *worldv1.CreatureState) { c.Level = 2 },
		"level4":        func(c *worldv1.CreatureState) { c.Level = 4 },
		"max55":         func(c *worldv1.CreatureState) { c.MaxHealth = 55 },
		"max70":         func(c *worldv1.CreatureState) { c.MaxHealth = 70 },
		"max72":         func(c *worldv1.CreatureState) { c.MaxHealth = 72 },
		"faction36":     func(c *worldv1.CreatureState) { c.FactionTemplate = 36 },
		"encoded707":    func(c *worldv1.CreatureState) { c.Guid = cold170LootGUID(707) },
		"zero_low":      func(c *worldv1.CreatureState) { c.Guid &^= 0xffffff },
		"other_kind":    func(c *worldv1.CreatureState) { c.Guid = 0xf1500002c4000031 },
		"other_map":     func(c *worldv1.CreatureState) { c.MapId = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			w, _, c := cold183GoalFixture()
			change(c)
			if _, ok := selectColdridgeLootGoal(w, 8, c); ok {
				t.Fatal("unsupported183 corpse profile accepted", c)
			}
		})
	}
}

func TestColdridge183LootGoalInvalidObservationsCannotPartiallyAdvanceCounts(t *testing.T) {
	changes := map[string]func(*worldv1.SnapshotResponse){
		"foreign_self": func(w *worldv1.SnapshotResponse) { w.Self.Guid = 7 },
		"other_class":  func(w *worldv1.SnapshotResponse) { w.Self.ClassId = 8 },
		"unknown_log":  func(w *worldv1.SnapshotResponse) { w.SelfQuestLogKnown = false },
		"partial_log":  func(w *worldv1.SnapshotResponse) { w.SelfQuestLog = w.SelfQuestLog[:24] },
		"nil_row":      func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24] = nil },
		"duplicate_slot": func(w *worldv1.SnapshotResponse) {
			w.SelfQuestLog[24].Slot = 2
		},
		"duplicate_quest": func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24].QuestId = 183 },
		"absent_counts":   func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].ObjectiveCounts = nil },
		"short_counts":    func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].ObjectiveCounts = []uint32{4, 0, 0} },
		"long_counts":     func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].ObjectiveCounts = []uint32{4, 0, 0, 0, 0} },
		"above12":         func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].ObjectiveCounts[0] = 13 },
		"overflow":        func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].ObjectiveCounts[0] = 65536 },
		"objective1":      func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].ObjectiveCounts[1] = 1 },
		"objective2":      func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].ObjectiveCounts[2] = 1 },
		"objective3":      func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].ObjectiveCounts[3] = 1 },
		"failed":          func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].Failed = true },
		"failed_state":    func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].State = 2 },
		"actual_revision": func(w *worldv1.SnapshotResponse) { w.Self.PositionRevision++ },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			w, p, c := cold183GoalFixture()
			g, ok := selectColdridgeLootGoal(w, 8, c)
			if !ok {
				t.Fatal("valid fixture not admitted")
			}
			before := g
			w.SelfQuestLog[2].ObjectiveCounts[0] = 4
			change(w)
			if g.context(w, p, 8, c.Guid) || g != before {
				t.Fatal("invalid later observation changed the operation's bound facts", g)
			}
		})
	}
}

func TestColdridge183LootGoalKeepsProgressAcrossSlotChangesAndCompletion(t *testing.T) {
	w, p, c := cold183GoalFixture()
	g, _ := selectColdridgeLootGoal(w, 8, c)
	q := w.SelfQuestLog[2]
	q.ObjectiveCounts[0] = 3
	if !g.context(w, p, 8, c.Guid) || g.counts != [2]uint32{3, 0} {
		t.Fatal("actual183 progress did not advance", g)
	}
	q.ObjectiveCounts[0] = 2
	if g.context(w, p, 8, c.Guid) || g.counts != [2]uint32{3, 0} {
		t.Fatal("183 progress regressed", g)
	}
	cold170MoveQuest(w, q, 19)
	q.ObjectiveCounts[0] = 12
	q.Complete, q.State = true, 1
	if !g.context(w, p, 8, c.Guid) || g.counts != [2]uint32{12, 0} || !g.matchesCorpse(c) {
		t.Fatal("reordered completed active183 lost its last corpse", g)
	}
	fresh, ok := selectColdridgeLootGoal(w, 8, c)
	if !ok || !fresh.context(w, p, 8, c.Guid) || g.context(w, p, 8, c.Guid+1) {
		t.Fatal("last-corpse permission lost exact GUID or actual completed quest")
	}
}

func TestColdridge183LootGoalCannotFallBackToAnotherQuest(t *testing.T) {
	for _, other := range []uint32{179, 170} {
		w, p, c := cold183GoalFixture()
		g, _ := selectColdridgeLootGoal(w, 8, c)
		w.SelfQuestLog[2].QuestId = other
		w.SelfQuestLog[2].ObjectiveCounts = []uint32{0, 0, 0, 0}
		if !ownColdridgeQuest(w, 8) || g.context(w, p, 8, c.Guid) || g.questID != 183 {
			t.Fatal("another quest rescued the bound183 operation", other, g)
		}
	}
}

func TestColdridge183LootGoalKeepsTheExistingRegionSealed(t *testing.T) {
	for _, y := range []float32{699.9, 700, 700.001, 701.332, 747.95306} {
		w, _, c := cold183GoalFixture()
		c.Y = y
		_, ok := selectColdridgeLootGoal(w, 8, c)
		if ok != (y <= 700) {
			t.Fatal("183 expanded the existing Coldridge region", y, ok)
		}
	}
}
