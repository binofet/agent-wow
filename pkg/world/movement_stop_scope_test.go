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
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestScopedGroundStopRequiresExactScopeOwnFiniteStationaryImage(t *testing.T) {
	for _, change := range []string{"connection", "body", "visit", "map", "motion", "unbound", "mover", "truncated", "trailing", "nonfinite", "forward", "turn", "swim", "extra"} {
		t.Run(change, func(t *testing.T) {
			e := scopeEngine(t, &scopeWriteConn{})
			requested := e.transport.snapshot()
			body := ownReportPacket(t, 0)
			switch change {
			case "connection":
				requested.ConnectionId += "old"
			case "body":
				requested.SelfGuid++
			case "visit":
				requested.VisitRevision++
			case "map":
				requested.MapId++
			case "motion":
				requested.MotionRevision++
			case "unbound":
				requested.VisitBound = false
			case "mover":
				body[1] = 100
			case "truncated":
				body = body[:len(body)-1]
			case "trailing":
				body = append(body, 0)
			case "nonfinite":
				binary.LittleEndian.PutUint32(body[13:], math.Float32bits(float32(math.NaN())))
			case "forward":
				binary.LittleEndian.PutUint32(body[3:], 1)
			case "turn":
				binary.LittleEndian.PutUint32(body[3:], 0x10)
			case "swim":
				binary.LittleEndian.PutUint32(body[3:], 0x200000)
			case "extra":
				binary.LittleEndian.PutUint16(body[7:], 1)
			}
			if err := e.transport.reserve(opcode.MSGMoveStop, body, requested); !modcb.PacketWasNotDispatched(err) {
				t.Fatal("unsupported or stale scoped STOP reached transport admission", change, err)
			}
		})
	}
}

func TestScopedGroundStopKnownWritePublishesExactReportAndConsumesScope(t *testing.T) {
	conn := &scopeWriteConn{}
	e := scopeEngine(t, conn)
	requested := e.transport.snapshot()
	body := ownReportPacket(t, movementflags.PassiveModifiers|0x100) // Walk mode is stationary without directional bits.
	if _, _, err := e.writeContext(context.Background(), opcode.MSGMoveStop, body, requested); err != nil || conn.writes != 1 {
		t.Fatal("current ordinary STOP was refused", err, conn.writes)
	}
	report := e.s.OwnMovementReport()
	if report == nil || report.Opcode != opcode.MSGMoveStop || !bytes.Equal(report.Payload, body) ||
		!proto.Equal(report.TransportScope, e.transport.scope) || report.TransportScope.MotionRevision != requested.MotionRevision+1 {
		t.Fatal("known STOP lost its exact postwrite report", report)
	}
	if _, _, err := e.writeContext(context.Background(), opcode.MSGMoveStop, body, requested); !modcb.PacketWasNotDispatched(err) || conn.writes != 1 {
		t.Fatal("successful STOP left its original scope reusable", err, conn.writes)
	}
	// The existing opaque unscoped STOP path is unchanged.
	if _, _, err := e.writeContext(context.Background(), opcode.MSGMoveStop, body); err != nil || conn.writes != 2 {
		t.Fatal("new scoped admission changed legacy STOP behavior", err, conn.writes)
	}
}

func TestScopedGroundStopOwnerLoopRefusesQueuedOldSameMapVisit(t *testing.T) {
	conn := &scopeWriteConn{}
	e := scopeEngine(t, conn)
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
	request := writeRequest{ctx: context.Background(), op: opcode.MSGMoveStop, body: ownReportPacket(t, 0), transportScope: old, reply: make(chan error, 1)}
	e.s.writes <- request
	select {
	case err := <-request.reply:
		if !modcb.PacketWasNotDispatched(err) {
			t.Fatal("old-visit STOP was admitted after ready same-map entry", err)
		}
	case <-time.After(time.Second):
		t.Fatal("owner loop blocked while fencing same-map entry")
	}
	select {
	case observed := <-runtime.facts:
		if observed.Opcode != opcode.SMSGNewWorld || observed.TransportScope.GetVisitRevision() == old.VisitRevision || observed.TransportScope.GetMapId() != old.MapId {
			t.Fatal("test did not process the actual same-map visit replacement", observed)
		}
	default:
		t.Fatal("ready entry did not pass through the owner loop before STOP refusal")
	}
	// The sole write is the required protocol ACK. No STOP or trigger is sent.
	if conn.writes != 1 || len(conn.opcodes) != 1 || conn.opcodes[0] != opcode.MSGMoveWorldportAck || e.s.OwnMovementReport() != nil {
		t.Fatal("stale STOP changed the replacement visit before its refusal", conn.opcodes)
	}
}
