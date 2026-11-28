package world

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestNaturalTaxiMenuScopeAtTheActualWriter(t *testing.T) {
	for _, test := range []string{"menu", "world behind", "changed source", "paid packet", "uncertain body write", "uncertain menu", "ground STOP not supplied"} {
		t.Run(test, func(t *testing.T) {
			conn := &scopeWriteConn{}
			e := scopeEngine(t, conn)
			e.transport.observe(opcode.SMSGUpdateObject, []byte{0, 0, 0, 0})
			scope := e.transport.snapshot()
			scope.TaxiArrivalQueryRevision = 7
			ctx := modsession.WithFlightpathWorldCheck(context.Background(), func() bool { return test != "world behind" })
			op := uint32(opcode.CMSGTaxiqueryavailablenodes)
			body := binary.LittleEndian.AppendUint64(nil, flightmasterTestGUID)
			switch test {
			case "changed source":
				conn.deadline = func() { e.transport.observe(opcode.SMSGMonsterMove, scopeSpline(99, 44, false, 31)) }
			case "paid packet":
				op = opcode.CMSGActivatetaxi
				body = binary.LittleEndian.AppendUint32(body, 6)
				body = binary.LittleEndian.AppendUint32(body, 8)
			case "uncertain body write":
				conn.fail = true
				if _, _, err := e.writeContext(context.Background(), opcode.MSGMoveHeartbeat, ownReportPacket(t, 1)); err == nil {
					t.Fatal("expected partial competing input")
				}
				conn.fail = false
			case "uncertain menu":
				conn.fail = true
			case "ground STOP not supplied":
				scope.TaxiArrivalQueryRevision = 0
			}
			_, _, err := e.writeContext(ctx, op, body, scope)
			if test == "menu" {
				if err != nil || conn.writes != 1 {
					t.Fatal(err, conn.writes)
				}
				m := &e.transport.taxiMenu
				reply := make([]byte, 72)
				binary.LittleEndian.PutUint32(reply, 1)
				binary.LittleEndian.PutUint64(reply[4:], flightmasterTestGUID)
				binary.LittleEndian.PutUint32(reply[12:], 8)
				before := e.transport.snapshot()
				m.markReply(opcode.SMSGShowtaxinodes, reply, before, m.writtenAt.Add(-time.Millisecond))
				if before.TaxiArrivalQueryRevision != 0 {
					t.Fatal("read before request authorized its menu")
				}
				after := e.transport.snapshot()
				m.markReply(opcode.SMSGShowtaxinodes, reply, after, m.writtenAt.Add(time.Millisecond))
				if after.TaxiArrivalQueryRevision != 7 || m.requested != nil {
					t.Fatal("current free menu receipt missing", after)
				}
			} else if test == "uncertain menu" {
				if err == nil || modcb.PacketWasNotDispatched(err) || e.transport.taxiMenu.requested != nil {
					t.Fatal("uncertain query acquired a response witness", err)
				}
			} else if !modcb.PacketWasNotDispatched(err) || e.transport.taxiMenu.requested != nil {
				t.Fatal("unqualified packet reached wire", test, err)
			}
		})
	}
}
