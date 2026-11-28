package world

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	actions "github.com/agent-wow/agent-wow/modules/buddy-actions"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	buddyworld "github.com/agent-wow/agent-wow/modules/buddy-world"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
)

type bodyUpdateTrailer struct {
	grpc.ServerTransportStream
	md metadata.MD
}

func (s *bodyUpdateTrailer) SetTrailer(md metadata.MD) error {
	s.md = metadata.Join(s.md, md)
	return nil
}

func compressedBodyUpdates(t *testing.T, blocks ...[]byte) []byte {
	t.Helper()
	raw := binary.LittleEndian.AppendUint32(nil, uint32(len(blocks)))
	for _, b := range blocks {
		raw = append(raw, b...)
	}
	out := bytes.NewBuffer(binary.LittleEndian.AppendUint32(nil, uint32(len(raw))))
	z := zlib.NewWriter(out)
	if _, err := z.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func bodyValuesBlock(guid byte, field int, value uint32) []byte {
	b := []byte{0, 0}
	if guid != 0 {
		b = []byte{0, 1, guid}
	}
	b = append(b, byte(field/32+1))
	for i := 0; i < field/32; i++ {
		b = binary.LittleEndian.AppendUint32(b, 0)
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(1)<<uint(field%32))
	return binary.LittleEndian.AppendUint32(b, value)
}

// This adapter only transports module calls. World supplies every observed
// fact, Core supplies every original write/attempt, and Actions runs unchanged.
type bodyContinuitySession struct {
	t          *testing.T
	e          *engine
	w          *buddyworld.Service
	started    time.Time
	afterWrite func(uint32)
}

func newBodyContinuitySession(t *testing.T) (*bodyContinuitySession, *scopeWriteConn) {
	c := &scopeWriteConn{}
	return &bodyContinuitySession{t: t, e: scopedBodyEngine(t, c), w: buddyworld.NewService(99), started: time.Now()}, c
}

func (c *bodyContinuitySession) observe(op uint16, wire []byte) (string, *modv1.WorldPacket) {
	c.t.Helper()
	c.e.observeTransportScope(op, wire)
	p := &modv1.WorldPacket{Opcode: uint32(op), Payload: wire, TransportScope: c.e.transport.snapshot()}
	tr := &bodyUpdateTrailer{}
	ctx := grpc.NewContextWithServerTransportStream(context.Background(), tr)
	if _, err := c.w.OnPacket(ctx, p); err != nil {
		c.t.Fatal(err)
	}
	result := modcb.BodyUpdateResult(tr.md.Get(modcb.BodyInputUpdateTrailer), tr.md.Get(modcb.BodyInputBarrierTrailer))
	if _, relevant := movementinfo.RouteHullSubject(uint32(op), wire); relevant {
		c.e.s.recordRouteHulls(p.TransportScope, tr.md.Get(modcb.RouteHullTrailer))
	}
	if op == opcode.SMSGUpdateObject || op == opcode.SMSGCompressedUpdateObject {
		positions := modcb.ObservedOwnerPositions(result, tr.md.Get(modcb.OwnerPositionTrailer))
		c.e.s.recordParsedBodyUpdate(p.TransportScope, modcb.BodyUpdateDetail(result, tr.md.Get(modcb.BodyInputFieldTrailer)), positions...)
	}
	return result, p
}

func TestFollowTakeoffChecksAllObservedOwnerPositionsAtTheSocket(t *testing.T) {
	const owner, self = 3, 99
	anchor := movementinfo.FollowPosition{100, 23, -10}
	for _, tc := range []struct {
		change string
		admit  bool
	}{
		{"same_heartbeat", true}, {"swim_in_band", true}, {"batch_at_radius", true},
		{"batch_out_and_back", false}, {"vertical_diversion", false}, {"owner_death_and_move", false},
		{"own_death_and_owner_move", false}, {"owner_destroy", false}, {"incomplete", false},
		{"no_route_anchor", false}, {"self_control", false}, {"same_map_new_visit", false},
	} {
		t.Run(tc.change, func(t *testing.T) {
			c, conn := newBodyContinuitySession(t)
			c.observe(opcode.SMSGLoginVerifyWorld, positionBody(349, 7, 8, 9, 0))
			c.observe(opcode.SMSGUpdateObject, bodyPlayerCreate(self, 7, 8, 9, 0))
			c.observe(opcode.SMSGUpdateObject, bodyPlayerCreate(owner, anchor[0], anchor[1], anchor[2], 0))
			route := modsession.WithRouteBodyInput(context.Background(), true)
			if tc.change != "no_route_anchor" {
				route = modsession.WithFollowPosition(route, anchor)
			}
			scope := c.e.transport.snapshot()
			ground, err := movementinfo.MoverFrame(self, movementinfo.Info{Flags: 1, X: 7, Y: 8, Z: 9})
			if err != nil {
				t.Fatal(err)
			}
			air, err := movementinfo.MoverFrame(self, movementinfo.Info{Flags: 0x1001, X: 7.7, Y: 8, Z: 8.9,
				FallTime: 100, JumpPresent: true, Jump: movementinfo.Jump{CosAngle: 1, XYSpeed: 7}})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := c.e.writeContext(route, opcode.MSGMoveStartForward, ground, scope); err != nil {
				t.Fatal(err)
			}
			source := c.e.transport.snapshot()
			ownerFrame := func(dx, dz float32) []byte {
				wire, err := movementinfo.MoverFrame(owner, movementinfo.Info{Time: 1234, Flags: movementinfo.SwimmingFlag,
					PitchPresent: true, X: anchor[0] + dx, Y: anchor[1], Z: anchor[2] + dz, Orientation: .7})
				if err != nil {
					t.Fatal(err)
				}
				return wire
			}
			ownerBlock := func(dx float32) []byte {
				wire := ownerFrame(dx, 0)
				block := append([]byte{1, 1, owner, 0x20, 0}, wire[2:]...)
				for _, speed := range []float32{2.5, 7, 4.5, 4.7, 2.5, 7, 4.5, 3.14, 3.14} {
					block = binary.LittleEndian.AppendUint32(block, math.Float32bits(speed))
				}
				return block
			}
			c.e.beforeScopedWrite = func() error {
				c.e.beforeScopedWrite = nil
				switch tc.change {
				case "same_heartbeat", "no_route_anchor":
					c.observe(opcode.MSGMoveHeartbeat, ownerFrame(0, 0))
				case "swim_in_band":
					c.observe(opcode.MSGMoveHeartbeat, ownerFrame(.7, .3))
				case "vertical_diversion":
					c.observe(opcode.MSGMoveHeartbeat, ownerFrame(0, 1.51))
				case "batch_at_radius":
					c.observe(opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, ownerBlock(.3), ownerBlock(1.5)))
				case "batch_out_and_back":
					c.observe(opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, ownerBlock(1.51), ownerBlock(0)))
				case "owner_death_and_move":
					c.observe(opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, bodyValuesBlock(owner, 0x18, 0), ownerBlock(0)))
				case "own_death_and_owner_move":
					c.observe(opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, ownerBlock(0), bodyValuesBlock(self, 0x18, 0), bodyValuesBlock(self, 0x18, 100)))
				case "owner_destroy":
					c.observe(opcode.SMSGDestroyObject, append(binary.LittleEndian.AppendUint64(nil, owner), 0))
				case "incomplete":
					c.observe(opcode.MSGMoveHeartbeat, ownerFrame(0, 0)[:13])
				case "self_control":
					c.observe(opcode.SMSGClientControlUpdate, []byte{1, self, 0})
					c.observe(opcode.MSGMoveHeartbeat, ownerFrame(0, 0))
				case "same_map_new_visit":
					c.observe(opcode.SMSGNewWorld, positionBody(349, 7, 8, 9, 0))
					c.observe(opcode.MSGMoveHeartbeat, ownerFrame(0, 0))
				}
				return nil
			}
			before := conn.writes
			_, _, err = c.e.writeContext(route, opcode.MSGMoveHeartbeat, air, source)
			if tc.admit {
				if err != nil || conn.writes != before+1 {
					t.Fatalf("valid takeoff refused: %v, writes %d to %d", err, before, conn.writes)
				}
			} else if !modcb.PacketWasNotDispatched(err) || conn.writes != before {
				t.Fatalf("diverted takeoff escaped: %v, writes %d to %d", err, before, conn.writes)
			}
		})
	}
}

func (c *bodyContinuitySession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	id := modcb.IncomingMovementAttempt(metadata.NewIncomingContext(ctx, md))
	if r.TransportScope != nil {
		if id == "" {
			c.t.Fatal("measured frame has no exact attempt")
		}
		ctx = modsession.WithBodyInput(modsession.WithWriteOrigin(ctx, modsession.WriteOrigin{MovementAttempt: id}))
	}
	_, _, err := c.e.writeContext(ctx, r.Opcode, r.Payload, r.TransportScope)
	if err == nil && c.afterWrite != nil {
		c.afterWrite(r.Opcode)
	}
	return &emptypb.Empty{}, err
}
func (c *bodyContinuitySession) GetClock(ctx context.Context, _ *emptypb.Empty, _ ...grpc.CallOption) (*modv1.ClockResponse, error) {
	return &modv1.ClockResponse{ClientTimeMs: 1000 + uint32(time.Since(c.started).Milliseconds())}, ctx.Err()
}
func (c *bodyContinuitySession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, _ ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	var response proto.Message
	var err error
	switch {
	case r.Module == "buddy-world" && r.Method == "snapshot":
		q := &worldv1.SnapshotRequest{}
		if err = r.Request.UnmarshalTo(q); err == nil {
			response, err = c.w.Snapshot(ctx, q)
		}
	case r.Module == "buddy-world" && r.Method == "observe_client_movement":
		q := &worldv1.ClientMovementRequest{}
		if err = r.Request.UnmarshalTo(q); err == nil {
			response, err = c.w.ObserveClientMovement(ctx, q)
		}
	case r.Module == "session" && (r.Method == modcb.BodyInputReportMethod || r.Method == modcb.SelfBodyInputReportMethod || r.Method == modcb.SettledBodyInputReportMethod):
		if r.Method == modcb.SettledBodyInputReportMethod {
			ctx = modsession.WithSettledBodySource(ctx)
		}
		if r.Method == modcb.SelfBodyInputReportMethod {
			ctx = modsession.WithSelfBodyInput(ctx)
		}
		q := &structpb.Struct{}
		if err = r.Request.UnmarshalTo(q); err != nil {
			break
		}
		p, reason := c.e.s.bodyInputReport(ctx, q.Fields["attemptId"].GetStringValue())
		if p == nil {
			return nil, status.Error(codes.FailedPrecondition, reason)
		}
		response = p
	default:
		return nil, fmt.Errorf("unexpected method %s.%s", r.Module, r.Method)
	}
	if err != nil {
		return nil, err
	}
	packed, err := anypb.New(response)
	return &modv1.InvokeModuleResponse{Result: packed}, err
}

func TestParsedWorldUpdateRetainsOnlyExactQualifiedPhysicalSource(t *testing.T) {
	for _, mode := range []string{"two_powers", "own_death_restore", "owner_death", "unknown_subject", "malformed", "sitting", "missing", "control_then_power", "competing_write", "new_visit"} {
		t.Run(mode, func(t *testing.T) {
			c, conn := newBodyContinuitySession(t)
			c.observe(opcode.SMSGLoginVerifyWorld, positionBody(349, 1, 2, 3, .5))
			ctx := modsession.WithBodyInput(modsession.WithWriteOrigin(context.Background(), modsession.WriteOrigin{MovementAttempt: strings.Repeat("a", 64)}))
			if _, _, err := c.e.writeContext(ctx, opcode.MSGMoveStartForward, scopedBodyPacket(t, opcode.MSGMoveStartForward), c.e.transport.snapshot()); err != nil {
				t.Fatal(err)
			}
			prior := c.e.s.OwnMovementReport()
			power := compressedBodyUpdates(t, bodyValuesBlock(23, 0x19, 17))
			add := func(b []byte) { c.observe(opcode.SMSGCompressedUpdateObject, b) }
			var want string
			switch mode {
			case "two_powers":
				add(power)
				c.observe(opcode.SMSGStandstateUpdate, []byte{0})
				add(power)
			case "own_death_restore":
				add(compressedBodyUpdates(t, bodyValuesBlock(99, 0x18, 0), bodyValuesBlock(99, 0x18, 100)))
				add(power)
				want = modcb.BodyUpdateSelfField
			case "owner_death":
				add(compressedBodyUpdates(t, bodyValuesBlock(3, 0x18, 0)))
				want = modcb.BodyUpdateOwnerField
			case "unknown_subject":
				add(compressedBodyUpdates(t, bodyValuesBlock(0, 0x19, 17)))
				want = modcb.BodyUpdateUnknownSubject
			case "malformed":
				add(append(slices.Clone(power), 1))
				want = modcb.BodyUpdateMalformed
			case "sitting":
				c.observe(opcode.SMSGStandstateUpdate, []byte{1})
				c.observe(opcode.SMSGStandstateUpdate, []byte{0})
				add(power)
				want = "own_stand_state"
			case "missing":
				c.e.observeTransportScope(opcode.SMSGCompressedUpdateObject, power)
				want = modcb.BodyUpdateMissing
			case "control_then_power":
				c.e.observeTransportScope(opcode.SMSGClientControlUpdate, []byte{1, 99, 0})
				add(power)
				want = "own_motion_or_control"
			case "competing_write":
				if _, _, err := c.e.writeContext(context.Background(), opcode.MSGMoveStartForward, prior.Payload); err != nil {
					t.Fatal(err)
				}
				add(power)
				want = "exact_write_unavailable"
			case "new_visit":
				c.observe(opcode.SMSGNewWorld, positionBody(349, 1, 2, 3, .5))
				add(power)
				want = "body_or_visit_changed"
			}
			got, reason := c.e.s.bodyInputReport(ctx, strings.Repeat("a", 64))
			class, _, _ := strings.Cut(reason, " ")
			if class != want || (got != nil) != (want == "") || (got != nil && !proto.Equal(got, prior)) || c.e.s.OwnMovementReport() != nil {
				t.Fatal("lost provenance", reason, want, got, prior)
			}
			writes := conn.writes
			_, _, err := c.e.writeContext(ctx, opcode.MSGMoveHeartbeat, scopedBodyPacket(t, opcode.MSGMoveHeartbeat), prior.TransportScope)
			if want == "" && (err != nil || conn.writes != writes+1) || want != "" && (!modcb.PacketWasNotDispatched(err) || conn.writes != writes) {
				t.Fatal("physical final admission", want, reason, err, conn.writes, writes)
			}
		})
	}
}

func bodyPlayerCreate(guid byte, x, y, z, o float32) []byte {
	// Real 3.3.5 CREATE_PLAYER with living movement, nine speeds and the
	// positive health/max-health and unit flags used by the normal follow guard.
	b := binary.LittleEndian.AppendUint32(nil, 1)
	b = append(b, 3, 1, guid, 4, 0x20, 0)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = append(b, 0, 0)
	b = binary.LittleEndian.AppendUint32(b, 123)
	for _, v := range []float32{x, y, z, o} {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(v))
	}
	b = binary.LittleEndian.AppendUint32(b, 0)
	for _, v := range []float32{2.5, 7, 4.5, 4.7, 2.5, 7, 4.5, 3.14, 3.14} {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(v))
	}
	b = append(b, 2)
	for _, v := range []uint32{1 << 0x18, (1 << (0x3b - 32)) | 1, 100, 100, 8} {
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	return b
}

func busyBodyUpdate(t *testing.T) []byte {
	foreignMove := []byte{1, 1, 23, 0x40, 0}
	for _, v := range []float32{30, 33, 20, 1} {
		foreignMove = binary.LittleEndian.AppendUint32(foreignMove, math.Float32bits(v))
	}
	return compressedBodyUpdates(t, bodyPlayerCreate(23, 30, 33, 20, 1)[4:], foreignMove,
		bodyValuesBlock(99, 0x18, 87), bodyValuesBlock(99, 0x18, 100), bodyValuesBlock(99, 0x3b, 8), bodyValuesBlock(99, 0x53, 7),
		bodyValuesBlock(99, 0x162, 0), bodyValuesBlock(23, 0x19, 17), []byte{4, 1, 0, 0, 0, 1, 23})
}

func TestCopiedMaraudonNativeFollowThroughParsedWorldAndCore(t *testing.T) {
	root := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if root == "" {
		t.Skip("set BUDDY_NAV_POOL_TEST_DATA_DIR to extracted Maraudon collision and liquid")
	}
	for _, mode := range []string{"ambient", "own_death_restore"} {
		t.Run(mode, func(t *testing.T) {
			c, conn := newBodyContinuitySession(t)
			a := actions.NewService(c, 99, "continuity-fixture")
			a.SetNavigationDataDir(root)
			ctx := context.Background()
			const x, y, z, o = 475.6561, 50.766533, -96.19031, 3.13039
			for _, p := range []struct {
				op   uint16
				wire []byte
			}{
				{opcode.SMSGLoginVerifyWorld, positionBody(349, x, y, z, o)},
				{opcode.SMSGUpdateObject, bodyPlayerCreate(99, x, y, z, o)},
				{opcode.SMSGUpdateObject, bodyPlayerCreate(3, 390.99612, 27.22958, -130.92584, .5)},
			} {
				_, packet := c.observe(p.op, p.wire)
				if _, err := a.OnPacket(ctx, packet); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.Follow(ctx, &actionsv1.ToggleRequest{Enabled: true}); err != nil {
				t.Fatal(err)
			}
			count := 0
			c.afterWrite = func(op uint32) {
				if op != opcode.MSGMoveStartForward && op != opcode.MSGMoveStartSwim {
					return
				}
				count++
				if mode == "own_death_restore" {
					c.observe(opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, bodyValuesBlock(99, 0x18, 0), bodyValuesBlock(99, 0x18, 100)))
				} else {
					c.observe(opcode.SMSGStandstateUpdate, []byte{0})
					c.observe(opcode.SMSGCompressedUpdateObject, busyBodyUpdate(t))
				}
			}
			md, _ := metadata.FromOutgoingContext(modcb.WithPolicySpell(ctx, strings.Repeat("b", 64)))
			out, err := a.MoveBody(metadata.NewIncomingContext(ctx, md), &actionsv1.BodyMovementRequest{SessionId: "continuity-fixture", SelfGuid: 99, VisitId: 1, SequenceId: 23, DurationMs: 500, Landing: "follow_water"})
			if err != nil {
				t.Fatal(err)
			}
			body := out.GetBodyMovement()
			if mode == "ambient" {
				if count != 2 || body.GetPhase() != "body_input_written" || out.MotionUnconfirmed || conn.opcodes[len(conn.opcodes)-1] != opcode.MSGMoveStop || !slices.Contains(conn.opcodes, opcode.MSGMoveHeartbeat) || !slices.Contains(conn.opcodes, opcode.MSGMoveStartSwim) {
					t.Fatal("measured follow interrupted", count, out, conn.opcodes)
				}
				return
			}
			if count != 1 || body.GetPhase() != "body_scope_unconfirmed_no_retry" || !body.GetTransportWritten() || !out.MotionUnconfirmed || !strings.Contains(body.Detail, modcb.BodyUpdateSelfField) || slices.Contains(conn.opcodes, opcode.MSGMoveHeartbeat) || slices.Contains(conn.opcodes, opcode.MSGMoveStop) {
				t.Fatal("mixed update lost partial custody", out, conn.opcodes)
			}
			stopped, err := a.Follow(ctx, &actionsv1.ToggleRequest{Enabled: false})
			if err != nil || stopped.MotionUnconfirmed || stopped.Moving || stopped.Reason != "follow_disabled" || conn.opcodes[len(conn.opcodes)-1] != opcode.MSGMoveStop || body.Phase != "body_scope_unconfirmed_no_retry" {
				t.Fatal("distinct explicit stop did not settle physical motion", stopped, err, conn.opcodes)
			}
			// A later action can start at this exact settled STOP through
			// returning peers, health changes, emotes and unrelated updates.
			// The interrupted sequence cannot send another frame.
			c.afterWrite = nil
			c.observe(opcode.SMSGStandstateUpdate, []byte{1})
			c.observe(opcode.SMSGStandstateUpdate, []byte{0})
			c.observe(opcode.SMSGCompressedUpdateObject, busyBodyUpdate(t))
			if _, err := a.Follow(ctx, &actionsv1.ToggleRequest{Enabled: true}); err != nil {
				t.Fatal(err)
			}
			before := conn.writes
			same := &actionsv1.BodyMovementRequest{SessionId: "continuity-fixture", SelfGuid: 99, VisitId: 1, SequenceId: 23, DurationMs: 500, Landing: "follow_water"}
			freshCtx := metadata.NewIncomingContext(ctx, md)
			if old, err := a.MoveBody(freshCtx, same); err != nil || old.GetBodyMovement().GetPhase() != "body_scope_unconfirmed_no_retry" || conn.writes != before {
				t.Fatal("replayed failed input", err, old)
			}
			same.SequenceId++
			next, err := a.MoveBody(freshCtx, same)
			if err != nil || next.GetBodyMovement().GetPhase() != "body_input_written" || next.MotionUnconfirmed || !slices.Contains(conn.opcodes[before:], opcode.MSGMoveStartSwim) {
				t.Fatal("new selection failed from settled STOP", err, next, conn.opcodes[before:])
			}
		})
	}
}

func TestBodyProofDistinguishesEqualLengthPacketsAndStandingChanges(t *testing.T) {
	c, _ := newBodyContinuitySession(t)
	c.observe(opcode.SMSGLoginVerifyWorld, positionBody(349, 1, 2, 3, .5))
	c.observe(opcode.SMSGUpdateObject, bodyPlayerCreate(99, 1, 2, 3, .5))
	// These two synthetic 27-byte frames have independently fixed contents.
	for _, row := range []struct{ hex, reason string }{
		{"1800000078016364000246716620c9046282003f100300026a002e", modcb.BodyUpdateHarmless},
		{"14000000780163640002c6644620c90c626a3331300000067b0097", modcb.BodyUpdateSelfField},
	} {
		wire, err := hex.DecodeString(row.hex)
		if err != nil || len(wire) != 27 {
			t.Fatal(err, len(wire))
		}
		if got, _ := c.observe(opcode.SMSGCompressedUpdateObject, wire); got != row.reason {
			t.Fatal(got, row.reason)
		}
	}
	for _, row := range []struct {
		value  uint32
		reason string
	}{
		{1, modcb.BodyUpdatePosture},            // sit
		{0, modcb.BodyUpdateHarmless},           // normal stand-up during a forward input
		{0x01000000, modcb.BodyUpdateSelfField}, // changed animation tier
		{0x01000007, modcb.BodyUpdateSelfField}, // dead stand state
		{0x01000000, modcb.BodyUpdateSelfField}, // cannot bridge that earlier state
	} {
		got, _ := c.observe(opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, bodyValuesBlock(99, 0x4a, row.value)))
		if got != row.reason {
			t.Fatal(row, got)
		}
	}
}

type bodyProofWaitContext struct {
	context.Context
	once  sync.Once
	ready func()
}

func (c *bodyProofWaitContext) Done() <-chan struct{} { c.once.Do(c.ready); return c.Context.Done() }

func TestPhysicalDrainAfterWaitingForWorld(t *testing.T) {
	for _, mode := range []string{"harmless", "transfer", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			c, conn := newBodyContinuitySession(t)
			prior := c.e.transport.snapshot()
			wire := compressedBodyUpdates(t, bodyValuesBlock(23, 0x19, 17))
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &bodyProofWaitContext{Context: base}
			drains := 0
			queuedTransfer := false
			c.e.beforeScopedWrite = func() error {
				drains++
				if drains == 1 {
					c.e.observeTransportScope(opcode.SMSGCompressedUpdateObject, wire)
				}
				if queuedTransfer {
					c.e.observeTransportScope(opcode.SMSGNewWorld, positionBody(prior.MapId, 1, 2, 3, .5))
					queuedTransfer = false
				}
				return nil
			}
			// The positive response for the earlier packet wakes the writer
			// only after a new physical event has become ready to drain.
			ctx.ready = func() {
				queuedTransfer = mode == "transfer"
				c.e.s.recordParsedBodyUpdate(c.e.s.TransportScope(), modcb.BodyUpdateHarmless)
				if mode == "cancel" {
					cancel()
				}
			}
			_, _, err := c.e.writeContext(modsession.WithBodyInput(ctx), opcode.MSGMoveHeartbeat, scopedBodyPacket(t, opcode.MSGMoveHeartbeat), prior)
			if mode == "harmless" && (err != nil || drains < 2 || conn.writes != 1) || mode != "harmless" && (!modcb.PacketWasNotDispatched(err) || conn.writes != 0) || mode == "transfer" && drains < 2 {
				t.Fatal(mode, err, drains, conn.writes)
			}
		})
	}
}

func TestBodyRefusalReportsParsedOwnFieldsInOriginalOrder(t *testing.T) {
	for _, row := range []struct {
		name   string
		blocks [][]byte
		want   string
	}{
		{"stand_and_health", [][]byte{bodyValuesBlock(99, 0x4a, 1), bodyValuesBlock(99, 0x18, 0), bodyValuesBlock(99, 0x18, 100)},
			"own_restricted_field 0x0018=0x00000064>0x00000000 0x0018=0x00000000>0x00000064"},
		{"unknown_prior_and_private", [][]byte{bodyValuesBlock(99, 0x41, 7), bodyValuesBlock(99, 0x0c, 0xabcdef99)},
			"own_restricted_field 0x0041=?>0x00000007 0x000c"},
		{"bounded", [][]byte{bodyValuesBlock(99, 0x4a, 7), bodyValuesBlock(99, 0x4a, 8), bodyValuesBlock(99, 0x4a, 9), bodyValuesBlock(99, 0x4a, 10), bodyValuesBlock(99, 0x4a, 11)},
			"own_restricted_field 0x004a=0x00000000>0x00000007 0x004a=0x00000007>0x00000008 0x004a=0x00000008>0x00000009 more"},
		{"owner", [][]byte{bodyValuesBlock(3, 0x18, 0)}, modcb.BodyUpdateOwnerField},
	} {
		t.Run(row.name, func(t *testing.T) {
			c, conn := newBodyContinuitySession(t)
			c.observe(opcode.SMSGLoginVerifyWorld, positionBody(349, 1, 2, 3, .5))
			c.observe(opcode.SMSGUpdateObject, bodyPlayerCreate(99, 1, 2, 3, .5))
			prior := c.e.transport.snapshot()
			c.observe(opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, row.blocks...))
			c.observe(opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, bodyValuesBlock(23, 0x19, 15)))
			_, _, err := c.e.writeContext(modsession.WithBodyInput(context.Background()), opcode.MSGMoveHeartbeat, scopedBodyPacket(t, opcode.MSGMoveHeartbeat), prior)
			if !modcb.PacketWasNotDispatched(err) || !strings.HasSuffix(err.Error(), "body input before write: "+row.want) || conn.writes != 0 {
				t.Fatal(row.name, err, conn.writes)
			}
		})
	}
	for _, fields := range [][]string{{"malformed"}, {"0x2100"}, {"0x1fe0"}, {"0x0162=0x00000000>0x00000001"}, {"0x0012=?>0x00000001"}, {"0x0018=not-a-value"}, {strings.Repeat("0", 512)}, {"more"}, {"0x0018", "0x0018", "0x0018", "0x0018"}} {
		if got := modcb.BodyUpdateDetail(modcb.BodyUpdateSelfField, fields); got != modcb.BodyUpdateSelfField {
			t.Fatal("unexpected diagnostic", got)
		}
	}
	if got := modcb.BodyUpdateDetail(modcb.BodyUpdateHarmless, []string{"0x0018"}); got != modcb.BodyUpdateHarmless {
		t.Fatal("diagnostic changed admission", got)
	}
}

func TestOwnPoseAndDestinationAreDistinctPhysicalSources(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		subject              byte
		op                   uint16
		settled, own, follow bool
	}{
		{"bystander leaves", 28, opcode.SMSGDestroyObject, true, true, true},
		{"owner leaves", 3, opcode.SMSGDestroyObject, true, true, false},
		{"self disappears", 99, opcode.SMSGDestroyObject, false, false, false},
		{"normal sit", 1, opcode.SMSGStandstateUpdate, true, false, false},
		{"genuine death stance", 7, opcode.SMSGStandstateUpdate, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newBodyContinuitySession(t)
			c.observe(opcode.SMSGLoginVerifyWorld, positionBody(349, 1, 2, 3, .5))
			ctx := modsession.WithBodyInput(context.Background())
			if _, _, err := c.e.writeContext(ctx, opcode.MSGMoveStop, scopedBodyPacket(t, opcode.MSGMoveStop), c.e.transport.snapshot()); err != nil {
				t.Fatal(err)
			}
			packet := []byte{tc.subject}
			if tc.op == opcode.SMSGDestroyObject {
				packet = append(binary.LittleEndian.AppendUint64(nil, uint64(tc.subject)), 0)
			}
			c.observe(tc.op, packet)
			for _, view := range []struct {
				name string
				ctx  context.Context
				want bool
			}{
				{"new settled source", modsession.WithSettledBodySource(ctx), tc.settled},
				{"ongoing direct", (modsession.WithSelfBodyInput(ctx)), tc.own},
				{"continuing follow", ctx, tc.follow},
			} {
				r, why := c.e.s.bodyInputReport(view.ctx, "")
				if (r != nil) != view.want {
					t.Fatal(view.name, why, r)
				}
			}
		})
	}
}

func TestCopiedMeasuredInputRespectsPhysicalCapabilitiesAtSelection(t *testing.T) {
	root := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if root == "" {
		t.Skip("set BUDDY_NAV_POOL_TEST_DATA_DIR to copied client assets")
	}
	for _, tc := range []struct {
		name              string
		field             int
		value             uint32
		crossing, allowed bool
	}{
		{"non-swimmer at ledge", 0x3b, 8 | 0x4000, true, false}, {"cannot turn toward pool", 0x3c, 0x8000, true, false},
		{"non-swimmer walks on supported ground", 0x3b, 8 | 0x4000, false, true}, {"no-turn straight ahead", 0x3c, 0x8000, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, conn := newBodyContinuitySession(t)
			a := actions.NewService(c, 99, "physical-capability")
			a.SetNavigationDataDir(root)
			ctx := context.Background()
			x, y, z, o := float32(475.6561), float32(50.766533), float32(-96.19031), float32(.18)
			if !tc.crossing {
				x, y, z = 480.098, 48.420002, -96.25103
			}
			for _, p := range []struct {
				op   uint16
				wire []byte
			}{
				{opcode.SMSGLoginVerifyWorld, positionBody(349, x, y, z, o)}, {opcode.SMSGUpdateObject, bodyPlayerCreate(99, x, y, z, o)},
				{opcode.SMSGUpdateObject, bodyPlayerCreate(3, 390.99612, 27.22958, -130.92584, .5)},
				{opcode.SMSGCompressedUpdateObject, compressedBodyUpdates(t, bodyValuesBlock(99, tc.field, tc.value))},
			} {
				_, p0 := c.observe(p.op, p.wire)
				if _, err := a.OnPacket(ctx, p0); err != nil {
					t.Fatal(err)
				}
			}
			mode := "ground_or_water"
			if tc.crossing {
				mode = "follow_water"
				if _, err := a.Follow(ctx, &actionsv1.ToggleRequest{Enabled: true}); err != nil {
					t.Fatal(err)
				}
			}
			md, _ := metadata.FromOutgoingContext(modcb.WithPolicySpell(ctx, strings.Repeat("f", 64)))
			out, err := a.MoveBody(metadata.NewIncomingContext(ctx, md), &actionsv1.BodyMovementRequest{SessionId: "physical-capability", SelfGuid: 99, VisitId: 1, SequenceId: 37, DurationMs: 120, Landing: mode})
			if err != nil || (conn.writes > 0) != tc.allowed || (out.GetBodyMovement().GetPhase() == "body_input_written") != tc.allowed {
				t.Fatal(err, conn.opcodes, out)
			}
		})
	}
}
