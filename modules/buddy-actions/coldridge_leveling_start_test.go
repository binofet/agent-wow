package buddyactions

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func coldLevelingStartSetup(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := cold182Setup(t, 0, 4, 3)
	for _, q := range f.world.SelfQuestLog {
		q.QuestId = 0
	}
	f.world.SelfQuestLog[3].QuestId = 218
	return s, f, advance
}

func coldLevelingStart(t *testing.T, s *Service, requestID string) *actionsv1.State {
	t.Helper()
	got, err := s.StartColdridgeLeveling(context.Background(), &actionsv1.ColdridgeLevelingRequest{RequestId: requestID})
	if err != nil {
		t.Fatal("local leveling fixture was rejected", err)
	}
	return got
}

func TestColdridgeLevelingStartRecordsExplicitFiniteGoalWithoutQuestCredit(t *testing.T) {
	s, f, _ := coldLevelingStartSetup(t)
	before := proto.Clone(f.world).(*worldv1.SnapshotResponse)
	got := coldLevelingStart(t, s, "level-five-one")
	r := got.ColdridgeLeveling
	if !got.Practicing || r == nil || r.RequestId != "level-five-one" || r.Goal != "coldridge_706_to_level_5" || r.TargetLevel != 5 || r.MaxPulls != 1 || r.SelfGuid != 8 || r.PositionRevision != f.world.Self.PositionRevision || r.SourceGuid != 0 || r.PullReserved || r.RemainingMs != uint64((3*time.Minute).Milliseconds()) || r.ObservedLevel != 4 || r.LevelFiveObserved {
		t.Fatal("start did not describe the bounded ordinary goal", got)
	}
	if !s.practiceUntil.Equal(s.now().Add(3*time.Minute)) || got.PracticeLeaseRemainingMs != r.RemainingMs || len(f.packets) != 0 || !proto.Equal(before, f.world) {
		t.Fatal("admission wrote gameplay, changed the own log, or used another deadline", got, f.packets)
	}
	// Returned protobufs must not let a caller rewrite the retained reservation.
	r.RequestId, r.MaxPulls, r.RemainingMs, r.LevelFiveObserved = "changed", 99, 999999, true
	again := state(t, s).ColdridgeLeveling
	if again == nil || again.RequestId != "level-five-one" || again.MaxPulls != 1 || again.RemainingMs != uint64((3*time.Minute).Milliseconds()) || again.LevelFiveObserved {
		t.Fatal("caller mutated the controller's retained goal", again)
	}
}

func TestColdridgeLevelingStartRequiresExactOwnContextAndExplicitRequest(t *testing.T) {
	cases := map[string]func(*Service, *fakeSession){
		"companion_service": func(s *Service, _ *fakeSession) { s.independent = false },
		"foreign_self":      func(_ *Service, f *fakeSession) { f.world.Self.Guid = 7 },
		"unknown_level":     func(_ *Service, f *fakeSession) { f.world.Self.LevelKnown = false },
		"level3":            func(_ *Service, f *fakeSession) { f.world.Self.Level = 3 },
		"level5":            func(_ *Service, f *fakeSession) { f.world.Self.Level = 5 },
		"unknown_class":     func(_ *Service, f *fakeSession) { f.world.Self.ClassKnown = false },
		"wrong_class":       func(_ *Service, f *fakeSession) { f.world.Self.ClassId = 8 },
		"wrong_map":         func(_ *Service, f *fakeSession) { f.world.Self.MapId = 1 },
		"unknown_map":       func(_ *Service, f *fakeSession) { f.world.Self.MapKnown = false },
		"unknown_raw_position": func(_ *Service, f *fakeSession) {
			f.world.Self.PositionKnown = false
		},
		"unknown_position_revision": func(s *Service, f *fakeSession) {
			f.world.Self.PositionRevision, s.positionRevision = 0, 0
		},
		"dead":            func(_ *Service, f *fakeSession) { f.world.Self.Alive = false },
		"combat":          func(_ *Service, f *fakeSession) { f.world.Self.UnitFlags |= 0x80000 },
		"unknown_log":     func(_ *Service, f *fakeSession) { f.world.SelfQuestLogKnown = false },
		"partial_log":     func(_ *Service, f *fakeSession) { f.world.SelfQuestLog = f.world.SelfQuestLog[:24] },
		"nil_slot":        func(_ *Service, f *fakeSession) { f.world.SelfQuestLog[24] = nil },
		"duplicate_slot":  func(_ *Service, f *fakeSession) { f.world.SelfQuestLog[24].Slot = 3 },
		"duplicate_quest": func(_ *Service, f *fakeSession) { f.world.SelfQuestLog[24].QuestId = 218 },
		"quest_missing":   func(_ *Service, f *fakeSession) { f.world.SelfQuestLog[3].QuestId = 0 },
		"quest_failed":    func(_ *Service, f *fakeSession) { f.world.SelfQuestLog[3].Failed = true },
		"quest_failed_state": func(_ *Service, f *fakeSession) {
			f.world.SelfQuestLog[3].State = 2
		},
		"quest_complete": func(_ *Service, f *fakeSession) { f.world.SelfQuestLog[3].Complete = true },
		"quest_complete_state": func(_ *Service, f *fakeSession) {
			f.world.SelfQuestLog[3].State = 1
		},
		"short_counters": func(_ *Service, f *fakeSession) {
			f.world.SelfQuestLog[3].ObjectiveCounts = []uint32{0, 0, 0}
		},
		"nonzero_counter": func(_ *Service, f *fakeSession) {
			f.world.SelfQuestLog[3].ObjectiveCounts[2] = 1
		},
		"current182": func(_ *Service, f *fakeSession) { f.world.SelfQuestLog[10].QuestId = 182 },
		"only_old182": func(_ *Service, f *fakeSession) {
			f.world.SelfQuestLog[3].QuestId = 182
		},
		"raw_outside_both_scopes": func(_ *Service, f *fakeSession) { f.world.Self.X = -8900 },
		"estimate_outside_corridor": func(s *Service, _ *fakeSession) {
			s.pos = cold182TestBaseline
		},
		"spell_unknown": func(s *Service, _ *fakeSession) { s.state.KnownShadowBolt = false },
		"cast_pending":  func(s *Service, _ *fakeSession) { s.state.CastPending = true },
		"lease_owned":   func(s *Service, _ *fakeSession) { s.state.InteractionOwner = "loot" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s, f, _ := coldLevelingStartSetup(t)
			change(s, f)
			if got, err := s.StartColdridgeLeveling(context.Background(), &actionsv1.ColdridgeLevelingRequest{RequestId: "unsafe-one"}); err == nil || got != nil {
				t.Fatal("unsafe admission succeeded", got, err)
			}
			if s.state.Practicing || s.stateLocked().ColdridgeLeveling != nil || countPackets(f, opcode.CMSGCastSpell) != 0 || countPackets(f, opcode.MSGMoveStartForward) != 0 {
				t.Fatal("failed admission armed gameplay or fabricated a goal", s.stateLocked())
			}
		})
	}
	for _, id := range []string{"", "contains space", "with.dot", "unicode_é", strings.Repeat("a", 65)} {
		t.Run("invalid_id/"+id, func(t *testing.T) {
			s, f, _ := coldLevelingStartSetup(t)
			if _, err := s.StartColdridgeLeveling(context.Background(), &actionsv1.ColdridgeLevelingRequest{RequestId: id}); err == nil || s.stateLocked().ColdridgeLeveling != nil || len(f.packets) != 0 {
				t.Fatal("invalid request ID had effects", id, err)
			}
		})
	}
	s, f, _ := coldLevelingStartSetup(t)
	if _, err := s.StartColdridgeLeveling(context.Background(), nil); err == nil || len(f.packets) != 0 {
		t.Fatal("nil request had effects", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.StartColdridgeLeveling(ctx, &actionsv1.ColdridgeLevelingRequest{RequestId: "cancelled"}); err == nil || s.stateLocked().ColdridgeLeveling != nil || len(f.packets) != 0 {
		t.Fatal("cancelled admission armed a goal", err)
	}
	s, _, _ = coldLevelingStartSetup(t)
	if got := coldLevelingStart(t, s, strings.Repeat("a", 64)); got.ColdridgeLeveling.RequestId != strings.Repeat("a", 64) {
		t.Fatal("the maximum valid request ID was not retained", got)
	}
}

func TestColdridgeLevelingRequestIdentityCannotRenewOrRearm(t *testing.T) {
	s, f, advance := coldLevelingStartSetup(t)
	coldLevelingStart(t, s, "same-ID_1")
	until := s.practiceUntil
	advance(37 * time.Second)
	again := coldLevelingStart(t, s, "same-ID_1")
	if !s.practiceUntil.Equal(until) || again.ColdridgeLeveling.RemainingMs != 143000 || again.ColdridgeLeveling.PullReserved || len(f.packets) != 0 {
		t.Fatal("duplicate request renewed or reserved the budget", again)
	}
	if _, err := s.StartColdridgeLeveling(context.Background(), &actionsv1.ColdridgeLevelingRequest{RequestId: "new-active-ID"}); err == nil || !s.practiceUntil.Equal(until) || state(t, s).ColdridgeLeveling.RequestId != "same-ID_1" {
		t.Fatal("a new active request replaced the bound goal", err)
	}
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err == nil || !s.practiceUntil.Equal(until) || state(t, s).ColdridgeLeveling.RequestId != "same-ID_1" {
		t.Fatal("ordinary Practice repurposed or renewed the explicit goal", err)
	}
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{}); err != nil {
		t.Fatal(err)
	}
	again = coldLevelingStart(t, s, "same-ID_1")
	if again.Practicing || again.ColdridgeLeveling == nil || again.ColdridgeLeveling.RequestId != "same-ID_1" || !s.practiceUntil.Equal(until) {
		t.Fatal("same ID rearmed a disabled goal or lost the record", again)
	}
	// A later explicit operation may replace a disabled record, but cannot make
	// that previously consumed request ID usable again.
	coldLevelingStart(t, s, "replacement")
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartColdridgeLeveling(context.Background(), &actionsv1.ColdridgeLevelingRequest{RequestId: "same-ID_1"}); err == nil || state(t, s).ColdridgeLeveling.RequestId != "replacement" {
		t.Fatal("a superseded ID regained authority", err)
	}
}

func TestColdridgeLevelingRecordSurvivesFiniteStopWithoutRestarting(t *testing.T) {
	for _, mode := range []string{"expiry", "death", "level5", "unknown_level"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := coldLevelingStartSetup(t)
			coldLevelingStart(t, s, "retained")
			until := s.practiceUntil
			switch mode {
			case "expiry":
				advance(3 * time.Minute)
			case "death":
				f.world.Self.Alive = false
			case "level5":
				f.world.Self.Level = 5
			case "unknown_level":
				f.world.Self.LevelKnown = false
			}
			s.Tick(context.Background())
			got := state(t, s)
			r := got.ColdridgeLeveling
			if got.Practicing || r == nil || r.RequestId != "retained" || r.PullReserved || r.SourceGuid != 0 || !s.practiceUntil.Equal(until) || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("finite stop lost the record, moved its deadline or invented a pull", got)
			}
			if mode == "expiry" || mode == "unknown_level" {
				if r.RemainingMs != 0 {
					t.Fatal("expired or unknown own state retained authority", r)
				}
			}
			if r.LevelFiveObserved != (mode == "level5") || mode == "level5" && r.ObservedLevel != 5 {
				t.Fatal("stop fabricated or lost the actual level observation", r)
			}
			got = coldLevelingStart(t, s, "retained")
			if got.Practicing || !s.practiceUntil.Equal(until) || got.ColdridgeLeveling.RequestId != "retained" {
				t.Fatal("duplicate ID restarted a terminal goal", got)
			}
		})
	}
}

func TestColdridgeLevelingRequestHistoryIsBoundedWithoutMakingOldIDsReusable(t *testing.T) {
	s, f, _ := coldLevelingStartSetup(t)
	for i := 0; i < 64; i++ {
		coldLevelingStart(t, s, "history-"+strconv.Itoa(i))
		if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	until := s.practiceUntil
	for _, id := range []string{"history-64", "history-0"} {
		if _, err := s.StartColdridgeLeveling(context.Background(), &actionsv1.ColdridgeLevelingRequest{RequestId: id}); err == nil || state(t, s).Practicing || state(t, s).ColdridgeLeveling.RequestId != "history-63" || !s.practiceUntil.Equal(until) {
			t.Fatal("full request history evicted old IDs or admitted another window", id, err)
		}
	}
	got := coldLevelingStart(t, s, "history-63")
	if got.Practicing || got.ColdridgeLeveling.RequestId != "history-63" || !s.practiceUntil.Equal(until) || len(f.packets) != 0 {
		t.Fatal("current duplicate was rearmed instead of returning its retained record", got)
	}
}

func TestColdridgeLevelingPullReservationIsSingleAndDoesNotMeanLevelOrQuestCredit(t *testing.T) {
	s, f, _ := coldLevelingStartSetup(t)
	coldLevelingStart(t, s, "one-pull")
	s.Tick(context.Background())
	if got := state(t, s); got.ColdridgeLeveling.PullReserved || got.ColdridgeLeveling.SourceGuid != 0 || got.PracticePulls != 0 || lastCast(f) != 0 {
		t.Fatal("planning consumed a pull or invented a source", got)
	}
	// Even a structurally valid706 GUID cannot create a handoff unless the
	// selected body is actually present and eligible in the final observation.
	missing := proto.Clone(f.world).(*worldv1.SnapshotResponse)
	missing.NearbyCreatures, missing.RequestedCreatures, missing.RequestedCreature = nil, nil, nil
	mixed := proto.Clone(f.world).(*worldv1.SnapshotResponse)
	mixed.NearbyCreatures[0].MaxHealth = 86
	s.mu.Lock()
	foreignReserved := s.reserveColdridgeLevelingPullLocked(f.world, cold182GoalGUID+1)
	missingReserved := s.reserveColdridgeLevelingPullLocked(missing, cold182GoalGUID)
	mixedReserved := s.reserveColdridgeLevelingPullLocked(mixed, cold182GoalGUID)
	s.mu.Unlock()
	if foreignReserved || missingReserved || mixedReserved || state(t, s).ColdridgeLeveling.PullReserved || lastCast(f) != 0 {
		t.Fatal("unobserved or unsupported body reserved the only pull", foreignReserved, missingReserved, mixedReserved, state(t, s))
	}
	s.Tick(context.Background())
	got := state(t, s)
	r := got.ColdridgeLeveling
	if !r.PullReserved || r.SourceGuid != cold182GoalGUID || r.MaxPulls != 1 || r.ObservedLevel != 4 || r.LevelFiveObserved || got.PracticePulls != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 || got.PracticeKills != 0 || got.ShadowBoltDamageEvents != 0 || got.ServerAssistGo != 0 {
		t.Fatal("first cast did not reserve exactly one truthful ordinary pull", got)
	}
	q := f.world.SelfQuestLog[3]
	if q.QuestId != 218 || q.Complete || q.Failed || len(q.ObjectiveCounts) != 4 || q.ObjectiveCounts[0] != 0 || q.ObjectiveCounts[1] != 0 || q.ObjectiveCounts[2] != 0 || q.ObjectiveCounts[3] != 0 {
		t.Fatal("leveling fabricated item-quest progress", q)
	}
	s.mu.Lock()
	firstAgain := s.reserveColdridgeLevelingPullLocked(f.world, cold182GoalGUID)
	secondGUID := s.reserveColdridgeLevelingPullLocked(f.world, cold182GoalGUID+1)
	s.mu.Unlock()
	if firstAgain || secondGUID || state(t, s).PracticePulls != 1 || state(t, s).ColdridgeLeveling.SourceGuid != cold182GoalGUID {
		t.Fatal("a duplicate or different GUID reused the only reservation", firstAgain, secondGUID, state(t, s))
	}
}

func TestColdridgeLevelingOrdinaryPracticeCannotInferAnExplicitLevelGoal(t *testing.T) {
	s, f, _ := coldLevelingStartSetup(t)
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err == nil || s.stateLocked().ColdridgeLeveling != nil || s.state.Practicing || len(f.packets) != 0 {
		t.Fatal("active item quest silently authorized ordinary706 leveling", err)
	}
}
