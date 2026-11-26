package world

import (
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

// A portal's STOP must still belong to the exact observed source visit when
// the owner loop writes it. World/Actions queues can lag a same-map entry.
// Legacy unscoped STOP remains available through its existing path.
func (e *engine) reserveGameplayPacket(op uint32, body []byte, requested *modv1.TransportScope, prior *modv1.WorldPacket) error {
	if op == opcode.MSGMoveStop && requested != nil {
		return e.transport.reserveMovementStop(body, requested, prior)
	}
	return e.transport.reserve(op, body, requested)
}

func (t *transportScopeState) reserveMovementStop(payload []byte, requested *modv1.TransportScope, prior *modv1.WorldPacket) error {
	current := t.scope
	if current == nil || t.exhausted || current.ConnectionId == "" || current.SelfGuid == 0 || !current.VisitBound ||
		current.VisitRevision == 0 || current.MotionRevision == 0 || !proto.Equal(requested, current) {
		return modcb.PacketNotDispatchedError("ground STOP transport scope is absent or no longer current")
	}
	c := teleportCursor{body: payload}
	if c.guid() != current.SelfGuid || c.bad {
		return modcb.PacketNotDispatchedError("ground STOP mover does not match the current body")
	}
	info, n, err := movementinfo.DecodePrefix(payload[c.offset:])
	if err != nil || n != len(payload)-c.offset || !info.AllFinite() || info.Flags&0xff != 0 {
		return modcb.PacketNotDispatchedError("ground STOP requires a complete finite stationary ground MovementInfo")
	}
	if !movementflags.GroundCompatible(info.Flags, uint32(info.ExtraFlags)) {
		// Only the immediately preceding known water entry can authorize its
		// stationary wet STOP, at the same contact point in the same scope.
		if !info.BodyWaterEntry(false) || prior.GetOpcode() != opcode.MSGMoveStartSwim || !proto.Equal(prior.GetTransportScope(), current) {
			return modcb.PacketNotDispatchedError("water STOP requires the current written water entry")
		}
		before := teleportCursor{body: prior.Payload}
		if before.guid() != current.SelfGuid || before.bad {
			return modcb.PacketNotDispatchedError("water entry belongs to another body")
		}
		entry, size, err := movementinfo.DecodePrefix(prior.Payload[before.offset:])
		if err != nil || size != len(prior.Payload)-before.offset || !entry.BodyWaterEntry(true) ||
			entry.X != info.X || entry.Y != info.Y || entry.Z != info.Z || entry.Orientation != info.Orientation {
			return modcb.PacketNotDispatchedError("water STOP differs from the written water entry")
		}
	}
	// A successful STOP advances MotionRevision before its report is published.
	// An ambiguous transport result closes the owner loop; this is no retry
	// permission for an unresolved Actions movement operation.
	return nil
}
