package buddynav

import (
	"math"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestTransportBodyFramesKeepTheActualPassengerTail(t *testing.T) {
	base := movementinfo.Info{Flags: 0x201, X: -3900, Y: -600, Z: 6, TransportPresent: true,
		Transport: movementinfo.Transport{GUID: 0x1fc0000000000001, X: 4, Y: -2, Z: 6, Time: 41230, Seat: -1}}
	for _, op := range []uint32{opcode.MSGMoveStartForward, opcode.MSGMoveHeartbeat, opcode.MSGMoveStop, opcode.MSGMoveFallLand, opcode.MSGMoveJump} {
		i := base
		if op == opcode.MSGMoveStop {
			i.Flags = 0x200
		}
		if op == opcode.MSGMoveFallLand {
			i.FallTime = 800
		}
		if op == opcode.MSGMoveJump {
			i.Flags = 0x1201
			i.JumpPresent = true
			i.Jump = movementinfo.Jump{Velocity: BodyJumpVelocity, XYSpeed: 7, CosAngle: 1}
		}
		wire, err := movementinfo.MoverFrame(11, i)
		if err != nil || !RouteBodyFrame(op, wire, 11) {
			t.Fatalf("ordinary measured passenger %s was refused: %v", opcode.WorldName(op), err)
		}
		for _, bad := range []movementinfo.Info{
			func() movementinfo.Info { q := i; q.Transport.GUID = 0xf110000000000001; return q }(),
			func() movementinfo.Info { q := i; q.Transport.GUID = 0x1fc0000000000000; return q }(),
			func() movementinfo.Info { q := i; q.Transport.GUID = 0x1fc0010000000001; return q }(),
			func() movementinfo.Info { q := i; q.Transport.Seat = 0; return q }(),
			func() movementinfo.Info { q := i; q.Transport.X = float32(math.NaN()); return q }(),
		} {
			broken, _ := movementinfo.MoverFrame(11, bad)
			if RouteBodyFrame(op, broken, 11) {
				t.Fatalf("invalid %s passenger frame accepted", opcode.WorldName(op))
			}
		}
		if RouteBodyFrame(op, wire[:len(wire)-1], 11) || RouteBodyFrame(op, wire, 12) {
			t.Fatal("incomplete or foreign passenger frame accepted")
		}
	}
	base.Flags = 0x1200
	base.JumpPresent = true
	base.FallTime = 500
	base.Jump = movementinfo.Jump{Velocity: BodyJumpVelocity, CosAngle: 1}
	frame, _ := movementinfo.MoverFrame(11, base)
	if !RouteBodyFrame(opcode.MSGMoveHeartbeat, frame, 11) {
		t.Fatal("releasing forward blocked the passenger's existing gravity")
	}
	base.Flags = 0x1201
	base.Transport.GUID = 0x1fc00000fedcba98
	base.Jump = movementinfo.Jump{CosAngle: 1, XYSpeed: 7}
	frame, _ = movementinfo.MoverFrame(11, base)
	if !RouteBodyFrame(opcode.MSGMoveHeartbeat, frame, 11) || RouteBodyFrame(opcode.MSGMoveJump, frame, 11) {
		t.Fatal("a passenger walk-off must continue gravity without a new jump impulse")
	}
}
