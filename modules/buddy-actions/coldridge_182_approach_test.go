package buddyactions

import (
	"context"
	"fmt"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func cold182ApproachRig(t *testing.T, bodyLevel uint32) (*Service, *fakeSession, func(time.Duration), *actionsv1.ApproachLootRequest) {
	t.Helper()
	s, f, advance := cold182Rig(t, 2, 4, bodyLevel)
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{}); err != nil {
		t.Fatal(err)
	}
	c := f.world.NearbyCreatures[0]
	c.X = cold182TestStage.x + 6
	c.Health, c.Alive, c.DynamicFlags = 0, false, 1
	f.world.NearbyCreatures = nil
	f.world.LootableCorpses = []*worldv1.CreatureState{c}
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot", LeaseMs: 15000})
	if err != nil {
		t.Fatal(err)
	}
	return s, f, advance, &actionsv1.ApproachLootRequest{Token: lease.Token, CorpseGuid: c.Guid}
}

func TestColdridge182ApproachCompleteActiveLastCorpseUsesItsPlanner(t *testing.T) {
	for _, bodyLevel := range []uint32{3, 4} {
		t.Run(fmt.Sprint(bodyLevel), func(t *testing.T) {
			s, f, advance, request := cold182ApproachRig(t, bodyLevel)
			q := cold182GoalQuest(f.world)
			q.ObjectiveCounts[0], q.Complete, q.State = 10, true, 1
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
			if plans == 0 || got.Moving || got.Reason != "loot_approach_within_distance" || !got.NavAvailable || s.coldridgeGoal.questID != 182 || s.coldridgeGoal.counts[0] != 10 || countPackets(f, opcode.MSGMoveStartForward) == 0 || countPackets(f, opcode.CMSGLoot) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("last706 approach lost its goal, planner or receipt separation", plans, got)
			}
		})
	}
}

func TestColdridge182ApproachInvalidPinCannotBorrow179OrNearbyOwner(t *testing.T) {
	for _, invalid := range []string{"removed", "failed", "expired"} {
		for _, body := range []string{"706", "179_705"} {
			t.Run(invalid+"/"+body, func(t *testing.T) {
				s, f, advance, request := cold182ApproachRig(t, 3)
				// Exercise the normal nearby-owner fallback, rather than allowing
				// independent snapshot stripping or a missing owner to hide a bypass.
				s.independent = false
				f.world.Owner = proto.Clone(f.world.Self).(*worldv1.PlayerState)
				f.world.Owner.Guid = ownerGUID
				f.world.Owner.X, f.world.Owner.Y, f.world.Owner.Z = s.pos.x, s.pos.y, s.pos.z
				if reason := safePlayers(f.world, 8); reason != "" || distance(s.pos, playerPosition(f.world.Owner)) > 30 {
					t.Fatal("fixture owner is not actually safe and nearby", reason)
				}
				q, c := cold182GoalQuest(f.world), f.world.LootableCorpses[0]
				if body == "179_705" {
					f.world.SelfQuestLog[20].QuestId = 179
					c.Entry, c.Guid, c.Level, c.MaxHealth, c.FactionTemplate = 705, coldridgeWolfGUID, 1, 42, 0
					request.CorpseGuid = c.Guid
				}
				switch invalid {
				case "removed":
					q.QuestId = 0
				case "failed":
					q.Failed = true
				case "expired":
					advance(10 * time.Minute)
					lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot", LeaseMs: 15000})
					if err != nil {
						t.Fatal(err)
					}
					request.Token = lease.Token
				}
				got, err := s.ApproachLoot(context.Background(), request)
				if err != nil || got.Reason != "loot_approach_bound_quest_182_scope_invalid" || got.Moving || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.CMSGLoot) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 {
					t.Fatal("invalid182 borrowed a legacy quest or generic owner's navigation", got, err)
				}
			})
		}
	}
}

func TestColdridge182ApproachRevalidatesAfterPlanBeforeMoving(t *testing.T) {
	for _, mode := range []string{"removed_with_other_quests", "failed", "regressed", "progress_then_regressed", "revision", "mixed_profile", "raw_body_outside", "known_destination_outside"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, request := cold182ApproachRig(t, 3)
			if mode == "progress_then_regressed" {
				cold182GoalQuest(f.world).ObjectiveCounts[0] = 3
			}
			plans := 0
			s.planColdridge182Path = func(ctx context.Context, root string, from, to position) ([]position, error) {
				plans++
				path, err := coldStraight(ctx, root, from, to)
				q, c := cold182GoalQuest(f.world), f.world.LootableCorpses[0]
				switch mode {
				case "removed_with_other_quests":
					q.QuestId = 0
					f.world.SelfQuestLog[10].QuestId, f.world.SelfQuestLog[20].QuestId = 170, 179
				case "failed":
					q.Failed = true
				case "regressed":
					q.ObjectiveCounts[0] = 1
				case "progress_then_regressed":
					q.ObjectiveCounts[0] = 2
				case "revision":
					f.world.Self.PositionRevision++
				case "mixed_profile":
					c.MaxHealth = 86
				case "raw_body_outside":
					c.X, c.Y, c.Z = -8900, 0, 100
					c.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 100, AgeMs: 100, DestinationKnown: true, DestinationX: to.x, DestinationY: to.y, DestinationZ: to.z}
				case "known_destination_outside":
					c.Trajectory = &worldv1.CreatureTrajectory{Known: true, Stopped: true, DestinationKnown: true, DestinationX: -8900, DestinationY: 0, DestinationZ: 100}
				}
				return path, err
			}
			got, err := s.ApproachLoot(context.Background(), request)
			if err != nil || plans != 1 || got.Moving || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.CMSGLoot) != 0 {
				t.Fatal("newer goal/position/body disproof was ignored after planning", plans, got, err)
			}
			if mode == "progress_then_regressed" && s.coldridgeGoal.counts[0] != 3 {
				t.Fatal("planning lost the just-observed counter highwater", s.coldridgeGoal)
			}
		})
	}
}
