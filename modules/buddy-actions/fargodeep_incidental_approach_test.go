package buddyactions

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
)

func TestIncidentalFargodeepApproachNeedsNoHistoricalEncounterForActualPartyCorpse(t *testing.T) {
	for _, guid := range []uint64{1, 4, 5} {
		s, f, _, r, advance := fargodeepApproachRig(t, guid)
		s.fargodeep = fargodeepEncounter{}
		for i := 0; i < 12; i++ {
			out, err := s.ApproachLoot(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if out.Reason == "loot_approach_within_distance" {
				break
			}
			advance(200 * time.Millisecond)
		}
		got := state(t, s)
		if got.Reason != "loot_approach_within_distance" || got.Moving || got.Fargodeep.TargetGuid != 0 || got.Fargodeep.Phase != "" || countPackets(f, opcode.CMSGLoot) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 {
			t.Fatal("incidental approach failed or fabricated encounter/mutation", got)
		}
	}
}

func TestIncidentalFargodeepApproachRetainsPartySafetyAndIncompleteOwnGoal(t *testing.T) {
	for name, change := range map[string]func(*Service, *fakeSession, *worldv1.CreatureState){
		"complete": func(s *Service, f *fakeSession, c *worldv1.CreatureState) {
			for _, q := range f.world.SelfQuestLog[:2] {
				q.Complete, q.State = true, 1
			}
		},
		"party_offline": func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.party.Group.Members[0].Online = false },
		"peer_injured":  func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.world.NearbyPlayers[0].Health = 84 },
		"peer_far": func(s *Service, f *fakeSession, c *worldv1.CreatureState) {
			f.world.NearbyPlayers[0].X = s.pos.x + 35.01
		},
		"peer_moving": func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.world.NearbyPlayers[0].MovementFlags = 1 },
		"recovery": func(s *Service, f *fakeSession, c *worldv1.CreatureState) {
			s.state.Recovery = &actionsv1.RecoveryState{Active: true}
		},
		"cast":    func(s *Service, f *fakeSession, c *worldv1.CreatureState) { s.state.CastPending = true },
		"mission": func(s *Service, f *fakeSession, c *worldv1.CreatureState) { s.fargodeep.active = true },
		"tap":     func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.DynamicFlags = 1 },
		"living":  func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.Health = 1; c.Alive = true },
	} {
		t.Run(name, func(t *testing.T) {
			s, f, c, r, _ := fargodeepApproachRig(t, 5)
			s.fargodeep = fargodeepEncounter{}
			change(s, f, c)
			out, e := s.ApproachLoot(context.Background(), r)
			if e == nil && out.Moving || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal("unsafe incidental movement", out, e)
			}
		})
	}
}

func TestIncidentalFargodeepApproachRechecksCurrentPartyAfterPhysicalPlan(t *testing.T) {
	for _, mode := range []string{"party", "tap", "injured", "complete", "lease"} {
		t.Run(mode, func(t *testing.T) {
			s, f, c, r, advance := fargodeepApproachRig(t, 5)
			s.fargodeep = fargodeepEncounter{}
			s.planMapPath = func(_ context.Context, _ string, _ uint32, a, b position) ([]position, error) {
				switch mode {
				case "party":
					f.party.Group.Members[0].Online = false
				case "tap":
					c.DynamicFlags = 1
				case "injured":
					f.world.NearbyPlayers[0].Health = 1
				case "complete":
					for _, q := range f.world.SelfQuestLog[:2] {
						q.Complete, q.State = true, 1
					}
				case "lease":
					advance(30 * time.Second)
				}
				return []position{a, b}, nil
			}
			out, e := s.ApproachLoot(context.Background(), r)
			if e == nil && out.Moving || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal("post-plan authority change moved", out, e)
			}
		})
	}
}

func TestIncidentalFargodeepApproachFinalClockGuardAndOwnedStop(t *testing.T) {
	for _, moving := range []bool{false, true} {
		for name, change := range map[string]func(*Service, *fakeSession, *worldv1.CreatureState, func(time.Duration)){
			"self_combat": func(s *Service, f *fakeSession, c *worldv1.CreatureState, advance func(time.Duration)) {
				f.world.Self.UnitFlags |= 0x80000
			},
			"peer_combat": func(s *Service, f *fakeSession, c *worldv1.CreatureState, advance func(time.Duration)) {
				f.world.NearbyPlayers[0].UnitFlags |= 0x80000
			},
			"peer_injured": func(s *Service, f *fakeSession, c *worldv1.CreatureState, advance func(time.Duration)) {
				f.world.NearbyPlayers[0].Health = 84
			},
			"party_offline": func(s *Service, f *fakeSession, c *worldv1.CreatureState, advance func(time.Duration)) {
				f.party.Group.Members[0].Online = false
			},
			"tap_lost": func(s *Service, f *fakeSession, c *worldv1.CreatureState, advance func(time.Duration)) {
				c.DynamicFlags = 1
			},
			"corpse_revived": func(s *Service, f *fakeSession, c *worldv1.CreatureState, advance func(time.Duration)) {
				c.Health, c.Alive = 1, true
			},
			"corpse_changed": func(s *Service, f *fakeSession, c *worldv1.CreatureState, advance func(time.Duration)) { c.X += .1 },
			"corpse_revision": func(s *Service, f *fakeSession, c *worldv1.CreatureState, advance func(time.Duration)) {
				c.PositionRevision++
			},
			"corpse_absent": func(s *Service, f *fakeSession, c *worldv1.CreatureState, advance func(time.Duration)) {
				f.world.LootableCorpses = nil
			},
			"self_correction": func(s *Service, f *fakeSession, c *worldv1.CreatureState, advance func(time.Duration)) {
				f.world.Self.PositionRevision++
			},
			"lease_expired": func(s *Service, f *fakeSession, c *worldv1.CreatureState, advance func(time.Duration)) {
				advance(30 * time.Second)
			},
			"cast": func(s *Service, f *fakeSession, c *worldv1.CreatureState, advance func(time.Duration)) {
				s.state.CastPending = true
			},
			"autoattack": func(s *Service, f *fakeSession, c *worldv1.CreatureState, advance func(time.Duration)) {
				s.state.AutoAttacking = true
			},
			"recovery": func(s *Service, f *fakeSession, c *worldv1.CreatureState, advance func(time.Duration)) {
				s.state.Recovery = &actionsv1.RecoveryState{Active: true}
			},
			"complete": func(s *Service, f *fakeSession, c *worldv1.CreatureState, advance func(time.Duration)) {
				for _, q := range f.world.SelfQuestLog[:2] {
					q.Complete, q.State = true, 1
				}
			},
			"token_replaced": func(s *Service, f *fakeSession, c *worldv1.CreatureState, advance func(time.Duration)) {
				s.interactionToken = "replacement"
				s.state.InteractionOwner = "professions"
			},
		} {
			label := "start/" + name
			if moving {
				label = "heartbeat/" + name
			}
			t.Run(label, func(t *testing.T) {
				s, f, c, r, advance := fargodeepApproachRig(t, 5)
				s.fargodeep = fargodeepEncounter{}
				if moving {
					out, err := s.ApproachLoot(context.Background(), r)
					if err != nil || !out.Moving {
						t.Fatal(out, err)
					}
					advance(200 * time.Millisecond)
				}
				starts, beats := countPackets(f, opcode.MSGMoveStartForward), countPackets(f, opcode.MSGMoveHeartbeat)
				f.onClock = func() { f.onClock = nil; change(s, f, c, advance) }
				out, err := s.ApproachLoot(context.Background(), r)
				if err != nil || countPackets(f, opcode.MSGMoveStartForward) != starts || countPackets(f, opcode.MSGMoveHeartbeat) != beats || s.lootApproachGuard != nil {
					t.Fatal("authority loss emitted movement or leaked guard", out, err, f.packets)
				}
				stops := countPackets(f, opcode.MSGMoveStop)
				if moving && name != "token_replaced" && (stops != 1 || out.Moving || out.MotionUnconfirmed) {
					t.Fatal("owned unsafe approach did not stop", out, f.packets)
				}
				if name == "token_replaced" && stops != 0 {
					t.Fatal("stopped replacement lease", f.packets)
				}
			})
		}
	}
}

type incidentalPartyCallback struct {
	*fakeSession
	afterParty func()
}

func (f *incidentalPartyCallback) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	out, err := f.fakeSession.InvokeModule(ctx, r, opts...)
	if r.Module == "buddy-party" && f.afterParty != nil {
		f.afterParty()
	}
	return out, err
}

func TestIncidentalFargodeepApproachWorldReadFollowsFinalPartyCallback(t *testing.T) {
	s, f, _, r, _ := fargodeepApproachRig(t, 5)
	s.fargodeep = fargodeepEncounter{}
	clockSeen := false
	f.onClock = func() { clockSeen = true }
	s.session = &incidentalPartyCallback{fakeSession: f, afterParty: func() {
		if clockSeen {
			f.world.Self.UnitFlags |= 0x80000
		}
	}}
	out, err := s.ApproachLoot(context.Background(), r)
	if err != nil || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || out.Moving {
		t.Fatal("final party callback hid current combat", out, err)
	}
}

func TestFargodeepRetainedCorpseApproachAlsoRechecksFinalClock(t *testing.T) {
	s, f, _, r, _ := fargodeepApproachRig(t, 5)
	for _, q := range f.world.SelfQuestLog[:2] {
		q.Complete, q.State = true, 1
	}
	f.onClock = func() { f.onClock = nil; f.world.Self.UnitFlags |= 0x80000 }
	out, err := s.ApproachLoot(context.Background(), r)
	if err != nil || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || out.Moving {
		t.Fatal("retained encounter bypassed final combat guard", out, err)
	}
}
