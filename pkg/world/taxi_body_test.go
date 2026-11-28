package world

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestPaidTaxiFencesFollowAndLogoutDeparturePoseAtSocket(t *testing.T) {
	for _, tc := range []string{"pending", "accepted", "invalid reply", "typed denial", "old origin", "changed after approval", "current origin"} {
		t.Run(tc, func(t *testing.T) {
			c := &scopeWriteConn{}
			e := scopedBodyEngine(t, c)
			ctx := context.Background()
			depart := ownReportPacket(t, 0)
			if err := e.write(opcode.MSGMoveStop, depart); err != nil {
				t.Fatal(err)
			}
			fare := binary.LittleEndian.AppendUint64(nil, flightmasterTestGUID)
			fare = binary.LittleEndian.AppendUint32(fare, 6)
			fare = binary.LittleEndian.AppendUint32(fare, 8)
			if err := e.write(opcode.CMSGActivatetaxi, fare); err != nil {
				t.Fatal(err)
			}
			if tc == "typed denial" {
				e.observeTransportScope(opcode.SMSGActivatetaxireply, []byte{5, 0, 0, 0})
			} else if tc == "invalid reply" {
				e.observeTransportScope(opcode.SMSGActivatetaxireply, []byte{0})
			} else if tc != "pending" {
				e.observeTransportScope(opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0})
			}
			e.observeTransportScope(opcode.SMSGMonsterMove, scopeSpline(e.transport.scope.SelfGuid, 41, false, 30))
			exp := e.transport.snapshot()
			info, _ := movementinfo.OwnFrame(depart, exp.SelfGuid)
			info.X, info.Y, info.Z = 101, 202, 320
			arrive, err := movementinfo.MoverFrame(exp.SelfGuid, info)
			if err != nil {
				t.Fatal(err)
			}
			if tc == "old origin" || tc == "current origin" || tc == "changed after approval" {
				coords := binary.LittleEndian.AppendUint64(nil, exp.MotionRevision)
				for _, v := range []float32{info.X, info.Y, info.Z} {
					coords = binary.LittleEndian.AppendUint32(coords, math.Float32bits(v))
				}
				e.s.recordTaxiBodyOrigin(exp, coords)
			}
			if tc == "changed after approval" {
				e.observeTransportScope(opcode.SMSGUpdateObject, nil)
			}
			before := c.writes
			for _, op := range []uint32{opcode.MSGMoveStartForward, opcode.MSGMoveHeartbeat, opcode.MSGMoveStop} {
				body := depart
				if tc == "current origin" || tc == "changed after approval" {
					body = arrive
				}
				_, _, err := e.writeContext(ctx, op, body)
				if tc == "current origin" || tc == "typed denial" {
					if err != nil {
						t.Fatalf("current input was refused: %v", err)
					}
				} else if !modcb.PacketWasNotDispatched(err) || c.writes != before {
					t.Fatalf("stale own movement was written: %x %v", op, err)
				}
			}
		})
	}
}
