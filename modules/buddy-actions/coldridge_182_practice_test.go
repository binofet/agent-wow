package buddyactions

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

var (
	cold182TestBaseline = position{x: -6222.4404, y: 686.27026, z: 384.9266}
	cold182TestStage    = position{x: -6361.6, y: 568.8, z: 385.89725}
)

func cold182Setup(t *testing.T, progress, ownLevel, bodyLevel uint32) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	w := cold183World(progress)
	cold183Quest(w).QuestId = 182
	w.Self.Level = ownLevel
	w.Self.X, w.Self.Y, w.Self.Z = cold182TestBaseline.x, cold182TestBaseline.y, cold182TestBaseline.z
	w.Self.Health, w.Self.MaxHealth = 98, 98
	w.Self.Power, w.Self.MaxPower, w.Self.BaseMana = 257, 257, 102
	c := cold182GoalCreature(bodyLevel)
	// The raw Talin baseline and Grelin client estimate remain distinct. The
	// nearby body and straight path are synthetic, not live706/route claims.
	c.PositionKnown, c.PositionRevision = true, 1
	c.X, c.Y, c.Z = cold182TestStage.x+3, cold182TestStage.y, cold182TestStage.z
	w.NearbyCreatures = []*worldv1.CreatureState{c}
	f := &fakeSession{world: w}
	s := NewIndependentService(f, 8)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	s.lastWorld = w
	s.pos, s.posKnown, s.posMap = cold182TestStage, true, 0
	s.positionRevision, s.posEstimated, s.posAt = w.Self.PositionRevision, true, now
	s.SetNavigationDataDir("/fixture/client-data")
	s.planColdridge182Path = coldStraight
	s.planColdridgePath = func(context.Context, string, position, position) ([]position, error) {
		t.Fatal("182 dispatched the legacy Coldridge planner")
		return nil, nil
	}
	s.planPath = func(context.Context, string, position, position) ([]position, error) {
		t.Fatal("182 dispatched the Northshire planner")
		return nil, nil
	}
	s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
		t.Fatal("182 fell back to the generic current-map planner")
		return nil, nil
	}
	learnSpells(s, shadowBolt)
	return s, f, func(d time.Duration) { now = now.Add(d) }
}

func cold182Rig(t *testing.T, progress, ownLevel, bodyLevel uint32) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := cold182Setup(t, progress, ownLevel, bodyLevel)
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal("synthetic182 fixture did not enter the reviewed goal scope", err)
	}
	return s, f, advance
}

func TestColdridge182PracticeDispatchesItsPlannerThenCastsWithoutCredit(t *testing.T) {
	for _, mode := range []string{"raw_outside_legacy_and_troll_area", "estimate_still_at_talin"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := cold182Setup(t, 2, 4, 3)
			if mode == "estimate_still_at_talin" {
				s.pos = cold182TestBaseline
			} else {
				f.world.Self.X, f.world.Self.Y, f.world.Self.Z = -8900, 0, 100
			}
			if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err == nil || state(t, s).Practicing || lastCast(f) != 0 || countPackets(f, opcode.MSGMoveStartForward) != 0 {
				t.Fatal("182 started from an unqualified raw baseline or combat estimate", state(t, s), err)
			}
		})
	}
	for _, ownLevel := range []uint32{4, 6} {
		for _, bodyLevel := range []uint32{3, 4} {
			t.Run(fmt.Sprintf("self%d/body%d", ownLevel, bodyLevel), func(t *testing.T) {
				s, f, _ := cold182Rig(t, 2, ownLevel, bodyLevel)
				plans := 0
				s.planColdridge182Path = func(ctx context.Context, root string, from, to position) ([]position, error) {
					plans++
					return coldStraight(ctx, root, from, to)
				}
				s.Tick(context.Background())
				if plans != 1 || s.practiceTarget != cold182GoalGUID || lastCast(f) != 0 || s.practicePullStarted || !state(t, s).NavAvailable {
					t.Fatal("182 planning used another scope or invented a pull", plans, state(t, s))
				}
				s.Tick(context.Background())
				got := state(t, s)
				if plans != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 || lastCast(f) != shadowBolt || s.pendingTarget != cold182GoalGUID || s.coldridgeGoal.questID != 182 || !s.practicePullStarted || got.PracticePulls != 1 || got.PracticeKills != 0 || got.ServerAssistStarts != 0 || got.ServerAssistGo != 0 || got.ShadowBoltDamageEvents != 0 || cold182GoalQuest(f.world).ObjectiveCounts[0] != 2 {
					t.Fatal("cast transport invented server, damage, kill or quest evidence", got)
				}
			})
		}
	}
}

func cold182DisprovePractice(s *Service, f *fakeSession, mode string) {
	q, c := cold182GoalQuest(f.world), f.world.NearbyCreatures[0]
	switch mode {
	case "removed_with_other_quests":
		q.QuestId = 0
		f.world.SelfQuestLog[10].QuestId, f.world.SelfQuestLog[20].QuestId = 170, 179
	case "failed":
		q.Failed = true
	case "regressed":
		q.ObjectiveCounts[0] = 1
	case "done":
		q.ObjectiveCounts[0] = 10
	case "mixed_profile":
		c.MaxHealth = 86 // level3/86 was never an observed profile.
	case "claimed":
		c.TargetKnown, c.TargetGuid = true, 3
	case "raw_body_outside":
		inside := position{x: c.X, y: c.Y, z: c.Z}
		c.X, c.Y, c.Z = -8900, 0, 100
		c.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 100, AgeMs: 100, DestinationKnown: true, DestinationX: inside.x, DestinationY: inside.y, DestinationZ: inside.z}
	case "known_destination_outside":
		c.Trajectory = &worldv1.CreatureTrajectory{Known: true, Stopped: true, DestinationKnown: true, DestinationX: -8900, DestinationY: 0, DestinationZ: 100}
	case "raw_self_outside":
		f.world.Self.X, f.world.Self.Y, f.world.Self.Z = -8900, 0, 100
	case "revision":
		f.world.Self.PositionRevision++
	case "level7":
		f.world.Self.Level = 7
	}
}

func TestColdridge182PracticeRechecksBoundEvidenceAfterPlanAndFacing(t *testing.T) {
	for _, boundary := range []string{"after_plan", "during_facing"} {
		for _, mode := range []string{"unchanged", "removed_with_other_quests", "failed", "regressed", "done", "mixed_profile", "claimed", "raw_body_outside", "known_destination_outside", "raw_self_outside", "revision", "level7"} {
			t.Run(boundary+"/"+mode, func(t *testing.T) {
				s, f, _ := cold182Rig(t, 2, 4, 3)
				s.Tick(context.Background())
				if s.practiceTarget != cold182GoalGUID || lastCast(f) != 0 {
					t.Fatal("fixture missed the plan-before-dispatch boundary", state(t, s))
				}
				faced := false
				if boundary == "during_facing" {
					f.onClock = func() {
						f.onClock = nil
						faced = true
						cold182DisprovePractice(s, f, mode)
					}
				} else {
					cold182DisprovePractice(s, f, mode)
				}
				s.Tick(context.Background())
				want := 0
				if mode == "unchanged" {
					want = 1
				}
				if countPackets(f, opcode.CMSGCastSpell) != want || state(t, s).PracticePulls != uint64(want) || s.practicePullStarted != (want == 1) {
					t.Fatal("a stale182 goal/profile/position/claim authorized the first cast", state(t, s))
				}
				if boundary == "during_facing" && (!faced || countPackets(f, opcode.MSGMoveSetFacing) != 1) {
					t.Fatal("fixture did not exercise successful-facing refresh", state(t, s))
				}
				if boundary == "during_facing" && (len(f.lookups) == 0 || f.lookups[len(f.lookups)-1] != cold182GoalGUID) {
					t.Fatal("final observation was not bound to the already-selected exact706 GUID", f.lookups)
				}
			})
		}
	}
}

func TestColdridge182PracticeFinishesOnlyItsStartedOwnFightAndExpires(t *testing.T) {
	for _, proof := range []string{"own", "foreign", "stale", "missing", "not_started"} {
		t.Run(proof, func(t *testing.T) {
			s, f, advance := cold182Rig(t, 9, 4, 3)
			s.Tick(context.Background())
			started := proof != "not_started"
			if started {
				s.Tick(context.Background())
				goPacket := smiteEvent(true, s.castCount, cold182GoalGUID)
				goPacket[1], goPacket[3] = 8, 8
				binary.LittleEndian.PutUint32(goPacket[5:], shadowBolt)
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: goPacket})
				if state(t, s).CastPending || state(t, s).ServerAssistGo != 1 {
					t.Fatal("complete own cast receipt did not bind", state(t, s))
				}
			}
			q, c := cold182GoalQuest(f.world), f.world.NearbyCreatures[0]
			q.ObjectiveCounts[0], q.Complete = 10, true
			f.world.Self.UnitFlags = 0x80000
			c.AttackVictimKnown, c.AttackVictimGuid, c.AttackAgeMs = true, 8, 100
			switch proof {
			case "foreign":
				c.AttackVictimGuid = 3
			case "stale":
				c.AttackAgeMs = 30001
			case "missing":
				c.AttackVictimKnown = false
			}
			advance(3 * time.Second)
			s.Tick(context.Background())
			wantCasts, wantPulls := 1, uint64(1)
			if proof == "own" {
				wantCasts = 2
			} else if !started {
				wantCasts, wantPulls = 0, 0
			}
			if countPackets(f, opcode.CMSGCastSpell) != wantCasts || state(t, s).Practicing != (proof == "own") || state(t, s).PracticePulls != wantPulls || state(t, s).PracticeKills != 0 {
				t.Fatal("count10 extended an unproven fight or started a new pull", state(t, s))
			}
		})
	}
	s, f, advance := cold182Rig(t, 2, 6, 4)
	s.Tick(context.Background())
	advance(10 * time.Minute)
	s.Tick(context.Background())
	if state(t, s).Practicing || lastCast(f) != 0 || state(t, s).PracticePhase != "goal_expired" {
		t.Fatal("182 extended the finite ten-minute practice goal", state(t, s))
	}
}
