package buddyactions

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func cold183ApproachRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), *actionsv1.ApproachLootRequest) {
	t.Helper()
	s, f, advance := cold183Rig(t, 2)
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{}); err != nil {
		t.Fatal(err)
	}
	c := cold183Creature(-6230)
	c.Health, c.Alive, c.DynamicFlags = 0, false, 1
	f.world.NearbyCreatures = nil
	f.world.LootableCorpses = append(f.world.LootableCorpses, c)
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot", LeaseMs: 15000})
	if err != nil {
		t.Fatal(err)
	}
	return s, f, advance, &actionsv1.ApproachLootRequest{Token: lease.Token, CorpseGuid: c.Guid}
}

func TestColdridge183ApproachUsesExactPinnedProfileAndAllowsLastCorpse(t *testing.T) {
	for _, complete := range []bool{false, true} {
		s, f, advance, request := cold183ApproachRig(t)
		if complete {
			q := cold183Quest(f.world)
			q.ObjectiveCounts[0], q.Complete, q.State = 12, true, 1
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
		if got.Moving || got.Reason != "loot_approach_within_distance" || !got.NavAvailable || s.coldridgeGoal.questID != 183 || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.CMSGLoot) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 {
			t.Fatal("bounded183 approach lost goal identity or claimed loot", complete, got)
		}
	}
}

func TestColdridge183ApproachCannotBorrowOtherQuestsOrCorpseProfiles(t *testing.T) {
	for _, mode := range []string{"removed_with_179", "removed_with_170", "failed_with_179", "failed_with_170", "regressed", "malformed", "unknown_log", "unpinned", "wrong_pin", "entry705", "entry707", "entry706", "wrong_guid", "wrong_type", "zero_low", "wrong_map", "wrong_level", "wrong_health", "wrong_faction", "level2_self", "expired", "not_lootable", "living"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, request := cold183ApproachRig(t)
			q, c := cold183Quest(f.world), f.world.LootableCorpses[0]
			switch mode {
			case "removed_with_179", "removed_with_170", "failed_with_179", "failed_with_170":
				other := uint32(179)
				if mode == "removed_with_170" || mode == "failed_with_170" {
					other = 170
				}
				f.world.SelfQuestLog[10].QuestId = other
				if mode == "removed_with_179" || mode == "removed_with_170" {
					q.QuestId = 0
				} else {
					q.Failed = true
				}
			case "regressed":
				q.ObjectiveCounts[0] = 1
			case "malformed":
				q.ObjectiveCounts[1] = 1
			case "unknown_log":
				f.world.SelfQuestLogKnown = false
			case "unpinned":
				s.coldridgeGoal = coldridgePracticeGoal{}
			case "wrong_pin":
				s.coldridgeGoal = coldridgePracticeGoal{questID: 170}
			case "entry705":
				f.world.SelfQuestLog[20].QuestId = 179
				c.Entry, c.Guid = 705, coldridgeWolfGUID
			case "entry707":
				f.world.SelfQuestLog[10].QuestId = 170
				c.Entry, c.Guid = 707, coldThreatGUID(707)
			case "entry706":
				c.Entry, c.Guid = 706, coldThreatGUID(706)
			case "wrong_guid":
				c.Guid = coldThreatGUID(724)
			case "wrong_type":
				c.Guid = uint64(0xf150)<<48 | uint64(708)<<24 | 1
			case "zero_low":
				c.Guid &^= 0xffffff
			case "wrong_map":
				c.MapId = 1
			case "wrong_level":
				c.Level = 2
			case "wrong_health":
				c.MaxHealth = 72
			case "wrong_faction":
				c.FactionTemplate = 36
			case "level2_self":
				f.world.Self.Level = 2
			case "expired":
				advance(10 * time.Minute)
				lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot", LeaseMs: 15000})
				if err != nil {
					t.Fatal(err)
				}
				request.Token = lease.Token
			case "not_lootable":
				c.DynamicFlags = 0
			case "living":
				c.Health, c.Alive = 71, true
			}
			request.CorpseGuid = c.Guid
			got, err := s.ApproachLoot(context.Background(), request)
			if err != nil || got.Moving || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.CMSGLoot) != 0 {
				t.Fatal("unsupported183 corpse/goal started movement or looted", mode, got, err)
			}
		})
	}
}

func TestColdridge183ApproachRechecksQuestAndPhysicalBaseAfterPlanning(t *testing.T) {
	for _, mode := range []string{"removed_with_other_quests", "regressed", "progress_then_regressed", "failed", "position_revision", "corpse_changed"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, request := cold183ApproachRig(t)
			if mode == "progress_then_regressed" {
				cold183Quest(f.world).ObjectiveCounts[0] = 4
			}
			s.planColdridgePath = func(ctx context.Context, root string, from, to position) ([]position, error) {
				path, err := coldStraight(ctx, root, from, to)
				q := cold183Quest(f.world)
				switch mode {
				case "removed_with_other_quests":
					q.QuestId = 0
					f.world.SelfQuestLog[10].QuestId, f.world.SelfQuestLog[20].QuestId = 170, 179
				case "regressed":
					q.ObjectiveCounts[0] = 1
				case "progress_then_regressed":
					q.ObjectiveCounts[0] = 3
				case "failed":
					q.Failed = true
				case "position_revision":
					f.world.Self.PositionRevision++
				case "corpse_changed":
					f.world.LootableCorpses[0].FactionTemplate = 36
				}
				return path, err
			}
			got, err := s.ApproachLoot(context.Background(), request)
			if err != nil || got.Moving || countPackets(f, opcode.MSGMoveStartForward) != 0 {
				t.Fatal("route reused disproven183 scope", mode, got, err)
			}
		})
	}
}

func TestColdridge183ApproachDoesNotExpandTheSealedRegion(t *testing.T) {
	for _, y := range []float32{701.332, 719.99316, 747.95306} {
		s, f, _, request := cold183ApproachRig(t)
		f.world.Self.Y = 699
		f.world.LootableCorpses[0].X, f.world.LootableCorpses[0].Y = -6238, y
		got, err := s.ApproachLoot(context.Background(), request)
		if err != nil || got.Moving || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.CMSGLoot) != 0 {
			t.Fatal("saved outside-region708 expanded corpse navigation scope", y, got, err)
		}
	}
}
