package buddyactions

import (
	"context"
	"math"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

func coldLevelingWorld() *worldv1.SnapshotResponse {
	w := cold182GoalWorld(0)
	w.Self.PositionKnown, w.Self.PositionRevision = true, 7
	cold182GoalQuest(w).QuestId = 218
	return w
}

func coldLevelingQuest(w *worldv1.SnapshotResponse) *worldv1.QuestLogEntry {
	for _, q := range w.SelfQuestLog {
		if q != nil && q.QuestId == 218 {
			return q
		}
	}
	return nil
}

func TestColdridgeLevelingGoalIsExplicitAndNeverChangesQuestPriority(t *testing.T) {
	w := coldLevelingWorld()
	if g, ok := selectColdridgePracticeGoal(w, 8); ok || g.leveling {
		t.Fatal("accepted item quest silently selected a leveling or kill goal", g, ok)
	}
	g := coldridgePracticeGoal{leveling: true}
	if active, needed := g.observe(w, 8); !active || !needed || g.questID != 0 || g.counts != [2]uint32{} || !g.targetNeeded(706) {
		t.Fatal("explicit ordinary leveling goal was treated as quest kill credit", g, active, needed)
	}
	for _, entry := range []uint32{705, 707, 708, 724, 808} {
		if g.supportsEntry(entry) || g.targetNeeded(entry) {
			t.Fatal("leveling borrowed another target or inferred the item source", entry)
		}
	}
	for i, id := range []uint32{179, 170, 183, 182} {
		w.SelfQuestLog[10+i].QuestId = id
	}
	for _, id := range []uint32{179, 170, 183, 182} {
		selected, ok := selectColdridgePracticeGoal(w, 8)
		if !ok || selected.leveling || selected.questID != id {
			t.Fatal("explicit leveling support changed legacy selection", id, selected, ok)
		}
		for _, q := range w.SelfQuestLog {
			if q.QuestId == id {
				q.Complete = true
			}
		}
	}
	if active, needed := g.observe(w, 8); active || needed || g.questID != 0 || !g.leveling {
		t.Fatal("additional accepted quests replaced the pinned leveling kind", g)
	}
	for _, q := range w.SelfQuestLog {
		if q.QuestId != 218 {
			q.QuestId = 0
		}
	}
	if active, needed := g.observe(w, 8); !active || !needed {
		t.Fatal("actual sole218 context did not restore the same explicit goal", g)
	}
	coldLevelingQuest(w).QuestId = 0
	w.SelfQuestLog[10].QuestId = 182
	if active, needed := g.observe(w, 8); active || needed || g.targetNeeded(706) {
		t.Fatal("another accepted quest rescued missing218 context", g)
	}
}

func TestColdridgeLevelingGoalRequiresActualOwnContextAndStopsNeedingAtFive(t *testing.T) {
	for _, level := range []uint32{3, 4, 5, 6} {
		w := coldLevelingWorld()
		w.Self.Level = level
		g := coldridgePracticeGoal{leveling: true}
		active, needed := g.observe(w, 8)
		if active != (level == 4 || level == 5) || needed != (level == 4) || g.targetNeeded(706) != (level == 4) {
			t.Fatal("leveling context or new-pull cap used an inferred level", level, g, active, needed)
		}
	}
	for name, mutate := range map[string]func(*worldv1.SnapshotResponse){
		"foreign_body":      func(w *worldv1.SnapshotResponse) { w.Self.Guid = 7 },
		"unknown_level":     func(w *worldv1.SnapshotResponse) { w.Self.LevelKnown = false },
		"other_class":       func(w *worldv1.SnapshotResponse) { w.Self.ClassId = 8 },
		"unknown_log":       func(w *worldv1.SnapshotResponse) { w.SelfQuestLogKnown = false },
		"partial_log":       func(w *worldv1.SnapshotResponse) { w.SelfQuestLog = w.SelfQuestLog[:24] },
		"duplicate_slot":    func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24].Slot = 5 },
		"duplicate_quest":   func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24].QuestId = 218 },
		"failed":            func(w *worldv1.SnapshotResponse) { coldLevelingQuest(w).Failed = true },
		"completed":         func(w *worldv1.SnapshotResponse) { coldLevelingQuest(w).Complete = true },
		"false_kill_count":  func(w *worldv1.SnapshotResponse) { coldLevelingQuest(w).ObjectiveCounts[0] = 1 },
		"unsupported_count": func(w *worldv1.SnapshotResponse) { coldLevelingQuest(w).ObjectiveCounts[3] = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			w := coldLevelingWorld()
			g := coldridgePracticeGoal{leveling: true}
			g.observe(w, 8)
			mutate(w)
			if active, needed := g.observe(w, 8); active || needed || g.targetNeeded(706) {
				t.Fatal("invalid current context reused earlier leveling authority", g)
			}
		})
	}
	g := coldridgePracticeGoal{leveling: true, questID: 182}
	if active, needed := g.observe(coldLevelingWorld(), 8); active || needed || g.usesTrollRegion() || g.supportsEntry(706) || g.eligible(cold182GoalCreature(3), coldLevelingWorld().Self) {
		t.Fatal("hybrid quest/leveling kind acquired authority", g)
	}
}

func TestColdridgeLevelingReusesOnlyObserved706TuplesAndOwnLevelGuard(t *testing.T) {
	g := coldridgePracticeGoal{leveling: true}
	w := coldLevelingWorld()
	for _, level := range []uint32{3, 4} {
		c := cold182GoalCreature(level)
		if !g.eligible(c, w.Self) {
			t.Fatal("actual706 profile was lost", c)
		}
		w.Self.Level = level - 1
		if g.eligible(c, w.Self) {
			t.Fatal("leveling waived own-level>=target-level", c, w.Self)
		}
		w.Self.Level = 4
	}
	for _, mutate := range []func(*worldv1.CreatureState){
		func(c *worldv1.CreatureState) { c.Level = 4 },
		func(c *worldv1.CreatureState) { c.MaxHealth = 86 },
		func(c *worldv1.CreatureState) { c.FactionTemplate = 189 },
		func(c *worldv1.CreatureState) { c.Entry = 808 },
		func(c *worldv1.CreatureState) { c.Guid = coldThreatGUID(708) },
		func(c *worldv1.CreatureState) { c.Health = 0; c.Alive = false },
	} {
		c := cold182GoalCreature(3)
		mutate(c)
		if g.eligible(c, w.Self) {
			t.Fatal("leveling widened the observed target profile", c)
		}
	}
}

func TestColdridgeLevelingKeepsRawBaselineSeparateFromClosedCombatRegion(t *testing.T) {
	g := coldridgePracticeGoal{leveling: true}
	if !g.usesTrollRegion() || !g.baselineContains(cold182TestBaseline) || g.contains(cold182TestBaseline) || !g.contains(cold182TestStage) {
		t.Fatal("leveling widened combat scope to its old raw baseline")
	}
	for _, p := range []position{{x: -6452.001, y: 530, z: 385}, {x: -6400, y: 524.99, z: 385}, {x: -6400, y: 530, z: 389.01}, {x: float32(math.NaN()), y: 530, z: 385}} {
		if g.contains(p) {
			t.Fatal("leveling expanded the182 box", p)
		}
	}
	c := cold182GoalCreature(3)
	c.PositionKnown, c.X, c.Y, c.Z = true, cold182TestStage.x, cold182TestStage.y, cold182TestStage.z
	if !g.positionEvidenceWithin(c) {
		t.Fatal("synthetic in-scope body rejected")
	}
	c.Trajectory = &worldv1.CreatureTrajectory{Known: true, DestinationKnown: true, Stopped: true, DestinationX: -6400, DestinationY: 524.99, DestinationZ: 385}
	if g.positionEvidenceWithin(c) {
		t.Fatal("leveling ignored an advertised out-of-scope destination")
	}
	c.Trajectory = nil
	c.Y = 524.99
	if g.positionEvidenceWithin(c) {
		t.Fatal("leveling accepted an out-of-scope raw body")
	}
}

func TestColdridgeLevelingRoutesUseExistingRegionalBudgetWithoutFallback(t *testing.T) {
	s, _, _ := cold182Setup(t, 0, 4, 3)
	g := coldridgePracticeGoal{leveling: true}
	called := false
	s.planColdridge182Path = func(ctx context.Context, root string, from, to position) ([]position, error) {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 250*time.Millisecond {
			t.Fatal("leveling planner lost the250ms cap")
		}
		return coldStraight(ctx, root, from, to)
	}
	to := cold182TestStage
	to.x += 3
	if _, err := s.coldridgeGoalNavigationPath(context.Background(), g, cold182TestStage, to); err != nil || !called {
		t.Fatal("leveling did not reuse the declared182 planner", err)
	}
	called = false
	if _, err := s.coldridgeGoalNavigationPath(context.Background(), g, cold182TestBaseline, to); err == nil || called {
		t.Fatal("raw baseline authorized a combat route outside the sealed region")
	}
}
