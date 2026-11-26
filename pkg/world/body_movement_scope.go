package world

import (
	"math"
	"time"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func bodyMovementScopeOpcode(op uint32) bool {
	switch op {
	case opcode.MSGMoveSetFacing, opcode.MSGMoveStartForward, opcode.MSGMoveHeartbeat, opcode.MSGMoveJump, opcode.MSGMoveFallLand, opcode.MSGMoveStartSwim:
		return true
	}
	return false
}

// Validate the existing normal body-input image without projecting a pose or
// deriving authority from it. Geometry and the input choice remain upstream.
func normalBodyMovementImage(op uint32, payload []byte, self uint64, measured bool) bool {
	if !bodyMovementScopeOpcode(op) || self == 0 {
		return false
	}
	i, ok := movementinfo.OwnFrame(payload, self)
	if !ok {
		return false
	}
	if op == opcode.MSGMoveStartSwim {
		return i.BodyWaterEntry(true) || measured && i.BodyWaterEntry(false)
	}
	if i.ExtraFlags != 0 || i.Flags & ^(uint32(1|0x1000)|movementflags.PassiveModifiers) != 0 {
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
	settling := op == opcode.MSGMoveHeartbeat && (j.Velocity == 0 || measured && math.Abs(float64(j.Velocity)-buddynav.BodyJumpVelocity) <= .0001) && j.XYSpeed == 0 && i.Flags&1 == 0
	walkOff := op == opcode.MSGMoveHeartbeat && j.Velocity == 0 && j.XYSpeed >= .1 && j.XYSpeed <= 14 && i.Flags&1 != 0
	duration := time.Duration(i.FallTime) * time.Millisecond
	return (jump || settling || walkOff) && (op != opcode.MSGMoveJump || jump) &&
		(duration <= buddynav.BodyMotionHorizon || (!settling || measured) && buddynav.BodyFlightInRange(duration, float64(j.XYSpeed), float64(j.Velocity))) &&
		math.Abs(float64(j.SinAngle)*float64(j.SinAngle)+float64(j.CosAngle)*float64(j.CosAngle)-1) <= .001
}

func (t *transportScopeState) reserveBodySource(requested *modv1.TransportScope) error {
	current := t.scope
	if current == nil || t.exhausted || current.ConnectionId == "" || current.SelfGuid == 0 || !current.VisitBound || current.SplineActive ||
		current.VisitRevision == 0 || current.MotionRevision == 0 || !proto.Equal(requested, current) {
		return modcb.PacketNotDispatchedError("body input transport scope is absent or no longer current")
	}
	return nil
}

func (t *transportScopeState) reserveBodyMovement(op uint32, payload []byte, requested *modv1.TransportScope, measured bool) error {
	if err := t.reserveBodySource(requested); err != nil {
		return err
	}
	if !normalBodyMovementImage(op, payload, requested.SelfGuid, measured) {
		return modcb.PacketNotDispatchedError("body input requires its complete finite normal MovementInfo")
	}
	return nil
}
