package world

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestAreaTriggerScopeRequiresExactCurrentBodyVisitAndWireBoundary(t *testing.T) {
	for _, change := range []string{"connection", "body", "visit", "map", "motion", "unbound", "transfer", "empty", "short", "long", "zero"} {
		t.Run(change, func(t *testing.T) {
			r := scopeRig(t, 0)
			requested := r.snapshot()
			body := binary.LittleEndian.AppendUint32(nil, 228)
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
			case "transfer":
				r.observe(opcode.SMSGTransferPending, binary.LittleEndian.AppendUint32(nil, 43))
			case "empty":
				body = nil
			case "short":
				body = body[:3]
			case "long":
				body = append(body, 0)
			case "zero":
				body = make([]byte, 4)
			}
			if err := r.reserve(opcode.CMSGAreatrigger, body, requested); !modcb.PacketWasNotDispatched(err) || len(r.areaTriggerAttempts) != 0 {
				t.Fatal("stale or malformed trigger reached reservation", change, err)
			}
		})
	}
	r := scopeRig(t, 0)
	body := binary.LittleEndian.AppendUint32(nil, math.MaxUint32)
	before := r.snapshot()
	if err := r.reserve(opcode.CMSGAreatrigger, body, before); err != nil || len(r.areaTriggerAttempts) != 1 || !proto.Equal(before, r.scope) {
		t.Fatal("transport guard changed raw scope or interpreted the opaque trigger ID", err)
	}
	if err := r.reserve(opcode.CMSGAreatrigger, body, before); !modcb.PacketWasNotDispatched(err) || len(r.areaTriggerAttempts) != 1 {
		t.Fatal("same exact trigger request could be replayed", err)
	}
}

func TestAreaTriggerScopeFinalOwnerLoopCallbackRefusesWithoutTransport(t *testing.T) {
	for _, change := range []string{"body", "actual body", "pending", "same map visit", "movement", "cancel", "logout"} {
		t.Run(change, func(t *testing.T) {
			conn := &scopeWriteConn{}
			e := scopeEngine(t, conn)
			requested := e.transport.snapshot()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conn.deadline = func() {
				switch change {
				case "body":
					e.transport.scope.SelfGuid++
				case "actual body":
					e.s.state.Character.GUID++
				case "pending":
					e.transport.observe(opcode.SMSGTransferPending, binary.LittleEndian.AppendUint32(nil, 43))
				case "same map visit":
					e.transport.observe(opcode.SMSGNewWorld, positionBody(0, 1, 2, 3, .5))
				case "movement":
					e.transport.sent(opcode.MSGMoveStop)
				case "cancel":
					cancel()
				case "logout":
					e.phase = LoggingOut
				}
			}
			_, anyBytes, err := e.writeContext(ctx, opcode.CMSGAreatrigger, binary.LittleEndian.AppendUint32(nil, 228), requested)
			if !modcb.PacketWasNotDispatched(err) || anyBytes || conn.writes != 0 || len(e.transport.areaTriggerAttempts) != 0 {
				t.Fatal("late owner-loop change bypassed trigger fencing", change, err, conn.writes)
			}
		})
	}
}

func TestAreaTriggerScopeUnknownWriteCannotReplayExactSource(t *testing.T) {
	conn := &scopeWriteConn{fail: true}
	e := scopeEngine(t, conn)
	requested := e.transport.snapshot()
	body := binary.LittleEndian.AppendUint32(nil, 228)
	_, anyBytes, err := e.writeContext(context.Background(), opcode.CMSGAreatrigger, body, requested)
	if err == nil || !anyBytes || modcb.PacketWasNotDispatched(err) || conn.writes != 1 || len(e.transport.areaTriggerAttempts) != 1 {
		t.Fatal("ambiguous trigger write lost its pre-transport reservation", anyBytes, err, conn.writes)
	}
	conn.fail = false
	if _, _, err := e.writeContext(context.Background(), opcode.CMSGAreatrigger, body, requested); !modcb.PacketWasNotDispatched(err) || conn.writes != 1 {
		t.Fatal("ambiguous same-source trigger was replayed", err, conn.writes)
	}
}

func TestAreaTriggerScopeNormalWireTransferAckAndOldVisitRefusal(t *testing.T) {
	entered := make(chan struct{})
	stop := ownReportPacket(t, 0)
	conn := peerConnection(t, func(p *testPeer) {
		p.login()
		if body := p.expect(opcode.MSGMoveStop); !bytes.Equal(body, stop) {
			t.Error("ordinary pre-entry movement report changed", body)
		}
		if body := p.expect(opcode.CMSGAreatrigger); !bytes.Equal(body, binary.LittleEndian.AppendUint32(nil, 228)) {
			t.Error("ordinary area-trigger packet invented a GUID, pose or destination", body)
		}
		p.send(opcode.SMSGTransferPending, binary.LittleEndian.AppendUint32(nil, 43))
		p.send(opcode.SMSGNewWorld, positionBody(43, 1, 2, 3, .5))
		if body := p.expect(opcode.MSGMoveWorldportAck); len(body) != 0 {
			t.Error("normal server-directed entry ACK changed", body)
		}
		p.sync(77)
		close(entered)
		if body := p.expect(opcode.CMSGMessagechat); !bytes.Equal(body, []byte{1, 2, 3}) {
			t.Error("old-visit refusal broke the current session", body)
		}
		p.closed()
	})
	s, err := enter(context.Background(), conn, Realm{}, Character{99, "Mira"}, nil, quietTimings())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SendPacket(context.Background(), opcode.MSGMoveStop, stop); err != nil {
		t.Fatal(err)
	}
	report := s.OwnMovementReport()
	if report == nil || !bytes.Equal(report.Payload, stop) {
		t.Fatal("actual successful STOP did not supply its own current report", report)
	}
	requested := report.TransportScope
	if err := s.SendScopedPacket(context.Background(), opcode.CMSGAreatrigger, binary.LittleEndian.AppendUint32(nil, 228), requested); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("synthetic server did not finish the actual normal map handoff")
	}
	if got := s.TransportScope(); got.VisitRevision == requested.VisitRevision || got.MapId != 43 || !got.VisitBound {
		t.Fatal("actual new-world packet did not replace the source scope", got)
	}
	if s.OwnMovementReport() != nil {
		t.Fatal("new world entry relabeled the old client STOP as a current pose")
	}
	if err := s.SendScopedPacket(context.Background(), opcode.CMSGAreatrigger, binary.LittleEndian.AppendUint32(nil, 228), requested); !modcb.PacketWasNotDispatched(err) {
		t.Fatal("old-visit request survived actual map handoff", err)
	}
	if err := s.SendPacket(context.Background(), opcode.CMSGMessagechat, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
}
