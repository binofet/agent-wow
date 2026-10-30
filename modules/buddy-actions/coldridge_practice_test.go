package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
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
