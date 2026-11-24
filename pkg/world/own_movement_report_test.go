package world

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func ownReportPacket(t *testing.T, flags uint32) []byte {
	t.Helper()
	info, err := movementinfo.Encode(movementinfo.Info{Flags: flags, Time: 0, X: 1, Y: 2, Z: 3, Orientation: .5})
	if err != nil {
		t.Fatal(err)
	}
	// Redundant packed-GUID zero is a valid original wire representation.
	return append([]byte{3, 99, 0}, info...)
}

func TestOwnMovementReportRetainsOnlyExactKnownWrittenNormalPacket(t *testing.T) {
	for _, op := range []uint32{opcode.MSGMoveStartForward, opcode.MSGMoveHeartbeat, opcode.MSGMoveStop} {
		conn := &scopeWriteConn{}
		e := scopeEngine(t, conn)
		e.publishTransportScope()
		if e.s.OwnMovementReport() != nil {
			t.Fatal("incoming world/header facts invented an own write report")
		}
		body := ownReportPacket(t, movementflags.PassiveModifiers)
		original := bytes.Clone(body)
		before := e.transport.snapshot()
		if _, _, err := e.writeContext(context.Background(), op, body); err != nil {
			t.Fatal(err)
		}
		r := e.s.OwnMovementReport()
		if r == nil || r.Opcode != op || !bytes.Equal(r.Payload, original) || !proto.Equal(r.TransportScope, e.transport.scope) ||
			r.TransportScope.MotionRevision != before.MotionRevision+1 || conn.writes != 1 {
			t.Fatal("report lost the successful packet or its original postwrite scope", r)
		}
		body[0], r.Payload[0], r.TransportScope.ConnectionId = 0, 0, "caller mutation"
		again := e.s.OwnMovementReport()
		if again == nil || !bytes.Equal(again.Payload, original) || !proto.Equal(again.TransportScope, e.transport.scope) {
			t.Fatal("report exposed mutable original bytes or scope", again)
		}
	}
}

func TestOwnMovementReportUnknownMalformedOrForeignWriteNeverSuppliesPose(t *testing.T) {
	for _, kind := range []string{"unknown write", "foreign", "nonfinite", "truncated", "trailing", "transport", "unsupported opcode"} {
		t.Run(kind, func(t *testing.T) {
			conn := &scopeWriteConn{}
			e := scopeEngine(t, conn)
			body := ownReportPacket(t, 0)
			if _, _, err := e.writeContext(context.Background(), opcode.MSGMoveStop, body); err != nil || e.s.OwnMovementReport() == nil {
				t.Fatal("fixture lacks prior known report", err)
			}
			op := uint32(opcode.MSGMoveStop)
			switch kind {
			case "unknown write":
				conn.fail = true
			case "foreign":
				body[1] = 100
			case "nonfinite":
				binary.LittleEndian.PutUint32(body[13:], math.Float32bits(float32(math.NaN())))
			case "truncated":
				body = body[:len(body)-1]
			case "trailing":
				body = append(body, 0)
			case "transport":
				binary.LittleEndian.PutUint32(body[3:], 0x200)
			case "unsupported opcode":
				op = opcode.MSGMoveJump
			}
			_, _, err := e.writeContext(context.Background(), op, body)
			if (err != nil) != (kind == "unknown write") || conn.writes != 2 || e.s.OwnMovementReport() != nil {
				t.Fatal("failed/foreign/unsupported write left a current own pose report", kind, err, e.s.OwnMovementReport())
			}
		})
	}
}

func TestOwnMovementReportScopeChangesNeverRelabelOldWire(t *testing.T) {
	for _, kind := range []string{"update", "malformed movement", "control", "same map entry", "transfer", "actual body", "logging out", "closed"} {
		t.Run(kind, func(t *testing.T) {
			e := scopeEngine(t, &scopeWriteConn{})
			if _, _, err := e.writeContext(context.Background(), opcode.MSGMoveStop, ownReportPacket(t, 0)); err != nil {
				t.Fatal(err)
			}
			original := proto.Clone(e.s.OwnMovementReport()).(*modv1.WorldPacket)
			switch kind {
			case "update":
				e.observeTransportScope(opcode.SMSGUpdateObject, nil)
			case "malformed movement":
				e.observeTransportScope(opcode.MSGMoveHeartbeat, []byte{1})
			case "control":
				e.observeTransportScope(opcode.SMSGClientControlUpdate, []byte{1, 99, 0})
			case "same map entry":
				e.observeTransportScope(opcode.SMSGNewWorld, positionBody(0, 1, 2, 3, .5))
			case "transfer":
				e.observeTransportScope(opcode.SMSGTransferPending, binary.LittleEndian.AppendUint32(nil, 43))
			case "actual body":
				e.s.state.Character.GUID++
			case "logging out":
				e.s.state.Status = LoggingOut
			case "closed":
				e.s.state.Status = Closed
			}
			if e.s.OwnMovementReport() != nil || !proto.Equal(original, e.ownMovementReport) {
				t.Fatal("new context relabeled or exposed the old report", kind, e.s.OwnMovementReport())
			}
			// Unrelated successful traffic is not a new movement observation.
			if _, _, err := e.writeContext(context.Background(), opcode.CMSGMessagechat, []byte{1, 2, 3}); err != nil || e.s.OwnMovementReport() != nil {
				t.Fatal("unrelated traffic restored stale pose authority", err)
			}
		})
	}
}

func TestOwnMovementReportModuleHookUsesSameDetachedCurrentSnapshot(t *testing.T) {
	runtime := newObserved()
	body := ownReportPacket(t, 0)
	conn := peerConnection(t, func(p *testPeer) {
		p.login()
		if got := p.expect(opcode.MSGMoveStop); !bytes.Equal(got, body) {
			t.Error("normal movement bytes changed before transport", got)
		}
		p.closed()
	})
	s, err := enter(context.Background(), conn, Realm{}, Character{99, "Mira"}, nil, quietTimings(), Options{Modules: runtime})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if runtime.session.OwnMovementReport == nil || runtime.session.OwnMovementReport() != nil {
		t.Fatal("module hook missing or incoming login pose invented a written report")
	}
	if err := s.SendPacket(context.Background(), opcode.MSGMoveStop, body); err != nil {
		t.Fatal(err)
	}
	report := runtime.session.OwnMovementReport()
	if report == nil || !proto.Equal(report, s.OwnMovementReport()) || !bytes.Equal(report.Payload, body) || !proto.Equal(report.TransportScope, s.TransportScope()) {
		t.Fatal("module hook did not expose the exact current written report", report)
	}
	report.Payload[0] = 0
	if !bytes.Equal(runtime.session.OwnMovementReport().Payload, body) {
		t.Fatal("module caller could mutate the retained report")
	}
}
