package buddyactions

import (
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
)

func coldridgeGoalSnapshot() *worldv1.SnapshotResponse {
	w := &worldv1.SnapshotResponse{
		Self:              &worldv1.PlayerState{Guid: 8, Present: true, MapKnown: true, ClassKnown: true, ClassId: 9, LevelKnown: true, Level: 2},
		SelfQuestLogKnown: true,
	}
	for i := range 25 {
		w.SelfQuestLog = append(w.SelfQuestLog, &worldv1.QuestLogEntry{Slot: uint32(i), ObjectiveCounts: make([]uint32, 4)})
	}
	w.SelfQuestLog[2].QuestId = 170
	return w
}

func coldridgeGoalCreature(entry uint32) *worldv1.CreatureState {
	return &worldv1.CreatureState{Guid: 0xf130000000000001 | uint64(entry)<<24, Entry: entry, Present: true, MapKnown: true, LevelKnown: true, Level: 2, HealthKnown: true, Health: 55, MaxHealth: 55, AliveKnown: true, Alive: true, FactionTemplate: 36, UnitFlags_2: 2048}
}

func TestColdridgeGoalSelectionPrefers179AndNeverChangesBoundQuest(t *testing.T) {
	w := coldridgeGoalSnapshot()
	w.SelfQuestLog[20].QuestId = 179
	g, ok := selectColdridgePracticeGoal(w, 8)
	if !ok || g.questID != 179 || !g.targetNeeded(705) || g.targetNeeded(707) || g.supportsEntry(724) {
		t.Fatal("selection did not preserve the179 priority", g, ok)
	}
	w.SelfQuestLog[20].Complete = true
	if active, needed := g.observe(w, 8); !active || needed || g.questID != 179 || g.targetNeeded(705) || !g.supportsEntry(705) {
		t.Fatal("completed bound179 switched to another incomplete quest", g, active, needed)
	}
	next, ok := selectColdridgePracticeGoal(w, 8)
	if !ok || next.questID != 170 {
		t.Fatal("a separate selection did not admit the observed next quest", next, ok)
	}
	w.SelfQuestLog[20].Complete = false
	if active, needed := next.observe(w, 8); !active || !needed || next.questID != 170 {
		t.Fatal("bound170 switched when179 appeared", next, active, needed)
	}
	w.SelfQuestLog[2].QuestId = 0
	if active, needed := next.observe(w, 8); active || needed || next.questID != 170 || next.targetNeeded(724) {
		t.Fatal("missing bound quest adopted179", next, active, needed)
	}
}

func TestColdridgeGoalFindsQuestByIDAfterSlotAndSliceReorder(t *testing.T) {
	w := coldridgeGoalSnapshot()
	w.SelfQuestLog[2].ObjectiveCounts[0] = 2
	g, ok := selectColdridgePracticeGoal(w, 8)
	if !ok {
		t.Fatal("initial goal unavailable")
	}
	w.SelfQuestLog[2].QuestId = 233 // Decoy at the old slot.
	w.SelfQuestLog[2].ObjectiveCounts = []uint32{0, 0, 0, 0}
	w.SelfQuestLog[19].QuestId = 170
	w.SelfQuestLog[19].ObjectiveCounts = []uint32{3, 1, 0, 0}
	for i, j := 0, len(w.SelfQuestLog)-1; i < j; i, j = i+1, j-1 {
		w.SelfQuestLog[i], w.SelfQuestLog[j] = w.SelfQuestLog[j], w.SelfQuestLog[i]
	}
	if active, needed := g.observe(w, 8); !active || !needed || g.counts != [2]uint32{3, 1} {
		t.Fatal("goal followed its previous slot or slice index", g, active, needed)
	}
}

func TestColdridgeGoalDoesNotTreatOtherAcceptedQuestsAsCombatGoals(t *testing.T) {
	w := coldridgeGoalSnapshot()
	w.SelfQuestLog[2].QuestId = 233
	w.SelfQuestLog[3].QuestId = 3115
	if g, ok := selectColdridgePracticeGoal(w, 8); ok || g.questID != 0 {
		t.Fatal("unmapped accepted quests authorized combat", g, ok)
	}
	w.SelfQuestLog[2].QuestId = 170
	for _, selfGUID := range []uint64{0, 7, 0xf130000000000008} {
		if g, ok := selectColdridgePracticeGoal(w, selfGUID); ok || g.questID != 0 {
			t.Fatal("invalid runtime player identity authorized a goal", selfGUID, g, ok)
		}
	}
}

func TestColdridgeGoalRejectsUnknownForeignAndMalformedOwnLog(t *testing.T) {
	cases := map[string]func(*worldv1.SnapshotResponse){
		"foreign_self":  func(w *worldv1.SnapshotResponse) { w.Self.Guid = 7 },
		"missing_self":  func(w *worldv1.SnapshotResponse) { w.Self = nil },
		"absent_self":   func(w *worldv1.SnapshotResponse) { w.Self.Present = false },
		"unknown_class": func(w *worldv1.SnapshotResponse) { w.Self.ClassKnown = false },
		"other_class":   func(w *worldv1.SnapshotResponse) { w.Self.ClassId = 8 },
		"unknown_level": func(w *worldv1.SnapshotResponse) { w.Self.LevelKnown = false },
		"level_zero":    func(w *worldv1.SnapshotResponse) { w.Self.Level = 0 },
		"level_six":     func(w *worldv1.SnapshotResponse) { w.Self.Level = 6 },
		"unknown_log":   func(w *worldv1.SnapshotResponse) { w.SelfQuestLogKnown = false },
		"short_log":     func(w *worldv1.SnapshotResponse) { w.SelfQuestLog = w.SelfQuestLog[:24] },
		"long_log": func(w *worldv1.SnapshotResponse) {
			w.SelfQuestLog = append(w.SelfQuestLog, &worldv1.QuestLogEntry{Slot: 25})
		},
		"nil_slot":             func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24] = nil },
		"invalid_slot":         func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24].Slot = 25 },
		"duplicate_slot":       func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24].Slot = 2 },
		"duplicate_quest":      func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[24].QuestId = 170 },
		"failed":               func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].Failed = true },
		"failed_state":         func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].State = 2 },
		"short_counts":         func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].ObjectiveCounts = []uint32{0, 0, 0} },
		"long_counts":          func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].ObjectiveCounts = []uint32{0, 0, 0, 0, 0} },
		"uint16_overflow":      func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].ObjectiveCounts[0] = 65536 },
		"above_required":       func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].ObjectiveCounts[1] = 7 },
		"unsupported_counter2": func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].ObjectiveCounts[2] = 1 },
		"unsupported_counter3": func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[2].ObjectiveCounts[3] = 1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w := coldridgeGoalSnapshot()
			g, _ := selectColdridgePracticeGoal(w, 8)
			mutate(w)
			if active, needed := g.observe(w, 8); active || needed || g.targetNeeded(707) || g.targetNeeded(724) {
				t.Fatal("invalid observation authorized the bound goal", g, active, needed)
			}
			if _, ok := selectColdridgePracticeGoal(w, 8); ok {
				t.Fatal("invalid observation authorized a fresh goal")
			}
		})
	}
}

func TestColdridgeGoalStopsEachObjectiveAndDoesNotInventCompletion(t *testing.T) {
	for _, tc := range []struct {
		counts           []uint32
		need707, need724 bool
	}{{[]uint32{6, 5, 0, 0}, false, true}, {[]uint32{5, 6, 0, 0}, true, false}} {
		w := coldridgeGoalSnapshot()
		w.SelfQuestLog[2].ObjectiveCounts = tc.counts
		g, ok := selectColdridgePracticeGoal(w, 8)
		if !ok || g.targetNeeded(707) != tc.need707 || g.targetNeeded(724) != tc.need724 {
			t.Fatal("objective requirements were exchanged or merged", g, tc)
		}
	}
	w := coldridgeGoalSnapshot()
	g, _ := selectColdridgePracticeGoal(w, 8)
	w.SelfQuestLog[2].ObjectiveCounts = []uint32{6, 5, 0, 0}
	if active, needed := g.observe(w, 8); !active || !needed || g.targetNeeded(707) || !g.targetNeeded(724) {
		t.Fatal("completed first objective authorized another707", g, active, needed)
	}
	// Structural eligibility and corpse entry matching survive objective completion
	// so the controller can finish an already-owned fight and identify its corpse.
	if !g.eligible(coldridgeGoalCreature(707), w.Self) || !g.supportsEntry(707) {
		t.Fatal("objective completion erased the structural target profile")
	}
	w.SelfQuestLog[2].ObjectiveCounts[1] = 6
	if active, needed := g.observe(w, 8); !active || needed || g.targetNeeded(707) || g.targetNeeded(724) || w.SelfQuestLog[2].Complete {
		t.Fatal("both exact objective counts did not stop new pulls before a lagging flag", g, active, needed)
	}
	if _, ok := selectColdridgePracticeGoal(w, 8); ok {
		t.Fatal("completed objective counts selected a new run")
	}
	for _, stateComplete := range []bool{false, true} {
		w = coldridgeGoalSnapshot()
		if stateComplete {
			w.SelfQuestLog[2].State = 1
		} else {
			w.SelfQuestLog[2].Complete = true
		}
		g = coldridgePracticeGoal{questID: 170}
		if active, needed := g.observe(w, 8); !active || needed || g.targetNeeded(724) {
			t.Fatal("authoritative completion flag permitted new pulls", g, active, needed)
		}
	}
}

func TestColdridgeGoalProgressCannotRegressOrPartiallyCommit(t *testing.T) {
	w := coldridgeGoalSnapshot()
	w.SelfQuestLog[2].ObjectiveCounts = []uint32{3, 4, 0, 0}
	g, _ := selectColdridgePracticeGoal(w, 8)
	for _, counts := range [][]uint32{{2, 5, 0, 0}, {4, 3, 0, 0}, {4, 5, 1, 0}} {
		w.SelfQuestLog[2].ObjectiveCounts = counts
		if active, needed := g.observe(w, 8); active || needed || g.counts != [2]uint32{3, 4} || g.targetNeeded(724) {
			t.Fatal("rejected observation lowered or partly advanced the highwater", g, active, needed)
		}
	}
	w.SelfQuestLog[2].ObjectiveCounts = []uint32{4, 5, 0, 0}
	if active, needed := g.observe(w, 8); !active || !needed || g.counts != [2]uint32{4, 5} {
		t.Fatal("valid monotonic observation was lost", g, active, needed)
	}
}

func TestColdridgeGoalTargetEligibilityRequiresBothVerifiedProfiles(t *testing.T) {
	w := coldridgeGoalSnapshot()
	g, _ := selectColdridgePracticeGoal(w, 8)
	for _, entry := range []uint32{707, 724} {
		if !g.eligible(coldridgeGoalCreature(entry), w.Self) || !g.supportsEntry(entry) || !g.targetNeeded(entry) {
			t.Fatal("verified level2/maxhealth55 profile was not admitted", entry)
		}
	}
	cases := map[string]func(*worldv1.CreatureState, *worldv1.PlayerState){
		"entry706": func(c *worldv1.CreatureState, _ *worldv1.PlayerState) {
			c.Entry = 706
			c.Guid = coldridgeGoalCreature(706).Guid
		},
		"old_wolf": func(c *worldv1.CreatureState, _ *worldv1.PlayerState) {
			c.Entry = 705
			c.Guid = coldridgeGoalCreature(705).Guid
		},
		"guid_entry":           func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.Guid = coldridgeGoalCreature(707).Guid },
		"guid_type":            func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.Guid &^= 0xffff000000000000 },
		"guid_zero_counter":    func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.Guid &^= 0xffffff },
		"absent":               func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.Present = false },
		"unknown_map":          func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.MapKnown = false },
		"other_map":            func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.MapId = 1 },
		"unknown_level":        func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.LevelKnown = false },
		"other_level":          func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.Level = 3 },
		"other_health_profile": func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.MaxHealth = 56 },
		"unknown_health":       func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.HealthKnown = false },
		"zero_health":          func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.Health = 0 },
		"incoherent_health":    func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.Health = 56 },
		"unknown_alive":        func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.AliveKnown = false },
		"dead":                 func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.Alive = false },
		"faction":              func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.FactionTemplate = 35 },
		"npc":                  func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.NpcFlags = 1 },
		"unattackable":         func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.UnitFlags = 2 },
		"feign":                func(c *worldv1.CreatureState, _ *worldv1.PlayerState) { c.UnitFlags_2 |= 1 },
		"self_level_unknown":   func(_ *worldv1.CreatureState, p *worldv1.PlayerState) { p.LevelKnown = false },
		"self_too_low":         func(_ *worldv1.CreatureState, p *worldv1.PlayerState) { p.Level = 1 },
		"self_map_unknown":     func(_ *worldv1.CreatureState, p *worldv1.PlayerState) { p.MapKnown = false },
		"self_other_map":       func(_ *worldv1.CreatureState, p *worldv1.PlayerState) { p.MapId = 1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := coldridgeGoalCreature(724)
			self := proto.Clone(w.Self).(*worldv1.PlayerState)
			mutate(c, self)
			if g.eligible(c, self) {
				t.Fatal("unverified or unsafe target profile was admitted", c, self)
			}
		})
	}
	if g.eligible(nil, w.Self) || g.eligible(coldridgeGoalCreature(724), nil) || g.supportsEntry(706) || g.targetNeeded(706) {
		t.Fatal("missing or unsupported target admitted")
	}
	// A damaged own combat target remains structurally eligible after its last
	// objective credit; the controller separately binds victim and fight ownership.
	c := coldridgeGoalCreature(724)
	c.Health, c.UnitFlags, c.AttackVictimKnown, c.AttackVictimGuid = 1, 0x80000, true, 8
	w.SelfQuestLog[2].ObjectiveCounts = []uint32{6, 6, 0, 0}
	g.observe(w, 8)
	if !g.eligible(c, w.Self) || g.targetNeeded(724) {
		t.Fatal("structural finishing eligibility was tied to objective-needed state")
	}
}

func TestColdridgeGoal179RetainsLegacyEligibilityAndCompletionSemantics(t *testing.T) {
	w := coldridgeGoalSnapshot()
	w.SelfQuestLog[2].QuestId = 179
	w.SelfQuestLog[2].ObjectiveCounts = nil // Legacy179 does not depend on counter layout.
	g, ok := selectColdridgePracticeGoal(w, 8)
	c := coldridgeGoalCreature(705)
	c.Level, c.MaxHealth, c.Health = 1, 42, 42
	if !ok || !g.eligible(c, w.Self) || !g.targetNeeded(705) || g.supportsEntry(707) {
		t.Fatal("legacy179/705 changed", g, ok)
	}
	for _, stateComplete := range []bool{false, true} {
		w.SelfQuestLog[2].Complete = !stateComplete
		w.SelfQuestLog[2].State = 0
		if stateComplete {
			w.SelfQuestLog[2].State = 1
		}
		if active, needed := g.observe(w, 8); !active || needed || !g.eligible(c, w.Self) || g.targetNeeded(705) {
			t.Fatal("legacy completion stopped being active-but-not-needed", g, active, needed)
		}
	}
}
