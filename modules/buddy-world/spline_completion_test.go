package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type completionSession struct {
	modv1.SessionClient
	clock                 uint32
	clockCalls            int
	clockError, sendError error
	onClock, onSend       func()
	packets               []*modv1.SendPacketRequest
}

func (f *completionSession) GetClock(context.Context, *emptypb.Empty, ...grpc.CallOption) (*modv1.ClockResponse, error) {
	f.clockCalls++
	if f.onClock != nil {
		f.onClock()
	}
	if f.clockError != nil {
		return nil, f.clockError
	}
	return &modv1.ClockResponse{ClientTimeMs: f.clock}, nil
}

func (f *completionSession) SendPacket(_ context.Context, req *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.packets = append(f.packets, proto.Clone(req).(*modv1.SendPacketRequest))
	if f.onSend != nil {
		f.onSend()
	}
	if f.sendError != nil {
		return nil, f.sendError
	}
	return &emptypb.Empty{}, nil
}

type completionRig struct {
	s      *Service
	f      *completionSession
	now    time.Time
	scope  *modv1.TransportScope
	header []byte
}

// Synthetic public-protocol packets exercise the real World parser. These are
// not observations from a live realm or a claim that an endpoint was reached.
func newCompletionRig(t *testing.T) *completionRig {
	t.Helper()
	x := &completionRig{s: NewIndependentService(1), f: &completionSession{clock: 0xfedcba98}, now: time.Unix(9000, 0), scope: &modv1.TransportScope{ConnectionId: "synthetic-connection", SelfGuid: 1, VisitBound: true, VisitRevision: 9, MapId: 0}}
	x.s.now = func() time.Time { return x.now }
	x.s.ConfigureSession(x.f)
	x.deliver(t, opcode.SMSGLoginVerifyWorld, login())
	fields := vitalFields()
	fields[unitFlags] = 0x100000
	x.deliver(t, opcode.SMSGUpdateObject, createFixture(1, fields))
	wire, _ := ownMovementOptionalWire(-1)
	x.deliver(t, opcode.MSGMoveHeartbeat, ownMovementMessage(1, wire))
	x.header = completionFlyingHeader(0, 14)
	x.start(t, x.header, 17)
	r := ownMovementSnapshot(t, x.s)
	if !r.SelfMotionAuthority.Known || r.SelfMotionAuthority.DurationRemainingMs != 1000 || r.SelfMotionAuthority.TransportScope.VisitRevision == r.VisitRevision {
		t.Fatal("fixture lacks current full standalone taxi proof and independent Core/World visit counters", r)
	}
	return x
}

func completionFlyingHeader(id uint32, destinationX float32) []byte {
	b := new(bytes.Buffer)
	b.Write([]byte{1, 1, 0}) // packed own GUID + normal world-space toggle
	for _, v := range []float32{10, 20, 30} {
		f32(b, v)
	}
	u32(b, id)
	b.WriteByte(0)
	u32(b, 0x2000)
	u32(b, 1000)
	u32(b, 2)
	for _, v := range []float32{11, 21, 31, destinationX, 24, 34} {
		f32(b, v)
	}
	return b.Bytes()
}

func (x *completionRig) deliver(t *testing.T, op uint32, body []byte) *worldv1.SnapshotResponse {
	t.Helper()
	x.scope.MotionRevision++
	_, err := x.s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: body, TransportScope: cloneMotionScope(x.scope)})
	if err != nil {
		t.Fatal(err)
	}
	return ownMovementSnapshot(t, x.s)
}

func (x *completionRig) start(t *testing.T, header []byte, generation uint64) *worldv1.SnapshotResponse {
	t.Helper()
	x.scope.SplineActive, x.scope.SplineIdKnown = true, true
	x.scope.SplineId = binary.LittleEndian.Uint32(header[15:])
	x.scope.SplineRevision = generation
	x.scope.SplineSourceRevision = x.scope.MotionRevision + 1
	x.scope.SplineSourceOpcode = opcode.SMSGMonsterMove
	return x.deliver(t, opcode.SMSGMonsterMove, header)
}

func (x *completionRig) request(t *testing.T) *worldv1.CompleteSplineRequest {
	t.Helper()
	r := ownMovementSnapshot(t, x.s)
	a := r.SelfMotionAuthority
	return &worldv1.CompleteSplineRequest{SelfGuid: a.SelfGuid, VisitRevision: a.VisitRevision, AuthorityRevision: a.Revision, MovementRevision: a.MovementRevision, SplineRevision: a.SplineRevision, ExpectedRevision: r.SplineCompletion.Revision, TransportScope: cloneMotionScope(a.TransportScope)}
}

func TestCompleteSplineRetainsFullMovementImageAndOpaqueZeroCounterLast(t *testing.T) {
	x := newCompletionRig(t)
	if _, err := x.s.CompleteSpline(context.Background(), x.request(t)); err == nil || x.f.clockCalls != 0 || len(x.f.packets) != 0 {
		t.Fatal("advertised duration is an ACK cadence guard", err)
	}
	x.now = x.now.Add(time.Second)
	before := ownMovementSnapshot(t, x.s)
	req := x.request(t)
	r, err := x.s.CompleteSpline(context.Background(), req)
	if err != nil || r.Revision != 1 || r.Operation.Pending || !r.Operation.TransportWritten || r.Operation.TransportUnknown || r.Operation.ArrivalKnown || !r.Operation.MovementImageInferred || r.Operation.Phase != "transport_written" || !r.Operation.SplineIdKnown || r.Operation.SplineId != 0 || len(x.f.packets) != 1 {
		t.Fatal(r, err)
	}
	p := x.f.packets[0]
	if p.Opcode != 0x2c9 || !proto.Equal(p.TransportScope, req.TransportScope) || !bytes.Equal(p.Payload[:2], []byte{1, 1}) {
		t.Fatal("completion lost opcode, packed mover or captured scope", p)
	}
	image, n, err := movementinfo.DecodePrefix(p.Payload[2:])
	expected := x.s.state.ownMovement.info
	expected.Time = x.f.clock
	expected.X, expected.Y, expected.Z = 14, 24, 34
	if err != nil || image != expected || len(p.Payload) != 2+n+4 || binary.LittleEndian.Uint32(p.Payload[2+n:]) != 0 {
		t.Fatal("full optional image/counter-last mismatch", image, expected, n, p.Payload, err)
	}
	after := ownMovementSnapshot(t, x.s)
	if !proto.Equal(before.SelfMovementFacts, after.SelfMovementFacts) || !proto.Equal(before.SelfSplineFacts, after.SelfSplineFacts) || after.Self.PositionKnown || after.Self.PositionInferred {
		t.Fatal("ACK write invented server facts or arrival", before, after)
	}
	// Both returned scope and receipt must be detached from retained custody.
	r.Operation.TransportScope.MotionRevision = 9999
	p.TransportScope.MotionRevision = 9999
	replay, err := x.s.CompleteSpline(context.Background(), req)
	if err != nil || replay.Operation.TransportScope.MotionRevision != req.TransportScope.MotionRevision || len(x.f.packets) != 1 || x.f.clockCalls != 1 {
		t.Fatal("exact replay or clone custody failed", replay, err)
	}
}

func TestCompleteSplineDeduplicatesCoreGenerationAcrossRepeatedHeaderAndNewOpaqueZero(t *testing.T) {
	x := newCompletionRig(t)
	x.now = x.now.Add(time.Second)
	first, err := x.s.CompleteSpline(context.Background(), x.request(t))
	if err != nil {
		t.Fatal(err)
	}
	oldRaw := ownMovementSnapshot(t, x.s).SelfSplineFacts.Revision
	x.start(t, x.header, 17) // byte-identical retransmission: new source counter, same physical generation.
	x.now = x.now.Add(time.Second)
	r := ownMovementSnapshot(t, x.s)
	if !r.SelfMotionAuthority.Known || r.SelfSplineFacts.Revision <= oldRaw {
		t.Fatal("fixture failed to retain actual retransmission", r)
	}
	if _, err := x.s.CompleteSpline(context.Background(), x.request(t)); err == nil || len(x.f.packets) != 1 {
		t.Fatal("raw revision minted another physical ACK", err)
	}
	x.start(t, completionFlyingHeader(0, 15), 18) // genuinely distinct header; opaque ID deliberately remains zero.
	x.now = x.now.Add(time.Second)
	r2, err := x.s.CompleteSpline(context.Background(), x.request(t))
	if err != nil || r2.Revision != first.Revision+1 || len(x.f.packets) != 2 || r2.Operation.SplineId != 0 || r2.Operation.ArrivalKnown {
		t.Fatal("distinct Core generation could not use new explicit CAS", r2, err)
	}
}

func TestCompleteSplineUnknownIsReservedBeforeClockAndNeverReplayed(t *testing.T) {
	x := newCompletionRig(t)
	x.now = x.now.Add(time.Second)
	req := x.request(t)
	x.f.onClock = func() {
		r := ownMovementSnapshot(t, x.s).SplineCompletion
		if r.Revision != 1 || !r.Operation.Pending || r.Operation.Phase != "reserved" {
			t.Fatal("clock ran before reservation", r)
		}
		copy := proto.Clone(req).(*worldv1.CompleteSplineRequest)
		copy.ExpectedRevision = 1
		if _, err := x.s.CompleteSpline(context.Background(), copy); err == nil {
			t.Fatal("concurrent proposal bypassed pending reservation")
		}
	}
	x.f.sendError = errors.New("unary lost after possible write")
	r, err := x.s.CompleteSpline(context.Background(), req)
	if err != nil || !r.Operation.Pending || !r.Operation.TransportUnknown || r.Operation.TransportWritten || r.Operation.ArrivalKnown || len(x.f.packets) != 1 {
		t.Fatal(r, err)
	}
	x.f.onClock = nil
	if _, err := x.s.CompleteSpline(context.Background(), req); err != nil || len(x.f.packets) != 1 || x.f.clockCalls != 1 {
		t.Fatal("uncertain exact request was replayed", err)
	}
	x.start(t, completionFlyingHeader(0, 15), 18)
	x.now = x.now.Add(time.Second)
	if _, err := x.s.CompleteSpline(context.Background(), x.request(t)); err == nil || len(x.f.packets) != 1 {
		t.Fatal("new generation discarded unresolved receipt", err)
	}
}

func TestCompleteSplineTypedNoDispatchAndClockFailureAllowOnlyNewExplicitCAS(t *testing.T) {
	for _, kind := range []string{"typed_core_refusal", "clock_error", "clock_scope_change"} {
		t.Run(kind, func(t *testing.T) {
			x := newCompletionRig(t)
			x.now = x.now.Add(time.Second)
			req := x.request(t)
			switch kind {
			case "typed_core_refusal":
				x.f.sendError = modcb.PacketNotDispatchedError("scope changed at transport owner")
			case "clock_error":
				x.f.clockError = errors.New("clock unavailable")
			case "clock_scope_change":
				x.f.onClock = func() { x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 49})) }
			}
			r, err := x.s.CompleteSpline(context.Background(), req)
			if err != nil || r.Revision != 1 || r.Operation.Pending || r.Operation.TransportWritten || r.Operation.TransportUnknown || r.Operation.Phase != "not_dispatched" || r.Operation.ArrivalKnown {
				t.Fatal(r, err)
			}
			writes := len(x.f.packets)
			clocks := x.f.clockCalls
			if _, err := x.s.CompleteSpline(context.Background(), req); err != nil || len(x.f.packets) != writes || x.f.clockCalls != clocks {
				t.Fatal("receipt replay made another attempt", err)
			}
			x.f.sendError, x.f.clockError, x.f.onClock = nil, nil, nil
			r, err = x.s.CompleteSpline(context.Background(), x.request(t))
			if err != nil || !r.Operation.TransportWritten || r.Revision != 2 || len(x.f.packets) != writes+1 {
				t.Fatal("known zero-send did not allow new explicit CAS", r, err)
			}
		})
	}
}

func TestCompleteSplineMalformedBarrierIsStickyOutsideRawRollback(t *testing.T) {
	x := newCompletionRig(t)
	x.now = x.now.Add(time.Second)
	before := ownMovementSnapshot(t, x.s)
	// A complete first block followed by a malformed second block must neither
	// commit raw facts nor leave the previous completion proof usable.
	bad := createFixture(1, vitalFields())
	binary.LittleEndian.PutUint32(bad, 2)
	bad = append(bad, 1, 1, 3)
	r := x.deliver(t, opcode.SMSGUpdateObject, bad)
	if r.MalformedPackets != before.MalformedPackets+1 || r.SelfMotionAuthority.Known || r.SelfMotionAuthority.UnitFlagsKnown || !proto.Equal(before.SelfMovementFacts, r.SelfMovementFacts) || !proto.Equal(before.SelfSplineFacts, r.SelfSplineFacts) {
		t.Fatal("malformed rollback retained current authority or leaked raw facts", r)
	}
	r = x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{unitHealth: 40}))
	if r.SelfMotionAuthority.Known {
		t.Fatal("later foreign/vitals packet restored malformed proof", r)
	}
	x.start(t, completionFlyingHeader(0, 15), 18)
	if r = ownMovementSnapshot(t, x.s); r.SelfMotionAuthority.Known || r.SelfMotionAuthority.UnitFlagsKnown {
		t.Fatal("fresh header invented a hidden taxi-flag observation", r)
	}
	x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitFlags: 0x100000}))
	if r = ownMovementSnapshot(t, x.s); !r.SelfMotionAuthority.Known {
		t.Fatal("fresh header plus actual taxi flag failed to restore proof", r)
	}
}

func TestCompleteSplineMotionGapAndUnsupportedBarrierCannotBeFreshenedByVitals(t *testing.T) {
	for _, kind := range []string{"gap", "unsupported", "missing_scope"} {
		t.Run(kind, func(t *testing.T) {
			x := newCompletionRig(t)
			switch kind {
			case "gap":
				x.scope.MotionRevision++ // unobserved inbound/outbound motion barrier.
			case "unsupported":
				x.deliver(t, opcode.SMSGFlightSplineSync, []byte{0, 0, 0, 0})
			case "missing_scope":
				packet(t, x.s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 49}))
			}
			r := x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 48}))
			if r.SelfMotionAuthority.Known || r.SelfMotionAuthority.UnitFlagsKnown {
				t.Fatal("current proof survived or was restored across unsupported history", kind, r)
			}
			x.now = x.now.Add(time.Second)
			if _, err := x.s.CompleteSpline(context.Background(), x.request(t)); err == nil || len(x.f.packets) != 0 {
				t.Fatal("unsupported history admitted completion", err)
			}
		})
	}
}

func TestCompleteSplineRejectsSupersessionModesAndScopeBindings(t *testing.T) {
	for _, kind := range []string{"ordinary_movement", "position_only", "root", "control", "knockback", "stop", "create", "same_map_visit", "transfer", "destroy", "foreign_scope", "cyclic", "relative", "not_flying", "final_facing"} {
		t.Run(kind, func(t *testing.T) {
			x := newCompletionRig(t)
			x.now = x.now.Add(time.Second)
			switch kind {
			case "ordinary_movement":
				b, _ := movementinfo.Encode(movementinfo.Info{X: 1, Y: 2, Z: 3})
				x.deliver(t, opcode.MSGMoveHeartbeat, ownMovementMessage(1, b))
			case "position_only":
				b := new(bytes.Buffer)
				u32(b, 1)
				b.Write([]byte{1, 1, 1, 0x40, 0})
				for _, v := range []float32{1, 2, 3, 0} {
					f32(b, v)
				}
				x.deliver(t, opcode.SMSGUpdateObject, b.Bytes())
			case "root":
				x.deliver(t, opcode.SMSGForceMoveRoot, []byte{1, 1, 0, 0, 0, 0})
			case "control":
				x.deliver(t, opcode.SMSGClientControlUpdate, []byte{1, 1, 1})
			case "knockback":
				x.deliver(t, opcode.SMSGMoveKnockBack, append([]byte{1, 1}, make([]byte, 20)...))
			case "stop":
				x.start(t, ownSplineWithID(t, monsterStopGolden, 0), 18)
			case "create":
				x.deliver(t, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
			case "same_map_visit":
				x.scope.VisitRevision++
				x.scope.SplineActive = false
				x.deliver(t, opcode.SMSGNewWorld, login())
			case "transfer":
				x.scope.VisitBound = false
				x.deliver(t, opcode.SMSGTransferPending, make([]byte, 4))
			case "destroy":
				x.deliver(t, opcode.SMSGDestroyObject, append(binary.LittleEndian.AppendUint64(nil, 1), 0))
			case "foreign_scope":
				x.scope.SelfGuid = 3
				x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 49}))
			case "cyclic":
				b := completionFlyingHeader(0, 14)
				binary.LittleEndian.PutUint32(b[20:], 0x82000)
				x.start(t, b, 18)
			case "relative":
				x.scope.SplineSourceOpcode = opcode.SMSGMonsterMoveTransport
				x.scope.SplineSourceRevision = x.scope.MotionRevision + 1
				x.scope.SplineRevision = 18
				b := completionFlyingHeader(0, 14)
				b = append([]byte{1, 1, 1, 42, 0}, b[2:]...)
				x.deliver(t, opcode.SMSGMonsterMoveTransport, b)
			case "not_flying":
				b := completionFlyingHeader(0, 14)
				binary.LittleEndian.PutUint32(b[20:], 0x40000)
				x.start(t, b, 18)
			case "final_facing":
				b := completionFlyingHeader(0, 14)
				b[19] = 4
				facing := binary.LittleEndian.AppendUint32(append([]byte(nil), b[:20]...), math.Float32bits(.5))
				b = append(facing, b[20:]...)
				x.start(t, b, 18)
			}
			r := ownMovementSnapshot(t, x.s)
			if r.SelfMotionAuthority.Known {
				t.Fatal("supersession/unsupported mode retained authority", kind, r)
			}
			if _, err := x.s.CompleteSpline(context.Background(), x.request(t)); err == nil || len(x.f.packets) != 0 {
				t.Fatal("stale authority emitted ACK", kind, err)
			}
		})
	}
}

func TestCompleteSplineSnapshotSettlementIsNotRawFreshnessOrNewAuthority(t *testing.T) {
	x := newCompletionRig(t)
	// Exercise the legacy inferred-pose read independently of receiving a new
	// control packet. The private standalone proof must survive its cache clear.
	x.s.state.players[0].trajectory.controlReturned = true
	x.now = x.now.Add(time.Second)
	r := ownMovementSnapshot(t, x.s)
	if !r.Self.PositionInferred || !r.Self.PositionKnown || !r.SelfMotionAuthority.Known || r.SelfSplineFacts.AgeMs != 1000 || r.SelfMovementFacts.AgeMs != 1000 {
		t.Fatal("legacy settlement changed raw freshness or current source", r)
	}
	before := proto.Clone(r.SelfMotionAuthority).(*worldv1.OwnMotionAuthority)
	r2 := ownMovementSnapshot(t, x.s)
	if !proto.Equal(before, r2.SelfMotionAuthority) || !proto.Equal(r.SelfMovementFacts, r2.SelfMovementFacts) || !proto.Equal(r.SelfSplineFacts, r2.SelfSplineFacts) {
		t.Fatal("snapshot minted source authority", r, r2)
	}
}

func TestCompleteSplineRequiresActualMovementAndActualUnitFlags(t *testing.T) {
	x := newCompletionRig(t)
	x.s.state.ownMovement = ownMovementObservation{} // empty AllFinite value is not a received image.
	if ownMovementSnapshot(t, x.s).SelfMotionAuthority.Known {
		t.Fatal("empty movement value became a ground baseline")
	}
	// A CREATE's omitted zero flags are actual zero, not absence or a taxi bit.
	x2 := newCompletionRig(t)
	fields := vitalFields()
	delete(fields, unitFlags)
	r := x2.deliver(t, opcode.SMSGUpdateObject, createFixture(1, fields))
	if !r.SelfMotionAuthority.UnitFlagsKnown || r.SelfMotionAuthority.UnitFlags != 0 || r.SelfMotionAuthority.UnitFlagsRevision != 2 || r.SelfMotionAuthority.UnitFlagsSourceOpcode != opcode.SMSGUpdateObject || r.SelfMotionAuthority.UnitFlagsAgeMs != 0 {
		t.Fatal("CREATE omitted-zero flags lost factual provenance", r)
	}
	x2.start(t, completionFlyingHeader(0, 15), 18)
	if ownMovementSnapshot(t, x2.s).SelfMotionAuthority.Known {
		t.Fatal("known zero flags were treated as taxi")
	}
}

func TestCompleteSplineRequiresEveryExactBindingBeforeReservation(t *testing.T) {
	for _, name := range []string{"self", "visit", "authority", "movement", "spline", "counter", "scope", "missing_scope"} {
		t.Run(name, func(t *testing.T) {
			x := newCompletionRig(t)
			x.now = x.now.Add(time.Second)
			req := x.request(t)
			switch name {
			case "self":
				req.SelfGuid++
			case "visit":
				req.VisitRevision++
			case "authority":
				req.AuthorityRevision++
			case "movement":
				req.MovementRevision++
			case "spline":
				req.SplineRevision++
			case "counter":
				req.ExpectedRevision++
			case "scope":
				req.TransportScope.ConnectionId = "replacement-connection"
			case "missing_scope":
				req.TransportScope = nil
			}
			if _, err := x.s.CompleteSpline(context.Background(), req); err == nil || len(x.f.packets) != 0 || x.f.clockCalls != 0 || ownMovementSnapshot(t, x.s).SplineCompletion.Revision != 0 {
				t.Fatal("bad binding reserved or dispatched", name, err)
			}
		})
	}
}

func TestCompleteSplineUnitFlagsProvenanceDoesNotRefreshFromVitalsOrSnapshots(t *testing.T) {
	x := newCompletionRig(t)
	before := ownMovementSnapshot(t, x.s).SelfMotionAuthority
	x.now = x.now.Add(time.Second)
	r := x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 49}))
	a := r.SelfMotionAuthority
	if !a.Known || !a.UnitFlagsKnown || a.UnitFlagsRevision != before.UnitFlagsRevision || a.UnitFlagsAgeMs != 1000 || a.UnitFlagsSourceOpcode != before.UnitFlagsSourceOpcode || a.UnitFlagsMotionRevision != before.UnitFlagsMotionRevision || a.TransportScope.MotionRevision <= before.TransportScope.MotionRevision || a.TransportScope.SplineSourceRevision != before.TransportScope.SplineSourceRevision {
		t.Fatal("foreign/general catchup refreshed actual taxi/source evidence", before, a)
	}
	for i := 0; i < 2; i++ {
		if after := ownMovementSnapshot(t, x.s).SelfMotionAuthority; !proto.Equal(a, after) {
			t.Fatal("snapshot refreshed flags or authority", a, after)
		}
	}
}

func TestCompleteSplineRequiresActualKnownAliveBody(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fields     map[int]uint32
		aliveKnown bool
	}{
		{"dead_health", map[int]uint32{unitHealth: 0}, true},
		{"ghost_with_health", map[int]uint32{playerFlags: 0x10}, true},
		{"dead_dynamic_flags", map[int]uint32{unitDynamicFlags: 0x20}, true},
		{"unknown_health", map[int]uint32{unitHealth: 101}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newCompletionRig(t)
			x.now = x.now.Add(time.Second)
			before := ownMovementSnapshot(t, x.s)
			r := x.deliver(t, opcode.SMSGUpdateObject, deltaFixture(1, tc.fields))
			if r.MalformedPackets != before.MalformedPackets || r.Self.AliveKnown != tc.aliveKnown || r.Self.Alive || r.SelfMotionAuthority.Known || r.SelfMotionAuthority.Reason != "actual alive body unavailable" || !proto.Equal(before.SelfMovementFacts, r.SelfMovementFacts) || !proto.Equal(before.SelfSplineFacts, r.SelfSplineFacts) {
				t.Fatal("death/unknown life did not fence completion while preserving raw history", r)
			}
			if _, err := x.s.CompleteSpline(context.Background(), x.request(t)); err == nil || x.f.clockCalls != 0 || len(x.f.packets) != 0 || ownMovementSnapshot(t, x.s).SplineCompletion.Revision != 0 {
				t.Fatal("dead/unknown body reserved or dispatched completion", err)
			}
		})
	}
}
