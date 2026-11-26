package buddyactions

import (
	"context"
	"math"
	"time"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// The session owns the order/ACK and its clock. Actions supplies only the
// first physical contact measured with this module's mounted client geometry.
func (s *Service) measureKnockback(ctx context.Context, packet *modv1.WorldPacket) {
	s.mu.Lock()
	dir := s.navDataDir
	s.mu.Unlock()
	k, err := movementinfo.DecodeKnockback(packet.Payload)
	if err != nil || k.Mover != s.selfGUID || packet.GetTransportScope().GetSelfGuid() != k.Mover || dir == "" {
		return
	}
	launch, err := modcb.ReadKnockbackReport(ctx, s.session, packet)
	if err != nil || launch.Opcode != opcode.CMSGMoveKnockBackAck || !proto.Equal(launch.TransportScope, packet.TransportScope) {
		return
	}
	i, ok := k.ReadAcknowledgement(launch.Payload)
	if !ok {
		return
	}
	start := buddynav.Point{X: i.X, Y: i.Y, Z: i.Z}
	points, err := buddynav.PlanKnockbackContext(ctx, dir, packet.TransportScope.MapId, start, math.Atan2(float64(k.Sin), float64(k.Cos)), float64(k.Horizontal), -float64(k.Vertical))
	if err != nil || len(points) < 2 {
		return
	}
	last := points[len(points)-1]
	elapsed := (last.At + time.Millisecond - 1).Milliseconds()
	if elapsed <= 0 || elapsed > math.MaxUint32 {
		return
	}
	contact := movementinfo.Info{X: last.Point.X, Y: last.Point.Y, Z: last.Point.Z, Orientation: i.Orientation, Time: uint32(elapsed)}
	if last.Swimming {
		contact.Flags = movementinfo.SwimmingFlag
		contact.PitchPresent = true
	}
	wire, err := movementinfo.MoverFrame(k.Mover, contact)
	if err == nil {
		_ = grpc.SetTrailer(ctx, metadata.Pairs(modcb.KnockbackContactTrailer, string(wire)))
	}
}
