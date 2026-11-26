package buddynav

import (
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"math"
	"time"
)

// RouteBodyFrame is the wire shape of a measured native ground/water route.
// A route can walk off a ledge or finish existing gravity. It does not jump.
func RouteBodyFrame(op uint32, payload []byte, self uint64) bool {
	i, ok := movementinfo.OwnFrame(payload, self)
	if !ok || i.ExtraFlags != 0 {
		return false
	}
	if i.Flags&movementinfo.SwimmingFlag != 0 {
		if i.FallTime != 0 || !i.PitchPresent || math.Abs(float64(i.Pitch)) > float64(float32(math.Pi/2)) {
			return false
		}
		switch op {
		case opcode.MSGMoveStop:
			return i.BodyWaterEntry(false)
		case opcode.MSGMoveStartSwim:
			return i.Flags == movementinfo.SwimmingFlag|1 || i.BodyWaterEntry(false)
		case opcode.MSGMoveHeartbeat, opcode.MSGMoveStartForward:
			return i.Flags == movementinfo.SwimmingFlag|1
		}
		return false
	}
	const fallingFlag = 0x1000
	if i.Flags&fallingFlag != 0 {
		if op != opcode.MSGMoveHeartbeat || !i.JumpPresent {
			return false
		}
		j := i.Jump
		forward := i.Flags == fallingFlag|1 && j.Velocity == 0 && j.XYSpeed >= .1 && j.XYSpeed <= 14
		settling := i.Flags == fallingFlag && j.XYSpeed == 0 && (j.Velocity == 0 || math.Abs(float64(j.Velocity)-BodyJumpVelocity) <= .0001)
		return (forward || settling) && math.Abs(float64(j.SinAngle)*float64(j.SinAngle)+float64(j.CosAngle)*float64(j.CosAngle)-1) <= .001 &&
			BodyFlightInRange(time.Duration(i.FallTime)*time.Millisecond, float64(j.XYSpeed), float64(j.Velocity))
	}
	switch op {
	case opcode.MSGMoveSetFacing, opcode.MSGMoveStop:
		return i.Flags == 0 && i.FallTime == 0
	case opcode.MSGMoveStartForward, opcode.MSGMoveStopSwim, opcode.MSGMoveHeartbeat:
		return i.Flags == 1 && i.FallTime == 0
	case opcode.MSGMoveFallLand:
		return i.Flags <= 1
	}
	return false
}
