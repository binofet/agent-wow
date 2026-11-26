package world

import (
	"context"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

// A portal's STOP must still belong to the exact observed source visit when
// the owner loop writes it. World/Actions queues can lag a same-map entry.
// Legacy unscoped STOP remains available through its existing path.
func (e *engine) reserveGameplayPacket(ctx context.Context, op uint32, body []byte, requested *modv1.TransportScope, prior *modv1.WorldPacket) error {
	qualified := modsession.IsBodyInput(ctx) && requested != nil
	if qualified {
		if reason := e.s.bodyProof(ctx, requested, e.transport.scope); reason != "" {
			return modcb.PacketNotDispatchedError("body input before write: " + reason)
		}
		requested = e.transport.scope
	}
	route := modsession.IsRouteBodyInput(ctx)
	if route {
		if !qualified || !movementinfo.RouteBodyFrame(op, body, requested.SelfGuid) {
			return modcb.PacketNotDispatchedError("invalid normal route body frame")
		}
		if err := e.transport.reserveBodySource(requested); err != nil {
			return err
		}
		if op != opcode.MSGMoveStop {
			return nil
		}
	}
	if op == opcode.MSGMoveStop && requested != nil {
		return e.transport.reserveMovementStop(body, requested, prior, qualified && prior != nil && e.s.bodyProof(ctx, prior.TransportScope, e.transport.scope) == "", route)
	}
	return e.transport.reserve(op, body, requested)
}

func (t *transportScopeState) reserveMovementStop(payload []byte, requested *modv1.TransportScope, prior *modv1.WorldPacket, qualifiedEntry bool, route ...bool) error {
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
		routeFrame := len(route) == 1 && route[0] && prior != nil && movementinfo.RouteBodyFrame(prior.Opcode, prior.Payload, current.SelfGuid) &&
			(prior.Opcode == opcode.MSGMoveStartSwim || prior.Opcode == opcode.MSGMoveHeartbeat || prior.Opcode == opcode.MSGMoveStartForward)
		if !info.BodyWaterEntry(false) || !routeFrame && prior.GetOpcode() != opcode.MSGMoveStartSwim || !qualifiedEntry && !proto.Equal(prior.GetTransportScope(), current) {
			return modcb.PacketNotDispatchedError("water STOP requires the current written water entry")
		}
		before := teleportCursor{body: prior.Payload}
		if before.guid() != current.SelfGuid || before.bad {
			return modcb.PacketNotDispatchedError("water entry belongs to another body")
		}
		entry, size, err := movementinfo.DecodePrefix(prior.Payload[before.offset:])
		if err != nil || size != len(prior.Payload)-before.offset || (!(routeFrame && entry.Flags == movementinfo.SwimmingFlag|1) && !entry.BodyWaterEntry(true) && !entry.BodyWaterEntry(false)) ||
			entry.X != info.X || entry.Y != info.Y || entry.Z != info.Z || entry.Orientation != info.Orientation {
			return modcb.PacketNotDispatchedError("water STOP differs from the written water entry")
		}
	}
	// A successful STOP advances MotionRevision before its report is published.
	// An ambiguous transport result closes the owner loop; this is no retry
	// permission for an unresolved Actions movement operation.
	return nil
}
