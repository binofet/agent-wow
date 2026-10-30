package buddyactions

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func coldThreatApproachRig(t *testing.T, entry uint32) (*Service, *fakeSession, func(time.Duration), *actionsv1.ApproachLootRequest) {
	t.Helper()
	s, f, advance := coldThreatRig(t, entry, 1, 0)
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{}); err != nil {
		t.Fatal(err)
	}
	c := coldThreatCreature(entry, -6230)
	c.Health, c.Alive, c.DynamicFlags = 0, false, 1
	f.world.NearbyCreatures = nil
	f.world.LootableCorpses = []*worldv1.CreatureState{c}
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot", LeaseMs: 15000})
	if err != nil {
		t.Fatal(err)
	}
	return s, f, advance, &actionsv1.ApproachLootRequest{Token: lease.Token, CorpseGuid: c.Guid}
}

func TestColdridgeThreatOwnerlessCorpseApproachUsesPinnedGoalAndBoundedRoute(t *testing.T) {
	for _, entry := range []uint32{707, 724} {
		t.Run(fmt.Sprint(entry), func(t *testing.T) {
			s, f, advance, request := coldThreatApproachRig(t, entry)
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
			if got.Moving || got.Reason != "loot_approach_within_distance" || !got.NavAvailable || got.Position.X < -6233.1 || got.Position.X > -6231.5 || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.CMSGLoot) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 || s.coldridgeGoal.questID != 170 {
				t.Fatal("bounded ownerless approach lost its pinned170 scope", got)
			}
		})
	}
}

func TestColdridgeThreatCorpseRequiresCurrentPinnedValidOwnGoalAndExactProfile(t *testing.T) {
	for _, mode := range []string{"missing_quest", "other_quest", "unknown_log", "failed_goal", "malformed_counts", "regressed_counts", "unpinned", "wrong_pin", "added179_corpse705", "entry705", "entry706", "other_entry", "guid_entry", "guid_type", "guid_zero_low", "wrong_map", "level_unknown", "wrong_level", "wrong_max_health", "alive", "health_unknown", "not_lootable", "far", "vertical", "no_assets", "partial_route", "route_bounds", "expired_goal"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, request := coldThreatApproachRig(t, 707)
			c, q := f.world.LootableCorpses[0], coldThreatQuest(f)
			switch mode {
			case "missing_quest":
				q.QuestId = 0
			case "other_quest":
				q.QuestId = 233
			case "unknown_log":
				f.world.SelfQuestLogKnown = false
			case "failed_goal":
				q.Failed, q.State = true, 2
			case "malformed_counts":
				q.ObjectiveCounts = []uint32{1, 0}
			case "regressed_counts":
				q.ObjectiveCounts[0] = 0
			case "unpinned":
				s.coldridgeGoal = coldridgePracticeGoal{}
			case "wrong_pin":
				s.coldridgeGoal = coldridgePracticeGoal{questID: 179}
			case "added179_corpse705":
				f.world.SelfQuestLog[1].QuestId = 179
				c.Entry, c.Guid = 705, coldridgeWolfGUID
			case "entry705":
				c.Entry, c.Guid = 705, coldridgeWolfGUID
			case "entry706":
				c.Entry, c.Guid = 706, coldThreatGUID(706)
			case "other_entry":
				c.Entry, c.Guid = 299, coldThreatGUID(299)
			case "guid_entry":
				c.Guid = coldThreatGUID(724)
			case "guid_type":
				c.Guid = uint64(0xf150)<<48 | uint64(707)<<24 | 1
			case "guid_zero_low":
				c.Guid = uint64(0xf130)<<48 | uint64(707)<<24
			case "wrong_map":
				c.MapId = 1
			case "level_unknown":
				c.LevelKnown = false
			case "wrong_level":
				c.Level = 3
			case "wrong_max_health":
				c.MaxHealth = 56
			case "alive":
				c.Health, c.Alive = 55, true
			case "health_unknown":
				c.HealthKnown = false
			case "not_lootable":
				c.DynamicFlags = 0
			case "far":
				c.X = -6227
			case "vertical":
				c.Z += 1.6
			case "no_assets":
				s.navDataDir = ""
			case "partial_route":
				s.planColdridgePath = func(context.Context, string, position, position) ([]position, error) {
					return nil, errors.New("mock partial")
				}
			case "route_bounds":
				s.planColdridgePath = func(_ context.Context, _ string, from, to position) ([]position, error) {
					return []position{from, {x: -6400, y: 331, z: 383}, to}, nil
				}
			case "expired_goal":
				advance(11 * time.Minute)
				// A fresh loot lease must not renew an expired practice goal.
				lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot", LeaseMs: 15000})
				if err != nil {
					t.Fatal(err)
				}
				request.Token = lease.Token
			}
			request.CorpseGuid = c.Guid
			got, err := s.ApproachLoot(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if got != nil && got.Moving || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.CMSGLoot) != 0 {
				t.Fatal("unsupported corpse/goal started movement or looted", mode, got, err)
			}
		})
	}
}

func TestColdridgeThreatCorpseApproachRechecksOwnGoalAfterPlanning(t *testing.T) {
	for _, mode := range []string{"quest_removed", "counter_regressed", "new_progress_regressed", "failed", "target_changed", "position_changed"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, request := coldThreatApproachRig(t, 724)
			if mode == "new_progress_regressed" {
				coldThreatQuest(f).ObjectiveCounts[0] = 3
			}
			s.planColdridgePath = func(ctx context.Context, root string, from, to position) ([]position, error) {
				path, err := coldStraight(ctx, root, from, to)
				switch mode {
				case "quest_removed":
					coldThreatQuest(f).QuestId = 0
				case "counter_regressed":
					coldThreatQuest(f).ObjectiveCounts[0] = 0
				case "new_progress_regressed":
					coldThreatQuest(f).ObjectiveCounts[0] = 2
				case "failed":
					coldThreatQuest(f).Failed = true
				case "target_changed":
					f.world.LootableCorpses[0].Entry = 707
				case "position_changed":
					f.world.Self.X += 4
					f.world.Self.PositionRevision++
				}
				return path, err
			}
			got, err := s.ApproachLoot(context.Background(), request)
			if err != nil || got.Moving || countPackets(f, opcode.MSGMoveStartForward) != 0 {
				t.Fatal("route reused stale goal/target/position", mode, got, err)
			}
		})
	}
}

func TestColdridgeThreatFinalCorpseCanBeApproachedAfterObjectiveCompletion(t *testing.T) {
	s, f, _, request := coldThreatApproachRig(t, 724)
	q := coldThreatQuest(f)
	q.ObjectiveCounts, q.Complete, q.State = []uint32{6, 6, 0, 0}, true, 1
	got, err := s.ApproachLoot(context.Background(), request)
	if err != nil || !got.Moving || countPackets(f, opcode.CMSGLoot) != 0 {
		t.Fatal("actual final corpse became ineligible solely because objective completed", got, err)
	}
}
