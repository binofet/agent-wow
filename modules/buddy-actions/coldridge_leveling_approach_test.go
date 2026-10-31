package buddyactions

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func coldLevelingApproachRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.ApproachLootRequest) {
	t.Helper()
	s, f, advance := coldLevelingRig(t)
	s.Tick(context.Background())
	s.Tick(context.Background())
	coldLevelingCastReceipt(t, s)
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{}); err != nil {
		t.Fatal(err)
	}
	c := f.world.NearbyCreatures[0]
	c.X, c.Health, c.Alive, c.DynamicFlags = cold182TestStage.x+6, 0, false, 1
	f.world.NearbyCreatures = nil
	f.world.LootableCorpses = []*worldv1.CreatureState{c}
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot", LeaseMs: 15000})
	if err != nil {
		t.Fatal(err)
	}
	return s, f, advance, &actionsv1.ApproachLootRequest{Token: lease.Token, CorpseGuid: c.Guid}
}

func TestColdridgeLevelingApproachRetainsOnlyItsLastSourceAtLevelFourOrFive(t *testing.T) {
	for _, level := range []uint32{4, 5} {
		s, f, advance, request := coldLevelingApproachRig(t)
		f.world.Self.Level = level
		plans := 0
		s.planColdridge182Path = func(ctx context.Context, root string, from, to position) ([]position, error) {
			plans++
			return coldStraight(ctx, root, from, to)
		}
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
		if plans == 0 || got.Moving || got.Practicing || got.Reason != "loot_approach_within_distance" || !got.NavAvailable || got.ColdridgeLeveling.SourceGuid != request.CorpseGuid || !got.ColdridgeLeveling.PullReserved || got.ColdridgeLeveling.PositionRevision != 7 || got.ColdridgeLeveling.ObservedLevel != level || got.ColdridgeLeveling.LevelFiveObserved != (level == 5) || countPackets(f, opcode.MSGMoveStartForward) == 0 || countPackets(f, opcode.CMSGCastSpell) != 1 || countPackets(f, opcode.CMSGLoot) != 0 {
			t.Fatal("last-source approach lost its pin, region or receipt separation", got)
		}
		if coldLevelingQuest(f.world).Complete || coldLevelingQuest(f.world).ObjectiveCounts[0] != 0 {
			t.Fatal("ordinary final-corpse movement invented218 completion")
		}
	}
}

func TestColdridgeLevelingApproachCannotBorrowQuestsOrNearbyOwner(t *testing.T) {
	for _, mode := range []string{"wrong_source", "unreserved", "missing_context", "failed_context", "extra179", "counter", "revision", "expired", "level6", "wrong_profile", "wrong_faction", "wrong_guid", "raw_body_outside", "destination_outside", "living", "not_lootable"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, request := coldLevelingApproachRig(t)
			// A safe nearby owner must not mask a failed retained-source check.
			s.independent = false
			f.world.Owner = proto.Clone(f.world.Self).(*worldv1.PlayerState)
			f.world.Owner.Guid = ownerGUID
			f.world.Owner.X, f.world.Owner.Y, f.world.Owner.Z = s.pos.x, s.pos.y, s.pos.z
			if reason := safePlayers(f.world, 8); reason != "" || distance(s.pos, playerPosition(f.world.Owner)) > 30 {
				t.Fatal("fixture owner is not genuinely safe and nearby", reason)
			}
			c, q := f.world.LootableCorpses[0], coldLevelingQuest(f.world)
			switch mode {
			case "wrong_source":
				c.Guid++
			case "unreserved":
				s.coldridgeLeveling.pullReserved = false
			case "missing_context":
				q.QuestId = 182
			case "failed_context":
				q.Failed = true
			case "extra179":
				f.world.SelfQuestLog[20].QuestId = 179
				c.Guid, c.Entry, c.Level, c.MaxHealth = coldridgeWolfGUID, 705, 1, 42
			case "counter":
				q.ObjectiveCounts[0] = 1
			case "revision":
				f.world.Self.PositionRevision++
			case "expired":
				advance(3 * time.Minute)
				lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot", LeaseMs: 15000})
				if err != nil {
					t.Fatal(err)
				}
				request.Token = lease.Token
			case "level6":
				f.world.Self.Level = 6
			case "wrong_profile":
				c.MaxHealth = 86
			case "wrong_faction":
				c.FactionTemplate = 189
			case "wrong_guid":
				c.Guid = coldThreatGUID(708)
			case "raw_body_outside":
				c.Y = 524.99
			case "destination_outside":
				c.Trajectory = &worldv1.CreatureTrajectory{Known: true, Stopped: true, DestinationKnown: true, DestinationX: c.X, DestinationY: 524.99, DestinationZ: c.Z}
			case "living":
				c.Health, c.Alive = 71, true
			case "not_lootable":
				c.DynamicFlags = 0
			}
			request.CorpseGuid = c.Guid
			got, err := s.ApproachLoot(context.Background(), request)
			if err != nil || got.Moving || got.Reason == "loot_approach_within_distance" || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.CMSGCastSpell) != 1 || countPackets(f, opcode.CMSGLoot) != 0 {
				t.Fatal("invalid leveling handoff borrowed a quest, owner or generic map route", mode, got, err)
			}
		})
	}
}

func TestColdridgeLevelingApproachRechecksSourceAndBaseAfterPlanning(t *testing.T) {
	for _, mode := range []string{"missing_context", "revision", "expired", "wrong_source", "destination_outside", "raw_body_outside"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, request := coldLevelingApproachRig(t)
			plans := 0
			s.planColdridge182Path = func(ctx context.Context, root string, from, to position) ([]position, error) {
				plans++
				path, err := coldStraight(ctx, root, from, to)
				c := f.world.LootableCorpses[0]
				switch mode {
				case "missing_context":
					coldLevelingQuest(f.world).QuestId = 0
				case "revision":
					f.world.Self.PositionRevision++
				case "expired":
					advance(3 * time.Minute)
				case "wrong_source":
					s.coldridgeLeveling.sourceGUID++
				case "destination_outside":
					c.Trajectory = &worldv1.CreatureTrajectory{Known: true, Stopped: true, DestinationKnown: true, DestinationX: c.X, DestinationY: 524.99, DestinationZ: c.Z}
				case "raw_body_outside":
					c.Y = 524.99
				}
				return path, err
			}
			got, err := s.ApproachLoot(context.Background(), request)
			// Expiring during a plan can also expire the exact interaction token.
			if plans != 1 || err == nil && got.Moving || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.CMSGLoot) != 0 {
				t.Fatal("post-plan disproof allowed a stale source/base to move", mode, plans, got, err)
			}
		})
	}
}

func TestColdridgeLevelingCorpseMovementExpiresBeforeItsLongerLootLease(t *testing.T) {
	for _, boundary := range []string{"clock", "fresh_snapshot"} {
		for _, movement := range []string{"start", "heartbeat"} {
			t.Run(boundary+"/"+movement, func(t *testing.T) {
				s, f, advance, request := coldLevelingApproachRig(t)
				f.world.Self.Level = 5
				advance(s.coldridgeLeveling.until.Sub(s.now()) - 500*time.Millisecond)
				lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot", LeaseMs: 15000})
				if err != nil {
					t.Fatal(err)
				}
				request.Token = lease.Token
				if !s.interactionUntil.After(s.coldridgeLeveling.until) || state(t, s).ColdridgeLeveling.RemainingMs != 500 {
					t.Fatal("fixture did not retain500ms handoff under a fresh15s lease", state(t, s))
				}
				if movement == "heartbeat" {
					got, err := s.ApproachLoot(context.Background(), request)
					if err != nil || !got.Moving || countPackets(f, opcode.MSGMoveStartForward) != 1 {
						t.Fatal("fixture did not begin valid level5 corpse cleanup", got, err)
					}
					advance(200 * time.Millisecond)
				}
				start, heartbeat, facing, stopped := countPackets(f, opcode.MSGMoveStartForward), countPackets(f, opcode.MSGMoveHeartbeat), countPackets(f, opcode.MSGMoveSetFacing), countPackets(f, opcode.MSGMoveStop)
				fired := coldLevelingExpireOnMovement(s, f, boundary, func() { advance(s.coldridgeLeveling.until.Sub(s.now())) })
				got, err := s.ApproachLoot(context.Background(), request)
				if err != nil || !*fired || got.Moving || !s.now().Before(s.interactionUntil) || countPackets(f, opcode.MSGMoveStartForward) != start || countPackets(f, opcode.MSGMoveHeartbeat) != heartbeat || countPackets(f, opcode.MSGMoveSetFacing) != facing || countPackets(f, opcode.CMSGCastSpell) != 1 || countPackets(f, opcode.CMSGLoot) != 0 || got.ColdridgeLeveling.RemainingMs != 0 || !got.ColdridgeLeveling.LevelFiveObserved || got.ColdridgeLeveling.SourceGuid != request.CorpseGuid {
					t.Fatal("a live loot lease extended expired level5 corpse movement", got, err, f.packets)
				}
				if movement == "heartbeat" && countPackets(f, opcode.MSGMoveStop) != stopped+1 {
					t.Fatal("handoff expiry blocked cleanup STOP", got, f.packets)
				}
			})
		}
	}
}

func TestHistoricalColdridgeLevelingRecordDoesNotFenceLaterOwnerMovement(t *testing.T) {
	for _, owner := range []string{"exploration", "training"} {
		t.Run(owner, func(t *testing.T) {
			s, f, advance, _ := coldLevelingApproachRig(t)
			advance(s.coldridgeLeveling.until.Sub(s.now()))
			s.state.InteractionOwner = owner
			if s.state.Practicing || !s.coldridgeGoal.leveling || !s.coldridgeLeveling.pullReserved || s.now().Before(s.coldridgeLeveling.until) {
				t.Fatal("fixture did not retain an expired, inactive leveling record")
			}
			// This tests only noninterference of the new helper. The owner's
			// ordinary lease/navigation gates still govern actual later movement.
			if !s.coldridgeLevelingMovementAllowedLocked(f.world, cold182TestBaseline) {
				t.Fatal("historical leveling record fenced later owner travel", owner)
			}
		})
	}
}

func TestColdridgeLevelingCorpseFinalMovementWorldRejectsUnsafeOwnState(t *testing.T) {
	for _, movement := range []string{"start", "heartbeat"} {
		for _, mode := range []string{"dead", "alive_unknown", "ground_unknown", "non_ground_movement", "extra_movement", "unsafe_unit_flag", "busy_unit_flag", "channel_spell", "channel_object", "health_unknown", "health_zero", "combat", "speed_unknown", "speed3.5", "speedNaN", "speedInf"} {
			t.Run(movement+"/"+mode, func(t *testing.T) {
				s, f, advance, request := coldLevelingApproachRig(t)
				f.world.Self.Level = 5
				if movement == "heartbeat" {
					got, err := s.ApproachLoot(context.Background(), request)
					if err != nil || !got.Moving || countPackets(f, opcode.MSGMoveStartForward) != 1 {
						t.Fatal("fixture did not start safe level5 corpse cleanup", got, err)
					}
					advance(200 * time.Millisecond)
				}
				start, heartbeat, facing, stopped := countPackets(f, opcode.MSGMoveStartForward), countPackets(f, opcode.MSGMoveHeartbeat), countPackets(f, opcode.MSGMoveSetFacing), countPackets(f, opcode.MSGMoveStop)
				fired := false
				f.onClock = func() {
					f.onClock = nil
					fired = true
					coldLevelingDisproveFinalMovementWorld(f, mode)
				}
				got, err := s.ApproachLoot(context.Background(), request)
				if err != nil || !fired || got.Moving || countPackets(f, opcode.MSGMoveStartForward) != start || countPackets(f, opcode.MSGMoveHeartbeat) != heartbeat || countPackets(f, opcode.MSGMoveSetFacing) != facing || countPackets(f, opcode.CMSGCastSpell) != 1 || countPackets(f, opcode.CMSGLoot) != 0 || got.ColdridgeLeveling.SourceGuid != request.CorpseGuid {
					t.Fatal("unsafe final body or combat allowed retained-corpse movement", mode, got, err, f.packets)
				}
				if movement == "heartbeat" && countPackets(f, opcode.MSGMoveStop) != stopped+1 {
					t.Fatal("unsafe corpse cleanup prevented its bounded STOP", mode, got, f.packets)
				}
			})
		}
	}
}
