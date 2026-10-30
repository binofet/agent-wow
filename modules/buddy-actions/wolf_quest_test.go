package buddyactions

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func setWolfQuest(f *fakeSession, complete bool) {
	f.world.SelfQuestLogKnown = true
	f.world.SelfQuestLog = make([]*worldv1.QuestLogEntry, 25)
	for i := range f.world.SelfQuestLog {
		f.world.SelfQuestLog[i] = &worldv1.QuestLogEntry{Slot: uint32(i)}
	}
	f.world.SelfQuestLog[0].QuestId = 33
	if complete {
		f.world.SelfQuestLog[0].Complete = true
		f.world.SelfQuestLog[0].State = 1
	}
}

func wolfQuestRig(t *testing.T, class, level uint32) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	base := class
	if base == 5 {
		base = 8
	}
	s, f, advance := practiceRig(t, base)
	s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: false})
	f.world.Self.ClassId, f.world.Self.Level = class, level
	if class == 5 {
		learnSpells(s, smite)
	}
	setWolfQuest(f, false)
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return s, f, advance
}

func TestPracticeCandidateWithin120YardsUsesVerifiedRouteInsteadOfStrandingRogue(t *testing.T) {
	for _, d := range []float32{60, 121} {
		s, f, advance := practiceRig(t, 4)
		f.world.NearbyCreatures[0].X = f.world.Self.X + d
		s.Tick(context.Background())
		if d == 121 {
			if state(t, s).Moving || state(t, s).PracticeTargetGuid != 0 || len(f.packets) != 0 {
				t.Fatal(state(t, s), f.packets)
			}
			continue
		}
		if !state(t, s).Moving || !state(t, s).NavAvailable || state(t, s).PracticeTargetGuid != enemyGUID {
			t.Fatal(state(t, s), f.packets)
		}
		for i := 0; i < 50 && countPackets(f, opcode.CMSGAttackswing) == 0; i++ {
			advance(200 * time.Millisecond)
			s.Tick(context.Background())
		}
		if countPackets(f, opcode.CMSGAttackswing) != 1 || lastCast(f) != sinisterStrike {
			t.Fatal(state(t, s), f.packets)
		}
	}
}

func TestOwnIncompleteQuest33AllowsSupportedClassesThroughLevelFive(t *testing.T) {
	for _, tc := range []struct{ class, level, spell uint32 }{{5, 3, smite}, {8, 2, fireball}, {4, 5, sinisterStrike}} {
		s, f, _ := wolfQuestRig(t, tc.class, tc.level)
		s.Tick(context.Background())
		if !state(t, s).Practicing || lastCast(f) != tc.spell || state(t, s).PracticePulls != 1 || state(t, s).HealAttempts != 0 {
			t.Fatal(state(t, s), f.packets)
		}
	}
}

func TestQuest33CompletionStopsNewPullsButFinishesProvenCurrentFight(t *testing.T) {
	s, f, advance := wolfQuestRig(t, 5, 3)
	s.Tick(context.Background())
	finishPracticeCast(s)
	f.world.Self.UnitFlags = 0x80000
	f.world.NearbyCreatures[0].AttackVictimKnown = true
	f.world.NearbyCreatures[0].AttackVictimGuid = 1
	setWolfQuest(f, true)
	advance(3 * time.Second)
	s.Tick(context.Background())
	if !state(t, s).Practicing || state(t, s).PracticePhase != "finishing_current_fight_to_goal" || state(t, s).PracticePulls != 1 || countPackets(f, opcode.CMSGCastSpell) != 2 {
		t.Fatal(state(t, s), f.packets)
	}
	finishPracticeCast(s)
	f.world.NearbyCreatures[0].Health = 0
	f.world.NearbyCreatures[0].Alive = false
	s.Tick(context.Background())
	if state(t, s).Practicing || state(t, s).PracticePhase != "quest_33_completion_observed" || state(t, s).PracticePulls != 1 {
		t.Fatal(state(t, s))
	}
}

func TestQuestPracticeRequiresOwnNonfailedKnownLogAndLevelLimit(t *testing.T) {
	for _, mode := range []string{"absent", "unknown", "failed", "duplicate", "complete", "level6"} {
		s, f, _ := wolfQuestRig(t, 5, 3)
		s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: false})
		switch mode {
		case "absent":
			f.world.SelfQuestLog[0].QuestId = 0
		case "unknown":
			f.world.SelfQuestLogKnown = false
		case "failed":
			f.world.SelfQuestLog[0].Failed = true
		case "duplicate":
			f.world.SelfQuestLog[1].Slot = 0
		case "complete":
			setWolfQuest(f, true)
		case "level6":
			f.world.Self.Level = 6
		}
		if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err == nil {
			t.Fatal("invalid quest practice accepted", mode)
		}
		if state(t, s).Practicing || len(f.packets) != 0 {
			t.Fatal(mode, state(t, s), f.packets)
		}
	}
}

func TestWolfQuestLootApproachOnlyIgnoresOwnerForOwnActive33AndCorpse299(t *testing.T) {
	for _, mode := range []string{"active", "complete", "absent", "unknown", "failed", "duplicate", "othercorpse", "combat"} {
		s, f, _, r := approachRig(t)
		f.world.Owner = nil
		f.world.LootableCorpses[0].Entry = 299
		setWolfQuest(f, mode == "complete")
		switch mode {
		case "absent":
			f.world.SelfQuestLog[0].QuestId = 0
		case "unknown":
			f.world.SelfQuestLogKnown = false
		case "failed":
			f.world.SelfQuestLog[0].Failed = true
		case "duplicate":
			f.world.SelfQuestLog[1].Slot = 0
		case "othercorpse":
			f.world.LootableCorpses[0].Entry = 69
		case "combat":
			f.world.Self.UnitFlags = 0x80000
		}
		got, err := s.ApproachLoot(context.Background(), r)
		if err != nil {
			t.Fatal(mode, err)
		}
		if mode == "active" || mode == "complete" {
			if !got.Moving || len(f.packets) != 1 {
				t.Fatal(mode, got, f.packets)
			}
		} else if got.Moving || len(f.packets) != 0 {
			t.Fatal(mode, got, f.packets)
		}
	}
}

func TestQuestTravelAllowsObservedEagan196WithSameMeshAndLeaseGates(t *testing.T) {
	s, f, _, r := travelRig(t)
	f.world.QuestGivers[0].Entry = 196
	r.NpcGuid = 0xf1300000c4000001
	f.world.QuestGivers[0].Guid = r.NpcGuid
	got, err := s.TravelQuest(context.Background(), r)
	if err != nil || got.Reason != "quest_travel_within_distance" || got.Moving || !got.NavAvailable {
		t.Fatal(got, err)
	}
}
