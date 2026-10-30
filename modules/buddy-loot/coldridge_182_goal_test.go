package buddyloot

import (
	"fmt"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

func cold182GoalFixture() (*worldv1.SnapshotResponse, *actionsv1.ClientPosition, *worldv1.CreatureState) {
	w, p := coldridgeLootGoalSnapshot()
	w.Self.Level, w.Self.PositionRevision = 4, 7
	w.SelfQuestLog[2].QuestId = 182
	w.SelfQuestLog[2].ObjectiveCounts = []uint32{1, 0, 0, 0}
	c := coldridgeLootGoalCorpse(706)
	c.Level, c.MaxHealth, c.FactionTemplate = 3, 71, 190
	return w, p, c
}

func TestColdridge182LootProfileKeepsTheTwoObservedTuplesPaired(t *testing.T) {
	for _, level := range []uint32{2, 3, 4, 5} {
		for _, maxHealth := range []uint32{55, 71, 86, 87} {
			t.Run(fmt.Sprintf("%d/%d", level, maxHealth), func(t *testing.T) {
				_, _, c := cold182GoalFixture()
				c.Level, c.MaxHealth = level, maxHealth
				want := level == 3 && maxHealth == 71 || level == 4 && maxHealth == 86
				if coldridge182CorpseProfile(c) != want {
					t.Fatal("accepted an unobserved level/max-health combination", c)
				}
			})
		}
	}
}

func TestColdridge182LootProfileRequiresExactEntryGUIDMapAndFaction(t *testing.T) {
	for name, change := range map[string]func(*worldv1.CreatureState){
		"entry705":      func(c *worldv1.CreatureState) { c.Entry = 705 },
		"entry708":      func(c *worldv1.CreatureState) { c.Entry = 708 },
		"encoded_entry": func(c *worldv1.CreatureState) { c.Guid = coldridgeLootGoalCorpse(708).Guid },
		"zero_low":      func(c *worldv1.CreatureState) { c.Guid &^= 0xffffff },
		"other_kind":    func(c *worldv1.CreatureState) { c.Guid = c.Guid&0x0000ffffffffffff | 0xf150000000000000 },
		"unknown_map":   func(c *worldv1.CreatureState) { c.MapKnown = false },
		"other_map":     func(c *worldv1.CreatureState) { c.MapId = 1 },
		"unknown_level": func(c *worldv1.CreatureState) { c.LevelKnown = false },
		"faction189":    func(c *worldv1.CreatureState) { c.FactionTemplate = 189 },
	} {
		t.Run(name, func(t *testing.T) {
			_, _, c := cold182GoalFixture()
			change(c)
			if coldridge182CorpseProfile(c) {
				t.Fatal("changed identity or observed profile was accepted", c)
			}
		})
	}
	if coldridge182CorpseProfile(nil) {
		t.Fatal("missing body authorized an observed profile")
	}
	// The structural profile is reusable after death. The common loot gate,
	// rather than this matcher, still proves presence/death/LOOTABLE/range.
	_, _, c := cold182GoalFixture()
	c.Present, c.AliveKnown, c.HealthKnown, c.PositionKnown = false, false, false, false
	c.DynamicFlags, c.Health = 0, 71
	if !coldridge182CorpseProfile(c) {
		t.Fatal("structural matching inferred a current corpse or victim requirement")
	}
}

func TestColdridge182LootQuestAlonePermitsOwnWarlockLevelFourThroughSix(t *testing.T) {
	for _, questID := range []uint32{179, 170, 183, 182} {
		for level := uint32(0); level <= 7; level++ {
			w, _, _ := cold182GoalFixture()
			w.Self.Level, w.SelfQuestLog[2].QuestId = level, questID
			_, ok := coldridgeLootQuest(w, 8, questID)
			minLevel, maxLevel := uint32(1), uint32(5)
			switch questID {
			case 170:
				minLevel = 2
			case 183:
				minLevel = 3
			case 182:
				minLevel, maxLevel = 4, 6
			}
			if ok != (level >= minLevel && level <= maxLevel) {
				t.Fatal("quest changed its own or another quest's level cap", questID, level, ok)
			}
		}
	}
	for name, change := range map[string]func(*worldv1.SnapshotResponse){
		"foreign_self":  func(w *worldv1.SnapshotResponse) { w.Self.Guid = 7 },
		"missing_self":  func(w *worldv1.SnapshotResponse) { w.Self = nil },
		"not_present":   func(w *worldv1.SnapshotResponse) { w.Self.Present = false },
		"unknown_class": func(w *worldv1.SnapshotResponse) { w.Self.ClassKnown = false },
		"other_class":   func(w *worldv1.SnapshotResponse) { w.Self.ClassId = 8 },
		"unknown_level": func(w *worldv1.SnapshotResponse) { w.Self.LevelKnown = false },
	} {
		t.Run(name, func(t *testing.T) {
			w, _, _ := cold182GoalFixture()
			change(w)
			if _, ok := coldridgeLootQuest(w, 8, 182); ok {
				t.Fatal("foreign, missing or unproven own character admitted182")
			}
		})
	}
	w, _, _ := cold182GoalFixture()
	if _, ok := coldridgeLootQuest(w, 0, 182); ok {
		t.Fatal("zero acting GUID admitted182")
	}
}

func TestColdridge182LootQuestRequiresTheWholeUnambiguousOwnLog(t *testing.T) {
	for name, change := range map[string]func(*worldv1.SnapshotResponse){
		"unknown_log": func(w *worldv1.SnapshotResponse) { w.SelfQuestLogKnown = false },
		"short_log":   func(w *worldv1.SnapshotResponse) { w.SelfQuestLog = w.SelfQuestLog[:24] },
		"long_log": func(w *worldv1.SnapshotResponse) {
			w.SelfQuestLog = append(w.SelfQuestLog, &worldv1.QuestLogEntry{Slot: 25})
		},
		"nil_slot":        func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24] = nil },
		"invalid_slot":    func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24].Slot = 25 },
		"duplicate_slot":  func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24].Slot = 2 },
		"duplicate_182":   func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24].QuestId = 182 },
		"duplicate_other": func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[23].QuestId, w.SelfQuestLog[24].QuestId = 233, 233 },
		"removed":         func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].QuestId = 0 },
		"failed":          func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].Failed = true },
		"failed_state":    func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].State = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			w, _, _ := cold182GoalFixture()
			change(w)
			if _, ok := coldridgeLootQuest(w, 8, 182); ok {
				t.Fatal("ambiguous, partial or inactive own182 log admitted")
			}
		})
	}
	w, _, _ := cold182GoalFixture()
	q := w.SelfQuestLog[2]
	cold170MoveQuest(w, q, 19)
	if got, ok := coldridgeLootQuest(w, 8, 182); !ok || got != q || got.Slot != 19 {
		t.Fatal("actual182 followed a cached slot or slice index", got, ok)
	}
}

func TestColdridge182LootQuestValidatesExactlyItsFourCounterShape(t *testing.T) {
	for count := uint32(0); count <= 10; count++ {
		w, _, _ := cold182GoalFixture()
		w.SelfQuestLog[2].ObjectiveCounts = []uint32{count, 0, 0, 0}
		if _, ok := coldridgeLootQuest(w, 8, 182); !ok {
			t.Fatal("observed182 counter within0..10 rejected", count)
		}
	}
	for _, counts := range [][]uint32{nil, {0}, {1, 0, 0}, {1, 0, 0, 0, 0}, {11, 0, 0, 0}, {65536, 0, 0, 0}, {2, 1, 0, 0}, {2, 0, 1, 0}, {2, 0, 0, 1}} {
		w, _, _ := cold182GoalFixture()
		w.SelfQuestLog[2].ObjectiveCounts = counts
		if _, ok := coldridgeLootQuest(w, 8, 182); ok {
			t.Fatal("unsupported182 counter shape admitted", counts)
		}
	}
}

func TestColdridge182LootCounterValidationDoesNotChangeLegacyAdmission(t *testing.T) {
	for _, legacy := range []uint32{179, 170, 183} {
		w, p, _ := cold182GoalFixture()
		w.SelfQuestLog[2].QuestId = legacy
		w.SelfQuestLog[19].QuestId = 182
		w.SelfQuestLog[19].ObjectiveCounts = []uint32{99, 1}
		if _, ok := coldridgeLootQuest(w, 8, legacy); !ok || !ownColdridgeQuest(w, 8) || !coldridgeQuestContext(w, p, 8) {
			t.Fatal("unselected malformed182 altered a legacy quest", legacy)
		}
		if _, ok := coldridgeLootQuest(w, 8, 182); ok {
			t.Fatal("another valid quest rescued selected malformed182", legacy)
		}
	}
	// Existing scan-wide170/183 validation continues to fail closed for182 too.
	for _, other := range []uint32{170, 183} {
		w, _, _ := cold182GoalFixture()
		w.SelfQuestLog[19].QuestId = other
		w.SelfQuestLog[19].ObjectiveCounts = []uint32{99, 0, 0, 0}
		if _, ok := coldridgeLootQuest(w, 8, 182); ok {
			t.Fatal("182 bypassed existing170/183 malformed-log rejection", other)
		}
	}
}

func TestColdridge182LootCompletedActiveQuestKeepsLastCorpseEligibilitySeparate(t *testing.T) {
	for _, completion := range []string{"counts", "flag", "state"} {
		w, _, c := cold182GoalFixture()
		switch completion {
		case "counts":
			w.SelfQuestLog[2].ObjectiveCounts[0] = 10
		case "flag":
			w.SelfQuestLog[2].Complete = true
		case "state":
			w.SelfQuestLog[2].State = 1
		}
		if _, ok := coldridgeLootQuest(w, 8, 182); !ok || !coldridge182CorpseProfile(c) {
			t.Fatal("credit arriving before the last corpse invalidated its own active quest", completion)
		}
	}
}

func TestColdridge182LootUnsupportedAreaCannotBorrowAnotherQuestAuthority(t *testing.T) {
	w, p, c := cold182GoalFixture()
	goal := coldridgeLootGoal{questID: 182, corpseGUID: c.Guid, positionRevision: 7, counts: [2]uint32{1, 0}}
	w.Self.X, w.Self.Y, w.Self.Z = -8900, 0, 100
	p.X, p.Y, p.Z = -8900, 0, 100
	c.X, c.Y, c.Z = -8900, 0, 100
	if !ownColdridgeQuest(w, 8) || !coldridge182CorpseProfile(c) {
		t.Fatal("valid pure182 facts are missing from the fixture")
	}
	// These Northshire coordinates are outside the Coldridge quest scopes.
	// Pure quest/profile facts cannot authorize a corpse in another area.
	if goal.containsPoint(p.X, p.Y, p.Z) || coldridgeQuestContext(w, p, 8) || goal.matchesCorpse(c) {
		t.Fatal("182 acquired spatial authority from pure quest/profile facts")
	}
	if _, ok := selectColdridgeLootGoal(w, 8, c); ok {
		t.Fatal("182 corpse selected in an unsupported area")
	}
	w.SelfQuestLog[19].QuestId = 179
	if coldridgeQuestContext(w, p, 8) {
		t.Fatal("accepted179 rescued an out-of-area182 observation")
	}
	if _, ok := selectColdridgeLootGoal(w, 8, c); ok || goal.context(w, p, 8, c.Guid) {
		t.Fatal("accepted179 rescued a bound182 corpse")
	}
	w.Self.X, w.Self.Y, w.Self.Z = -6240, 331, 383
	p.X, p.Y, p.Z = -6240, 331, 383
	if !coldridgeQuestContext(w, p, 8) {
		t.Fatal("legacy179 no longer admits its own unchanged region")
	}
	oldCorpse := coldridgeLootGoalCorpse(705)
	if old, ok := selectColdridgeLootGoal(w, 8, oldCorpse); !ok || old.questID != 179 {
		t.Fatal("182 changed the legacy quest/corpse pairing", old, ok)
	}
	w.SelfQuestLog[19].QuestId = 0
	if _, ok := selectColdridgeLootGoal(w, 8, oldCorpse); ok {
		t.Fatal("actual182 rescued a corpse belonging only to179")
	}
	w.SelfQuestLog[2].ObjectiveCounts[0] = 3
	w.Self.X, w.Self.Y, w.Self.Z = -8900, 0, 100
	p.X, p.Y, p.Z = -8900, 0, 100
	before := goal
	if goal.context(w, p, 8, c.Guid) || goal != before || goal.context(w, p, 8, c.Guid+1) {
		t.Fatal("rejected spatial/identity proof changed the retained operation", goal)
	}
	for _, quest := range []uint32{179, 170, 183} {
		old := coldridgeLootGoal{questID: quest}
		if !old.containsPoint(-6240, 331, 383) || old.containsPoint(-6240, 700.001, 383) {
			t.Fatal("new182 policy expanded a legacy sealed region", quest)
		}
	}
}
