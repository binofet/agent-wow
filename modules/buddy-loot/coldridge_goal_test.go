package buddyloot

import (
	"math"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

func coldridgeLootGoalSnapshot() (*worldv1.SnapshotResponse, *actionsv1.ClientPosition) {
	w := &worldv1.SnapshotResponse{
		Self:              &worldv1.PlayerState{Guid: 8, Present: true, MapKnown: true, PositionKnown: true, X: -6240, Y: 331, Z: 383, AliveKnown: true, Alive: true, HealthKnown: true, Health: 68, MaxHealth: 68, UnitFlagsKnown: true, MovementFlagsKnown: true, SpeedsKnown: true, RunSpeed: 7, ClassKnown: true, ClassId: 9, LevelKnown: true, Level: 2},
		SelfQuestLogKnown: true,
	}
	for i := range 25 {
		w.SelfQuestLog = append(w.SelfQuestLog, &worldv1.QuestLogEntry{Slot: uint32(i), ObjectiveCounts: make([]uint32, 4)})
	}
	w.SelfQuestLog[2].QuestId = 170
	return w, &actionsv1.ClientPosition{Known: true, MapKnown: true, X: -6240, Y: 331, Z: 383}
}

func coldridgeLootGoalCorpse(entry uint32) *worldv1.CreatureState {
	return &worldv1.CreatureState{Guid: 0xf130000000000001 | uint64(entry)<<24, Entry: entry, Present: true, MapKnown: true, PositionKnown: true, X: -6238, Y: 331, Z: 383, LevelKnown: true, Level: 2, HealthKnown: true, MaxHealth: 55, AliveKnown: true, DynamicFlags: 1, FactionTemplate: 36}
}

func TestColdridgeLootGoalSelectsOnlyTheActualQuestCorpsePair(t *testing.T) {
	for _, questID := range []uint32{179, 170, 182, 183} {
		for _, entry := range []uint32{705, 707, 724, 706} {
			w, p := coldridgeLootGoalSnapshot()
			w.SelfQuestLog[2].QuestId = questID
			c := coldridgeLootGoalCorpse(entry)
			g, ok := selectColdridgeLootGoal(w, 8, c)
			want := questID == 179 && entry == 705 || questID == 170 && (entry == 707 || entry == 724)
			if ok != want || ok && (g.questID != questID || g.corpseGUID != c.Guid || !g.matchesCorpse(c) || !g.context(w, p, 8, c.Guid)) {
				t.Fatal("cross-authorized a quest/corpse pair", questID, entry, g, ok)
			}
		}
	}
	w, _ := coldridgeLootGoalSnapshot()
	w.SelfQuestLog[19].QuestId = 179
	for _, entry := range []uint32{705, 707, 724} {
		g, ok := selectColdridgeLootGoal(w, 8, coldridgeLootGoalCorpse(entry))
		wantQuest := uint32(170)
		if entry == 705 {
			wantQuest = 179
		}
		if !ok || g.questID != wantQuest {
			t.Fatal("another active quest replaced the exact corpse mapping", entry, g, ok)
		}
	}
}

func TestColdridgeLootGoalRescansReorderedLogAndIgnoresOldSlotDecoy(t *testing.T) {
	w, p := coldridgeLootGoalSnapshot()
	w.SelfQuestLog[2].ObjectiveCounts = []uint32{2, 3, 0, 0}
	c := coldridgeLootGoalCorpse(724)
	g, _ := selectColdridgeLootGoal(w, 8, c)
	w.SelfQuestLog[2].QuestId = 233
	w.SelfQuestLog[2].ObjectiveCounts = []uint32{0, 0, 0, 0}
	w.SelfQuestLog[20].QuestId = 170
	w.SelfQuestLog[20].ObjectiveCounts = []uint32{3, 4, 0, 0}
	for i, j := 0, len(w.SelfQuestLog)-1; i < j; i, j = i+1, j-1 {
		w.SelfQuestLog[i], w.SelfQuestLog[j] = w.SelfQuestLog[j], w.SelfQuestLog[i]
	}
	if !g.context(w, p, 8, c.Guid) || g.counts != [2]uint32{3, 4} || g.questID != 170 {
		t.Fatal("goal followed its previous slot or array position", g)
	}
	// Cache eviction does not invent another corpse or invalidate an already
	// bound transaction; context uses the exact pending GUID rather than discovery.
	w.LootableCorpses = nil
	if !g.context(w, p, 8, c.Guid) || g.context(w, p, 8, c.Guid+1) || g.context(w, p, 8, 0) {
		t.Fatal("pending corpse identity was lost", g)
	}
}

func TestColdridgeLootGoalContextRejectsInvalidPinnedIdentity(t *testing.T) {
	w, p := coldridgeLootGoalSnapshot()
	w.SelfQuestLog[19].QuestId = 179
	for _, g := range []coldridgeLootGoal{
		{questID: 179, corpseGUID: coldridgeLootGoalCorpse(724).Guid},
		{questID: 170, corpseGUID: coldridgeLootGoalCorpse(705).Guid},
		{questID: 170, corpseGUID: coldridgeLootGoalCorpse(724).Guid &^ 0xffff000000000000},
		{questID: 170, corpseGUID: coldridgeLootGoalCorpse(724).Guid &^ 0xffffff},
		{questID: 182, corpseGUID: coldridgeLootGoalCorpse(724).Guid},
		{questID: 170},
	} {
		if g.context(w, p, 8, g.corpseGUID) {
			t.Fatal("invalid bound quest/corpse identity authorized context", g)
		}
	}
}

func TestColdridgeLootGoalPinsAuthoritativePositionRevisionAcrossApproach(t *testing.T) {
	w, p := coldridgeLootGoalSnapshot()
	w.Self.PositionRevision = 7
	w.SelfQuestLog[2].ObjectiveCounts = []uint32{2, 3, 0, 0}
	c := coldridgeLootGoalCorpse(724)
	g, ok := selectColdridgeLootGoal(w, 8, c)
	if !ok || g.positionRevision != 7 {
		t.Fatal("selection did not bind its authoritative position base", g, ok)
	}
	// Ordinary approach changes the client estimate and its local revision, while
	// retaining the authoritative base observed when this operation was selected.
	p.X, p.Estimated, p.Revision = -6239, true, 99
	if !g.context(w, p, 8, c.Guid) {
		t.Fatal("ordinary estimated approach invalidated its unchanged base", g)
	}
	w.Self.PositionRevision = 8
	w.Self.X = -6239 // Same map and still inside Coldridge.
	w.SelfQuestLog[2].ObjectiveCounts = []uint32{3, 4, 0, 0}
	if g.context(w, p, 8, c.Guid) || g.counts != [2]uint32{2, 3} || g.positionRevision != 7 {
		t.Fatal("authoritative correction rebased an old operation or changed highwaters", g)
	}
	// A separate operation can bind the newly observed base; the old one cannot.
	fresh, ok := selectColdridgeLootGoal(w, 8, c)
	if !ok || fresh.positionRevision != 8 || !fresh.context(w, p, 8, c.Guid) || fresh.counts != [2]uint32{3, 4} {
		t.Fatal("fresh selection could not use the corrected authoritative base", fresh, ok)
	}
}

func TestColdridgeLootGoalAnotherQuestCannotRescueRemovedOrFailedBoundQuest(t *testing.T) {
	for _, bound := range []uint32{179, 170} {
		for _, change := range []string{"removed", "failed", "failed_state"} {
			w, p := coldridgeLootGoalSnapshot()
			w.SelfQuestLog[2].QuestId = bound
			entry, otherQuest := uint32(724), uint32(179)
			if bound == 179 {
				entry, otherQuest = 705, 170
			}
			c := coldridgeLootGoalCorpse(entry)
			g, _ := selectColdridgeLootGoal(w, 8, c)
			w.SelfQuestLog[19].QuestId = otherQuest
			switch change {
			case "removed":
				w.SelfQuestLog[2].QuestId = 0
			case "failed":
				w.SelfQuestLog[2].Failed = true
			case "failed_state":
				w.SelfQuestLog[2].State = 2
			}
			if !ownColdridgeQuest(w, 8) || !coldridgeQuestContext(w, p, 8) {
				t.Fatal("read-only preflight lost the other actual quest")
			}
			if g.context(w, p, 8, c.Guid) || g.questID != bound || g.corpseGUID != c.Guid {
				t.Fatal("read-only OR preflight rescued a different bound quest", bound, change, g)
			}
		}
	}
}

func TestColdridgeLootGoalRejectsMalformedOrUnsafeObservationWithoutChangingHighwater(t *testing.T) {
	cases := map[string]func(*worldv1.SnapshotResponse, *actionsv1.ClientPosition){
		"foreign_self":  func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.Self.Guid = 7 },
		"missing_self":  func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.Self = nil },
		"unknown_class": func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.Self.ClassKnown = false },
		"other_class":   func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.Self.ClassId = 8 },
		"unknown_level": func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.Self.LevelKnown = false },
		"level_one":     func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.Self.Level = 1 },
		"level_six":     func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.Self.Level = 6 },
		"unknown_log":   func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.SelfQuestLogKnown = false },
		"short_log":     func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.SelfQuestLog = w.SelfQuestLog[:24] },
		"long_log": func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) {
			w.SelfQuestLog = append(w.SelfQuestLog, &worldv1.QuestLogEntry{Slot: 25})
		},
		"nil_slot":        func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.SelfQuestLog[24] = nil },
		"invalid_slot":    func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.SelfQuestLog[24].Slot = 25 },
		"duplicate_slot":  func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.SelfQuestLog[24].Slot = 2 },
		"duplicate_quest": func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.SelfQuestLog[24].QuestId = 170 },
		"short_counts": func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) {
			w.SelfQuestLog[2].ObjectiveCounts = []uint32{2, 3, 0}
		},
		"long_counts": func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) {
			w.SelfQuestLog[2].ObjectiveCounts = []uint32{2, 3, 0, 0, 0}
		},
		"overflow": func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) {
			w.SelfQuestLog[2].ObjectiveCounts[0] = 65536
		},
		"above_requirement": func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) {
			w.SelfQuestLog[2].ObjectiveCounts[1] = 7
		},
		"unsupported_counter2": func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) {
			w.SelfQuestLog[2].ObjectiveCounts[2] = 1
		},
		"unsupported_counter3": func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) {
			w.SelfQuestLog[2].ObjectiveCounts[3] = 1
		},
		"combat":               func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.Self.UnitFlags = 0x80000 },
		"own_region":           func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.Self.X = -8900 },
		"own_position_unknown": func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.Self.PositionKnown = false },
		"own_dead":             func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.Self.Alive = false },
		"own_unusual_speed":    func(w *worldv1.SnapshotResponse, _ *actionsv1.ClientPosition) { w.Self.RunSpeed = 8 },
		"estimated_region":     func(_ *worldv1.SnapshotResponse, p *actionsv1.ClientPosition) { p.X = -8900 },
		"estimated_unknown":    func(_ *worldv1.SnapshotResponse, p *actionsv1.ClientPosition) { p.Known = false },
		"estimated_map":        func(_ *worldv1.SnapshotResponse, p *actionsv1.ClientPosition) { p.MapId = 1 },
		"estimated_nonfinite":  func(_ *worldv1.SnapshotResponse, p *actionsv1.ClientPosition) { p.X = float32(math.NaN()) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w, p := coldridgeLootGoalSnapshot()
			w.SelfQuestLog[2].ObjectiveCounts = []uint32{2, 3, 0, 0}
			c := coldridgeLootGoalCorpse(724)
			g, _ := selectColdridgeLootGoal(w, 8, c)
			mutate(w, p)
			if g.context(w, p, 8, c.Guid) || g.counts != [2]uint32{2, 3} {
				t.Fatal("invalid context accepted or changed counter highwaters", g)
			}
		})
	}
}

func TestColdridgeLootGoalProgressIsAtomicAndCannotRegress(t *testing.T) {
	w, p := coldridgeLootGoalSnapshot()
	w.SelfQuestLog[2].ObjectiveCounts = []uint32{2, 3, 0, 0}
	c := coldridgeLootGoalCorpse(707)
	g, _ := selectColdridgeLootGoal(w, 8, c)
	w.SelfQuestLog[2].ObjectiveCounts = []uint32{3, 4, 0, 0}
	if !g.context(w, p, 8, c.Guid) || g.counts != [2]uint32{3, 4} {
		t.Fatal("monotonic own quest progress not observed", g)
	}
	for _, counts := range [][]uint32{{2, 5, 0, 0}, {4, 3, 0, 0}, {4, 5, 1, 0}} {
		w.SelfQuestLog[2].ObjectiveCounts = counts
		if g.context(w, p, 8, c.Guid) || g.counts != [2]uint32{3, 4} {
			t.Fatal("regressive or malformed snapshot partially committed highwaters", g)
		}
	}
}

func TestColdridgeLootGoalCompletionStillAllowsTheExactLastCorpse(t *testing.T) {
	for _, completion := range []string{"counts", "flag", "state"} {
		w, p := coldridgeLootGoalSnapshot()
		c := coldridgeLootGoalCorpse(724)
		g, _ := selectColdridgeLootGoal(w, 8, c)
		switch completion {
		case "counts":
			w.SelfQuestLog[2].ObjectiveCounts = []uint32{6, 6, 0, 0}
		case "flag":
			w.SelfQuestLog[2].Complete = true
		case "state":
			w.SelfQuestLog[2].State = 1
		}
		if !g.context(w, p, 8, c.Guid) || !g.matchesCorpse(c) {
			t.Fatal("last corpse rejected because quest credit arrived first", completion, g)
		}
		fresh, ok := selectColdridgeLootGoal(w, 8, c)
		if !ok || !fresh.context(w, p, 8, c.Guid) {
			t.Fatal("actual completed but active quest rejected last corpse selection", completion)
		}
	}
}

func TestColdridgeLootGoalMatchesExactGUIDAndVerifiedCorpseProfile(t *testing.T) {
	cases := map[string]func(*worldv1.CreatureState){
		"other_guid":       func(c *worldv1.CreatureState) { c.Guid++ },
		"encoded_entry":    func(c *worldv1.CreatureState) { c.Guid = coldridgeLootGoalCorpse(707).Guid },
		"wrong_entry":      func(c *worldv1.CreatureState) { c.Entry = 705 },
		"unknown_map":      func(c *worldv1.CreatureState) { c.MapKnown = false },
		"other_map":        func(c *worldv1.CreatureState) { c.MapId = 1 },
		"unknown_position": func(c *worldv1.CreatureState) { c.PositionKnown = false },
		"region":           func(c *worldv1.CreatureState) { c.Y = 701 },
		"nonfinite":        func(c *worldv1.CreatureState) { c.Z = float32(math.NaN()) },
		"active_trajectory": func(c *worldv1.CreatureState) {
			c.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, AgeMs: 500}
		},
		"unknown_level":   func(c *worldv1.CreatureState) { c.LevelKnown = false },
		"other_level":     func(c *worldv1.CreatureState) { c.Level = 3 },
		"other_maxhealth": func(c *worldv1.CreatureState) { c.MaxHealth = 56 },
		"other_faction":   func(c *worldv1.CreatureState) { c.FactionTemplate = 35 },
	}
	w, p := coldridgeLootGoalSnapshot()
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := coldridgeLootGoalCorpse(724)
			g, _ := selectColdridgeLootGoal(w, 8, c)
			mutate(c)
			if g.matchesCorpse(c) {
				t.Fatal("changed corpse matched exact bound profile", c)
			}
		})
	}
	for _, mutate := range []func(*worldv1.CreatureState){
		func(c *worldv1.CreatureState) { c.Guid &^= 0xffff000000000000 },
		func(c *worldv1.CreatureState) { c.Guid &^= 0xffffff },
	} {
		c := coldridgeLootGoalCorpse(724)
		mutate(c)
		if _, ok := selectColdridgeLootGoal(w, 8, c); ok {
			t.Fatal("invalid type or zero low GUID selected", c)
		}
	}
	// Death clears victim tracking. Receiving-client LOOTABLE, not an invented
	// own killer or stale attack-victim field, authorizes the common loot gate.
	c := coldridgeLootGoalCorpse(707)
	g, ok := selectColdridgeLootGoal(w, 8, c)
	if !ok || c.AttackVictimKnown || c.AttackVictimGuid != 0 || !g.matchesCorpse(c) || !eligibleWithin(w.Self, p, c, 12, 1.5) {
		t.Fatal("valid dead receiving-client corpse required invented victim proof")
	}
	for _, mode := range []string{"living", "not_lootable", "absent", "far"} {
		c := coldridgeLootGoalCorpse(707)
		switch mode {
		case "living":
			c.Alive, c.Health = true, 55
		case "not_lootable":
			c.DynamicFlags = 0
		case "absent":
			c.Present = false
		case "far":
			c.X += 20
		}
		if !g.matchesCorpse(c) || eligibleWithin(w.Self, p, c, 12, 1.5) {
			t.Fatal("structural match replaced the common actual loot gate", mode)
		}
	}
}

func TestColdridgeLootGoal179PreservesLegacyCompletedQuestAndCorpse(t *testing.T) {
	w, p := coldridgeLootGoalSnapshot()
	w.Self.Level = 1
	w.SelfQuestLog[2].QuestId = 179
	w.SelfQuestLog[2].ObjectiveCounts = nil
	w.SelfQuestLog[2].Complete = true
	c := coldridgeLootGoalCorpse(705)
	c.LevelKnown, c.Level, c.MaxHealth, c.FactionTemplate = false, 0, 0, 0
	g, ok := selectColdridgeLootGoal(w, 8, c)
	if !ok || !g.matchesCorpse(c) || !g.context(w, p, 8, c.Guid) || !ownColdridgeQuest(w, 8) {
		t.Fatal("legacy179/705 scope or completed-last-corpse semantics changed", g, ok)
	}
}
