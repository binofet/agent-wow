package buddyworld

import (
	"context"
	"sort"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type clientDiscoveryAnchor struct {
	known                          bool
	guid, serverRevision, sequence uint64
	mapID                          uint32
	x, y, z, orientation           float32
	at                             time.Time
}

// ObserveClientMovement receives controller-written movement, never a server
// observation. It cannot change public/private player data or raw distances.
func (s *Service) ObserveClientMovement(_ context.Context, r *worldv1.ClientMovementRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.state.players[0]
	if r == nil || r.SelfGuid == 0 || r.SelfGuid != p.guid || r.Sequence == 0 {
		return nil, status.Error(codes.InvalidArgument, "client movement identity or sequence is invalid")
	}
	if r.Sequence <= s.state.discoverySequence {
		return nil, status.Error(codes.FailedPrecondition, "client movement sequence is not newer")
	}
	if !r.Valid {
		s.state.discoverySequence = r.Sequence
		s.state.discovery = clientDiscoveryAnchor{}
		return &emptypb.Empty{}, nil
	}
	if !validFloat(r.X) || !validFloat(r.Y) || !validFloat(r.Z) || !validFloat(r.Orientation) {
		return nil, status.Error(codes.InvalidArgument, "client movement position is invalid")
	}
	if !p.present || !p.mapKnown || !p.positionKnown || p.mapID != r.MapId || p.positionRevision != r.ServerPositionRevision {
		return nil, status.Error(codes.FailedPrecondition, "client movement server baseline changed")
	}
	s.state.discoverySequence = r.Sequence
	s.state.discovery = clientDiscoveryAnchor{known: true, guid: p.guid, mapID: p.mapID, serverRevision: p.positionRevision, sequence: r.Sequence, x: r.X, y: r.Y, z: r.Z, orientation: r.Orientation, at: s.now()}
	return &emptypb.Empty{}, nil
}

func (s *observations) clientDiscoveryReference() (observed, bool) {
	a, p := s.discovery, s.players[0]
	if !a.known || !p.present || !p.mapKnown || !p.positionKnown || a.guid != p.guid || a.mapID != p.mapID || a.serverRevision != p.positionRevision {
		return observed{}, false
	}
	return observed{guid: p.guid, present: true, mapKnown: true, mapID: a.mapID, positionKnown: true, x: a.x, y: a.y, z: a.z, orientation: a.orientation}, true
}

func (s *observations) discoverySnapshot(now time.Time) *worldv1.DiscoveryAnchor {
	if _, known := s.clientDiscoveryReference(); !known {
		return nil
	}
	a := s.discovery
	return &worldv1.DiscoveryAnchor{SelfGuid: a.guid, MapId: a.mapID, ServerPositionRevision: a.serverRevision, Sequence: a.sequence, X: a.x, Y: a.y, Z: a.z, Orientation: a.orientation, Estimated: true, AgeMs: age(now, a.at)}
}

func (s *observations) selfDiscoveryDistance(p observed) (float64, bool) {
	ref, known := s.clientDiscoveryReference()
	if !known {
		ref = s.players[0]
	}
	return observedDistance(p, ref)
}

// Membership remains a consumer check. Here a positive relation to any actually
// observed player receives retention priority, without inventing a party.
func (s *observations) attackerPriority(p *observed) int {
	if p == nil || !p.present || !p.attackVictimKnown || !p.known[unitHealth] || p.fields[unitHealth] == 0 {
		return 0
	}
	victim := p.attackVictim
	if victim == s.players[0].guid && s.players[0].present {
		return 3
	}
	if victim != 0 && victim == s.players[1].guid && s.players[1].present {
		return 2
	}
	if v := s.nearbyPlayers[victim]; v != nil && v.present {
		return 1
	}
	return 0
}

func (s *observations) sortDiscoveryCreatures(items []*worldv1.CreatureState, ownerFallback, attackers bool) {
	_, anchorKnown := s.clientDiscoveryReference()
	if !anchorKnown && ownerFallback {
		sortCreatures(items)
		return
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := s.creatures[items[i].Guid], s.creatures[items[j].Guid]
		if attackers {
			pa, pb := s.attackerPriority(a), s.attackerPriority(b)
			if pa != pb {
				return pa > pb
			}
		}
		da, ka := s.selfDiscoveryDistance(*a)
		db, kb := s.selfDiscoveryDistance(*b)
		if ka != kb {
			return ka
		}
		if ka && da != db {
			return da < db
		}
		return a.guid < b.guid
	})
}
