package world

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func scopedBodyPacket(t *testing.T, op uint32) []byte {
	t.Helper()
	i := movementinfo.Info{Flags: movementflags.PassiveModifiers, Time: 123, X: 1, Y: 2, Z: 3, Orientation: .5}
	switch op {
	case opcode.MSGMoveStartForward, opcode.MSGMoveFallLand:
		i.Flags |= 1
	case opcode.MSGMoveHeartbeat:
		i.Flags |= 0x1000
		i.JumpPresent, i.FallTime, i.Jump = true, 300, movementinfo.Jump{CosAngle: 1}
	case opcode.MSGMoveJump:
		i.Flags |= 0x1001
		i.JumpPresent, i.Jump = true, movementinfo.Jump{Velocity: 7.955547, CosAngle: 1, XYSpeed: 7}
	}
	b, err := movementinfo.Encode(i)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte{3, 99, 0}, b...)
}

func scopedBodyEngine(t *testing.T, conn *scopeWriteConn) *engine {
	t.Helper()
	e := scopeEngine(t, conn)
	// The common fixture has an active server spline for completion tests.
	// A real new entry resets it; normal input begins in this bound ground visit.
	e.observeTransportScope(opcode.SMSGNewWorld, positionBody(0, 1, 2, 3, .5))
	return e
}

func TestScopedBodyMovementKnownWritesRetainExactFullReportsAndConsumeScope(t *testing.T) {
	for _, op := range []uint32{opcode.MSGMoveSetFacing, opcode.MSGMoveStartForward, opcode.MSGMoveHeartbeat, opcode.MSGMoveJump, opcode.MSGMoveFallLand} {
		t.Run(opcode.WorldName(op), func(t *testing.T) {
			conn := &scopeWriteConn{}
			e := scopedBodyEngine(t, conn)
			requested, body := e.transport.snapshot(), scopedBodyPacket(t, op)
			original := bytes.Clone(body)
			if _, _, err := e.writeContext(context.Background(), op, body, requested); err != nil || conn.writes != 1 {
				t.Fatal("current normal body image was refused", err, conn.writes)
			}
			report := e.s.OwnMovementReport()
			if report == nil || report.Opcode != op || !bytes.Equal(report.Payload, original) || !proto.Equal(report.TransportScope, e.transport.scope) ||
				report.TransportScope.MotionRevision != requested.MotionRevision+1 {
				t.Fatal("known body write lost its original wire or postwrite scope", report)
			}
			if _, _, err := e.writeContext(context.Background(), op, body, requested); !modcb.PacketWasNotDispatched(err) || conn.writes != 1 {
				t.Fatal("body write replayed its stale source scope", err, conn.writes)
			}
			// The retained historical bytes themselves are never relabeled.
			if !bytes.Equal(report.Payload, original) || report.TransportScope.MotionRevision != requested.MotionRevision+1 {
				t.Fatal("refusal mutated the detached known report", report)
			}
		})
	}
}

func TestScopedBodyMovementRejectsUnsupportedImagesAndBindings(t *testing.T) {
	for _, mode := range []string{"connection", "self", "visit", "map", "motion", "unbound", "active_spline", "mover", "truncated", "trailing", "nonfinite", "swim", "transport", "extra", "bad_jump", "bad_direction", "fall_time", "ground_jump", "falling_land", "falling_facing", "falling_start"} {
		t.Run(mode, func(t *testing.T) {
			e := scopedBodyEngine(t, &scopeWriteConn{})
			scope, op := e.transport.snapshot(), uint32(opcode.MSGMoveHeartbeat)
			body := scopedBodyPacket(t, op)
			switch mode {
			case "connection":
				scope.ConnectionId += "old"
			case "self":
				scope.SelfGuid++
			case "visit":
				scope.VisitRevision++
			case "map":
				scope.MapId++
			case "motion":
				scope.MotionRevision++
			case "unbound":
				scope.VisitBound = false
			case "active_spline":
				e.observeTransportScope(opcode.SMSGMonsterMove, scopeSpline(99, 4, false, 4))
				scope = e.transport.snapshot()
			case "mover":
				body[1]++
			case "truncated":
				body = body[:len(body)-1]
			case "trailing":
				body = append(body, 0)
			case "nonfinite":
				binary.LittleEndian.PutUint32(body[13:], math.Float32bits(float32(math.NaN())))
			case "swim":
				binary.LittleEndian.PutUint32(body[3:], 0x200000)
			case "transport":
				binary.LittleEndian.PutUint32(body[3:], 0x200)
			case "extra":
				binary.LittleEndian.PutUint16(body[7:], 1)
			case "bad_jump":
				binary.LittleEndian.PutUint32(body[33:], math.Float32bits(3))
			case "bad_direction":
				binary.LittleEndian.PutUint32(body[41:], math.Float32bits(2))
			case "fall_time":
				binary.LittleEndian.PutUint32(body[29:], 1101)
			case "ground_jump":
				op, body = opcode.MSGMoveJump, scopedBodyPacket(t, opcode.MSGMoveSetFacing)
			case "falling_land":
				op = opcode.MSGMoveFallLand
			case "falling_facing":
				op = opcode.MSGMoveSetFacing
			case "falling_start":
				op = opcode.MSGMoveStartForward
			}
			if err := e.transport.reserve(op, body, scope); !modcb.PacketWasNotDispatched(err) {
				t.Fatal("unsupported body image or stale binding reached admission", mode, err)
			}
		})
	}
	for _, mode := range []string{"unknown", "foreign"} {
		t.Run(mode+"_report", func(t *testing.T) {
			conn := &scopeWriteConn{}
			e := scopedBodyEngine(t, conn)
			body := scopedBodyPacket(t, opcode.MSGMoveJump)
			if mode == "unknown" {
				conn.fail = true
			} else {
				body[1]++
			}
			_, _, err := e.writeContext(context.Background(), opcode.MSGMoveJump, body, e.transport.snapshot())
			if err == nil || e.s.OwnMovementReport() != nil || conn.writes != map[string]int{"unknown": 1, "foreign": 0}[mode] {
				t.Fatal("failed or foreign write invented a known full report", err, conn.writes, e.s.OwnMovementReport())
			}
		})
	}
}

func TestScopedBodyMovementOwnerLoopRefusesEveryQueuedOldSameMapVisit(t *testing.T) {
	for _, op := range []uint32{opcode.MSGMoveSetFacing, opcode.MSGMoveStartForward, opcode.MSGMoveHeartbeat, opcode.MSGMoveJump, opcode.MSGMoveFallLand} {
		t.Run(opcode.WorldName(op), func(t *testing.T) {
			conn := &scopeWriteConn{}
			e := scopedBodyEngine(t, conn)
			runtime := &scopeObservedRuntime{observedRuntime: newObserved(), facts: make(chan *modv1.WorldPacket, 128)}
			e.s.modules, e.s.ctx, e.s.started = runtime, context.Background(), time.Now()
			e.s.abort, e.s.startLogin, e.s.writes = make(chan struct{}), make(chan struct{}), make(chan writeRequest)
			old := e.transport.snapshot()
			incoming := make(chan packet, 1)
			armed := true
			conn.deadline = func() {
				if armed {
					armed = false
					incoming <- packet{opcode: opcode.SMSGNewWorld, body: positionBody(old.MapId, 1, 2, 3, .5)}
				}
			}
			finished := make(chan error, 1)
			go func() { finished <- e.loop(incoming) }()
			t.Cleanup(func() {
				close(e.s.abort)
				<-finished
				if e.pongTimer != nil {
					e.pongTimer.Stop()
				}
				runtime.Stop()
				runtime.Close()
			})
			request := writeRequest{ctx: context.Background(), op: op, body: scopedBodyPacket(t, op), transportScope: old, reply: make(chan error, 1)}
			e.s.writes <- request
			select {
			case err := <-request.reply:
				if !modcb.PacketWasNotDispatched(err) {
					t.Fatal("queued body input crossed a same-map visit boundary", op, err)
				}
			case <-time.After(time.Second):
				t.Fatal("owner loop stalled before scoped body refusal")
			}
			select {
			case observed := <-runtime.facts:
				if observed.Opcode != opcode.SMSGNewWorld || observed.TransportScope.GetVisitRevision() == old.VisitRevision || observed.TransportScope.MapId != old.MapId {
					t.Fatal("actual same-map boundary was not processed", observed)
				}
			default:
				t.Fatal("fixture did not process ready NEW_WORLD before admission")
			}
			if conn.writes != 1 || len(conn.opcodes) != 1 || conn.opcodes[0] != opcode.MSGMoveWorldportAck || e.s.OwnMovementReport() != nil {
				t.Fatal("old body input changed replacement visit; only required ACK is allowed", op, conn.opcodes)
			}
		})
	}
}
