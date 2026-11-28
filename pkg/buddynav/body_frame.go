package buddynav

import (
	"math"
	"time"

	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// RouteBodyFrame validates a measured native ground, water or passenger frame.
func RouteBodyFrame(op uint32, payload []byte, self uint64) bool {
	i, ok := movementinfo.OwnFrame(payload, self)
	if !ok || i.ExtraFlags != 0 {
		return false
	}
	if i.TransportPresent {
		return transportBodyFrame(op, i)
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
		if op != opcode.MSGMoveHeartbeat && op != opcode.MSGMoveJump || !i.JumpPresent {
			return false
		}
		j := i.Jump
		jump := math.Abs(float64(j.Velocity)-BodyJumpVelocity) <= .0001
		forward := i.Flags == fallingFlag|1 && (j.Velocity == 0 || jump) && j.XYSpeed >= .1 && j.XYSpeed <= 14
		settling := i.Flags == fallingFlag && j.XYSpeed == 0 && (j.Velocity == 0 || math.Abs(float64(j.Velocity)-BodyJumpVelocity) <= .0001)
		return (forward || settling) && (op != opcode.MSGMoveJump || forward && jump) && math.Abs(float64(j.SinAngle)*float64(j.SinAngle)+float64(j.CosAngle)*float64(j.CosAngle)-1) <= .001 &&
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

// IsMovingTransportGUID recognizes the build-12340 moving transport's global
// object counter. These GUIDs have no template-entry bits.
func IsMovingTransportGUID(guid uint64) bool {
	return guid>>32 == 0x1fc00000 && uint32(guid) != 0
}

// TransportBodyFrame checks that a passenger frame names one moving transport and preserves
// its complete local coordinates and clock. Vehicle seats and swimming while
// attached are separate mechanics and cannot be silently encoded as a boat.
func TransportBodyFrame(op uint32, payload []byte, self uint64) bool {
	i, ok := movementinfo.OwnFrame(payload, self)
	return ok && transportBodyFrame(op, i)
}

func transportBodyFrame(op uint32, i movementinfo.Info) bool {
	if !i.TransportPresent || i.ExtraFlags != 0 || !IsMovingTransportGUID(i.Transport.GUID) ||
		i.Transport.Seat != -1 || i.Transport.Time2Present || i.PitchPresent || i.SplineElevationPresent {
		return false
	}
	flags := i.Flags &^ uint32(0x200)
	if op == opcode.MSGMoveFallLand {
		return flags <= 1 && !i.JumpPresent && BodyFlightInRange(time.Duration(i.FallTime)*time.Millisecond, 0, BodyJumpVelocity)
	}
	if flags == 0 || flags == 1 {
		if i.JumpPresent || i.FallTime != 0 {
			return false
		}
		return (op == opcode.MSGMoveSetFacing || op == opcode.MSGMoveStop) && flags == 0 ||
			(op == opcode.MSGMoveStartForward || op == opcode.MSGMoveHeartbeat) && flags == 1
	}
	if flags != 0x1001 && flags != 0x1000 || !i.JumpPresent || op != opcode.MSGMoveJump && op != opcode.MSGMoveHeartbeat {
		return false
	}
	j := i.Jump
	jump := math.Abs(float64(j.Velocity)-BodyJumpVelocity) <= .0001
	forward := flags == 0x1001 && (jump || j.Velocity == 0) && j.XYSpeed >= .1 && j.XYSpeed <= 14
	settling := op == opcode.MSGMoveHeartbeat && flags == 0x1000 && j.XYSpeed == 0 && (j.Velocity == 0 || jump)
	return (forward || settling) && (op != opcode.MSGMoveJump || forward && jump) &&
		math.Abs(float64(j.SinAngle)*float64(j.SinAngle)+float64(j.CosAngle)*float64(j.CosAngle)-1) <= .001 &&
		BodyFlightInRange(time.Duration(i.FallTime)*time.Millisecond, float64(j.XYSpeed), float64(j.Velocity))
}
