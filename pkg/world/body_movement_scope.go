package world

import (
	"math"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func bodyMovementScopeOpcode(op uint32) bool {
	switch op {
	case opcode.MSGMoveSetFacing, opcode.MSGMoveStartForward, opcode.MSGMoveHeartbeat, opcode.MSGMoveJump, opcode.MSGMoveFallLand:
		return true
	}
	return false
}

// Validate the existing normal body-input image without projecting a pose or
// deriving authority from it. Geometry and the input choice remain upstream.
func normalBodyMovementImage(op uint32, payload []byte, self uint64) bool {
	if !bodyMovementScopeOpcode(op) || self == 0 {
		return false
	}
	c := teleportCursor{body: payload}
	if c.guid() != self || c.bad {
		return false
	}
	i, n, err := movementinfo.DecodePrefix(payload[c.offset:])
	if err != nil || n != len(payload)-c.offset || !i.AllFinite() || i.ExtraFlags != 0 ||
		i.Flags & ^(uint32(1|0x1000)|movementflags.PassiveModifiers) != 0 {
		return false
	}
	falling := i.Flags&0x1000 != 0
	if op == opcode.MSGMoveJump && !falling || (op == opcode.MSGMoveFallLand || op == opcode.MSGMoveSetFacing || op == opcode.MSGMoveStartForward) && falling {
		return false
	}
	if !falling {
		return true
	}
	j := i.Jump
	jump := math.Abs(float64(j.Velocity)-7.955547) <= .0001 && j.XYSpeed >= .1 && j.XYSpeed <= 14
	settling := j.Velocity == 0 && j.XYSpeed == 0 && i.Flags&1 == 0
	return (jump || settling) && (op != opcode.MSGMoveJump || jump) && i.FallTime <= 1100 &&
		math.Abs(float64(j.SinAngle)*float64(j.SinAngle)+float64(j.CosAngle)*float64(j.CosAngle)-1) <= .001
}

func (t *transportScopeState) reserveBodyMovement(op uint32, payload []byte, requested *modv1.TransportScope) error {
	current := t.scope
	if current == nil || t.exhausted || current.ConnectionId == "" || current.SelfGuid == 0 || !current.VisitBound || current.SplineActive ||
		current.VisitRevision == 0 || current.MotionRevision == 0 || !proto.Equal(requested, current) {
		return modcb.PacketNotDispatchedError("body input transport scope is absent or no longer current")
	}
	if !normalBodyMovementImage(op, payload, current.SelfGuid) {
		return modcb.PacketNotDispatchedError("body input requires its complete finite normal MovementInfo")
	}
	return nil
}
