package buddyworld

import (
	"context"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// Called under the packet lock after the ordinary World parser has committed
// or withdrawn the whole batch. An unrelated record leaves a hull's source
// ordinal intact. Re-creating its numeric GUID produces a different birth.
func (s *Service) publishRouteHulls(ctx context.Context, before map[uint64]*observed) {
	self := &s.state.players[0]
	if !self.present || !self.mapKnown {
		return
	}
	packets := s.observed + s.ignored + s.malformed
	var trailers metadata.MD
	for id, p := range s.state.gameObjects {
		if !movingTransport(p) || !p.present || !p.mapKnown || p.mapID != self.mapID {
			continue
		}
		old := before[id]
		if old == nil || p.transportBirth != old.transportBirth || p.transport != old.transport || p.transportFraction != old.transportFraction ||
			p.positionKnown != old.positionKnown || p.positionRevision != old.positionRevision ||
			p.fields[0x04] != old.fields[0x04] || p.fields[0x08] != old.fields[0x08] ||
			p.fields[0x10] != old.fields[0x10] {
			p.routeHullPackets = packets
		}
		hull := modcb.RouteHull{GUID: id, Birth: p.transportBirth, Packets: p.routeHullPackets}
		if hull.Valid() {
			if trailers == nil {
				trailers = metadata.MD{}
			}
			trailers.Append(modcb.RouteHullTrailer, hull.Wire())
		}
	}
	if len(trailers) != 0 {
		_ = grpc.SetTrailer(ctx, trailers)
	}
}
