package movementinfo

import (
	"encoding/binary"
	"math"

	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// FollowPosition is an observed destination in the current route's bound map.
type FollowPosition [3]float32

// Ordinary mixed follow replans when the leader leaves this radius from the
// selected destination. The physical takeoff uses the same three-dimensional test.
const FollowDestinationRadius = 1.5

func (p FollowPosition) Valid() bool {
	return finite(p[:]...) && math.Abs(float64(p[0])) < 1e6 && math.Abs(float64(p[1])) < 1e6 && math.Abs(float64(p[2])) < 1e6
}

func (p FollowPosition) WithinDestination(q FollowPosition) bool {
	x, y, z := float64(p[0])-float64(q[0]), float64(p[1])-float64(q[1]), float64(p[2])-float64(q[2])
	return x*x+y*y+z*z <= FollowDestinationRadius*FollowDestinationRadius
}

func (p FollowPosition) Wire() string {
	var b [12]byte
	for n, x := range p {
		binary.LittleEndian.PutUint32(b[n*4:], math.Float32bits(x))
	}
	return string(b[:])
}

func ReadFollowPosition(wire string) (FollowPosition, bool) {
	var p FollowPosition
	if len(wire) != 12 {
		return p, false
	}
	for n := range p {
		p[n] = math.Float32frombits(binary.LittleEndian.Uint32([]byte(wire[n*4:])))
	}
	return p, p.Valid()
}

func (i Info) FollowPosition() (FollowPosition, bool) {
	p := FollowPosition{i.X, i.Y, i.Z}
	flags := i.Flags
	if flags&SwimmingFlag != 0 {
		flags &^= SwimmingFlag | 0x00400000 | 0x00800000 // swim, ascend, descend
	}
	return p, p.Valid() && i.AllFinite() && math.Abs(float64(i.Orientation)) < 1e6 && movementflags.GroundCompatible(flags, uint32(i.ExtraFlags))
}

// Relocation, knockback, jumping, forced speed/control or a server spline
// can invalidate the route even at the same coordinates.
func OrdinaryFollowMovement(op uint32) bool {
	switch op {
	case opcode.MSGMoveStartForward, opcode.MSGMoveStartBackward, opcode.MSGMoveStop,
		opcode.MSGMoveStartStrafeLeft, opcode.MSGMoveStartStrafeRight, opcode.MSGMoveStopStrafe,
		opcode.MSGMoveStartTurnLeft, opcode.MSGMoveStartTurnRight, opcode.MSGMoveStopTurn,
		opcode.MSGMoveStartPitchUp, opcode.MSGMoveStartPitchDown, opcode.MSGMoveStopPitch,
		opcode.MSGMoveSetRunMode, opcode.MSGMoveSetWalkMode, opcode.MSGMoveSetFacing, opcode.MSGMoveSetPitch,
		opcode.MSGMoveHeartbeat, opcode.MSGMoveStartSwim, opcode.MSGMoveStopSwim,
		opcode.MSGMoveStartAscend, opcode.MSGMoveStartDescend, opcode.MSGMoveStopAscend:
		return true
	}
	return false
}
