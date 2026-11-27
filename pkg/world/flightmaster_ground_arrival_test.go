package world

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const flightmasterTestGUID = uint64(0xf130000625000045)

func TestFlightmasterQueryScopeRequiresTheSameKnownPhysicalGroundStop(t *testing.T) {
	for _, change := range []string{"status", "menu", "paid hop", "missing World confirmation", "new STOP at same coordinate",
		"new START", "new uncertain write", "own correction", "same map new visit", "active impulse",
		"not actually stationary", "activation packet", "missing GUID"} {
		t.Run(change, func(t *testing.T) {
			c := &scopeWriteConn{}
			e := scopedBodyEngine(t, c)
			body := ownReportPacket(t, 0)
			if change == "not actually stationary" {
				body = ownReportPacket(t, 1)
			}
			if _, _, err := e.writeContext(context.Background(), opcode.MSGMoveStop, body); err != nil {
				t.Fatal(err)
			}
			scope := e.transport.snapshot()
			ctx := modsession.WithFlightpathWorldCheck(context.Background(), func() bool { return true })
			payload := binary.LittleEndian.AppendUint64(nil, flightmasterTestGUID)
			code := uint32(opcode.CMSGTaxinodeStatusQuery)
			switch change {
			case "menu":
				code = opcode.CMSGTaxiqueryavailablenodes
			case "paid hop":
				code = opcode.CMSGActivatetaxi
				payload = binary.LittleEndian.AppendUint32(payload, 6)
				payload = binary.LittleEndian.AppendUint32(payload, 8)
			case "missing World confirmation":
				ctx = context.Background()
			case "new STOP at same coordinate":
				e.write(opcode.MSGMoveStop, body)
			case "new START":
				e.write(opcode.MSGMoveStartForward, ownReportPacket(t, 1))
			case "new uncertain write":
				c.fail = true
				if _, _, err := e.writeContext(context.Background(), opcode.MSGMoveStartForward, ownReportPacket(t, 1)); err == nil {
					t.Fatal("fixture did not fail")
				}
				c.fail = false
			case "own correction":
				e.observeTransportScope(opcode.SMSGMonsterMove, scopeSpline(99, 3, false, 12))
			case "same map new visit":
				e.observeTransportScope(opcode.SMSGNewWorld, positionBody(0, 1, 2, 3, .5))
			case "active impulse":
				e.impulse = &knockbackMotion{}
			case "activation packet":
				code = opcode.CMSGActivatetaxi
			case "missing GUID":
				payload = make([]byte, 8)
			}
			before := c.writes
			_, _, err := e.writeContext(ctx, code, payload, scope)
			if change == "status" || change == "menu" || change == "paid hop" {
				if err != nil || c.writes != before+1 {
					t.Fatal(err, c.writes)
				}
			} else if !modcb.PacketWasNotDispatched(err) || c.writes != before {
				t.Fatal("stale proof reached game transport", change, err, c.writes)
			}
		})
	}
}

func TestFlightmasterSocketDrainsAlreadyReceivedTargetMovementOrDespawn(t *testing.T) {
	for _, pair := range []struct{ incoming, outgoing uint16 }{
		{opcode.SMSGMonsterMove, opcode.CMSGTaxinodeStatusQuery}, {opcode.SMSGDestroyObject, opcode.CMSGTaxinodeStatusQuery},
		{opcode.SMSGMonsterMove, opcode.CMSGActivatetaxi}, {opcode.SMSGDestroyObject, opcode.CMSGActivatetaxi},
	} {
		code := pair.incoming
		t.Run(opcode.WorldName(uint32(code))+"/"+opcode.WorldName(uint32(pair.outgoing)), func(t *testing.T) {
			c := &scopeWriteConn{}
			e := scopedBodyEngine(t, c)
			rt := &scopeObservedRuntime{observedRuntime: newObserved(), facts: make(chan *modv1.WorldPacket, 128)}
			e.s.modules, e.s.ctx, e.s.started = rt, context.Background(), time.Now()
			e.s.abort, e.s.startLogin, e.s.writes = make(chan struct{}), make(chan struct{}), make(chan writeRequest)
			if _, _, err := e.writeContext(context.Background(), opcode.MSGMoveStop, ownReportPacket(t, 0)); err != nil {
				t.Fatal(err)
			}
			stop := e.transport.snapshot()
			ready := make(chan packet, 1)
			payload := scopeSpline(flightmasterTestGUID, 1, false, 30)
			if code == opcode.SMSGDestroyObject {
				payload = append(binary.LittleEndian.AppendUint64(nil, flightmasterTestGUID), 0)
			}
			armed := true
			c.deadline = func() {
				if armed {
					armed = false
					ready <- packet{opcode: code, body: payload}
				}
			}
			done := make(chan error, 1)
			go func() { done <- e.loop(ready) }()
			t.Cleanup(func() {
				close(e.s.abort)
				<-done
				if e.pongTimer != nil {
					e.pongTimer.Stop()
				}
				rt.Stop()
				rt.Close()
			})
			ctx := modsession.WithFlightpathWorldCheck(context.Background(), func() bool { return len(rt.facts) == 0 })
			body := binary.LittleEndian.AppendUint64(nil, flightmasterTestGUID)
			if pair.outgoing == opcode.CMSGActivatetaxi {
				body = binary.LittleEndian.AppendUint32(body, 6)
				body = binary.LittleEndian.AppendUint32(body, 8)
			}
			req := writeRequest{ctx: ctx, op: uint32(pair.outgoing), body: body, transportScope: stop, reply: make(chan error, 1)}
			e.s.writes <- req
			select {
			case err := <-req.reply:
				if !modcb.PacketWasNotDispatched(err) || c.writes != 1 {
					t.Fatal("old NPC snapshot survived final physical drain", err, c.writes)
				}
			case <-time.After(time.Second):
				t.Fatal("socket did not finish the query")
			}
			select {
			case p := <-rt.facts:
				if p.Opcode != uint32(code) || p.TransportScope.MotionRevision != stop.MotionRevision {
					t.Fatal("fixture did not isolate NPC-only change", p, stop)
				}
			default:
				t.Fatal("received NPC change never reached the ordered queue")
			}
		})
	}
}
