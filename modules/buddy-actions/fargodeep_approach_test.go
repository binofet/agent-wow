package buddyactions

import (
	"context"
	"errors"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func fargodeepApproachRig(t *testing.T, guid uint64) (*Service, *fakeSession, *worldv1.CreatureState, *actionsv1.ApproachLootRequest, func(time.Duration)) {
	t.Helper()
	s, f, c, _, advance := fargodeepRig(t, guid)
	c.Alive = false
	c.Health = 0
	c.DynamicFlags = 1 | 8
	c.X = -9811
	f.world.LootableCorpses = []*worldv1.CreatureState{c}
	s.fargodeep = fargodeepEncounter{target: c.Guid, engager: 5, session: f.party.SessionId, phase: "target_death_observed"}
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot"})
	if err != nil {
		t.Fatal(err)
	}
	return s, f, c, &actionsv1.ApproachLootRequest{Token: lease.Token, CorpseGuid: c.Guid}, advance
}
func TestFargodeepCorpseApproachWithoutJoelRetainsCompleteQuestAndVerifiedRoute(t *testing.T) {
	for _, guid := range []uint64{1, 4, 5} {
		t.Run(string(rune('0'+guid)), func(t *testing.T) {
			s, f, c, r, advance := fargodeepApproachRig(t, guid)
			for _, q := range f.world.SelfQuestLog[:2] {
				q.State = 1
				q.Complete = true
			}
			for i := 0; i < 12; i++ {
				out, err := s.ApproachLoot(context.Background(), r)
				if err != nil || !out.NavAvailable || out.Position.MapId != 0 {
					t.Fatal(out, err)
				}
				if out.Reason == "loot_approach_within_distance" {
					break
				}
				advance(200 * time.Millisecond)
			}
			got := state(t, s)
			if got.Moving || got.MotionUnconfirmed || got.InteractionOwner != "loot" || got.Reason != "loot_approach_within_distance" || distance(s.pos, position{x: c.X, y: c.Y, z: c.Z}) > 3.01 || got.Fargodeep.Phase != "target_death_observed" {
				t.Fatal(got)
			}
			if countPackets(f, opcode.CMSGLoot) != 0 || countPackets(f, opcode.CMSGAutostoreLootItem) != 0 {
				t.Fatal("approach emitted loot mutation", f.packets)
			}
		})
	}
}
func TestFargodeepCorpseApproachRejectsEveryUnboundOrUnsafeException(t *testing.T) {
	for name, change := range map[string]func(*Service, *fakeSession, *worldv1.CreatureState, *actionsv1.ApproachLootRequest){
		"wrong token": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			r.Token = "foreign"
		},
		"wrong target": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			s.fargodeep.target++
		},
		"wrong phase": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			s.fargodeep.phase = "disabled"
		},
		"still active": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			s.fargodeep.active = true
		},
		"old session": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			s.fargodeep.session = "old"
		},
		"no quest": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			f.world.SelfQuestLog[0].QuestId = 0
			f.world.SelfQuestLog[1].QuestId = 0
		},
		"unknown quest": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			f.world.SelfQuestLogKnown = false
		},
		"failed quests": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			for _, q := range f.world.SelfQuestLog[:2] {
				q.State = 2
				q.Failed = true
			}
		},
		"unknown flags": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			c.UnitFlagsKnown = false
		},
		"unknown dynamic flags": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			c.DynamicFlagsKnown = false
		},
		"no tap": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			c.DynamicFlags = 1
		},
		"no lootable": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			c.DynamicFlags = 8
		},
		"alive": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			c.Alive = true
			c.Health = 20
		},
		"named boss": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			c.Entry = 327
		},
		"geomancer": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			c.Entry = 476
		},
		"high level": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			c.Level = 8
		},
		"unknown level": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			c.LevelKnown = false
		},
		"map mismatch": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			c.MapId = 1
		},
		"outside mission": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			c.X = -9699
		},
		"far": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			c.X = f.world.Self.X + 13
		},
		"vertical": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) { c.Z += 2 },
		"self combat": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			f.world.Self.UnitFlags = 0x80000
		},
		"ally combat": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			f.world.NearbyPlayers[0].UnitFlags = 0x80000
		},
		"party absent": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			f.party.Group.InGroup = false
		},
		"party offline": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.ApproachLootRequest) {
			f.party.Group.Members[0].Online = false
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, f, c, r, _ := fargodeepApproachRig(t, 5)
			if name == "wrong target" || name == "wrong phase" || name == "old session" {
				// COMPLETE needs the legacy exact retained encounter. Incomplete
				// own quests separately authorize actual incidental party corpses.
				for _, q := range f.world.SelfQuestLog[:2] {
					q.Complete, q.State = true, 1
				}
			}
			change(s, f, c, r)
			out, err := s.ApproachLoot(context.Background(), r)
			if (err == nil && out.Moving) || len(f.packets) != 0 {
				t.Fatal(out, err, f.packets)
			}
		})
	}
}
func TestFargodeepCorpseApproachRevalidatesMissionAfterPlanWithoutFallback(t *testing.T) {
	for _, mode := range []string{"missing data", "wall", "excess route", "target changed", "tap lost", "party lost", "lease expired", "self combat", "target revived"} {
		t.Run(mode, func(t *testing.T) {
			s, f, c, r, advance := fargodeepApproachRig(t, 5)
			s.planMapPath = func(_ context.Context, _ string, _ uint32, a, b position) ([]position, error) {
				switch mode {
				case "missing data":
					return nil, errors.New("missing")
				case "wall":
					return nil, errors.New("surface ray blocked")
				case "excess route":
					return []position{a, {x: a.x - 10, y: a.y, z: a.z}, b}, nil
				case "target changed":
					c.PositionRevision++
					c.X++
				case "tap lost":
					c.DynamicFlags = 1
				case "party lost":
					f.party.Group.InGroup = false
				case "lease expired":
					advance(30 * time.Second)
				case "self combat":
					f.world.Self.UnitFlags = 0x80000
				case "target revived":
					c.Alive = true
					c.Health = 100
				}
				return []position{a, b}, nil
			}
			out, err := s.ApproachLoot(context.Background(), r)
			if (err == nil && out.Moving) || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal(out, err, f.packets)
			}
		})
	}
}
func TestFargodeepCorpseApproachCancelStopsOnlyExactToken(t *testing.T) {
	s, f, _, r, _ := fargodeepApproachRig(t, 5)
	out, err := s.ApproachLoot(context.Background(), r)
	if err != nil || !out.Moving {
		t.Fatal(out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err = s.ApproachLoot(ctx, r)
	if err == nil || out == nil || out.Moving || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal(out, err, f.packets)
	}
	s, f, _, r, _ = fargodeepApproachRig(t, 5)
	s.state.Moving = true
	old := proto.Clone(r).(*actionsv1.ApproachLootRequest)
	s.interactionToken = "replacement-token"
	_, err = s.ApproachLoot(ctx, old)
	if err == nil || len(f.packets) != 0 || !s.state.Moving {
		t.Fatal("old cleanup stopped replacement", err, f.packets)
	}
}
