package world

import (
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"google.golang.org/protobuf/proto"
)

// A portal's STOP must still belong to the exact observed source visit when
// the owner loop writes it. World/Actions queues can lag a same-map entry.
// Legacy unscoped STOP remains available through its existing path.
func (t *transportScopeState) reserveMovementStop(payload []byte, requested *modv1.TransportScope) error {
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
	if err != nil || n != len(payload)-c.offset || !info.AllFinite() || !movementflags.GroundCompatible(info.Flags, uint32(info.ExtraFlags)) || info.Flags&0xff != 0 {
		return modcb.PacketNotDispatchedError("ground STOP requires a complete finite stationary ground MovementInfo")
	}
	// A successful STOP advances MotionRevision before its report is published.
	// An ambiguous transport result closes the owner loop; this is no retry
	// permission for an unresolved Actions movement operation.
	return nil
}
