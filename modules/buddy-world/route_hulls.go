package buddyworld

import (
	"context"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type routeRider struct {
	ship, revision uint64
}

// A paired frame can replace the ship's CREATE as the selected geometric
// checkpoint. Receiving, replacing or losing that frame affects its own ship.
func (s *Service) routeHullRiders(mapID uint32, packets uint64) []modcb.RouteHullRider {
	var current map[uint64]routeRider
	var roster []modcb.RouteHullRider
	visit := s.state.visitRevision
	record := func(p *observed) {
		frame := p.passenger
		if frame == nil || frame.source.visit != visit || !frame.info.TransportPresent ||
			!p.present || !p.mapKnown || p.mapID != mapID || !p.positionKnown || p.positionInferred || !p.movementKnown {
			return
		}
		ship := frame.info.Transport.GUID
		hull := s.state.gameObjects[ship]
		_, _, alive := p.life()
		if !alive || hull == nil || !movingTransport(hull) || !hull.present || !hull.mapKnown || hull.mapID != mapID {
			return
		}
		if current == nil {
			current = make(map[uint64]routeRider)
		}
		r := routeRider{ship, frame.source.revision}
		current[p.guid] = r
		roster = append(roster, modcb.RouteHullRider{Player: p.guid, Ship: ship})
		if s.routeRiders[p.guid] != r {
			hull.routeHullPackets = packets
		}
	}
	if len(s.state.gameObjects) != 0 {
		for i := range s.state.players {
			record(&s.state.players[i])
		}
		for _, p := range s.state.nearbyPlayers {
			record(p)
		}
	}
	for player, earlier := range s.routeRiders {
		if current[player].ship != earlier.ship {
			if hull := s.state.gameObjects[earlier.ship]; hull != nil {
				hull.routeHullPackets = packets
			}
		}
	}
	s.routeRiders = current
	return roster
}

// Called under the packet lock after the ordinary World parser has committed
// or withdrawn the whole batch. An unrelated record leaves a hull's source
// ordinal intact. Re-creating its numeric GUID produces a different birth.
func (s *Service) publishRouteHulls(ctx context.Context, before map[uint64]*observed) {
	self := &s.state.players[0]
	packets := s.observed + s.ignored + s.malformed
	var roster []modcb.RouteHullRider
	if self.present && self.mapKnown {
		roster = s.routeHullRiders(self.mapID, packets)
	} else {
		s.routeRiders = nil
	}
	trailers := metadata.Pairs(modcb.RouteHullTrailer, modcb.RouteHullHeader(s.routeHullSequence, roster))
	for id, p := range s.state.gameObjects {
		if !self.present || !self.mapKnown || !movingTransport(p) || !p.present || !p.mapKnown || p.mapID != self.mapID {
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
			trailers.Append(modcb.RouteHullTrailer, hull.Wire())
		}
	}
	_ = grpc.SetTrailer(ctx, trailers)
}
