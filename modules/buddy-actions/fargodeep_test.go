package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func fargodeepRig(t *testing.T, guid uint64) (*Service, *fakeSession, *worldv1.CreatureState, *actionsv1.FargodeepEncounterRequest, func(time.Duration)) {
	t.Helper()
	class := map[uint64]uint32{1: 5, 4: 8, 5: 4}[guid]
	s, f, _, advance := selfDefenseRig(t, class)
	s.selfGUID = guid
	f.world.Self.Guid = guid
	s.state.Healing = class == 5
	s.healingUntil = s.now().Add(time.Hour)
	f.world.Self.UnitFlags = 0
	f.world.Self.X, f.world.Self.Y, f.world.Self.Z = -9820, 124, 5
	f.world.Self.Level = 6
	f.world.Self.Health, f.world.Self.MaxHealth = 100, 100
	f.world.Self.Power = 128
	f.world.Self.MaxPower = 128
	if class == 4 {
		f.world.Self.PowerType = 3
	}
	f.world.SelfQuestLogKnown = true
	f.world.SelfQuestLogRevision = 3
	f.world.SelfQuestLog = make([]*worldv1.QuestLogEntry, 25)
	for i := range f.world.SelfQuestLog {
		f.world.SelfQuestLog[i] = &worldv1.QuestLogEntry{Slot: uint32(i)}
	}
	f.world.SelfQuestLog[0].QuestId = 47
	f.world.SelfQuestLog[1].QuestId = 60
	f.world.NearbyPlayers = nil
	members := []*partyv1.Member{}
	for _, g := range []uint64{1, 4, 5} {
		members = append(members, &partyv1.Member{Guid: g, Self: g == guid, Online: true})
		if g == guid {
			continue
		}
		p := proto.Clone(f.world.Self).(*worldv1.PlayerState)
		p.Guid = g
		p.ClassId = map[uint64]uint32{1: 5, 4: 8, 5: 4}[g]
		p.X = -9819
		p.PowerType = 0
		if g == 5 {
			p.PowerType = 3
		}
		f.world.NearbyPlayers = append(f.world.NearbyPlayers, p)
	}
	f.party = &partyv1.StateResponse{SessionId: "test-fargodeep-body", Group: &partyv1.Group{Known: true, InGroup: true, Members: members}}
	target := &worldv1.CreatureState{Guid: 0xf130000000000000 | uint64(40)<<24 | 111, Entry: 40, Present: true, MapKnown: true, PositionKnown: true, X: -9817, Y: 124, Z: 5, PositionRevision: 1, LevelKnown: true, Level: 6, HealthKnown: true, Health: 100, MaxHealth: 100, AliveKnown: true, Alive: true, TargetKnown: true, UnitFlagsKnown: true, DynamicFlagsKnown: true, CombatReachKnown: true, CombatReach: 1.5}
	f.world.NearbyCreatures = []*worldv1.CreatureState{target}
	f.world.Owner = nil
	f.world.OwnerTarget = nil
	f.lookupCreatures = map[uint64]*worldv1.CreatureState{target.Guid: target}
	s.planMapPath = func(_ context.Context, _ string, _ uint32, a, b position) ([]position, error) {
		return []position{a, b}, nil
	}
	r := &actionsv1.FargodeepEncounterRequest{Enabled: true, TargetGuid: target.Guid, EngagerGuid: 5, ExpectedQuestLogRevision: 3, SessionId: f.party.SessionId}
	return s, f, target, r, advance
}
func armFargodeep(t *testing.T, s *Service, r *actionsv1.FargodeepEncounterRequest) {
	t.Helper()
	out, err := s.FargodeepEncounter(context.Background(), r)
	if err != nil || out.Fargodeep == nil || !out.Fargodeep.Active {
		t.Fatal(out, err)
	}
}
func TestFargodeepOnlyDesignatedEngagerPullsAfterCompleteVerifiedRoute(t *testing.T) {
	for _, guid := range []uint64{1, 4, 5} {
		t.Run(fmt.Sprint(guid), func(t *testing.T) {
			s, f, target, r, advance := fargodeepRig(t, guid)
			plans := 0
			s.planMapPath = func(_ context.Context, _ string, _ uint32, a, b position) ([]position, error) {
				plans++
				return []position{a, b}, nil
			}
			armFargodeep(t, s, r)
			s.Tick(context.Background())
			advance(200 * time.Millisecond)
			s.Tick(context.Background())
			got := state(t, s)
			if guid == 5 {
				if plans != 1 || countPackets(f, opcode.CMSGAttackswing) != 1 || got.LastCastTargetGuid != target.Guid || got.Fargodeep.Phase != "initial_attack_written_waiting_for_attack_start" {
					t.Fatal(got, plans, f.packets)
				}
			} else if plans != 0 || got.AssistAttempts != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || got.Fargodeep.Phase != "waiting_for_actual_party_attack_start" {
				t.Fatal(got, plans, f.packets)
			}
			if got.ServerAssistGo != 0 || got.AssistDamageEvents != 0 || got.MeleeDamageEvents != 0 {
				t.Fatal("write inferred actual damage", got)
			}
		})
	}
}
func TestFargodeepSupportsUseActualPartyAttackWithoutJoelAndCompleteOwnQuests(t *testing.T) {
	for _, guid := range []uint64{1, 4} {
		t.Run(fmt.Sprint(guid), func(t *testing.T) {
			s, f, target, r, _ := fargodeepRig(t, guid)
			for _, q := range f.world.SelfQuestLog[:2] {
				q.Complete = true
				q.State = 1
			}
			armFargodeep(t, s, r)
			target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, 5, 100
			target.DynamicFlags = 4 | 8
			for _, p := range f.world.NearbyPlayers {
				if p.Guid == 5 {
					p.UnitFlags = 0x80000
				}
			}
			s.Tick(context.Background())
			if got := state(t, s); got.AssistAttempts != 1 || got.LastCastTargetGuid != target.Guid || !got.Fargodeep.Active || countPackets(f, opcode.MSGMoveStartForward) != 0 {
				t.Fatal(got, f.packets)
			}
		})
	}
}
func TestFargodeepHealsPartyBeforeOffenseAndPreservesHealReserve(t *testing.T) {
	s, f, target, r, _ := fargodeepRig(t, 1)
	armFargodeep(t, s, r)
	target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, 5, 100
	for _, p := range f.world.NearbyPlayers {
		if p.Guid == 5 {
			p.Health = 20
			p.UnitFlags = 0x80000
		}
	}
	s.Tick(context.Background())
	if got := state(t, s); got.HealAttempts != 1 || got.LastCastSpellId != lesserHeal || got.LastCastTargetGuid != 5 || got.AssistAttempts != 0 {
		t.Fatal(got)
	}
	s, f, target, r, _ = fargodeepRig(t, 1)
	armFargodeep(t, s, r)
	target.AttackVictimKnown, target.AttackVictimGuid, target.AttackAgeMs = true, 5, 100
	f.world.Self.Power = 20
	s.Tick(context.Background())
	if got := state(t, s); got.AssistAttempts != 0 || got.Reason != "assist_mana_reserved_for_healing" {
		t.Fatal(got)
	}
}
func TestFargodeepRejectsUnknownQuestPartySourceRoleOrIdleOwnership(t *testing.T) {
	for name, change := range map[string]func(*Service, *fakeSession, *worldv1.CreatureState, *actionsv1.FargodeepEncounterRequest){
		"foreign body": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			r.SessionId = "old-body"
		},
		"old log": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			r.ExpectedQuestLogRevision--
		},
		"unknown unit flags": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			c.UnitFlagsKnown = false
		},
		"unknown dynamic flags": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			c.DynamicFlagsKnown = false
		},
		"unknown log": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			f.world.SelfQuestLogKnown = false
		},
		"duplicate slot": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			f.world.SelfQuestLog[2].Slot = 0
		},
		"no quest": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			f.world.SelfQuestLog[0].QuestId = 0
			f.world.SelfQuestLog[1].QuestId = 0
		},
		"complete engager": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			for _, q := range f.world.SelfQuestLog[:2] {
				q.State = 1
				q.Complete = true
			}
		},
		"bad class": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			f.world.Self.ClassId = 9
		},
		"unknown ally": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			f.world.NearbyPlayers = nil
		},
		"offline ally": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			f.party.Group.Members[0].Online = false
		},
		"foreign party self": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			f.party.Group.Members[0].Self = true
		},
		"Joel returned": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			f.world.Owner = &worldv1.PlayerState{Guid: 3, Present: true}
		},
		"geomancer": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			c.Entry = 476
		},
		"named boss": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			c.Entry = 327
		},
		"spoofed entry": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			c.Guid = 0xf130000000000000 | uint64(41)<<24 | 111
			r.TargetGuid = c.Guid
		},
		"high level": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			c.Level = 8
		},
		"absent": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			c.Present = false
		},
		"other map": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			c.MapId = 1
		},
		"lease": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			s.state.InteractionOwner = "loot"
			s.interactionUntil = s.now().Add(time.Minute)
		},
		"cast": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			s.state.CastPending = true
		},
		"assist disabled": func(s *Service, f *fakeSession, c *worldv1.CreatureState, r *actionsv1.FargodeepEncounterRequest) {
			s.state.Assisting = false
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, f, c, r, _ := fargodeepRig(t, 5)
			change(s, f, c, r)
			if got, err := s.FargodeepEncounter(context.Background(), r); err == nil || got != nil || len(f.packets) != 0 {
				t.Fatal(got, err, f.packets)
			}
		})
	}
}
func TestFargodeepInitialPullWaitsForRecoveryIsolationAndActualStationaryTarget(t *testing.T) {
	for name, change := range map[string]func(*Service, *fakeSession, *worldv1.CreatureState){
		"low ally HP":   func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.world.NearbyPlayers[0].Health = 80 },
		"low ally mana": func(s *Service, f *fakeSession, c *worldv1.CreatureState) { f.world.NearbyPlayers[0].Power = 1 },
		"unclaimed neighbor": func(s *Service, f *fakeSession, c *worldv1.CreatureState) {
			n := proto.Clone(c).(*worldv1.CreatureState)
			n.Guid++
			n.X += 3
			f.world.NearbyCreatures = append(f.world.NearbyCreatures, n)
		},
		"moving patrol":           func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.MovementFlags = 1 },
		"selected another player": func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.TargetGuid = 99 },
		"foreign tap":             func(s *Service, f *fakeSession, c *worldv1.CreatureState) { c.DynamicFlags = 4 },
	} {
		t.Run(name, func(t *testing.T) {
			s, f, c, r, _ := fargodeepRig(t, 5)
			armFargodeep(t, s, r)
			change(s, f, c)
			s.Tick(context.Background())
			if got := state(t, s); got.AssistAttempts != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || got.Moving {
				t.Fatal(got, f.packets)
			}
		})
	}
}
func TestFargodeepRouteFailureAndPlanRacesNeverPullOrMove(t *testing.T) {
	for _, mode := range []string{"missing assets", "partial", "foreign victim", "party lost", "quest complete", "correction", "map changed", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			s, f, c, r, advance := fargodeepRig(t, 5)
			armFargodeep(t, s, r)
			s.planMapPath = func(ctx context.Context, _ string, _ uint32, a, b position) ([]position, error) {
				switch mode {
				case "missing assets":
					return nil, errors.New("missing")
				case "partial":
					return []position{a, {x: 100}}, nil
				case "foreign victim":
					c.AttackVictimKnown = true
					c.AttackVictimGuid = 99
				case "party lost":
					f.party.Group.InGroup = false
				case "quest complete":
					for _, q := range f.world.SelfQuestLog[:2] {
						q.State = 1
						q.Complete = true
					}
				case "correction":
					f.world.Self.PositionRevision++
					f.world.Self.X = 20
				case "map changed":
					f.world.Self.MapId = 1
				case "deadline":
					return nil, context.DeadlineExceeded
				}
				return []position{a, b}, nil
			}
			s.Tick(context.Background())
			advance(200 * time.Millisecond)
			s.Tick(context.Background())
			if got := state(t, s); got.AssistAttempts != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 || countPackets(f, opcode.MSGMoveStartForward) != 0 || got.Moving {
				t.Fatal(got, f.packets)
			}
		})
	}
}
func TestFargodeepPrewriteCallbacksRevalidatePartyTargetQuestManaAndBody(t *testing.T) {
	for _, mode := range []string{"victim", "party", "quest", "mana", "correction", "map", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			s, f, c, r, advance := fargodeepRig(t, 4)
			r.EngagerGuid = 4
			armFargodeep(t, s, r)
			s.Tick(context.Background())
			advance(200 * time.Millisecond)
			fired := false
			f.onClock = func() {
				if fired {
					return
				}
				fired = true
				switch mode {
				case "victim":
					c.AttackVictimKnown = true
					c.AttackVictimGuid = 99
				case "party":
					f.party.Group.InGroup = false
				case "quest":
					for _, q := range f.world.SelfQuestLog[:2] {
						q.State = 1
						q.Complete = true
					}
				case "mana":
					f.world.Self.Power = 0
				case "correction":
					f.world.Self.PositionRevision++
					f.world.Self.X = 20
				case "map":
					f.world.Self.MapId = 1
				case "expiry":
					advance(3 * time.Minute)
				}
			}
			s.Tick(context.Background())
			if !fired || countPackets(f, opcode.CMSGCastSpell) != 0 || state(t, s).AssistAttempts != 0 {
				t.Fatal(mode, fired, state(t, s), f.packets)
			}
		})
	}
}
func TestFargodeepBoundCancellationDeathExpiryAndReplay(t *testing.T) {
	for _, mode := range []string{"disable", "follow stop", "assist stop", "logout", "expiry", "death", "target death"} {
		t.Run(mode, func(t *testing.T) {
			s, f, c, r, advance := fargodeepRig(t, 4)
			armFargodeep(t, s, r)
			switch mode {
			case "disable":
				r.Enabled = false
				s.FargodeepEncounter(context.Background(), r)
			case "follow stop":
				s.Follow(context.Background(), &actionsv1.ToggleRequest{})
			case "assist stop":
				s.Assist(context.Background(), &actionsv1.ToggleRequest{})
			case "logout":
				s.BeforeLogout(context.Background(), nil)
			case "expiry":
				advance(3 * time.Minute)
				s.Tick(context.Background())
			case "death":
				f.world.Self.Alive = false
				f.world.Self.Health = 0
				s.Tick(context.Background())
			case "target death":
				c.Alive = false
				c.Health = 0
				s.Tick(context.Background())
			}
			got := state(t, s)
			if got.Fargodeep.Active || got.Fargodeep.TargetGuid != c.Guid || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal(got, f.packets)
			}
			r.Enabled = true
			if _, err := s.FargodeepEncounter(context.Background(), r); err == nil {
				t.Fatal("same target replayed")
			}
		})
	}
	s, _, c, r, _ := fargodeepRig(t, 4)
	armFargodeep(t, s, r)
	wrong := proto.Clone(r).(*actionsv1.FargodeepEncounterRequest)
	wrong.Enabled = false
	wrong.TargetGuid = c.Guid + 1
	if _, err := s.FargodeepEncounter(context.Background(), wrong); err == nil || !state(t, s).Fargodeep.Active {
		t.Fatal("foreign cleanup stopped encounter")
	}
	if _, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "loot"}); err == nil {
		t.Fatal("lease stole encounter")
	}
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err == nil {
		t.Fatal("practice stole encounter")
	}
}
func TestFargodeepSupportRejectsTargetSelectionAndStaleOrForeignAttackProof(t *testing.T) {
	for _, mode := range []string{"selection only", "foreign victim", "stale", "unobserved victim", "additional attacker"} {
		t.Run(mode, func(t *testing.T) {
			s, f, c, r, _ := fargodeepRig(t, 4)
			armFargodeep(t, s, r)
			c.AttackVictimKnown, c.AttackVictimGuid, c.AttackAgeMs = true, 5, 100
			switch mode {
			case "selection only":
				c.AttackVictimKnown = false
				c.TargetGuid = 5
			case "foreign victim":
				c.AttackVictimGuid = 99
			case "stale":
				c.AttackAgeMs = 30001
			case "unobserved victim":
				f.world.NearbyPlayers = nil
			case "additional attacker":
				n := proto.Clone(c).(*worldv1.CreatureState)
				n.Guid++
				n.AttackVictimGuid = 1
				f.world.NearbyCreatures = append(f.world.NearbyCreatures, n)
			}
			s.Tick(context.Background())
			if got := state(t, s); got.AssistAttempts != 0 || got.Moving {
				t.Fatal(got, f.packets)
			}
		})
	}
}
func TestFargodeepNoSameBodyTargetRetryAfterAmbiguousWrite(t *testing.T) {
	s, f, c, r, advance := fargodeepRig(t, 4)
	r.EngagerGuid = 4
	armFargodeep(t, s, r)
	s.Tick(context.Background())
	advance(200 * time.Millisecond)
	f.failSend = true
	s.Tick(context.Background())
	writes := countPackets(f, opcode.CMSGCastSpell)
	f.failSend = false
	r.Enabled = false
	s.FargodeepEncounter(context.Background(), r)
	r.Enabled = true
	if _, err := s.FargodeepEncounter(context.Background(), r); err == nil || countPackets(f, opcode.CMSGCastSpell) != writes {
		t.Fatal("ambiguous target retried", c.Guid, writes, state(t, s))
	}
}
func TestFargodeepNormalAttackPacketTargetsBoundActualGuid(t *testing.T) {
	s, f, c, r, advance := fargodeepRig(t, 5)
	armFargodeep(t, s, r)
	s.Tick(context.Background())
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	for _, p := range f.packets {
		if p.Opcode == opcode.CMSGAttackswing && (len(p.Payload) != 8 || binary.LittleEndian.Uint64(p.Payload) != c.Guid) {
			t.Fatal(p)
		}
	}
}

func TestFargodeepGCDDoesNotConsumeTheOnlyInitialAttack(t *testing.T) {
	s, f, _, r, advance := fargodeepRig(t, 4)
	r.EngagerGuid = 4
	armFargodeep(t, s, r)
	s.Tick(context.Background())
	s.nextAssist = s.now().Add(time.Second)
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if s.fargodeep.pulled || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("GCD falsely consumed pull", state(t, s))
	}
	advance(time.Second)
	s.Tick(context.Background())
	if !s.fargodeep.pulled || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("ready initial cast never issued", state(t, s))
	}
}
func TestFargodeepIdleOrTerminalCleanupNeverStopsUnrelatedMovement(t *testing.T) {
	s, f, c, r, _ := fargodeepRig(t, 4)
	armFargodeep(t, s, r)
	c.Alive = false
	c.Health = 0
	s.Tick(context.Background())
	before := state(t, s)
	s.state.Moving = true
	r.Enabled = false
	out, err := s.FargodeepEncounter(context.Background(), r)
	if err != nil || out.Fargodeep.Phase != "target_death_observed" || out.Fargodeep.TargetGuid != c.Guid || !out.Moving || out.TransportWrites != before.TransportWrites || len(f.packets) != 0 {
		t.Fatal(out, err, f.packets)
	}
	s, f, _, r, _ = fargodeepRig(t, 4)
	r.Enabled = false
	s.state.Moving = true
	out, err = s.FargodeepEncounter(context.Background(), r)
	if err != nil || !out.Moving || len(f.packets) != 0 {
		t.Fatal("stale cleanup stopped unrelated movement", out, err)
	}
}
func TestFargodeepReservationsAreBoundedAndLifetimeScoped(t *testing.T) {
	s, _, c, r, _ := fargodeepRig(t, 4)
	s.fargodeepSeen = map[uint64]bool{}
	for i := uint64(0); i < 128; i++ {
		s.fargodeepSeen[c.Guid+100+i] = true
	}
	if _, err := s.FargodeepEncounter(context.Background(), r); err == nil || len(s.fargodeepSeen) != 128 {
		t.Fatal("reservation bound evicted history")
	}
}
func TestFargodeepActualTunnelerAndExplicitMissionScene(t *testing.T) {
	s, f, c, r, _ := fargodeepRig(t, 4)
	old := c.Guid
	c.Entry = 475
	c.Guid = 0xf130000000000000 | uint64(475)<<24 | 111
	r.TargetGuid = c.Guid
	delete(f.lookupCreatures, old)
	f.lookupCreatures[c.Guid] = c
	armFargodeep(t, s, r)
	for _, p := range []position{{x: -10001, y: 124, z: 5}, {x: -9699, y: 124, z: 5}, {x: -9820, y: 49, z: 5}, {x: -9820, y: 321, z: 5}, {x: -9820, y: 124, z: -11}, {x: -9820, y: 124, z: 66}} {
		if fargodeepArea(p) {
			t.Fatal("mission bounds escaped", p)
		}
	}
}

func TestFargodeepActualAggroDuringApproachBecomesDefense(t *testing.T) {
	s, f, c, r, advance := fargodeepRig(t, 5)
	c.X = -9808
	armFargodeep(t, s, r)
	s.Tick(context.Background())
	advance(200 * time.Millisecond)
	c.X = -9814
	c.PositionRevision++
	c.AttackVictimKnown = true
	c.AttackVictimGuid = 5
	c.AttackAgeMs = 100
	f.world.Self.UnitFlags = 0x80000
	s.Tick(context.Background())
	if got := state(t, s); !got.Fargodeep.Active || !s.fargodeep.pulled || got.Fargodeep.Phase != "verified_route_waiting_fresh_frame" {
		t.Fatal("actual aggro demanded fresh initial-pull readiness", got)
	}
}

func TestFargodeepRogueRechecksActualEnergyAfterFacing(t *testing.T) {
	for _, mode := range []string{"low energy", "unknown energy", "wrong power type"} {
		t.Run(mode, func(t *testing.T) {
			s, f, c, r, _ := fargodeepRig(t, 5)
			armFargodeep(t, s, r)
			c.AttackVictimKnown, c.AttackVictimGuid, c.AttackAgeMs = true, 5, 100
			f.world.Self.UnitFlags = 0x80000
			f.onClock = func() {
				switch mode {
				case "low energy":
					f.world.Self.Power = 0
				case "unknown energy":
					f.world.Self.PowerKnown = false
				case "wrong power type":
					f.world.Self.PowerType = 0
				}
			}
			s.Tick(context.Background())
			if got := state(t, s); countPackets(f, opcode.CMSGAttackswing) != 1 || countPackets(f, opcode.CMSGCastSpell) != 0 || got.AssistAttempts != 0 {
				t.Fatal(got, f.packets)
			}
		})
	}
}

func ordinaryFargodeepPatrol(c *worldv1.CreatureState, age uint64) {
	c.MovementFlags = 0x08000001
	c.PositionAgeMs = age
	c.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 3000, AgeMs: age, DestinationKnown: true, DestinationX: c.X, DestinationY: c.Y, DestinationZ: c.Z}
}

func TestFargodeepPatrolUsesOnlyFreshHeaderOrRecentOrdinaryEndpoint(t *testing.T) {
	for _, mode := range []string{"fresh header", "elapsed endpoint", "walking endpoint", "midleg", "stale endpoint", "cyclic", "transport", "unknown destination", "flying", "zero duration", "long duration", "distant endpoint"} {
		t.Run(mode, func(t *testing.T) {
			_, _, c, _, _ := fargodeepRig(t, 5)
			ordinaryFargodeepPatrol(c, 4000)
			c.X -= 10 // The old server header must not win over the elapsed endpoint.
			wantX, want := c.Trajectory.DestinationX, true
			switch mode {
			case "fresh header":
				c.Trajectory.AgeMs, c.PositionAgeMs = 500, 500
				wantX = c.X
			case "walking endpoint":
				c.MovementFlags = 0x08000101
			case "midleg":
				c.Trajectory.AgeMs, c.PositionAgeMs = 1000, 1000
				want = false
			case "stale endpoint":
				c.Trajectory.AgeMs = 8001
				want = false
			case "cyclic":
				c.Trajectory.Cyclic = true
				want = false
			case "transport":
				c.Trajectory.TransportRelative = true
				want = false
			case "unknown destination":
				c.Trajectory.DestinationKnown = false
				want = false
			case "flying":
				c.MovementFlags |= 0x2000000
				want = false
			case "zero duration":
				c.Trajectory.DurationMs = 0
				want = false
			case "long duration":
				c.Trajectory.DurationMs = 120001
				want = false
			case "distant endpoint":
				c.Trajectory.DestinationX += 70
				want = false
			}
			p, known := fargodeepCreaturePosition(c)
			if known != want || known && p.x != wantX {
				t.Fatal(mode, p, known, wantX, want)
			}
		})
	}
}

func TestFargodeepOrdinaryPatrolCanPullWithVerifiedCurrentEndpoint(t *testing.T) {
	for _, guid := range []uint64{1, 4, 5} {
		for _, age := range []uint64{500, 4000} {
			t.Run(fmt.Sprintf("%d/%d", guid, age), func(t *testing.T) {
				s, f, c, r, advance := fargodeepRig(t, guid)
				r.EngagerGuid = guid
				ordinaryFargodeepPatrol(c, age)
				if age > 3000 {
					c.X -= 10
				}
				want, _ := fargodeepCreaturePosition(c)
				plans := 0
				s.planMapPath = func(_ context.Context, _ string, _ uint32, a, b position) ([]position, error) {
					plans++
					if distance(b, want) > .001 {
						t.Fatal("stale header planned", b, want)
					}
					return []position{a, b}, nil
				}
				armFargodeep(t, s, r)
				s.Tick(context.Background())
				advance(200 * time.Millisecond)
				s.Tick(context.Background())
				got := state(t, s)
				if plans != 1 || got.AssistAttempts == 0 && countPackets(f, opcode.CMSGAttackswing) == 0 || !got.Fargodeep.Active {
					t.Fatal(got, plans, f.packets)
				}
			})
		}
	}
}

func TestFargodeepPatrolMidlegWaitsAndReplansWithoutGuessingOrLosingPartyGates(t *testing.T) {
	s, f, c, r, advance := fargodeepRig(t, 5)
	ordinaryFargodeepPatrol(c, 1000)
	armFargodeep(t, s, r) // Reservation is safe; it cannot authorize a midleg pull.
	s.Tick(context.Background())
	if got := state(t, s); !got.Fargodeep.Active || got.Fargodeep.Phase != "waiting_for_fresh_patrol_position" || len(f.packets) != 0 {
		t.Fatal(got, f.packets)
	}
	c.Trajectory.AgeMs, c.PositionAgeMs = 4000, 4000
	s.Tick(context.Background())
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGAttackswing) != 1 {
		t.Fatal(state(t, s), f.packets)
	}
	for _, mode := range []string{"owner", "party"} {
		t.Run(mode, func(t *testing.T) {
			s, f, c, r, _ := fargodeepRig(t, 5)
			ordinaryFargodeepPatrol(c, 1000)
			armFargodeep(t, s, r)
			if mode == "owner" {
				f.world.Owner = &worldv1.PlayerState{Guid: 3, Present: true}
			} else {
				f.party.Group.InGroup = false
			}
			s.Tick(context.Background())
			if state(t, s).Fargodeep.Active || len(f.packets) != 0 {
				t.Fatal(state(t, s), f.packets)
			}
		})
	}
}

func TestFargodeepMovingPlanIsDiscardedAndTwelveFreshReplansAreBounded(t *testing.T) {
	for _, eventuallyStable := range []bool{true, false} {
		t.Run(fmt.Sprint(eventuallyStable), func(t *testing.T) {
			s, f, c, r, advance := fargodeepRig(t, 5)
			ordinaryFargodeepPatrol(c, 4000)
			armFargodeep(t, s, r)
			plans := 0
			s.planMapPath = func(_ context.Context, _ string, _ uint32, a, b position) ([]position, error) {
				plans++
				if !eventuallyStable || plans <= 4 {
					c.Trajectory.DestinationY += .2
					c.PositionRevision++
				}
				return []position{a, b}, nil
			}
			for i := 0; i < 15 && s.fargodeep.active && countPackets(f, opcode.CMSGAttackswing) == 0; i++ {
				advance(200 * time.Millisecond)
				s.Tick(context.Background())
			}
			got := state(t, s)
			if eventuallyStable {
				if plans != 5 || countPackets(f, opcode.CMSGAttackswing) != 1 {
					t.Fatal(got, plans, f.packets)
				}
			} else if plans != 12 || got.Fargodeep.Active || got.Fargodeep.Phase != "route_replan_limit" || countPackets(f, opcode.CMSGAttackswing) != 0 || countPackets(f, opcode.MSGMoveStartForward) != 0 {
				t.Fatal(got, plans, f.packets)
			}
		})
	}
}

func TestFargodeepPatrolChangingDuringFinalFacingCannotUseStaleRoute(t *testing.T) {
	for _, mode := range []string{"endpoint moved", "midleg", "cyclic"} {
		t.Run(mode, func(t *testing.T) {
			s, f, c, r, advance := fargodeepRig(t, 4)
			r.EngagerGuid = 4
			ordinaryFargodeepPatrol(c, 4000)
			armFargodeep(t, s, r)
			s.Tick(context.Background())
			advance(200 * time.Millisecond)
			f.onClock = func() {
				switch mode {
				case "endpoint moved":
					c.Trajectory.DestinationY += 3
				case "midleg":
					c.Trajectory.AgeMs, c.PositionAgeMs = 1000, 1000
				case "cyclic":
					c.Trajectory.Cyclic = true
				}
			}
			s.Tick(context.Background())
			if countPackets(f, opcode.CMSGCastSpell) != 0 || state(t, s).AssistAttempts != 0 {
				t.Fatal(state(t, s), f.packets)
			}
		})
	}
}

func TestFargodeepRogueWalksFreshVerifiedPatrolRouteWithoutJoel(t *testing.T) {
	s, f, c, r, advance := fargodeepRig(t, 5)
	c.X = -9806
	ordinaryFargodeepPatrol(c, 4000)
	plans := 0
	s.planMapPath = func(_ context.Context, _ string, _ uint32, a, b position) ([]position, error) {
		plans++
		path := []position{a}
		for at := a.x + 1; at < b.x; at++ {
			path = append(path, position{x: at, y: a.y, z: a.z})
		}
		return append(path, b), nil
	}
	armFargodeep(t, s, r)
	for i := 0; i < 45 && s.fargodeep.active && countPackets(f, opcode.CMSGAttackswing) == 0; i++ {
		advance(200 * time.Millisecond)
		// A new actual ordinary header/endpoint may change while approaching.
		if i == 4 {
			c.Trajectory.DestinationX -= 1
			c.PositionRevision++
		}
		s.Tick(context.Background())
	}
	got := state(t, s)
	if !got.Fargodeep.Active || countPackets(f, opcode.MSGMoveStartForward) < 1 || countPackets(f, opcode.MSGMoveHeartbeat) < 1 || countPackets(f, opcode.CMSGAttackswing) != 1 || plans != 2 {
		t.Fatal(got, plans, f.packets)
	}
	if distance(s.pos, position{x: c.Trajectory.DestinationX, y: c.Y, z: c.Z}) > 3.5 || got.Moving {
		t.Fatal("did not stop in current melee range", got)
	}
}

func TestFargodeepMovementClockRebindsPartyTargetAndOwner(t *testing.T) {
	for _, mode := range []string{"owner", "party", "target changed", "midleg", "own quest", "explicit assist stop"} {
		t.Run(mode, func(t *testing.T) {
			s, f, c, r, advance := fargodeepRig(t, 5)
			c.X += 10
			ordinaryFargodeepPatrol(c, 4000)
			armFargodeep(t, s, r)
			s.Tick(context.Background())
			advance(200 * time.Millisecond)
			f.onClock = func() {
				switch mode {
				case "owner":
					f.world.Owner = &worldv1.PlayerState{Guid: 3, Present: true}
				case "party":
					f.party.Group.InGroup = false
				case "target changed":
					c.Trajectory.DestinationY += 3
				case "midleg":
					c.Trajectory.AgeMs, c.PositionAgeMs = 1000, 1000
				case "own quest":
					f.world.SelfQuestLogKnown = false
				case "explicit assist stop":
					s.state.Assisting = false
				}
			}
			s.Tick(context.Background())
			if countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || countPackets(f, opcode.CMSGAttackswing) != 0 {
				t.Fatal(state(t, s), f.packets)
			}
		})
	}
}
