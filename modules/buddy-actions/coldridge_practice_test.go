package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

const coldridgeWolfGUID uint64 = 0xf1300002c1000012

func coldStraight(_ context.Context, _ string, a, b position) ([]position, error) {
	n := int(math.Ceil(distance(a, b)))
	if n < 1 {
		n = 1
	}
	path := make([]position, n+1)
	for i := range path {
		f := float32(i) / float32(n)
		path[i] = position{x: a.x + (b.x-a.x)*f, y: a.y + (b.y-a.y)*f, z: a.z + (b.z-a.z)*f}
	}
	return path, nil
}

func coldridgeRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	p := f.world.Self
	p.ClassKnown, p.ClassId, p.LevelKnown, p.Level = true, 9, true, 1
	p.X, p.Y, p.Z = -6240, 331, 383
	p.HealthKnown, p.Health, p.MaxHealth = true, 100, 100
	p.PowerKnown, p.Power, p.MaxPower, p.PowerType = true, 100, 100, 0
	p.BaseManaKnown, p.BaseMana = true, 100
	f.world.Owner = nil
	f.world.SelfQuestLogKnown = true
	f.world.SelfQuestLog = nil
	for i := range 25 {
		f.world.SelfQuestLog = append(f.world.SelfQuestLog, &worldv1.QuestLogEntry{Slot: uint32(i)})
	}
	f.world.SelfQuestLog[0].QuestId = 179
	f.world.NearbyCreatures = []*worldv1.CreatureState{{Guid: coldridgeWolfGUID, Entry: 705, Present: true, MapKnown: true, PositionKnown: true, X: -6237, Y: 331, Z: 383, LevelKnown: true, Level: 1, HealthKnown: true, Health: 42, MaxHealth: 42, AliveKnown: true, Alive: true}}
	learnSpells(s, shadowBolt)
	s.SetNavigationDataDir("/fixture/client-data")
	s.planColdridgePath = coldStraight
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return s, f, advance
}

func TestColdridgePullUsesActualOwnQuestObservedWolfAndLearnedShadowBolt(t *testing.T) {
	s, f, _ := coldridgeRig(t)
	s.Tick(context.Background())
	if lastCast(f) != 0 || !state(t, s).NavAvailable {
		t.Fatal("planning alone pulled", state(t, s))
	}
	s.Tick(context.Background())
	if lastCast(f) != shadowBolt || s.pendingTarget != coldridgeWolfGUID || state(t, s).PracticePulls != 1 || state(t, s).ShadowBoltDamageEvents != 0 || state(t, s).PracticeKills != 0 {
		t.Fatal("cast/transport invented progress", state(t, s))
	}
	p := binary.LittleEndian.AppendUint64(nil, 3)
	p = binary.LittleEndian.AppendUint64(p, coldridgeWolfGUID)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGPartykilllog, Payload: p})
	if state(t, s).PracticeKills != 0 {
		t.Fatal("foreign kill credited")
	}
	binary.LittleEndian.PutUint64(p, s.selfGUID)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGPartykilllog, Payload: p})
	if state(t, s).PracticeKills != 1 {
		t.Fatal("actual own kill not credited")
	}
}

func TestColdridgeFailsClosedBeforePullForEveryBoundAndFreshState(t *testing.T) {
	for _, mode := range []string{"quest", "failed", "unknownlog", "duplicateslot", "entry", "guid", "level", "claimed", "health", "mana", "book", "base", "combat", "region", "assets", "partial", "wall", "teleport"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := coldridgeRig(t)
			s.Tick(context.Background()) // Verified path must be rechecked before use.
			p, target := f.world.Self, f.world.NearbyCreatures[0]
			switch mode {
			case "quest":
				f.world.SelfQuestLog[0].QuestId = 33
			case "failed":
				f.world.SelfQuestLog[0].Failed = true
			case "unknownlog":
				f.world.SelfQuestLogKnown = false
			case "duplicateslot":
				f.world.SelfQuestLog[24].Slot = 0
			case "entry":
				target.Entry = 299
			case "guid":
				target.Guid = enemyGUID
			case "level":
				target.Level = 2
			case "claimed":
				target.AttackVictimKnown, target.AttackVictimGuid = true, 3
			case "health":
				p.Health = 94
			case "mana":
				p.Power = 59
			case "book":
				s.state.KnownShadowBolt = false
			case "base":
				p.BaseManaKnown = false
			case "combat":
				p.UnitFlags |= 0x80000
			case "region":
				p.X = -8900
			case "assets":
				s.practiceRoute = nil
				s.navDataDir = ""
			case "partial":
				s.practiceRoute = nil
				s.planColdridgePath = func(context.Context, string, position, position) ([]position, error) {
					return nil, errors.New("partial")
				}
			case "wall":
				s.practiceRoute = nil
				s.planColdridgePath = func(context.Context, string, position, position) ([]position, error) {
					return []position{{x: -6240, y: 331, z: 383}, {x: -6237, y: 331, z: 383}}, nil
				}
			case "teleport":
				p.X += 15
				p.PositionRevision++
			}
			s.Tick(context.Background())
			if lastCast(f) != 0 || state(t, s).PracticePulls != 0 {
				t.Fatal("unsafe/stale state pulled", mode, state(t, s))
			}
		})
	}
}

func TestColdridgeApproachStopsOnDisableExpiryDeathOrQuestCompletion(t *testing.T) {
	for _, mode := range []string{"disable", "expiry", "death", "complete"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := coldridgeRig(t)
			f.world.NearbyCreatures[0].X = -6200
			s.Tick(context.Background())
			s.Tick(context.Background())
			if !state(t, s).Moving || lastCast(f) != 0 {
				t.Fatal("approach did not start", state(t, s))
			}
			advance(200 * time.Millisecond)
			switch mode {
			case "disable":
				s.Practice(context.Background(), &actionsv1.ToggleRequest{})
			case "expiry":
				advance(11 * time.Minute)
			case "death":
				f.world.Self.Alive = false
			case "complete":
				f.world.SelfQuestLog[0].Complete = true
			}
			s.Tick(context.Background())
			if state(t, s).Practicing || state(t, s).Moving || countPackets(f, opcode.MSGMoveStop) != 1 {
				t.Fatal(state(t, s))
			}
		})
	}
}

func TestColdridgeCompleteQuestFinishesOnlyAlreadyProvenFightAndNeverNewPull(t *testing.T) {
	s, f, _ := coldridgeRig(t)
	s.Tick(context.Background())
	s.Tick(context.Background())
	f.world.SelfQuestLog[0].Complete = true
	f.world.Self.UnitFlags |= 0x80000
	c := f.world.NearbyCreatures[0]
	c.AttackVictimKnown, c.AttackVictimGuid, c.AttackAgeMs = true, s.selfGUID, 100
	s.Tick(context.Background())
	if !state(t, s).Practicing || state(t, s).PracticePhase != "finishing_current_fight_to_goal" {
		t.Fatal(state(t, s))
	}
	c.Health, c.Alive = 0, false
	f.world.Self.UnitFlags = 0
	s.Tick(context.Background())
	if state(t, s).Practicing || state(t, s).PracticePulls != 1 {
		t.Fatal(state(t, s))
	}
}

func TestColdridgeQuestContinuesAfterLevelTwoAndRecoversBeforeAnotherPull(t *testing.T) {
	s, f, _ := coldridgeRig(t)
	f.world.Self.Level, f.world.Self.Health = 2, 94
	s.Tick(context.Background())
	if !state(t, s).Practicing || state(t, s).PracticePhase != "recovering_before_next_pull" || lastCast(f) != 0 {
		t.Fatal(state(t, s))
	}
	f.world.Self.Health, f.world.Self.Power = 100, 59
	s.Tick(context.Background())
	if lastCast(f) != 0 || state(t, s).PracticePhase != "recovering_before_next_pull" {
		t.Fatal(state(t, s))
	}
	f.world.Self.Power = 60
	s.Tick(context.Background())
	s.Tick(context.Background())
	if lastCast(f) != shadowBolt || !state(t, s).Practicing {
		t.Fatal(state(t, s))
	}
	// Loss of authoritative quest knowledge never becomes permission to keep
	// fighting or select a second target, even with an existing victim proof.
	f.world.SelfQuestLogKnown = false
	f.world.Self.UnitFlags |= 0x80000
	f.world.NearbyCreatures[0].AttackVictimKnown, f.world.NearbyCreatures[0].AttackVictimGuid = true, s.selfGUID
	s.Tick(context.Background())
	if state(t, s).Practicing || state(t, s).PracticePulls != 1 {
		t.Fatal(state(t, s))
	}
}

func TestColdridgePlannedRouteMustStillMatchTargetAndCancellation(t *testing.T) {
	for _, mode := range []string{"target_moved", "canceled", "cast_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := coldridgeRig(t)
			s.Tick(context.Background())
			ctx := context.Background()
			switch mode {
			case "target_moved":
				f.world.NearbyCreatures[0].X += 4
				f.world.NearbyCreatures[0].PositionRevision++
			case "canceled":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			case "cast_failure":
				s.failedAssistTarget = coldridgeWolfGUID
			}
			s.Tick(ctx)
			if lastCast(f) != 0 || state(t, s).PracticePulls != 0 {
				t.Fatal(state(t, s))
			}
		})
	}
}

func coldApproachRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.ApproachLootRequest) {
	t.Helper()
	s, f, advance := coldridgeRig(t)
	s.Practice(context.Background(), &actionsv1.ToggleRequest{})
	f.world.NearbyCreatures = nil
	f.world.LootableCorpses = []*worldv1.CreatureState{{Guid: coldridgeWolfGUID, Entry: 705, Present: true, MapKnown: true, PositionKnown: true, X: -6230, Y: 331, Z: 383, HealthKnown: true, AliveKnown: true, DynamicFlags: 1}}
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot", LeaseMs: 15000})
	if err != nil {
		t.Fatal(err)
	}
	return s, f, advance, &actionsv1.ApproachLootRequest{Token: lease.Token, CorpseGuid: coldridgeWolfGUID}
}

func TestColdridgeCorpseApproachRequiresOwnQuestAndVerifiedBoundedRoute(t *testing.T) {
	s, f, advance, request := coldApproachRig(t)
	for range 12 {
		got, err := s.ApproachLoot(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if got.Reason == "loot_approach_within_distance" {
			break
		}
		advance(200 * time.Millisecond)
	}
	got := state(t, s)
	if got.Moving || got.Reason != "loot_approach_within_distance" || got.Position.X < -6233.1 || got.Position.X > -6231.5 || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.CMSGLoot) != 0 {
		t.Fatal(got)
	}
	for _, mode := range []string{"no_assets", "partial", "wrong_entry", "wrong_guid", "unknown_quest", "combat", "changed_after_plan"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, request := coldApproachRig(t)
			switch mode {
			case "no_assets":
				s.navDataDir = ""
			case "partial":
				s.planColdridgePath = func(context.Context, string, position, position) ([]position, error) { return nil, errors.New("wall") }
			case "wrong_entry":
				f.world.LootableCorpses[0].Entry = 299
			case "wrong_guid":
				f.world.LootableCorpses[0].Guid, request.CorpseGuid = approachCorpseGUID, approachCorpseGUID
			case "unknown_quest":
				f.world.SelfQuestLogKnown = false
			case "combat":
				f.world.Self.UnitFlags |= 0x80000
			case "changed_after_plan":
				s.planColdridgePath = func(ctx context.Context, root string, a, b position) ([]position, error) {
					path, err := coldStraight(ctx, root, a, b)
					f.world.SelfQuestLog[0].QuestId = 0
					return path, err
				}
			}
			got, err := s.ApproachLoot(context.Background(), request)
			if err != nil || got.Moving || countPackets(f, opcode.MSGMoveStartForward) != 0 {
				t.Fatal(mode, got, err)
			}
		})
	}
}

func coldThreatGUID(entry uint32) uint64 { return uint64(0xf130)<<48 | uint64(entry)<<24 | 0x123 }

func coldThreatCreature(entry uint32, x float32) *worldv1.CreatureState {
	// Live public snapshots established these profiles for both quest170
	// entries. Geometry/GUID low bits here are local navigation fixtures.
	return &worldv1.CreatureState{Guid: coldThreatGUID(entry), Entry: entry, Present: true, MapKnown: true, MapId: 0, PositionKnown: true, X: x, Y: 331, Z: 383, PositionRevision: 1, LevelKnown: true, Level: 2, HealthKnown: true, Health: 55, MaxHealth: 55, AliveKnown: true, Alive: true, FactionTemplate: 36, UnitFlags_2: 2048, TargetKnown: true}
}

func coldThreatQuest(f *fakeSession) *worldv1.QuestLogEntry {
	for _, q := range f.world.SelfQuestLog {
		if q != nil && q.QuestId == 170 {
			return q
		}
	}
	return nil
}

func coldThreatRig(t *testing.T, entry uint32, first, second uint32) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := coldridgeRig(t)
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{}); err != nil {
		t.Fatal(err)
	}
	p := f.world.Self
	p.Level, p.Health, p.MaxHealth = 2, 68, 68
	p.Power, p.MaxPower, p.BaseMana = 223, 223, 98
	f.world.SelfQuestLogRevision = 6
	for i := range f.world.SelfQuestLog {
		f.world.SelfQuestLog[i] = &worldv1.QuestLogEntry{Slot: uint32(i), ObjectiveCounts: []uint32{0, 0, 0, 0}}
	}
	f.world.SelfQuestLog[0].QuestId = 170
	f.world.SelfQuestLog[0].ObjectiveCounts = []uint32{first, second, 0, 0}
	f.world.NearbyCreatures = []*worldv1.CreatureState{coldThreatCreature(entry, -6237)}
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal("valid actual quest170 profile did not start", err)
	}
	return s, f, advance
}

func TestColdridgeThreatPlansThenCastsOnlyActualSupportedProfile(t *testing.T) {
	for _, entry := range []uint32{707, 724} {
		t.Run(fmt.Sprint(entry), func(t *testing.T) {
			s, f, _ := coldThreatRig(t, entry, 0, 0)
			s.Tick(context.Background())
			if lastCast(f) != 0 || !state(t, s).NavAvailable || s.practiceTarget != coldThreatGUID(entry) {
				t.Fatal("planning did not bind observed quest target without casting", state(t, s))
			}
			s.Tick(context.Background())
			got := state(t, s)
			if lastCast(f) != shadowBolt || s.pendingTarget != coldThreatGUID(entry) || got.PracticePulls != 1 || got.PracticeKills != 0 || got.ShadowBoltDamageEvents != 0 || got.ServerAssistGo != 0 || s.coldridgeGoal.questID != 170 {
				t.Fatal("actual cast transport invented progress or switched goal", got)
			}
			if q := coldThreatQuest(f); q.ObjectiveCounts[0] != 0 || q.ObjectiveCounts[1] != 0 {
				t.Fatal("transport changed authoritative objective counters", q)
			}
		})
	}
}

func TestColdridgeThreatSkipsSatisfiedObjectiveAndFollowsQuestIDAcrossSlots(t *testing.T) {
	for _, wanted := range []uint32{707, 724} {
		t.Run(fmt.Sprint(wanted), func(t *testing.T) {
			first, second, satisfied := uint32(6), uint32(0), uint32(707)
			if wanted == 707 {
				first, second, satisfied = 0, 6, 724
			}
			s, f, _ := coldThreatRig(t, wanted, first, second)
			f.world.NearbyCreatures = []*worldv1.CreatureState{coldThreatCreature(satisfied, -6239), coldThreatCreature(wanted, -6233)}
			s.Tick(context.Background())
			if s.practiceTarget != coldThreatGUID(wanted) {
				t.Fatal("selected nearer already-satisfied objective", state(t, s))
			}
			moved := proto.Clone(coldThreatQuest(f)).(*worldv1.QuestLogEntry)
			moved.Slot = 8
			f.world.SelfQuestLog[0] = &worldv1.QuestLogEntry{Slot: 0, ObjectiveCounts: []uint32{0, 0, 0, 0}}
			f.world.SelfQuestLog[8] = moved
			f.world.SelfQuestLog[8], f.world.SelfQuestLog[20] = f.world.SelfQuestLog[20], f.world.SelfQuestLog[8]
			f.world.SelfQuestLogRevision++
			s.Tick(context.Background())
			if lastCast(f) != shadowBolt || s.pendingTarget != coldThreatGUID(wanted) || s.coldridgeGoal.questID != 170 {
				t.Fatal("moving quest slot changed pinned goal/needed target", state(t, s))
			}
		})
	}
}

func TestColdridgeThreatGoalDoesNotSwitchWhen179Appears(t *testing.T) {
	s, f, _ := coldThreatRig(t, 707, 0, 0)
	f.world.SelfQuestLog[1].QuestId = 179
	f.world.NearbyCreatures = append([]*worldv1.CreatureState{{Guid: coldridgeWolfGUID, Entry: 705, Present: true, MapKnown: true, PositionKnown: true, X: -6239, Y: 331, Z: 383, LevelKnown: true, Level: 1, HealthKnown: true, Health: 42, MaxHealth: 42, AliveKnown: true, Alive: true}}, f.world.NearbyCreatures...)
	s.Tick(context.Background())
	s.Tick(context.Background())
	if s.coldridgeGoal.questID != 170 || s.pendingTarget != coldThreatGUID(707) || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("new179 switched ongoing170 goal", state(t, s))
	}
	coldThreatQuest(f).QuestId = 0
	f.world.SelfQuestLogRevision++
	s.Tick(context.Background())
	if state(t, s).Practicing || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("missing pinned170 fell back to179", state(t, s))
	}
}

func TestColdridgeThreatStopsBeforeNewPullWhenBothObjectivesDone(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			s, f, _ := coldThreatRig(t, 707, 0, 0)
			s.Tick(context.Background())
			q := coldThreatQuest(f)
			q.ObjectiveCounts, q.Complete = []uint32{6, 6, 0, 0}, complete
			if complete {
				q.State = 1
			}
			f.world.SelfQuestLogRevision++
			s.Tick(context.Background())
			if state(t, s).Practicing || lastCast(f) != 0 || state(t, s).PracticePulls != 0 {
				t.Fatal("completed objective log authorized a new pull", state(t, s))
			}
		})
	}
}

func TestColdridgeThreatFinishedGoalOnlyFinishesExistingOwnFight(t *testing.T) {
	for _, proof := range []string{"own", "foreign", "none"} {
		t.Run(proof, func(t *testing.T) {
			s, f, _ := coldThreatRig(t, 724, 0, 0)
			s.Tick(context.Background())
			s.Tick(context.Background())
			coldThreatQuest(f).ObjectiveCounts = []uint32{6, 6, 0, 0}
			f.world.Self.UnitFlags |= 0x80000
			c := f.world.NearbyCreatures[0]
			if proof != "none" {
				c.AttackVictimKnown, c.AttackAgeMs, c.AttackVictimGuid = true, 100, s.selfGUID
				if proof == "foreign" {
					c.AttackVictimGuid = 3
				}
			}
			s.Tick(context.Background())
			got := state(t, s)
			if proof == "own" {
				if !got.Practicing || got.PracticePhase != "finishing_current_fight_to_goal" {
					t.Fatal("proven existing own fight was not retained", got)
				}
				c.Health, c.Alive, f.world.Self.UnitFlags = 0, false, 0
				s.Tick(context.Background())
				if state(t, s).Practicing {
					t.Fatal("goal done after target death did not stop", state(t, s))
				}
			} else if got.Practicing {
				t.Fatal("unknown or foreign fight extended completed goal", got)
			}
			if countPackets(f, opcode.CMSGCastSpell) != 1 || state(t, s).PracticePulls != 1 {
				t.Fatal("goal completion selected another pull", state(t, s))
			}
		})
	}
}

func TestColdridgeThreatRejectsMalformedRegressedOrChangedLogAfterPlanning(t *testing.T) {
	for _, mode := range []string{"unknown", "partial", "duplicate_slot", "duplicate_quest", "nil_slot", "short_counts", "oversized_count", "unexpected_objective", "regressed", "failed", "objective_satisfied"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := coldThreatRig(t, 707, 2, 0)
			s.Tick(context.Background())
			q := coldThreatQuest(f)
			switch mode {
			case "unknown":
				f.world.SelfQuestLogKnown = false
			case "partial":
				f.world.SelfQuestLog = f.world.SelfQuestLog[:24]
			case "duplicate_slot":
				f.world.SelfQuestLog[24].Slot = 0
			case "duplicate_quest":
				f.world.SelfQuestLog[1].QuestId = 170
			case "nil_slot":
				f.world.SelfQuestLog[10] = nil
			case "short_counts":
				q.ObjectiveCounts = []uint32{2, 0, 0}
			case "oversized_count":
				q.ObjectiveCounts[0] = 7
			case "unexpected_objective":
				q.ObjectiveCounts[3] = 1
			case "regressed":
				q.ObjectiveCounts[0] = 1
			case "failed":
				q.Failed, q.State = true, 2
			case "objective_satisfied":
				q.ObjectiveCounts[0] = 6
			}
			f.world.SelfQuestLogRevision++
			s.Tick(context.Background())
			if lastCast(f) != 0 || state(t, s).PracticePulls != 0 {
				t.Fatal("invalid/regressed/no-longer-needed planned target was pulled", mode, state(t, s))
			}
			if mode != "objective_satisfied" && state(t, s).Practicing {
				t.Fatal("invalid own log kept practice active", mode, state(t, s))
			}
		})
	}
}

func TestColdridgeThreatWrongProfileClaimMapOrGUIDCannotPull(t *testing.T) {
	for _, mode := range []string{"wrong_level", "level_unknown", "wrong_max_health", "overflow_health", "foreign_claim", "target_claim", "tapped", "combat", "npc", "unsafe_flags", "unsafe_flags2", "map", "guid_entry", "guid_type", "guid_zero_low", "own_level_one", "own_level_six"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := coldThreatRig(t, 707, 0, 0)
			c := f.world.NearbyCreatures[0]
			switch mode {
			case "wrong_level":
				c.Level = 3
			case "level_unknown":
				c.LevelKnown = false
			case "wrong_max_health":
				c.MaxHealth = 56
			case "overflow_health":
				c.Health = 56
			case "foreign_claim":
				c.AttackVictimKnown, c.AttackVictimGuid = true, 3
			case "target_claim":
				c.TargetGuid = 3
			case "tapped":
				c.DynamicFlags = 4
			case "combat":
				c.UnitFlags = 0x80000
			case "npc":
				c.NpcFlags = 1
			case "unsafe_flags":
				c.UnitFlags = 0x100
			case "unsafe_flags2":
				c.UnitFlags_2 |= 1
			case "map":
				c.MapId = 1
			case "guid_entry":
				c.Guid = coldThreatGUID(724)
			case "guid_type":
				c.Guid = uint64(0xf110)<<48 | uint64(707)<<24 | 1
			case "guid_zero_low":
				c.Guid = uint64(0xf130)<<48 | uint64(707)<<24
			case "own_level_one":
				f.world.Self.Level = 1
			case "own_level_six":
				f.world.Self.Level = 6
			}
			s.Tick(context.Background())
			s.Tick(context.Background())
			if lastCast(f) != 0 || state(t, s).PracticePulls != 0 {
				t.Fatal("unsafe quest170 profile was pulled", mode, state(t, s))
			}
		})
	}
}

func TestColdridgeThreatSelectedDeathClearsBeforeAnotherTarget(t *testing.T) {
	s, f, _ := coldThreatRig(t, 707, 0, 0)
	s.Tick(context.Background())
	f.world.NearbyCreatures[0].Health, f.world.NearbyCreatures[0].Alive = 0, false
	f.world.NearbyCreatures = append(f.world.NearbyCreatures, coldThreatCreature(724, -6235))
	s.Tick(context.Background())
	if s.practiceTarget != 0 || s.practiceRoute != nil || lastCast(f) != 0 || state(t, s).PracticePhase != "target_death_observed" {
		t.Fatal("selected dead target was retained or replaced with an immediate pull", state(t, s))
	}
	s.Tick(context.Background())
	s.Tick(context.Background())
	if s.pendingTarget != coldThreatGUID(724) || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("fresh needed target did not get a separate plan and cast", state(t, s))
	}
}

func TestColdridgeThreatStartRequiresActualOwnLevelTwo(t *testing.T) {
	s, f, _ := coldThreatRig(t, 707, 0, 0)
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{}); err != nil {
		t.Fatal(err)
	}
	f.world.Self.Level = 1
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err == nil || state(t, s).Practicing || lastCast(f) != 0 {
		t.Fatal("quest170 admitted a level1 acting character", state(t, s), err)
	}
}

func TestColdridgeThreatPartialObjectiveCompletionRequiresExistingOwnFight(t *testing.T) {
	for _, ownedFight := range []bool{true, false} {
		t.Run(fmt.Sprint(ownedFight), func(t *testing.T) {
			s, f, _ := coldThreatRig(t, 707, 5, 0)
			s.Tick(context.Background())
			s.Tick(context.Background())
			coldThreatQuest(f).ObjectiveCounts[0] = 6
			if ownedFight {
				f.world.Self.UnitFlags |= 0x80000
				c := f.world.NearbyCreatures[0]
				c.AttackVictimKnown, c.AttackVictimGuid, c.AttackAgeMs = true, s.selfGUID, 100
			}
			s.Tick(context.Background())
			got := state(t, s)
			if ownedFight {
				if !got.Practicing || got.PracticePhase != "finishing_current_fight_to_goal" {
					t.Fatal("proven existingfight lost at partialobjectivecompletion", got)
				}
			} else if got.Practicing {
				t.Fatal("completed selectedobjective kept an unproven fight", got)
			}
			if got.PracticePulls != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("partial completion started new work", got)
			}
		})
	}
}

func TestColdridgeThreatRechecksUnclaimedTargetAfterRoutePlan(t *testing.T) {
	for _, mode := range []string{"target_claim", "combat_unknown_victim", "own_victim_before_pull", "foreign_victim", "own_tapped", "foreign_tapped", "wrong_level", "wrong_profile", "wrong_faction", "low_own_health", "low_own_mana"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := coldThreatRig(t, 724, 0, 0)
			s.Tick(context.Background())
			c := f.world.NearbyCreatures[0]
			switch mode {
			case "target_claim":
				c.TargetGuid = 3
			case "combat_unknown_victim":
				c.UnitFlags |= 0x80000
			case "own_victim_before_pull":
				c.AttackVictimKnown, c.AttackVictimGuid = true, s.selfGUID
			case "foreign_victim":
				c.AttackVictimKnown, c.AttackVictimGuid = true, 3
			case "own_tapped":
				c.DynamicFlags = 8
			case "foreign_tapped":
				c.DynamicFlags = 4
			case "wrong_level":
				c.Level = 3
			case "wrong_profile":
				c.MaxHealth = 54
			case "wrong_faction":
				c.FactionTemplate = 55
			case "low_own_health":
				f.world.Self.Health = 60
			case "low_own_mana":
				f.world.Self.Power = 0
			}
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 0 || state(t, s).PracticePulls != 0 {
				t.Fatal("new claim/profile/resource state after plan authorized a pull", mode, state(t, s))
			}
		})
	}
}

func TestColdridgeThreatWaitingForDispatchNeverClaimsAnOwnedPull(t *testing.T) {
	for _, wait := range []string{"assist_cooldown", "heal_cooldown", "facing_clock_failure"} {
		for _, claim := range []string{"nonzero_target", "combat_unknown_victim", "own_victim_without_cast"} {
			t.Run(wait+"/"+claim, func(t *testing.T) {
				s, f, advance := coldThreatRig(t, 707, 0, 0)
				s.Tick(context.Background()) // Complete verified route, no CAST.
				switch wait {
				case "assist_cooldown":
					s.nextAssist = s.now().Add(time.Second)
				case "heal_cooldown":
					s.nextHeal = s.now().Add(time.Second)
				case "facing_clock_failure":
					f.failClock = true
				}
				s.Tick(context.Background())
				if countPackets(f, opcode.CMSGCastSpell) != 0 || s.practicePullStarted || state(t, s).PracticePulls != 0 || s.practiceRoute == nil {
					t.Fatal("waiting/cannot-face reserved an own fight before first cast", state(t, s))
				}
				c := f.world.NearbyCreatures[0]
				switch claim {
				case "nonzero_target":
					c.TargetKnown, c.TargetGuid = true, 3
				case "combat_unknown_victim":
					c.UnitFlags |= 0x80000
				case "own_victim_without_cast":
					c.AttackVictimKnown, c.AttackVictimGuid = true, s.selfGUID
				}
				f.failClock = false
				advance(time.Second)
				s.Tick(context.Background())
				if countPackets(f, opcode.CMSGCastSpell) != 0 || s.practicePullStarted || state(t, s).PracticePulls != 0 {
					t.Fatal("cooldown/facing wait created an exemption from fresh claim checks", state(t, s))
				}
			})
		}
	}
}

func TestColdridgeThreatSuccessfulFacingRechecksLatestDispatchSnapshot(t *testing.T) {
	for _, change := range []string{"unchanged", "target_claimed", "quest_complete", "quest_failed", "counts_regressed", "self_map_corrected", "self_revision_corrected"} {
		t.Run(change, func(t *testing.T) {
			s, f, _ := coldThreatRig(t, 707, 2, 0)
			s.Tick(context.Background()) // Verified route from the original snapshot.
			if s.practiceTarget != coldThreatGUID(707) || s.practiceRoute == nil || lastCast(f) != 0 {
				t.Fatal("fixture did not reach planned unpulled target", state(t, s))
			}
			facingClockObserved := false
			f.onClock = func() {
				// GetClock is followed by movementLocked's fresh world read.
				// Facing can succeed even when that read disproves the old pull.
				f.onClock = nil
				facingClockObserved = true
				q := coldThreatQuest(f)
				switch change {
				case "target_claimed":
					f.world.NearbyCreatures[0].TargetGuid = 3
				case "quest_complete":
					q.ObjectiveCounts, q.Complete, q.State = []uint32{6, 6, 0, 0}, true, 1
				case "quest_failed":
					q.Failed, q.State = true, 2
				case "counts_regressed":
					q.ObjectiveCounts[0] = 1
				case "self_map_corrected":
					f.world.Self.MapId = 1
				case "self_revision_corrected":
					f.world.Self.X += 4
					f.world.Self.PositionRevision++
				}
				f.world.SelfQuestLogRevision++
			}
			s.Tick(context.Background())
			if !facingClockObserved || countPackets(f, opcode.MSGMoveSetFacing) != 1 {
				t.Fatal("fixture did not complete the successful-facing seam", state(t, s))
			}
			got := state(t, s)
			if change == "unchanged" {
				if countPackets(f, opcode.CMSGCastSpell) != 1 || s.pendingTarget != coldThreatGUID(707) || !s.practicePullStarted || got.PracticePulls != 1 || got.PracticeKills != 0 || got.ShadowBoltDamageEvents != 0 {
					t.Fatal("unchanged fresh profile failed cast or fabricated progress", got)
				}
			} else if countPackets(f, opcode.CMSGCastSpell) != 0 || s.practicePullStarted || got.PracticePulls != 0 {
				t.Fatal("successful facing reused disproven earlier snapshot for CAST", change, got)
			}
		})
	}
}
