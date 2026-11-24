package world

import (
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func ownMovementReportOpcode(op uint32) bool {
	return bodyMovementScopeOpcode(op) || op == opcode.MSGMoveStop
}

// Called only after a successful transport write and its scope advancement.
// Keep the exact original packet, including packed-GUID encoding and clock;
// never seed this report from an incoming pose or canonical re-encoding.
func (e *engine) retainOwnMovementReport(op uint32, body []byte) {
	if !ownMovementReportOpcode(op) {
		return
	}
	scope := e.transport.scope
	if scope == nil || !scope.VisitBound || scope.VisitRevision == 0 || scope.MotionRevision == 0 || scope.ConnectionId == "" ||
		scope.SelfGuid == 0 || e.s.state.Character.GUID != scope.SelfGuid {
		return
	}
	c := teleportCursor{body: body}
	if c.guid() != scope.SelfGuid || c.bad {
		return
	}
	info, n, err := movementinfo.DecodePrefix(body[c.offset:])
	if err != nil || n != len(body)-c.offset || !info.AllFinite() {
		return
	}
	legacyGround := (op == opcode.MSGMoveStartForward || op == opcode.MSGMoveHeartbeat || op == opcode.MSGMoveStop) &&
		movementflags.GroundCompatible(info.Flags, uint32(info.ExtraFlags))
	if !legacyGround && !normalBodyMovementImage(op, body, scope.SelfGuid) {
		return
	}
	e.ownMovementReport = &modv1.WorldPacket{Opcode: op, Payload: append([]byte(nil), body...), TransportScope: cloneTransportScope(scope)}
}

// OwnMovementReport is an exact known written client packet, not an observed
// server pose, permission to move, or proof of trigger admission. Scope changes
// make it unavailable until another successful own normal movement write.
func (s *Session) OwnMovementReport() *modv1.WorldPacket {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, scope := s.ownMovementReportSnapshot, s.transportScopeSnapshot
	if s.state.Status != InWorld || r == nil || scope == nil || !scope.VisitBound || scope.VisitRevision == 0 ||
		scope.SelfGuid != s.state.Character.GUID || !proto.Equal(r.TransportScope, scope) {
		return nil
	}
	return proto.Clone(r).(*modv1.WorldPacket)
}
