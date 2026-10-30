package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fakeSession struct {
	mu                                sync.Mutex
	world                             *worldv1.SnapshotResponse
	packets                           []*modv1.SendPacketRequest
	clock                             uint32
	failSend, failClock, failSnapshot bool
}

func (f *fakeSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.packets = append(f.packets, proto.Clone(r).(*modv1.SendPacketRequest))
	if f.failSend {
		return nil, errors.New("ambiguous transport failure")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}
func (f *fakeSession) GetClock(ctx context.Context, _ *emptypb.Empty, _ ...grpc.CallOption) (*modv1.ClockResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failClock {
		return nil, errors.New("clock unavailable")
	}
	f.clock += 200
	return &modv1.ClockResponse{ClientTimeMs: f.clock}, ctx.Err()
}
func (f *fakeSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, _ ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failSnapshot {
		return nil, errors.New("snapshot unavailable")
	}
	if r.Module != "buddy-world" || r.Method != "snapshot" || !r.Request.MessageIs(&worldv1.SnapshotRequest{}) {
		return nil, errors.New("wrong module invocation")
	}
	value, _ := anypb.New(f.world)
	return &modv1.InvokeModuleResponse{Result: value}, ctx.Err()
}
func rig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	player := func(guid uint64, x float32) *worldv1.PlayerState {
		return &worldv1.PlayerState{Guid: guid, Present: true, MapKnown: true, PositionKnown: true, X: x, PositionRevision: 1, MovementFlagsKnown: true, HealthKnown: true, Health: 50, MaxHealth: 100, PowerKnown: true, Power: 128, MaxPower: 128, PowerType: 0, UnitFlagsKnown: true, AliveKnown: true, Alive: true, SpeedsKnown: true, WalkSpeed: 2.5, RunSpeed: 7}
	}
	f := &fakeSession{world: &worldv1.SnapshotResponse{Self: player(1, 0), Owner: player(3, 10)}}
	s := NewService(f, 1)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	return s, f, func(d time.Duration) { now = now.Add(d) }
}
func learn(s *Service) {
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInitialSpells, Payload: []byte{0, 1, 0, 2, 8, 0, 0, 0, 0, 0, 0}})
}
func state(t *testing.T, s *Service) *actionsv1.State {
	t.Helper()
	result, err := s.Status(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func toggle(t *testing.T, s *Service, enabled bool) {
	t.Helper()
	if _, err := s.Follow(context.Background(), &actionsv1.ToggleRequest{Enabled: enabled}); err != nil {
		t.Fatal(err)
	}
}

func TestDisabledControllerDoesNotPollOrWrite(t *testing.T) {
	s, f, _ := rig(t)
	s.Tick(context.Background())
	if len(f.packets) != 0 {
		t.Fatal("disabled writes")
	}
	if got := state(t, s); got.Following || got.Healing || got.KnownLesserHeal {
		t.Fatalf("initial %+v", got)
	}
}

func TestFollowUsesSharedClockNormalRunStepsAndStopsNearOwner(t *testing.T) {
	s, f, advance := rig(t)
	toggle(t, s, true)
	s.Tick(context.Background())
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if len(f.packets) != 2 || f.packets[0].Opcode != opcode.MSGMoveStartForward || f.packets[1].Opcode != opcode.MSGMoveHeartbeat {
		t.Fatalf("packets %+v", f.packets)
	}
	_, n, _ := readGUID(f.packets[1].Payload)
	p := f.packets[1].Payload[n:]
	if clock := binary.LittleEndian.Uint32(p[6:]); clock != 400 {
		t.Fatalf("clock %d", clock)
	}
	x := math.Float32frombits(binary.LittleEndian.Uint32(p[10:]))
	if math.Abs(float64(x-1.4)) > 0.001 {
		t.Fatalf("runstep %f", x)
	}
	f.world.Owner.X = 4
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if got := state(t, s); got.Moving || !got.Following || got.Reason != "within_follow_distance" {
		t.Fatalf("near owner %+v", got)
	}
	if f.packets[2].Opcode != opcode.MSGMoveStop {
		t.Fatal("missing stop")
	}
}

func TestFollowRefusesUnknownStaleUnsafeRangeMapAndSpeed(t *testing.T) {
	for _, change := range []func(*worldv1.SnapshotResponse){
		func(w *worldv1.SnapshotResponse) { w.Owner.Present = false }, func(w *worldv1.SnapshotResponse) { w.Owner.MapId = 1 },
		func(w *worldv1.SnapshotResponse) { w.Owner.MovementFlags = 1; w.Owner.PositionAgeMs = 3000 },
		func(w *worldv1.SnapshotResponse) { w.Self.MovementFlags = 0x200000 }, func(w *worldv1.SnapshotResponse) { w.Owner.Alive = false },
		func(w *worldv1.SnapshotResponse) { w.Owner.X = 40 }, func(w *worldv1.SnapshotResponse) { w.Owner.Z = 3 },
		func(w *worldv1.SnapshotResponse) { w.Self.RunSpeed = 14 }, func(w *worldv1.SnapshotResponse) { w.Self.SpeedsKnown = false },
		func(w *worldv1.SnapshotResponse) { w.Self.MapKnown = false }, func(w *worldv1.SnapshotResponse) { w.Owner.Guid = 4 },
		func(w *worldv1.SnapshotResponse) { w.Self.UnitFlags = 0x40000 },
	} {
		s, f, _ := rig(t)
		change(f.world)
		toggle(t, s, true)
		s.Tick(context.Background())
		if state(t, s).Moving || len(f.packets) != 0 {
			t.Fatalf("unsafe state allowed: %+v", state(t, s))
		}
	}
}

func TestStationaryOldPositionIsKnownButLateControllerStops(t *testing.T) {
	s, f, advance := rig(t)
	f.world.Owner.PositionAgeMs = 60000
	toggle(t, s, true)
	s.Tick(context.Background())
	if !state(t, s).Moving {
		t.Fatal("stationary known target rejected")
	}
	advance(time.Second)
	s.Tick(context.Background())
	if got := state(t, s); !got.Following || got.Moving || got.Reason != "follow_tick_late" {
		t.Fatalf("late %+v", got)
	}
}

func TestFollowStopCorrectionAndLease(t *testing.T) {
	for _, mode := range []string{"explicit", "correction", "lease", "lost"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := rig(t)
			toggle(t, s, true)
			s.Tick(context.Background())
			switch mode {
			case "explicit":
				toggle(t, s, false)
			case "correction":
				f.world.Self.PositionRevision++
				advance(200 * time.Millisecond)
				s.Tick(context.Background())
			case "lease":
				advance(4 * time.Hour)
				s.Tick(context.Background())
			case "lost":
				f.failSnapshot = true
				advance(200 * time.Millisecond)
				s.Tick(context.Background())
			}
			if got := state(t, s); got.Moving || (got.Following != (mode == "correction" || mode == "lost")) {
				t.Fatalf("failed to stop %+v", got)
			}
			if len(f.packets) != 2 || f.packets[1].Opcode != opcode.MSGMoveStop {
				t.Fatalf("stop packets %+v", f.packets)
			}
		})
	}
}

func TestHealKnownSpellHealthManaRangeAndStaleGates(t *testing.T) {
	for _, change := range []func(*Service, *fakeSession){
		func(s *Service, f *fakeSession) { s.state.KnownLesserHeal = false },
		func(s *Service, f *fakeSession) { f.world.Owner.Health = 100 }, func(s *Service, f *fakeSession) { f.world.Owner.HealthKnown = false },
		func(s *Service, f *fakeSession) { f.world.Self.Power = 20 }, func(s *Service, f *fakeSession) { f.world.Self.PowerType = 1 },
		func(s *Service, f *fakeSession) { f.world.Owner.X = 36 }, func(s *Service, f *fakeSession) { f.world.Owner.MovementFlags = 1; f.world.Owner.PositionAgeMs = 3000 },
	} {
		s, f, _ := rig(t)
		learn(s)
		change(s, f)
		result, err := s.HealOnce(context.Background(), &emptypb.Empty{})
		if err != nil || result.CastPending || len(f.packets) > 0 {
			t.Fatalf("unsafe heal %+v err=%v", result, err)
		}
	}
}

func TestHealStopsMovementThenWaitsForServerAndThrottles(t *testing.T) {
	s, f, advance := rig(t)
	learn(s)
	toggle(t, s, true)
	s.Tick(context.Background())
	advance(200 * time.Millisecond)
	result, err := s.HealOnce(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Moving || !result.CastPending || result.HealAttempts != 1 || result.ServerCastGo != 0 {
		t.Fatalf("transport falsely accepted %+v", result)
	}
	if len(f.packets) != 3 || f.packets[1].Opcode != opcode.MSGMoveStop || f.packets[2].Opcode != opcode.CMSGCastSpell {
		t.Fatalf("order %+v", f.packets)
	}
	_, _ = s.HealOnce(context.Background(), &emptypb.Empty{})
	if len(f.packets) != 3 {
		t.Fatal("cast retried while pending")
	}
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: []byte{1, 2, 8, 0, 0, 49}})
	if got := state(t, s); got.CastPending || got.Healing || got.LastCastFailure != 49 || got.ServerCastGo != 0 {
		t.Fatalf("failure %+v", got)
	}
	_, _ = s.HealOnce(context.Background(), &emptypb.Empty{})
	if len(f.packets) != 3 {
		t.Fatal("server failure retried immediately")
	}
}

func TestHealTimeoutAndTransportErrorAreNotSuccessOrRetried(t *testing.T) {
	s, f, advance := rig(t)
	learn(s)
	_, _ = s.HealOnce(context.Background(), &emptypb.Empty{})
	advance(6 * time.Second)
	s.Tick(context.Background())
	if got := state(t, s); got.CastPending || got.Reason != "cast_result_timeout_no_retry" || got.ServerCastGo != 0 {
		t.Fatalf("timeout %+v", got)
	}
	if len(f.packets) != 1 {
		t.Fatal("timeout retried")
	}
	s, f, _ = rig(t)
	learn(s)
	f.failSend = true
	result, _ := s.HealOnce(context.Background(), &emptypb.Empty{})
	if result.CastPending || result.TransportWrites != 0 || result.HealAttempts != 1 {
		t.Fatalf("transport failure %+v", result)
	}
	s.Tick(context.Background())
	if len(f.packets) != 1 {
		t.Fatal("transport failure retried")
	}
}

func TestHealAbortsIfMovementStopCannotBeWritten(t *testing.T) {
	s, f, _ := rig(t)
	learn(s)
	toggle(t, s, true)
	s.Tick(context.Background())
	f.failClock = true
	result, _ := s.HealOnce(context.Background(), &emptypb.Empty{})
	if result.CastPending || len(f.packets) != 1 || result.Reason != "movement_stop_unconfirmed_heal_aborted" {
		t.Fatalf("heal despite stop failure %+v", result)
	}
	if !result.MotionUnconfirmed || result.Following {
		t.Fatalf("unconfirmed stop not latched %+v", result)
	}
	f.failClock = false
	result, _ = s.HealOnce(context.Background(), &emptypb.Empty{})
	if result.CastPending || len(f.packets) != 1 || !result.MotionUnconfirmed {
		t.Fatalf("second heal bypassed unconfirmed stop %+v", result)
	}
	toggle(t, s, true)
	if state(t, s).Following {
		t.Fatal("follow bypassed unconfirmed stop")
	}
	toggle(t, s, false)
	if state(t, s).MotionUnconfirmed || len(f.packets) != 2 || f.packets[1].Opcode != opcode.MSGMoveStop {
		t.Fatal("explicit stop failed to recover")
	}
	result, _ = s.HealOnce(context.Background(), &emptypb.Empty{})
	if !result.CastPending || len(f.packets) != 3 {
		t.Fatalf("heal did not recover after explicit stop %+v", result)
	}
}

func TestMalformedAndForeignCastPacketsCannotClaimSuccess(t *testing.T) {
	s, _, _ := rig(t)
	learn(s)
	_, _ = s.HealOnce(context.Background(), &emptypb.Empty{})
	for _, op := range []uint32{opcode.SMSGCastFailed, opcode.SMSGSpellStart, opcode.SMSGSpellGo, opcode.SMSGSpellFailure} {
		_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: []byte{0xff}})
	}
	if got := state(t, s); got.ServerCastGo != 0 || got.MalformedPackets != 4 || !got.CastPending {
		t.Fatalf("malformed %+v", got)
	}
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: []byte{99, 2, 8, 0, 0, 49}})
	if !state(t, s).CastPending {
		t.Fatal("unmatched count consumed cast")
	}
}

func TestBeforeLogoutStopsAndRejectsNewControls(t *testing.T) {
	s, f, _ := rig(t)
	toggle(t, s, true)
	s.Tick(context.Background())
	_, err := s.BeforeLogout(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if got := state(t, s); got.Following || got.Healing || got.Moving {
		t.Fatalf("logout %+v", got)
	}
	if _, err := s.Follow(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err == nil {
		t.Fatal("follow enabled during logout")
	}
	s.Tick(context.Background())
	if len(f.packets) != 2 {
		t.Fatalf("writes after logout %d", len(f.packets))
	}
}

func TestConcurrentStatusControlsAndPackets(t *testing.T) {
	s, _, _ := rig(t)
	s.now = time.Now
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 20; n++ {
				_, _ = s.Status(context.Background(), &emptypb.Empty{})
				_, _ = s.Healing(context.Background(), &actionsv1.ToggleRequest{})
				_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: []byte{0}})
			}
		}()
	}
	wg.Wait()
}

func serverSpellPacket(goEvent bool, count byte) []byte {
	data := append(packedGUID(1), packedGUID(1)...)
	data = append(data, count)
	header := make([]byte, 12)
	binary.LittleEndian.PutUint32(header, lesserHeal)
	data = append(data, header...)
	if goEvent {
		data = append(data, 1)
		target := make([]byte, 8)
		binary.LittleEndian.PutUint64(target, ownerGUID)
		data = append(data, target...)
		data = append(data, 0)
	}
	data = append(data, 2, 0, 0, 0)
	return append(data, packedGUID(ownerGUID)...)
}

func TestServerSpellEventsRequireCompleteExpectedTargetBody(t *testing.T) {
	s, _, _ := rig(t)
	learn(s)
	_, _ = s.HealOnce(context.Background(), &emptypb.Empty{})
	for _, op := range []uint32{opcode.SMSGSpellStart, opcode.SMSGSpellGo} {
		wire := serverSpellPacket(op == opcode.SMSGSpellGo, 1)
		for length := 0; length < len(wire); length++ {
			_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: wire[:length]})
			if got := state(t, s); !got.CastPending || got.ServerCastGo != 0 || got.ServerCastStarts != 0 {
				t.Fatalf("accepted truncated op%x length%d %+v", op, length, got)
			}
		}
	}
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellStart, Payload: serverSpellPacket(false, 1)})
	if got := state(t, s); !got.CastPending || got.ServerCastStarts != 1 {
		t.Fatalf("start %+v", got)
	}
	wrong := serverSpellPacket(true, 1)
	wrong[len(wrong)-1] = 4
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: wrong})
	if !state(t, s).CastPending {
		t.Fatal("wrong target consumed pending cast")
	}
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: serverSpellPacket(true, 1)})
	if got := state(t, s); got.CastPending || got.ServerCastGo != 1 || got.Reason != "server_spell_go_health_effect_unverified" {
		t.Fatalf("go %+v", got)
	}
}

func TestGeneratedGRPCControlsAndState(t *testing.T) {
	s, _, _ := rig(t)
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	actionsv1.RegisterBuddyActionsServer(server, s)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	defer listener.Close()
	connection, err := grpc.NewClient("passthrough:///actions-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	client := actionsv1.NewBuddyActionsClient(connection)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response, err := client.Follow(ctx, &actionsv1.ToggleRequest{Enabled: true})
	if err != nil || !response.Following || response.Moving || response.FollowLeaseRemainingMs != 14400000 {
		t.Fatalf("follow %+v err%v", response, err)
	}
	response, err = client.Healing(ctx, &actionsv1.ToggleRequest{Enabled: true})
	if err != nil || !response.Healing {
		t.Fatalf("healing %+v err%v", response, err)
	}
	if _, err := client.BeforeLogout(ctx, &emptypb.Empty{}); err != nil {
		t.Fatal(err)
	}
	response, err = client.Status(ctx, &emptypb.Empty{})
	if err != nil || response.Following || response.Healing {
		t.Fatalf("closed %+v err%v", response, err)
	}
}

func TestCastPowerTailAndUnsupportedFlagsAreValidated(t *testing.T) {
	for _, goEvent := range []bool{false, true} {
		wire := serverSpellPacket(goEvent, 1)
		_, _, _, offset, err := castEvent(wire)
		if err != nil {
			t.Fatal(err)
		}
		binary.LittleEndian.PutUint32(wire[offset:], 0x800)
		if validCastBody(wire, offset, goEvent) {
			t.Fatal("accepted missing remaining-power field")
		}
		wire = append(wire, 100, 0, 0, 0)
		if !validCastBody(wire, offset, goEvent) {
			t.Fatal("rejected complete paid heal")
		}
		for n := 1; n < 4; n++ {
			if validCastBody(wire[:len(wire)-n], offset, goEvent) {
				t.Fatal("accepted truncated power")
			}
		}
		binary.LittleEndian.PutUint32(wire[offset:], 0x20)
		if validCastBody(wire, offset, goEvent) {
			t.Fatal("accepted unsupported projectile body")
		}
	}
}

func TestFreshAuthoritativeStoppedPositionClearsUnconfirmedMotion(t *testing.T) {
	s, f, _ := rig(t)
	learn(s)
	toggle(t, s, true)
	s.Tick(context.Background())
	f.failClock = true
	_, _ = s.HealOnce(context.Background(), &emptypb.Empty{})
	f.failClock = false
	f.world.Self.PositionRevision++
	f.world.Self.MovementFlags = 0
	result, _ := s.HealOnce(context.Background(), &emptypb.Empty{})
	if result.MotionUnconfirmed || !result.CastPending {
		t.Fatalf("authoritative stopped position failed to recover %+v", result)
	}
}
