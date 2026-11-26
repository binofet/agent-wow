package buddyworld

import (
	"context"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

func (s *Service) observeKnockbackLocked(ctx context.Context, packet *modv1.WorldPacket, p observed, next *observations) {
	bound := scopeMatchesSelf(packet.TransportScope, p, next.visitRevision)
	if !bound || p.receivedMotionInvalid || p.receivedPosition || p.receivedOwnCreate || p.known[unitHealth] && p.fields[unitHealth] == 0 {
		s.knockback = nil
	}
	if bound && p.receivedMovement && p.positionKnown && p.known[unitHealth] && p.fields[unitHealth] > 0 && p.receivedMovementInfo.AllFinite() {
		wire, err := movementinfo.MoverFrame(p.guid, p.receivedMovementInfo)
		if err == nil {
			_ = grpc.SetTrailer(ctx, metadata.Pairs(modcb.KnockbackPoseTrailer, string(wire)))
		}
	}
	if bound && packet.Opcode == opcode.SMSGMoveKnockBack {
		if k, err := movementinfo.DecodeKnockback(packet.Payload); err == nil && k.Mover == p.guid {
			s.knockback = proto.Clone(packet).(*modv1.WorldPacket)
		}
	}
}

// Only a known socket write for this received order can restore the client's
// pose. Keep its provenance distinct from the last realm MovementInfo.
func (s *Service) settleKnockback(ctx context.Context) {
	s.mu.Lock()
	order, client := s.knockback, s.session
	s.mu.Unlock()
	if order == nil || client == nil {
		return
	}
	r, err := modcb.ReadKnockbackReport(ctx, client, order)
	if err != nil || !sameMotionVisit(order.TransportScope, r.TransportScope) || r.TransportScope.MotionRevision <= order.TransportScope.MotionRevision {
		return
	}
	i, ok := movementinfo.OwnFrame(r.Payload, order.TransportScope.SelfGuid)
	if !ok || !(r.Opcode == opcode.MSGMoveFallLand && i.Flags == 0 || r.Opcode == opcode.MSGMoveStartSwim && i.BodyWaterEntry(false)) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := &s.state.players[0]
	if s.knockback != order || p.positionKnown || p.known[unitHealth] && p.fields[unitHealth] == 0 || !scopeMatchesSelf(r.TransportScope, *p, s.state.visitRevision) {
		return
	}
	now := s.now()
	p.x, p.y, p.z, p.orientation = i.X, i.Y, i.Z, i.Orientation
	p.positionKnown, p.positionInferred, p.movementKnown = true, true, true
	p.movementFlags, p.extraFlags, p.movementTime = i.Flags, uint32(i.ExtraFlags), i.Time
	p.positionRevision++
	p.positionAt, p.observedAt = now, now
	s.knockback = nil
}
