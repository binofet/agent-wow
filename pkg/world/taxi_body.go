package world

import (
	"encoding/binary"
	"math"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

// The physical writer owns the reservation independently of subscriber queues.
// A rejection before boarding can restore the original body. A successful
// flight requires a checked first local frame at the new body origin.
type taxiBodyGuard struct {
	pending, boarded bool
	booking          *modv1.TransportScope
	uncertainOrigin  uint64
}

func (g taxiBodyGuard) active() bool { return g.pending || g.boarded }

func (e *engine) needsPhysicalDrain(op uint32, requested *modv1.TransportScope) bool {
	return requested != nil || op == opcode.CMSGMoveSplineDone || e.taxiBody.active() && voluntaryTaxiBodyFrame(op)
}

func voluntaryTaxiBodyFrame(op uint32) bool {
	return movementinfo.OrdinaryFollowMovement(op) || op == opcode.MSGMoveJump || op == opcode.MSGMoveFallLand
}

func (g *taxiBodyGuard) entering(op uint32, scope *modv1.TransportScope) {
	switch op {
	case opcode.CMSGActivatetaxi, opcode.CMSGActivatetaxiexpress:
		g.pending = true
		g.booking = cloneTransportScope(scope)
	}
}

func (g *taxiBodyGuard) observe(op uint16, data []byte) {
	if op != opcode.SMSGActivatetaxireply || !g.pending || len(data) != 4 {
		return
	}
	switch code := binary.LittleEndian.Uint32(data); {
	case code == 0:
		g.pending, g.boarded = false, true
	case code <= 12:
		g.pending = false
	}
}

func (s *Session) recordTaxiBodyOrigin(scope *modv1.TransportScope, data []byte) {
	if !scope.GetVisitBound() || scope.GetMotionRevision() == 0 || len(data) != modcb.TaxiBodyOriginSize ||
		binary.LittleEndian.Uint64(data) == 0 || binary.LittleEndian.Uint64(data) > scope.MotionRevision {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !proto.Equal(scope, s.transportScopeSnapshot) {
		return
	}
	s.taxiBodyOrigin = &modv1.WorldPacket{TransportScope: cloneTransportScope(scope), Payload: append([]byte(nil), data...)}
}

func (e *engine) taxiBodyFrameCurrent(payload []byte) bool {
	g, current := &e.taxiBody, e.transport.scope
	if g.pending || !g.boarded || !current.GetVisitBound() || current.GetSelfGuid() == 0 || g.booking == nil ||
		g.booking.ConnectionId != current.ConnectionId || g.booking.SelfGuid != current.SelfGuid || current.MotionRevision <= g.booking.MotionRevision {
		return false
	}
	i, ok := movementinfo.OwnFrame(payload, current.SelfGuid)
	if !ok || !i.AllFinite() {
		return false
	}
	e.s.mu.RLock()
	defer e.s.mu.RUnlock()
	r := e.s.taxiBodyOrigin
	return r != nil && proto.Equal(r.TransportScope, current) && len(r.Payload) == modcb.TaxiBodyOriginSize &&
		binary.LittleEndian.Uint64(r.Payload) > g.booking.MotionRevision &&
		binary.LittleEndian.Uint64(r.Payload) > g.uncertainOrigin &&
		math.Float32frombits(binary.LittleEndian.Uint32(r.Payload[8:])) == i.X &&
		math.Float32frombits(binary.LittleEndian.Uint32(r.Payload[12:])) == i.Y &&
		math.Float32frombits(binary.LittleEndian.Uint32(r.Payload[16:])) == i.Z
}
