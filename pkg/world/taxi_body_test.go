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

func TestPaidTaxiAmbiguousGroundStartRequiresNewActualOrigin(t *testing.T) {
	c := &scopeWriteConn{}
	e := scopedBodyEngine(t, c)
	fare := binary.LittleEndian.AppendUint64(nil, flightmasterTestGUID)
	fare = binary.LittleEndian.AppendUint32(fare, 6)
	fare = binary.LittleEndian.AppendUint32(fare, 8)
	if err := e.write(opcode.CMSGActivatetaxi, fare); err != nil {
		t.Fatal(err)
	}
	e.observeTransportScope(opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0})
	e.observeTransportScope(opcode.SMSGMonsterMove, scopeSpline(e.transport.scope.SelfGuid, 41, false, 30))
	body := ownReportPacket(t, 0)
	i, _ := movementinfo.OwnFrame(body, e.transport.scope.SelfGuid)
	makeOrigin := func(rev uint64) []byte {
		p := binary.LittleEndian.AppendUint64(nil, rev)
		for _, n := range []float32{i.X, i.Y, i.Z} {
			p = binary.LittleEndian.AppendUint32(p, math.Float32bits(n))
		}
		return p
	}
	rev := e.transport.scope.MotionRevision
	e.s.recordTaxiBodyOrigin(e.transport.snapshot(), makeOrigin(rev))
	c.fail = true
	if _, _, err := e.writeContext(context.Background(), opcode.MSGMoveStop, body); err == nil || modcb.PacketWasNotDispatched(err) {
		t.Fatal("fixture failed to enter transport")
	}
	c.fail = false
	e.observeTransportScope(opcode.SMSGUpdateObject, nil)
	e.s.recordTaxiBodyOrigin(e.transport.snapshot(), makeOrigin(rev))
	if err := e.write(opcode.MSGMoveStop, body); !modcb.PacketWasNotDispatched(err) {
		t.Fatal("old receipt replayed after bystander traffic", err)
	}
	e.observeTransportScope(opcode.MSGMoveHeartbeat, body)
	e.s.recordTaxiBodyOrigin(e.transport.snapshot(), makeOrigin(e.transport.scope.MotionRevision))
	if err := e.write(opcode.MSGMoveSetFacing, body); err != nil {
		t.Fatal("new own observation refused", err)
	}
}

func TestTaxiSocketUsesGroundProofOfLaterVisitWithoutCreditingFare(t *testing.T) {
	for _, mode := range []string{"accepted new visit", "unanswered new visit", "same visit", "missing current proof", "wrong new origin"} {
		t.Run(mode, func(t *testing.T) {
			c := &scopeWriteConn{}
			e := scopedBodyEngine(t, c)
			fare := binary.LittleEndian.AppendUint64(nil, flightmasterTestGUID)
			fare = binary.LittleEndian.AppendUint32(fare, 6)
			fare = binary.LittleEndian.AppendUint32(fare, 8)
			if err := e.write(opcode.CMSGActivatetaxi, fare); err != nil {
				t.Fatal(err)
			}
			if mode != "unanswered new visit" {
				e.observeTransportScope(opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0})
			}
			if mode != "same visit" {
				e.observeTransportScope(opcode.SMSGNewWorld, positionBody(0, 7, 13, 19, 0.5))
			}
			body := ownReportPacket(t, 0)
			i, _ := movementinfo.OwnFrame(body, e.transport.scope.SelfGuid)
			i.X, i.Y, i.Z = 7, 13, 19
			body, _ = movementinfo.MoverFrame(e.transport.scope.SelfGuid, i)
			e.observeTransportScope(opcode.MSGMoveHeartbeat, body)
			if mode != "missing current proof" {
				w := binary.LittleEndian.AppendUint64(nil, e.transport.scope.MotionRevision)
				for _, n := range []float32{i.X, i.Y, i.Z} {
					w = binary.LittleEndian.AppendUint32(w, math.Float32bits(n))
				}
				e.s.recordTaxiBodyOrigin(e.transport.snapshot(), w)
			}
			if mode == "wrong new origin" {
				i.X++
				body, _ = movementinfo.MoverFrame(e.transport.scope.SelfGuid, i)
			}
			before := c.writes
			err := e.write(opcode.MSGMoveStop, body)
			want := mode == "accepted new visit" || mode == "unanswered new visit"
			if want && (err != nil || c.writes != before+1 || e.taxiBody.active()) || !want && (!modcb.PacketWasNotDispatched(err) || c.writes != before) {
				t.Fatal(mode, err, c.writes-before)
			}
		})
	}
}
