package world

import (
	"context"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"strings"
)

// A portal's STOP must still belong to the exact observed source visit when
// the owner loop writes it. World/Actions queues can lag a same-map entry.
// Legacy unscoped STOP remains available through its existing path.
func (e *engine) reserveGameplayPacket(ctx context.Context, op uint32, body []byte, requested *modv1.TransportScope, prior *modv1.WorldPacket) error {
	if e.taxiBody.active() && voluntaryTaxiBodyFrame(op) && !e.taxiBodyFrameCurrent(body) {
		return modcb.PacketNotDispatchedError("ordinary movement requires the current post-taxi body origin")
	}
	if requested != nil && modcb.FlightpathGroundPacket(op, body) {
		if requested.TaxiArrivalQueryRevision != 0 {
			return e.reserveNaturalTaxiMenu(ctx, op, requested)
		}
		// The socket owner has drained ready packets. Its latest known STOP
		// and the complete World observation must both still be current here.
		stop := e.ownMovementReport
		if stop.GetOpcode() != opcode.MSGMoveStop || !proto.Equal(stop.GetTransportScope(), requested) ||
			e.transport.exhausted || e.impulse != nil && !e.impulse.finished ||
			e.s.bodyProof(modsession.WithSettledBodySource(ctx), requested, e.transport.scope) != "" ||
			!modsession.FlightpathWorldCurrent(ctx) {
			return modcb.PacketNotDispatchedError("flightmaster observation or stationary body changed before write")
		}
		i, ok := movementinfo.OwnFrame(stop.Payload, requested.SelfGuid)
		if !ok || !movementflags.GroundCompatible(i.Flags, uint32(i.ExtraFlags)) || i.Flags&0xff != 0 || i.FallTime != 0 {
			return modcb.PacketNotDispatchedError("flightmaster interaction requires a written stationary ground STOP")
		}
		return nil
	}
	if k, ok := ctx.Value(knockbackFrameKey{}).(*knockbackMotion); ok {
		if e.impulse != k || k.finished || requested == nil || !proto.Equal(requested, k.lastScope) || e.s.bodyProof(ctx, requested, e.transport.scope) != "" {
			return modcb.PacketNotDispatchedError("knockback movement source changed")
		}
		return nil
	}
	if k := e.impulse; k != nil && !k.finished && strings.HasPrefix(opcode.WorldName(op), "MSG_MOVE_") && op != opcode.MSGMoveTeleportAck && op != opcode.MSGMoveWorldportAck && e.s.bodyProof(modsession.WithSelfBodyInput(ctx), k.lastScope, e.transport.scope) == "" {
		return modcb.PacketNotDispatchedError("server knockback owns the moving body")
	}
	qualified := modsession.IsBodyInput(ctx) && requested != nil
	if qualified {
		if reason := e.s.bodyProof(ctx, requested, e.transport.scope); reason != "" {
			return modcb.PacketNotDispatchedError("body input before write: " + reason)
		}
		requested = e.transport.scope
	}
	route := modsession.IsRouteBodyInput(ctx)
	if route {
		if !qualified || !buddynav.RouteBodyFrame(op, body, requested.SelfGuid) {
			return modcb.PacketNotDispatchedError("invalid normal route body frame")
		}
		if err := e.transport.reserveBodySource(requested); err != nil {
			return err
		}
		if op != opcode.MSGMoveStop {
			return nil
		}
	}
	if qualified && bodyMovementScopeOpcode(op) {
		return e.transport.reserveBodyMovement(op, body, requested, true)
	}
	if op == opcode.MSGMoveStop && requested != nil {
		return e.transport.reserveMovementStop(body, requested, prior, qualified && prior != nil && e.s.bodyProof(ctx, prior.TransportScope, e.transport.scope) == "", route)
	}
	return e.transport.reserve(op, body, requested)
}

func (t *transportScopeState) reserveMovementStop(payload []byte, requested *modv1.TransportScope, prior *modv1.WorldPacket, qualifiedEntry, route bool) error {
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
		// A measured landing stops at its actual entry. A native surface route
		// can also stop at its latest complete swimming checkpoint.
		if !info.BodyWaterEntry(false) || prior == nil || !qualifiedEntry && !proto.Equal(prior.TransportScope, current) {
			return modcb.PacketNotDispatchedError("water STOP requires a current written water position")
		}
		entry, ok := movementinfo.OwnFrame(prior.Payload, current.SelfGuid)
		entered := prior.Opcode == opcode.MSGMoveStartSwim && (entry.BodyWaterEntry(true) || entry.BodyWaterEntry(false))
		routeCheckpoint := route && (prior.Opcode == opcode.MSGMoveHeartbeat || prior.Opcode == opcode.MSGMoveStartForward || prior.Opcode == opcode.MSGMoveStartSwim) &&
			entry.Flags == movementinfo.SwimmingFlag|1 && buddynav.RouteBodyFrame(prior.Opcode, prior.Payload, current.SelfGuid)
		if !ok || !entered && !routeCheckpoint || entry.X != info.X || entry.Y != info.Y || entry.Z != info.Z || entry.Orientation != info.Orientation {
			return modcb.PacketNotDispatchedError("water STOP differs from the written water position")
		}
	}
	// A successful STOP advances MotionRevision before its report is published.
	// An ambiguous transport result closes the owner loop; this is no retry
	// permission for an unresolved Actions movement operation.
	return nil
}
