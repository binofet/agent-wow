package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

func catchupSetup(t *testing.T, level uint32) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := cold182Setup(t, 0, level, 3)
	for _, q := range f.world.SelfQuestLog {
		q.QuestId = 0
		q.ObjectiveCounts = []uint32{0, 0, 0, 0}
	}
	f.world.SelfQuestLog[0].QuestId = 218
	q := f.world.SelfQuestLog[1]
	q.QuestId, q.Complete, q.State = 2160, true, 1
	own := f.world.Self
	own.Health, own.MaxHealth, own.Power, own.MaxPower, own.BaseMana = 113, 113, 283, 283, 113
	own.ChannelSpellKnown, own.ChannelObjectKnown = true, true
	f.world.InventoryKnown, f.world.InventoryRevision = true, 9
	for bag := uint32(0); bag < 5; bag++ {
		b := &worldv1.InventoryBag{Bag: bag, SlotKnown: true, ContentsKnown: true}
		if bag == 0 {
			b.SlotCount = 16
			for slot := uint32(0); slot < 16; slot++ {
				b.Slots = append(b.Slots, &worldv1.BackpackSlot{Slot: slot})
			}
			b.Slots[8] = &worldv1.BackpackSlot{Slot: 8, ItemGuid: coldridgeCatchupCrateGUID, Entry: 7646, StackCount: 1, MetadataKnown: true}
		}
		f.world.InventoryBags = append(f.world.InventoryBags, b)
	}
	c := f.world.NearbyCreatures[0]
	c.PositionAgeMs, c.ObservationAgeMs = 1000, 500
	f.party = &partyv1.StateResponse{SessionId: "catchup-session"}
	return s, f, advance
}
func catchupRequest(level uint32) *actionsv1.ColdridgeCatchupRequest {
	return &actionsv1.ColdridgeCatchupRequest{RequestId: "catchup-one", TargetGuid: cold182GoalGUID, SessionId: "catchup-session", ExpectedStartLevel: level}
}
func catchupStart(t *testing.T, s *Service, level uint32) *actionsv1.State {
	t.Helper()
	v, e := s.StartColdridgeCatchup(context.Background(), catchupRequest(level))
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func catchupPull(t *testing.T, s *Service, f *fakeSession) {
	t.Helper()
	s.Tick(context.Background())
	if lastCast(f) != 0 {
		t.Fatal("planning cast")
	}
	s.Tick(context.Background())
	if lastCast(f) != 686 || state(t, s).PracticePulls != 1 || !state(t, s).ColdridgeCatchup.PullReserved {
		t.Fatal("single selected pull absent", state(t, s))
	}
}

func TestColdridgeCatchupDistinctContractAdmitsOnlyExplicitNextLevelStages(t *testing.T) {
	for _, level := range []uint32{5, 6, 7} {
		t.Run(string(rune('0'+level)), func(t *testing.T) {
			s, f, _ := catchupSetup(t, level)
			before := proto.Clone(f.world)
			v := catchupStart(t, s, level)
			r := v.ColdridgeCatchup
			if !v.Practicing || r == nil || r.Goal != "coldridge_706_catchup" || r.StartLevel != level || r.TargetLevel != level+1 || r.TargetLevel > 8 || r.MaxPulls != 1 || r.TargetGuid != cold182GoalGUID || r.SourceGuid != 0 || r.PullReserved || r.RemainingMs != 180000 || r.SessionId != "catchup-session" || v.ColdridgeLeveling != nil || !proto.Equal(before, f.world) || len(f.packets) != 0 {
				t.Fatal("wrong distinct catch-up admission", v)
			}
			catchupPull(t, s, f)
			r = state(t, s).ColdridgeCatchup
			if r.SourceGuid != cold182GoalGUID || r.ObservedLevel != level || r.TargetLevelObserved || len(s.coldridgeLevelingRequests) != 0 || s.coldridgeLeveling.requestID != "" {
				t.Fatal("catchup fabricated credit or changed old record", r)
			}
		})
	}
}
func TestColdridgeCatchupRejectsWrongActorGoalSourceResourceOrContextBeforeArm(t *testing.T) {
	changes := map[string]func(*Service, *fakeSession){
		"companion":              func(s *Service, f *fakeSession) { s.independent = false },
		"other_body":             func(s *Service, f *fakeSession) { f.world.Self.Guid = 7 },
		"level4":                 func(s *Service, f *fakeSession) { f.world.Self.Level = 4 },
		"level8":                 func(s *Service, f *fakeSession) { f.world.Self.Level = 8 },
		"class":                  func(s *Service, f *fakeSession) { f.world.Self.ClassId = 8 },
		"dead":                   func(s *Service, f *fakeSession) { f.world.Self.Alive = false },
		"combat":                 func(s *Service, f *fakeSession) { f.world.Self.UnitFlags |= 0x80000 },
		"channel_spell_unknown":  func(s *Service, f *fakeSession) { f.world.Self.ChannelSpellKnown = false },
		"channel_object_unknown": func(s *Service, f *fakeSession) { f.world.Self.ChannelObjectKnown = false },
		"channel_spell":          func(s *Service, f *fakeSession) { f.world.Self.ChannelSpellId = 1 },
		"channel_object":         func(s *Service, f *fakeSession) { f.world.Self.ChannelObjectGuid = 1 },
		"health":                 func(s *Service, f *fakeSession) { f.world.Self.Health-- },
		"mana":                   func(s *Service, f *fakeSession) { f.world.Self.Power-- },
		"unknown_book":           func(s *Service, f *fakeSession) { s.state.KnownShadowBolt = false },
		"partial_log":            func(s *Service, f *fakeSession) { f.world.SelfQuestLog = f.world.SelfQuestLog[:24] },
		"duplicate_slot":         func(s *Service, f *fakeSession) { f.world.SelfQuestLog[24].Slot = 1 },
		"empty_complete":         func(s *Service, f *fakeSession) { f.world.SelfQuestLog[24].Complete = true },
		"empty_failed":           func(s *Service, f *fakeSession) { f.world.SelfQuestLog[24].Failed = true },
		"empty_state":            func(s *Service, f *fakeSession) { f.world.SelfQuestLog[24].State = 1 },
		"empty_counter":          func(s *Service, f *fakeSession) { f.world.SelfQuestLog[24].ObjectiveCounts[0] = 1 },
		"empty_partial_counters": func(s *Service, f *fakeSession) { f.world.SelfQuestLog[24].ObjectiveCounts = []uint32{0} },
		"absent218":              func(s *Service, f *fakeSession) { f.world.SelfQuestLog[0].QuestId = 0 },
		"complete218":            func(s *Service, f *fakeSession) { f.world.SelfQuestLog[0].Complete = true },
		"failed2160":             func(s *Service, f *fakeSession) { f.world.SelfQuestLog[1].Failed = true },
		"incomplete2160": func(s *Service, f *fakeSession) {
			f.world.SelfQuestLog[1].State = 0
			f.world.SelfQuestLog[1].Complete = false
		},
		"other_quest":       func(s *Service, f *fakeSession) { f.world.SelfQuestLog[24].QuestId = 182 },
		"counter":           func(s *Service, f *fakeSession) { f.world.SelfQuestLog[0].ObjectiveCounts[0] = 1 },
		"missingcrate":      func(s *Service, f *fakeSession) { f.world.InventoryBags[0].Slots[8] = &worldv1.BackpackSlot{Slot: 8} },
		"foreigncrate":      func(s *Service, f *fakeSession) { f.world.InventoryBags[0].Slots[8].ItemGuid++ },
		"doublecrate":       func(s *Service, f *fakeSession) { f.world.InventoryBags[0].Slots[8].StackCount = 2 },
		"unknown_inventory": func(s *Service, f *fakeSession) { f.world.InventoryKnown = false },
		"duplicate_bag":     func(s *Service, f *fakeSession) { f.world.InventoryBags[4].Bag = 0 },
		"full_bag": func(s *Service, f *fakeSession) {
			for i, x := range f.world.InventoryBags[0].Slots {
				if x.ItemGuid == 0 {
					x.ItemGuid = 0x4000000000001000 + uint64(i)
					x.Entry = 117
					x.StackCount = 1
					x.MetadataKnown = true
				}
			}
		},
		"wrong_profile4": func(s *Service, f *fakeSession) {
			f.world.NearbyCreatures[0].Level = 4
			f.world.NearbyCreatures[0].MaxHealth = 86
			f.world.NearbyCreatures[0].Health = 86
		},
		"wrong_faction":        func(s *Service, f *fakeSession) { f.world.NearbyCreatures[0].FactionTemplate = 59 },
		"wrongGUID":            func(s *Service, f *fakeSession) { f.world.NearbyCreatures[0].Guid++ },
		"claimed":              func(s *Service, f *fakeSession) { f.world.NearbyCreatures[0].DynamicFlags = 4 },
		"injuredtarget":        func(s *Service, f *fakeSession) { f.world.NearbyCreatures[0].Health-- },
		"stale_position":       func(s *Service, f *fakeSession) { f.world.NearbyCreatures[0].PositionAgeMs = 30001 },
		"stale_observation":    func(s *Service, f *fakeSession) { f.world.NearbyCreatures[0].ObservationAgeMs = 30001 },
		"zero_target_revision": func(s *Service, f *fakeSession) { f.world.NearbyCreatures[0].PositionRevision = 0 },
		"outside_raw_target":   func(s *Service, f *fakeSession) { f.world.NearbyCreatures[0].Y = 524 },
		"outside_dest": func(s *Service, f *fakeSession) {
			f.world.NearbyCreatures[0].Trajectory = &worldv1.CreatureTrajectory{Known: true, DestinationKnown: true, DestinationX: -6400, DestinationY: 524, DestinationZ: 385}
		},
		"Hands_estimate": func(s *Service, f *fakeSession) { s.pos = position{x: -6248.84, y: 135.042, z: 431.5892} },
		"wrong_session":  func(s *Service, f *fakeSession) { f.party.SessionId = "foreign" },
		"lease":          func(s *Service, f *fakeSession) { s.state.InteractionOwner = "loot" },
		"pending":        func(s *Service, f *fakeSession) { s.state.CastPending = true },
		"cache32": func(s *Service, f *fakeSession) {
			for len(f.world.NearbyCreatures) < 32 {
				f.world.NearbyCreatures = append(f.world.NearbyCreatures, proto.Clone(f.world.NearbyCreatures[0]).(*worldv1.CreatureState))
			}
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f, _ := catchupSetup(t, 5)
			change(s, f)
			if got, err := s.StartColdridgeCatchup(context.Background(), catchupRequest(5)); err == nil || got != nil || state(t, s).Practicing || len(s.coldridgeCatchupRequests) != 0 || len(f.packets) != 0 {
				t.Fatal("unsafe admission armed/wrote", got, err, state(t, s))
			}
		})
	}
	for _, q := range []*actionsv1.ColdridgeCatchupRequest{nil, {RequestId: "bad id", TargetGuid: cold182GoalGUID, SessionId: "catchup-session", ExpectedStartLevel: 5}, {RequestId: "id", TargetGuid: cold182GoalGUID, SessionId: "catchup-session", ExpectedStartLevel: 8}, {RequestId: "id", TargetGuid: cold182GoalGUID, SessionId: "", ExpectedStartLevel: 5}} {
		s, f, _ := catchupSetup(t, 5)
		if _, e := s.StartColdridgeCatchup(context.Background(), q); e == nil || len(f.packets) != 0 {
			t.Fatal("malformed request wrote")
		}
	}
}
func TestColdridgeCatchupRequestSourceConsumptionAndNoRenewal(t *testing.T) {
	s, f, advance := catchupSetup(t, 5)
	catchupStart(t, s, 5)
	until := s.practiceUntil
	advance(13 * time.Second)
	v := catchupStart(t, s, 5)
	if v.ColdridgeCatchup.RemainingMs != 167000 || !s.practiceUntil.Equal(until) {
		t.Fatal("renewed")
	}
	q := catchupRequest(5)
	q.TargetGuid++
	if _, e := s.StartColdridgeCatchup(context.Background(), q); e == nil {
		t.Fatal("changed sameID")
	}
	if _, e := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); e == nil {
		t.Fatal("ordinary practice replaced catchup")
	}
	s.Practice(context.Background(), &actionsv1.ToggleRequest{})
	if v = catchupStart(t, s, 5); v.Practicing || !s.practiceUntil.Equal(until) {
		t.Fatal("duplicate rearmed")
	}
	q = catchupRequest(5)
	q.RequestId = "new-id-same-body"
	if _, e := s.StartColdridgeCatchup(context.Background(), q); e == nil || len(f.packets) != 0 {
		t.Fatal("newID reused consumed body")
	}
}
func TestColdridgeCatchupNeverSelectsAnotherBodyOrStartsAnotherPull(t *testing.T) {
	s, f, _ := catchupSetup(t, 5)
	catchupStart(t, s, 5)
	other := proto.Clone(f.world.NearbyCreatures[0]).(*worldv1.CreatureState)
	other.Guid++
	other.X--
	f.world.NearbyCreatures = append(f.world.NearbyCreatures, other)
	catchupPull(t, s, f)
	c := f.world.NearbyCreatures[0]
	c.Health, c.Alive = 0, false
	s.Tick(context.Background())
	got := state(t, s)
	if got.Practicing || got.ColdridgeCatchup.SourceGuid != c.Guid || got.PracticePulls != 1 || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("death continued/selected other", got)
	}
}
func TestColdridgeCatchupNextLevelStopsOffenseAndKeepsOnlyMatchingCorpse(t *testing.T) {
	for _, level := range []uint32{5, 6, 7} {
		t.Run(string(rune('0'+level)), func(t *testing.T) {
			s, f, _ := catchupSetup(t, level)
			catchupStart(t, s, level)
			catchupPull(t, s, f)
			f.world.Self.Level = level + 1
			c := f.world.NearbyCreatures[0]
			c.Health, c.Alive = 0, false
			s.Tick(context.Background())
			r := state(t, s).ColdridgeCatchup
			if state(t, s).Practicing || r.ObservedLevel != level+1 || !r.TargetLevelObserved || countPackets(f, opcode.CMSGCastSpell) != 1 {
				t.Fatal("cap not observed/stopped", state(t, s))
			}
			s.mu.Lock()
			good := s.coldridgeCatchupCorpseContextLocked(f.world, c)
			other := proto.Clone(c).(*worldv1.CreatureState)
			other.Guid++
			bad := s.coldridgeCatchupCorpseContextLocked(f.world, other)
			s.mu.Unlock()
			if !good || bad {
				t.Fatal("wrong corpse handoff", good, bad)
			}
			// STOP never means cancellation. An actual matching packet may settle later.
			if !state(t, s).CastPending {
				t.Fatal("STOP invented cast cancellation")
			}
		})
	}
}
func TestColdridgeCatchupPostFacingWorldAndPacketBoundaryCanOnlyConsume(t *testing.T) {
	for _, mode := range []string{"deadline", "levelup", "death", "crate", "foreignvictim", "channel_spell_unknown", "channel_object_unknown", "channel_spell", "channel_object"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := catchupSetup(t, 5)
			catchupStart(t, s, 5)
			s.Tick(context.Background())
			old := s.now
			s.now = func() time.Time {
				if s.coldridgeCatchup.pullReserved {
					switch mode {
					case "deadline":
						advance(3 * time.Minute)
					case "levelup":
						f.world.Self.Level = 6
						s.lastWorld.Self.Level = 6
					case "death":
						s.lastWorld.Self.Alive = false
					case "crate":
						s.lastWorld.InventoryBags[0].Slots[8].StackCount = 2
					case "foreignvictim":
						practiceTargetObservation(s.lastWorld, cold182GoalGUID).AttackVictimKnown = true
						practiceTargetObservation(s.lastWorld, cold182GoalGUID).AttackVictimGuid = 7
					case "channel_spell_unknown":
						s.lastWorld.Self.ChannelSpellKnown = false
					case "channel_object_unknown":
						s.lastWorld.Self.ChannelObjectKnown = false
					case "channel_spell":
						s.lastWorld.Self.ChannelSpellId = 1
					case "channel_object":
						s.lastWorld.Self.ChannelObjectGuid = 1
					}
				}
				return old()
			}
			s.Tick(context.Background())
			got := state(t, s)
			if got.Practicing || !got.ColdridgeCatchup.PullReserved || got.PracticePulls != 1 || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("boundary wrote or restored reservation", got)
			}
		})
	}
}
func TestColdridgeCatchupRetainsMatchedServerEvidenceWithoutQuestXPClaims(t *testing.T) {
	s, f, _ := catchupSetup(t, 5)
	catchupStart(t, s, 5)
	catchupPull(t, s, f)
	p := binary.LittleEndian.AppendUint64(nil, 8)
	p = binary.LittleEndian.AppendUint64(p, cold182GoalGUID)
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGPartykilllog, Payload: p})
	if state(t, s).PracticeKills != 1 || f.world.SelfQuestLog[0].ObjectiveCounts[0] != 0 || f.world.SelfQuestLog[1].ObjectiveCounts[0] != 0 || state(t, s).ColdridgeCatchup.ObservedLevel != 5 {
		t.Fatal("kill invented quest or level")
	}
}
func TestColdridgeCatchupPlannerFailureAndAmbiguousCastNeverRetry(t *testing.T) {
	for _, mode := range []string{"planner", "send"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := catchupSetup(t, 5)
			catchupStart(t, s, 5)
			if mode == "planner" {
				s.planColdridge182Path = func(context.Context, string, position, position) ([]position, error) {
					return nil, errors.New("fixture no route")
				}
			} else {
				f.failSend = true
			}
			s.Tick(context.Background())
			s.Tick(context.Background())
			n := countPackets(f, opcode.CMSGCastSpell)
			s.Tick(context.Background())
			s.Tick(context.Background())
			if state(t, s).Practicing || countPackets(f, opcode.CMSGCastSpell) != n {
				t.Fatal("uncertain run retried", state(t, s))
			}
		})
	}
}
func TestColdridgeCatchupDescriptorAdds75WithoutReplacing70(t *testing.T) {
	raw, e := os.ReadFile("module.pb")
	if e != nil {
		t.Fatal(e)
	}
	set := &descriptorpb.FileDescriptorSet{}
	if proto.Unmarshal(raw, set) != nil {
		t.Fatal("descriptor")
	}
	api := actionsv1.File_modules_buddy_actions_actions_proto
	for _, f := range set.File {
		if f.GetName() == api.Path() && !proto.Equal(f, protodesc.ToFileDescriptorProto(api)) {
			t.Fatal("installed/API mismatch")
		}
	}
	fields := api.Messages().ByName("State").Fields()
	if fields.ByName("coldridge_leveling").Number() != 70 || fields.ByName("coldridge_catchup").Number() != 75 {
		t.Fatal("old/new publication collision")
	}
}

func TestColdridgeCatchupMovementDeadlineFencesStartHeartbeatAndFacing(t *testing.T) {
	for _, boundary := range []string{"clock", "fresh_snapshot"} {
		for _, movement := range []string{"start", "heartbeat", "facing"} {
			t.Run(boundary+"/"+movement, func(t *testing.T) {
				s, f, advance := catchupSetup(t, 5)
				catchupStart(t, s, 5)
				if movement != "facing" {
					// Synthetic30yd route inside the existing sealed region. It
					// requires actual navigation, rather than immediate cast facing.
					f.world.NearbyCreatures[0].X = s.pos.x - 30
				}
				s.Tick(context.Background())
				if s.practiceTarget != cold182GoalGUID || s.practiceReplans != 1 {
					t.Fatal("fixture did not plan its live706 route", state(t, s))
				}
				if movement == "heartbeat" {
					s.Tick(context.Background())
					if !state(t, s).Moving || countPackets(f, opcode.MSGMoveStartForward) != 1 {
						t.Fatal("fixture did not begin a valid pre-deadline route", state(t, s))
					}
					advance(200 * time.Millisecond)
				}
				start, heartbeat, facing, stopped := countPackets(f, opcode.MSGMoveStartForward), countPackets(f, opcode.MSGMoveHeartbeat), countPackets(f, opcode.MSGMoveSetFacing), countPackets(f, opcode.MSGMoveStop)
				fired := coldLevelingExpireOnMovement(s, f, boundary, func() { advance(s.coldridgeCatchup.until.Sub(s.now())) })
				s.Tick(context.Background())
				if state(t, s).Practicing {
					t.Fatal("a fenced movement or facing cycle did not stop practice")
				}
				s.Tick(context.Background())
				got := state(t, s)
				if !*fired || countPackets(f, opcode.MSGMoveStartForward) != start || countPackets(f, opcode.MSGMoveHeartbeat) != heartbeat || countPackets(f, opcode.MSGMoveSetFacing) != facing || countPackets(f, opcode.CMSGCastSpell) != 0 || got.Practicing || got.Moving || got.ColdridgeCatchup.RemainingMs != 0 || got.ColdridgeCatchup.PullReserved {
					t.Fatal("movement dependency latency escaped the fixed leveling window", got, f.packets)
				}
				if movement == "heartbeat" && countPackets(f, opcode.MSGMoveStop) != stopped+1 {
					t.Fatal("expired movement prevented the one cleanup STOP", got, f.packets)
				}
			})
		}
	}
}

func TestColdridgeCatchupFinalMovementWorldRejectsUnsafeOwnState(t *testing.T) {
	for _, movement := range []string{"start", "heartbeat", "facing"} {
		for _, mode := range []string{"dead", "alive_unknown", "ground_unknown", "non_ground_movement", "extra_movement", "unsafe_unit_flag", "busy_unit_flag", "channel_spell", "channel_object", "channel_spell_unknown", "channel_object_unknown", "health_unknown", "health_zero", "combat", "speed_unknown", "speed3.5", "speedNaN", "speedInf"} {
			t.Run(movement+"/"+mode, func(t *testing.T) {
				s, f, advance := catchupSetup(t, 5)
				catchupStart(t, s, 5)
				if movement != "facing" {
					f.world.NearbyCreatures[0].X = s.pos.x - 30
				}
				s.Tick(context.Background())
				if movement == "heartbeat" {
					s.Tick(context.Background())
					if !state(t, s).Moving || countPackets(f, opcode.MSGMoveStartForward) != 1 {
						t.Fatal("fixture did not start a safe pre-callback route")
					}
					advance(200 * time.Millisecond)
				}
				start, heartbeat, facing, stopped := countPackets(f, opcode.MSGMoveStartForward), countPackets(f, opcode.MSGMoveHeartbeat), countPackets(f, opcode.MSGMoveSetFacing), countPackets(f, opcode.MSGMoveStop)
				fired := false
				f.onClock = func() {
					f.onClock = nil
					fired = true
					switch mode {
					case "channel_spell_unknown":
						f.world.Self.ChannelSpellKnown = false
					case "channel_object_unknown":
						f.world.Self.ChannelObjectKnown = false
					default:
						coldLevelingDisproveFinalMovementWorld(f, mode)
					}
				}
				s.Tick(context.Background())
				got := state(t, s)
				if !fired || countPackets(f, opcode.MSGMoveStartForward) != start || countPackets(f, opcode.MSGMoveHeartbeat) != heartbeat || countPackets(f, opcode.MSGMoveSetFacing) != facing || countPackets(f, opcode.CMSGCastSpell) != 0 || got.Practicing || got.Moving || got.ColdridgeCatchup.PullReserved {
					t.Fatal("last movement world disproof reused the earlier safe body", mode, got, f.packets)
				}
				if movement == "heartbeat" && countPackets(f, opcode.MSGMoveStop) != stopped+1 {
					t.Fatal("unsafe final world prevented the required cleanup STOP", mode, got, f.packets)
				}
			})
		}
	}
}

func TestColdridgeCatchupHistoryBoundAndReturnedRecordsCannotRearm(t *testing.T) {
	s, f, _ := catchupSetup(t, 5)
	for i := 0; i < 64; i++ {
		q := catchupRequest(5)
		q.RequestId = fmt.Sprintf("bounded-%d", i)
		q.TargetGuid += uint64(i)
		f.world.NearbyCreatures[0].Guid = q.TargetGuid
		got, err := s.StartColdridgeCatchup(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		got.ColdridgeCatchup.RequestId = "caller-edit"
		q.SessionId = "caller-edit"
		if state(t, s).ColdridgeCatchup.RequestId != fmt.Sprintf("bounded-%d", i) {
			t.Fatal("returned snapshot rewrote run")
		}
		s.Practice(context.Background(), &actionsv1.ToggleRequest{})
	}
	q := catchupRequest(5)
	q.RequestId = "bounded-64"
	q.TargetGuid += 64
	f.world.NearbyCreatures[0].Guid = q.TargetGuid
	if _, err := s.StartColdridgeCatchup(context.Background(), q); err == nil || len(s.coldridgeCatchupRequests) != 64 || len(s.coldridgeCatchupBodies) != 64 {
		t.Fatal("bounded history evicted/renewed authority")
	}
}

func TestColdridgeCatchupDoesNotChangeOldLevelFiveRecordOrLegacyQuestGoals(t *testing.T) {
	s, f, _ := catchupSetup(t, 5)
	old := coldridgeLevelingRun{requestID: "accepted-historical-level5", observedLevel: 5, levelFiveObserved: true, sourceGUID: cold182GoalGUID + 99, pullReserved: true}
	s.coldridgeLeveling = old
	s.coldridgeLevelingRequests = map[string]struct{}{old.requestID: {}}
	catchupStart(t, s, 5)
	if s.coldridgeLeveling != old || len(s.coldridgeLevelingRequests) != 1 || state(t, s).ColdridgeLeveling != nil {
		t.Fatal("new mode rewrote old run/history")
	}
	s.Practice(context.Background(), &actionsv1.ToggleRequest{})
	if _, err := s.StartColdridgeLeveling(context.Background(), &actionsv1.ColdridgeLevelingRequest{RequestId: "attempt-old"}); err == nil {
		t.Fatal("new mode widened old level4/sole218 gate")
	}
	if _, err := s.Practice(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err == nil || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("new item context silently enabled old quest practice")
	}
}
