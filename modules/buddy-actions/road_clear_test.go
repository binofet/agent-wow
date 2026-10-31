package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type roadFake struct {
	*fakeSession
	worldCalls  int
	beforeWorld func(int)
	trace       []string
	filtered    []*worldv1.CreatureState
	queries     []*worldv1.SnapshotRequest
}

func (f *roadFake) InvokeModule(ctx context.Context, q *modv1.InvokeModuleRequest, o ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	f.trace = append(f.trace, q.Module+"."+q.Method)
	if q.Module == "buddy-world" {
		f.worldCalls++
		if f.beforeWorld != nil {
			f.beforeWorld(f.worldCalls)
		}
	}
	r, err := f.fakeSession.InvokeModule(ctx, q, o...)
	if err != nil || q.Module != "buddy-world" {
		return r, err
	}
	query := &worldv1.SnapshotRequest{}
	_ = q.Request.UnmarshalTo(query)
	f.queries = append(f.queries, proto.Clone(query).(*worldv1.SnapshotRequest))
	if query.CreatureFactionTemplate != 0 {
		w := &worldv1.SnapshotResponse{}
		_ = r.Result.UnmarshalTo(w)
		w.RequestedCreatures = nil
		sources := w.NearbyCreatures
		if f.filtered != nil {
			sources = f.filtered
		}
		for _, c := range sources {
			if c != nil && c.Present && c.FactionTemplate == query.CreatureFactionTemplate {
				w.RequestedCreatures = append(w.RequestedCreatures, proto.Clone(c).(*worldv1.CreatureState))
			}
		}
		r.Result, _ = anypb.New(w)
	}
	return r, nil
}
func (f *roadFake) SendPacket(ctx context.Context, q *modv1.SendPacketRequest, o ...grpc.CallOption) (*emptypb.Empty, error) {
	f.trace = append(f.trace, "packet")
	return f.fakeSession.SendPacket(ctx, q, o...)
}

func roadRig(t *testing.T) (*Service, *roadFake, *worldv1.CreatureState, *actionsv1.RoadClearRequest, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	rf := &roadFake{fakeSession: f}
	s.session = rf
	s.independent = true
	s.selfGUID = 8
	p := f.world.Self
	p.Guid = 8
	p.ClassId = 9
	p.LevelKnown = true
	p.Level = 5
	p.Health = 113
	p.MaxHealth = 113
	p.Power = 283
	p.MaxPower = 283
	p.BaseManaKnown = true
	p.BaseMana = 113
	p.FactionTemplate = 115
	p.CombatReachKnown = true
	p.CombatReach = 1.5
	p.ChannelSpellKnown = true
	p.ChannelObjectKnown = true
	p.ShapeshiftFormKnown = true
	p.X = -6450
	p.Y = 563
	p.Z = 385.94476
	p.PositionRevision = 77
	f.world.Owner = nil
	f.world.ObservedPackets = 100
	f.world.SelfQuestLogKnown = true
	f.world.SelfQuestLogRevision = 2
	for i := 0; i < 25; i++ {
		f.world.SelfQuestLog = append(f.world.SelfQuestLog, &worldv1.QuestLogEntry{Slot: uint32(i)})
	}
	f.world.SelfQuestLog[3].QuestId = 218
	f.world.SelfQuestLog[3].ObjectiveCounts = []uint32{0, 0, 0, 0}
	f.world.SelfSkillsKnown = true
	f.world.SelfSkillsRevision = 1
	for i := 0; i < 128; i++ {
		f.world.SelfSkills = append(f.world.SelfSkills, &worldv1.SkillState{Slot: uint32(i)})
	}
	f.world.InventoryKnown = true
	f.world.InventoryRevision = 1
	for i := 0; i < 5; i++ {
		b := &worldv1.InventoryBag{Bag: uint32(i), SlotKnown: true, ContentsKnown: true}
		if i == 0 {
			b.SlotCount = 16
			for j := 0; j < 16; j++ {
				b.Slots = append(b.Slots, &worldv1.BackpackSlot{Slot: uint32(j)})
			}
		}
		f.world.InventoryBags = append(f.world.InventoryBags, b)
	}
	s.pos = position{x: -6225, y: 100, z: 430.436}
	s.posMap = 0
	s.posKnown = true
	s.posEstimated = true
	s.posAt = s.now()
	s.posRevision = 9
	s.positionRevision = 77
	c := &worldv1.CreatureState{Guid: 0xf130000000000000 | 1718<<24 | 26633, Entry: 1718, Present: true, MapKnown: true, PositionKnown: true, X: -6205.47, Y: 99.902, Z: 430.436, PositionRevision: 1, LevelKnown: true, Level: 4, HealthKnown: true, Health: 86, MaxHealth: 86, AliveKnown: true, Alive: true, FactionTemplate: 59, UnitFlagsKnown: true, DynamicFlagsKnown: true, TargetKnown: true, CombatReachKnown: true, CombatReach: 1.05}
	f.world.NearbyCreatures = []*worldv1.CreatureState{c}
	f.lookupCreatures = map[uint64]*worldv1.CreatureState{c.Guid: c}
	f.party.SessionId = "road-session"
	s.navDataDir = "/fixture-only"
	s.planMapPath = func(_ context.Context, _ string, _ uint32, a, b position) ([]position, error) {
		n := int(math.Ceil(distance(a, b)))
		p := make([]position, n+1)
		for i := range p {
			v := float32(i) / float32(n)
			p[i] = position{x: a.x + (b.x-a.x)*v, y: a.y + (b.y-a.y)*v, z: a.z + (b.z-a.z)*v}
		}
		return p, nil
	}
	learnSpells(s, shadowBolt)
	s.lastWorld = proto.Clone(f.world).(*worldv1.SnapshotResponse)
	q := &actionsv1.RoadClearRequest{RequestId: "road-one", TargetGuid: c.Guid, SessionId: "road-session", ExpectedPosition: s.clientPositionLocked(), PositionRevision: 77}
	return s, rf, c, q, advance
}
func roadStart(t *testing.T, s *Service, q *actionsv1.RoadClearRequest) {
	t.Helper()
	a, err := s.StartRoadClear(context.Background(), q)
	if err != nil || a.RoadClear == nil || !a.RoadClear.Active {
		t.Fatalf("road arm failed: %v %v", a, err)
	}
}
func roadEvent(goEvent bool, count byte, target uint64) []byte {
	p := append(packedGUID(8), packedGUID(8)...)
	p = append(p, count)
	p = binary.LittleEndian.AppendUint32(p, shadowBolt)
	p = append(p, make([]byte, 8)...)
	if goEvent {
		p = append(p, 1)
		p = binary.LittleEndian.AppendUint64(p, target)
		p = append(p, 0)
	}
	p = binary.LittleEndian.AppendUint32(p, 2)
	return append(p, packedGUID(target)...)
}
func roadDamage(target uint64, n uint32) []byte {
	p := append(packedGUID(target), packedGUID(8)...)
	tail := make([]byte, 32)
	binary.LittleEndian.PutUint32(tail, shadowBolt)
	binary.LittleEndian.PutUint32(tail[4:], n)
	tail[12] = 32
	return append(p, tail...)
}
func roadPacket(s *Service, op uint32, p []byte) {
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: p})
}
func roadCastResult(s *Service, c *worldv1.CreatureState, damage uint32) {
	roadPacket(s, opcode.SMSGSpellStart, roadEvent(false, s.castCount, c.Guid))
	roadPacket(s, opcode.SMSGSpellGo, roadEvent(true, s.castCount, c.Guid))
	roadPacket(s, opcode.SMSGSpellnonmeleedamagelog, roadDamage(c.Guid, damage))
	c.Health -= damage
	c.UnitFlags |= 0x80000
	c.TargetGuid = 8
	c.AttackVictimKnown = true
	c.AttackVictimGuid = 8
	s.session.(*roadFake).world.Self.UnitFlags |= 0x80000
	p := binary.LittleEndian.AppendUint64(nil, c.Guid)
	p = binary.LittleEndian.AppendUint64(p, 8)
	roadPacket(s, opcode.SMSGAttackstart, p)
}

func TestRoadClearExplicitOnePullStationaryAndActualDeath(t *testing.T) {
	s, f, c, q, advance := roadRig(t)
	roadStart(t, s, q)
	a := s.roadSnapshotLocked()
	if a.SourceGuid != 0 || a.PullReserved || a.RemainingMs != 180000 || a.Goal != roadClearGoal || a.Origin.AgeMs != 0 || a.PositionRevision != 77 {
		t.Fatal(a)
	}
	s.Tick(context.Background())
	if countPackets(f.fakeSession, opcode.CMSGCastSpell) != 1 || !s.state.CastPending || !s.road.reserved {
		t.Fatal(s.roadSnapshotLocked())
	}
	for _, d := range []time.Duration{200 * time.Millisecond, 1500 * time.Millisecond, 500 * time.Millisecond, 500 * time.Millisecond} {
		advance(d)
		s.Tick(context.Background())
		if !s.road.active || countPackets(f.fakeSession, opcode.CMSGCastSpell) != 1 {
			t.Fatal("first pending was canceled/recast", s.roadSnapshotLocked())
		}
	}
	roadCastResult(s, c, 13)
	advance(300 * time.Millisecond)
	s.Tick(context.Background())
	if countPackets(f.fakeSession, opcode.CMSGCastSpell) != 2 {
		t.Fatal("second bolt missing", s.roadSnapshotLocked())
	}
	// Same exact damage bytes on a later matched cast are ordinary evidence.
	roadCastResult(s, c, 13)
	advance(3 * time.Second)
	s.Tick(context.Background())
	if countPackets(f.fakeSession, opcode.CMSGCastSpell) != 3 {
		t.Fatal("equal damage falsely deduplicated", s.roadSnapshotLocked())
	}
	roadCastResult(s, c, 60)
	c.Health = 0
	c.Alive = false
	c.UnitFlags = 0
	c.PositionRevision = 2
	s.Tick(context.Background())
	a = s.roadSnapshotLocked()
	if a.Active || !a.SourceDeathObserved || a.SourceGuid != c.Guid || a.SourceDeathPositionRevision != 2 || a.RemainingMs == 0 {
		t.Fatal(a)
	}
	for _, p := range f.packets {
		if p.Opcode != opcode.CMSGCastSpell && p.Opcode != opcode.MSGMoveSetFacing {
			t.Fatal("unexpected movement/attack", p)
		}
	}
	if countPackets(f.fakeSession, opcode.CMSGAttackswing) != 0 || len(f.world.SelfQuestLog[3].ObjectiveCounts) != 4 || f.world.SelfQuestLog[3].ObjectiveCounts[0] != 0 {
		t.Fatal("quest/melee inference")
	}
}

func TestRoadClearAdmissionRejectsPartialUnknownForeignAndUnsafe(t *testing.T) {
	cases := map[string]func(*Service, *roadFake, *worldv1.CreatureState, *actionsv1.RoadClearRequest){
		"companion": func(s *Service, _ *roadFake, _ *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) {
			s.independent = false
		},
		"other_guid": func(s *Service, _ *roadFake, _ *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) { s.selfGUID = 7 },
		"not_full_hp": func(_ *Service, f *roadFake, _ *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) {
			f.world.Self.Health = 112
		},
		"mana": func(_ *Service, f *roadFake, _ *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) {
			f.world.Self.Power = 282
		},
		"malformed_world_epoch": func(_ *Service, f *roadFake, _ *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) {
			f.world.MalformedPackets = 1
		},
		"partial_skills": func(_ *Service, f *roadFake, _ *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) {
			f.world.SelfSkills = f.world.SelfSkills[:127]
		},
		"duplicate_skill": func(_ *Service, f *roadFake, _ *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) {
			f.world.SelfSkills[127].Slot = 1
		},
		"partial_log": func(_ *Service, f *roadFake, _ *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) {
			f.world.SelfQuestLog = f.world.SelfQuestLog[:24]
		},
		"duplicate_quest": func(_ *Service, f *roadFake, _ *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) {
			f.world.SelfQuestLog[4].QuestId = 218
		},
		"unknown_bag": func(_ *Service, f *roadFake, _ *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) {
			f.world.InventoryBags[4].ContentsKnown = false
		},
		"unknown_item": func(_ *Service, f *roadFake, _ *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) {
			f.world.InventoryBags[0].Slots[0].ItemGuid = 0x4000000000000001
		},
		"unknown_cast": func(s *Service, _ *roadFake, _ *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) {
			s.roadCast.known = false
		},
		"unknown_position": func(_ *Service, _ *roadFake, c *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) {
			c.PositionKnown = false
		},
		"wrong_profile": func(_ *Service, _ *roadFake, c *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) {
			c.MaxHealth = 71
		},
		"claimed": func(_ *Service, _ *roadFake, c *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) {
			c.TargetGuid = 7
		},
		"moving_target": func(_ *Service, _ *roadFake, c *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) {
			c.MovementFlags = 1
		},
		"wrong_session": func(_ *Service, _ *roadFake, _ *worldv1.CreatureState, q *actionsv1.RoadClearRequest) {
			q.SessionId = "other"
		},
		"pose_provenance": func(_ *Service, _ *roadFake, _ *worldv1.CreatureState, q *actionsv1.RoadClearRequest) {
			q.ExpectedPosition.Estimated = false
		},
		"too_close": func(_ *Service, _ *roadFake, c *worldv1.CreatureState, _ *actionsv1.RoadClearRequest) { c.X -= 5 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s, f, c, q, _ := roadRig(t)
			change(s, f, c, q)
			if a, err := s.StartRoadClear(context.Background(), q); err == nil || a != nil || len(f.packets) != 0 {
				t.Fatal("unsafe road admitted", a, err)
			}
		})
	}
}

func TestRoadClearFinalClockAndWorldChangesNeverCast(t *testing.T) {
	for name, change := range map[string]func(*Service, *roadFake, *worldv1.CreatureState){
		"foreign_claim":  func(_ *Service, _ *roadFake, c *worldv1.CreatureState) { c.TargetGuid = 7 },
		"raw_correction": func(_ *Service, f *roadFake, _ *worldv1.CreatureState) { f.world.Self.PositionRevision++ },
		"slow":           func(_ *Service, f *roadFake, _ *worldv1.CreatureState) { f.world.Self.RunSpeed = 3.5 },
		"channel":        func(_ *Service, f *roadFake, _ *worldv1.CreatureState) { f.world.Self.ChannelSpellId = 99 },
		"session":        func(_ *Service, f *roadFake, _ *worldv1.CreatureState) { f.party.SessionId = "replacement" },
		"unknown_vitals": func(_ *Service, f *roadFake, _ *worldv1.CreatureState) { f.world.Self.HealthKnown = false },
	} {
		t.Run(name, func(t *testing.T) {
			s, f, c, q, _ := roadRig(t)
			roadStart(t, s, q)
			f.onClock = func() { change(s, f, c) }
			s.Tick(context.Background())
			if countPackets(f.fakeSession, opcode.CMSGCastSpell) != 0 || countPackets(f.fakeSession, opcode.MSGMoveSetFacing) != 0 || s.road.active || s.road.reserved {
				t.Fatal(s.roadSnapshotLocked())
			}
		})
	}
	for _, which := range []string{"late_final_world", "claimed_final_world"} {
		t.Run(which, func(t *testing.T) {
			s, f, c, q, advance := roadRig(t)
			roadStart(t, s, q)
			base := f.worldCalls
			f.beforeWorld = func(n int) {
				if n == base+3 {
					if which == "late_final_world" {
						advance(180 * time.Second)
					} else {
						c.TargetGuid = 7
					}
				}
			}
			s.Tick(context.Background())
			if countPackets(f.fakeSession, opcode.MSGMoveSetFacing) != 1 || countPackets(f.fakeSession, opcode.CMSGCastSpell) != 0 || s.road.reserved {
				t.Fatal(s.roadSnapshotLocked())
			}
		})
	}
}

func TestRoadClearResultsStopReplayAndPreservePending(t *testing.T) {
	t.Run("go_alone_and_unknown_timeout", func(t *testing.T) {
		s, f, c, q, advance := roadRig(t)
		roadStart(t, s, q)
		s.Tick(context.Background())
		roadPacket(s, opcode.SMSGSpellStart, roadEvent(false, s.castCount, c.Guid))
		roadPacket(s, opcode.SMSGSpellGo, roadEvent(true, s.castCount, c.Guid))
		advance(3 * time.Second)
		s.Tick(context.Background())
		if countPackets(f.fakeSession, opcode.CMSGCastSpell) != 1 || !s.road.active {
			t.Fatal(s.roadSnapshotLocked())
		}
		advance(2 * time.Second)
		s.Tick(context.Background())
		if s.road.active || s.roadSnapshotLocked().RemainingMs != 0 {
			t.Fatal(s.roadSnapshotLocked())
		}
	})
	t.Run("stop_while_pending", func(t *testing.T) {
		s, f, c, q, advance := roadRig(t)
		roadStart(t, s, q)
		s.Tick(context.Background())
		_, _ = s.Practice(context.Background(), &actionsv1.ToggleRequest{})
		advance(10 * time.Second)
		s.Tick(context.Background())
		if !s.state.CastPending || s.pendingTarget != c.Guid || s.road.active {
			t.Fatal("STOP pretended to cancel", s.stateLocked())
		}
		roadPacket(s, opcode.SMSGSpellStart, roadEvent(false, s.castCount, c.Guid))
		roadPacket(s, opcode.SMSGSpellGo, roadEvent(true, s.castCount, c.Guid))
		if s.state.CastPending || s.state.ServerAssistGo != 1 {
			t.Fatal("late matching GO unaccounted")
		}
		_, _ = s.StartRoadClear(context.Background(), q)
		if s.road.active || countPackets(f.fakeSession, opcode.CMSGCastSpell) != 1 {
			t.Fatal("replay rearmed")
		}
		other := proto.Clone(q).(*actionsv1.RoadClearRequest)
		other.RequestId = "new-id"
		if _, err := s.StartRoadClear(context.Background(), other); err == nil {
			t.Fatal("same body rearmed")
		}
	})
	t.Run("ambiguous_send", func(t *testing.T) {
		s, f, c, q, _ := roadRig(t)
		roadStart(t, s, q)
		f.beforeWorld = func(n int) {
			if n == 5 {
				f.failSend = true
			}
		}
		s.Tick(context.Background())
		if !s.road.reserved || !s.state.CastPending || s.pendingTarget != c.Guid || s.road.active || countPackets(f.fakeSession, opcode.CMSGCastSpell) != 1 {
			t.Fatal(s.stateLocked())
		}
	})
	t.Run("duplicate_damage_same_cast", func(t *testing.T) {
		s, _, c, q, _ := roadRig(t)
		roadStart(t, s, q)
		s.Tick(context.Background())
		roadCastResult(s, c, 13)
		roadPacket(s, opcode.SMSGSpellnonmeleedamagelog, roadDamage(c.Guid, 13))
		if s.road.active || !s.road.invalid {
			t.Fatal("duplicate result granted retry")
		}
	})
}
