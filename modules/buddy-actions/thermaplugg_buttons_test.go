package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type thermapluggTestSession struct {
	*fakeSession
	objects     map[uint32]*worldv1.GameObjectState
	objectReads int
	onObject    func(uint32)
	onSnapshot  func()
	useError    error
	onUse       func()
}

func (f *thermapluggTestSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	q := &worldv1.SnapshotRequest{}
	if r.Module == "buddy-world" && r.Method == "snapshot" && r.Request.UnmarshalTo(q) == nil && q.GameObjectEntry != 0 {
		f.objectReads++
		if f.onObject != nil {
			f.onObject(q.GameObjectEntry)
		}
		w := proto.Clone(f.world).(*worldv1.SnapshotResponse)
		if object := f.objects[q.GameObjectEntry]; object != nil {
			w.RequestedGameObjects = []*worldv1.GameObjectState{proto.Clone(object).(*worldv1.GameObjectState)}
		}
		v, err := anypb.New(w)
		return &modv1.InvokeModuleResponse{Result: v}, err
	}
	if r.Module == "buddy-world" && r.Method == "snapshot" && f.onSnapshot != nil {
		f.onSnapshot()
	}
	return f.fakeSession.InvokeModule(ctx, r, opts...)
}

func (f *thermapluggTestSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if r.Opcode == opcode.CMSGGameobjUse {
		if f.onUse != nil {
			f.onUse()
		}
		if f.useError != nil {
			return nil, f.useError
		}
	}
	return f.fakeSession.SendPacket(ctx, r, opts...)
}

const thermapluggEntry = 7800 // Observed boss identity in these native fixtures.

func thermapluggRig(t *testing.T) (*Service, *thermapluggTestSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	useStarterSemanticPolicy(t, s) // Real source; isolate physical semantics from the separate production watchdog.
	f.world.Self.MapId, f.world.Owner.MapId, f.world.Self.ClassId = thermapluggMap, thermapluggMap, 4
	f.world.VisitRevision = 1
	s.encounterVisit = encounterVisit{revision: 1, mapID: thermapluggMap, bound: true}
	f.world.OwnerTarget = &worldv1.CreatureState{Guid: uint64(0xf130)<<48 | uint64(thermapluggEntry)<<24 | 1, Entry: thermapluggEntry, Present: true, MapKnown: true, MapId: thermapluggMap,
		HealthKnown: true, Health: 100, AliveKnown: true, Alive: true, UnitFlagsKnown: true, UnitFlags: 0x80000}
	s.state.Assisting, s.assistUntil = true, s.now().Add(time.Minute)
	fixture := &thermapluggTestSession{fakeSession: f, objects: make(map[uint32]*worldv1.GameObjectState)}
	for _, entry := range []uint32{142208, 142214, 142211} {
		fixture.objects[entry] = &worldv1.GameObjectState{Guid: uint64(0xf110)<<48 | uint64(entry)<<24 | 1, Entry: entry,
			Present: true, MapKnown: true, MapId: thermapluggMap, PositionKnown: true, PositionRevision: 1, X: 1, TypeKnown: true,
			GameObjectType: 1, StateKnown: true, State: 1, FlagsKnown: true}
	}
	s.session = fixture
	return s, fixture, advance
}

func TestThermapluggButtonUsesWiredGuidAndWaitsForServer(t *testing.T) {
	s, f, advance := thermapluggRig(t)
	ctx := context.Background()
	run := func() bool { return s.thermapluggButtonsTickLocked(ctx, f.world) }
	if run() || len(f.packets) != 0 {
		t.Fatal("closed chute took over combat")
	}
	f.objects[142211].State = 0
	f.world.OwnerTarget.UnitFlags = 0
	advance(time.Second)
	if run() || len(f.packets) != 0 {
		t.Fatal("idle boss triggered a button")
	}
	f.world.OwnerTarget.UnitFlags = 0x80000
	if !run() || len(f.packets) != 1 || f.packets[0].Opcode != opcode.CMSGGameobjUse ||
		binary.LittleEndian.Uint64(f.packets[0].Payload) != f.objects[142214].Guid {
		t.Fatalf("expected only the paired button, got %v", f.packets)
	}
	advance(200 * time.Millisecond)
	if run() || len(f.packets) != 1 {
		t.Fatal("button retries displaced combat before the server response")
	}
	f.objects[142211].State = 1
	advance(800 * time.Millisecond)
	if !run() || s.state.Reason != "thermaplugg_bomb_chute_closed" || len(f.packets) != 1 {
		t.Fatalf("missing closure observation: %s", s.state.Reason)
	}
}

func TestThermapluggApproachStopsOnKnockdownAndCanRetry(t *testing.T) {
	s, f, advance := thermapluggRig(t)
	ctx := context.Background()
	f.objects[142211].State, f.objects[142214].X = 0, 9
	run := func() bool { return s.thermapluggButtonsTickLocked(ctx, f.world) }
	if !run() || len(f.packets) != 1 || f.packets[0].Opcode != opcode.MSGMoveStartForward {
		t.Fatalf("expected an approach along the approved route, got %v", f.packets)
	}
	advance(200 * time.Millisecond)
	f.onClock = func() { f.world.Self.UnitFlags = 0x40000 }
	if run() || len(f.packets) != 2 || f.packets[1].Opcode != opcode.MSGMoveStop {
		t.Fatalf("knockdown after clock should allow only STOP, got %v", f.packets)
	}
	f.onClock, f.world.Self.UnitFlags, f.world.Self.X = nil, 0, 8
	f.world.Self.PositionRevision++
	// Consume the physical refusal once; Lua now owns the retry deadline.
	if run() || len(f.packets) != 2 {
		t.Fatal("failed approach feedback did not yield before retry", f.packets)
	}
	advance(2 * time.Second)
	if !run() || len(f.packets) != 3 || f.packets[2].Opcode != opcode.CMSGGameobjUse {
		t.Fatalf("knockdown retry: packets=%v reason=%s policy=%s feedback=%s memory=%v pos=%v raw=%d client=%d lastWorld=%v bound=%+v", f.packets, s.state.Reason, s.starterSpells.lastError, s.thermaplugg.feedback, s.thermaplugg.memory, s.pos, s.positionRevision, s.posRevision, s.lastWorld.GetSelf(), s.thermaplugg.bound)
	}
}
