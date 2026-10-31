package buddyworld

import (
	"sort"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

func gameObjectSnapshot(p *observed, now time.Time) *worldv1.GameObjectState {
	return &worldv1.GameObjectState{Guid: p.guid, Entry: p.fields[objectEntry], Present: p.present, MapKnown: p.mapKnown, MapId: p.mapID, PositionKnown: p.positionKnown, X: p.x, Y: p.y, Z: p.z, Orientation: p.orientation, PositionRevision: p.positionRevision, PositionAgeMs: age(now, p.positionAt), ObservationAgeMs: age(now, p.observedAt), TypeKnown: p.known[gameObjectBytes], GameObjectType: (p.fields[gameObjectBytes] >> 8) & 0xff, FlagsKnown: p.known[gameObjectFlags], Flags: p.fields[gameObjectFlags], DynamicFlagsKnown: p.known[gameObjectDynamic], DynamicFlags: p.fields[gameObjectDynamic], CreatedByKnown: p.known[gameObjectCreatedBy] && p.known[gameObjectCreatedBy+1], CreatedByGuid: uint64(p.fields[gameObjectCreatedBy]) | uint64(p.fields[gameObjectCreatedBy+1])<<32, StateKnown: p.known[gameObjectBytes], State: p.fields[gameObjectBytes] & 0xff}
}

func (s *observations) appendRequested(r *worldv1.SnapshotResponse, now time.Time, creatureEntry, objectEntryID, creatureFaction uint32) {
	if creatureEntry != 0 || creatureFaction != 0 {
		self := s.players[0]
		for _, p := range s.creatures {
			if !p.present || creatureEntry != 0 && (!p.known[objectEntry] || p.fields[objectEntry] != creatureEntry) {
				continue
			}
			// Preserve entry-only lookup behavior. A requested faction must be
			// an actual observation, never a default field value. Unknown NPC
			// maps remain visible uncertainty; only known foreign maps exclude.
			if creatureFaction != 0 && (!self.present || !self.mapKnown || p.mapKnown && p.mapID != self.mapID || !p.known[0x37] || p.fields[0x37] != creatureFaction) {
				continue
			}
			r.RequestedCreatures = append(r.RequestedCreatures, creatureSnapshot(p, s, now))
		}
		sort.Slice(r.RequestedCreatures, func(i, j int) bool {
			a, b := r.RequestedCreatures[i], r.RequestedCreatures[j]
			if a.DistanceToSelfObservedKnown != b.DistanceToSelfObservedKnown {
				return a.DistanceToSelfObservedKnown
			}
			if a.DistanceToSelfObserved != b.DistanceToSelfObserved {
				return a.DistanceToSelfObserved < b.DistanceToSelfObserved
			}
			return a.Guid < b.Guid
		})
		if len(r.RequestedCreatures) > 32 {
			r.RequestedCreatures = r.RequestedCreatures[:32]
		}
	}
	if objectEntryID != 0 {
		for _, p := range s.gameObjects {
			if p.present && p.known[objectEntry] && p.fields[objectEntry] == objectEntryID {
				r.RequestedGameObjects = append(r.RequestedGameObjects, gameObjectSnapshot(p, now))
			}
		}
		sort.Slice(r.RequestedGameObjects, func(i, j int) bool {
			a, b := s.gameObjects[r.RequestedGameObjects[i].Guid], s.gameObjects[r.RequestedGameObjects[j].Guid]
			da, ka := observedDistance(*a, s.players[0])
			db, kb := observedDistance(*b, s.players[0])
			if ka != kb {
				return ka
			}
			if ka && da != db {
				return da < db
			}
			return a.guid < b.guid
		})
		if len(r.RequestedGameObjects) > 32 {
			r.RequestedGameObjects = r.RequestedGameObjects[:32]
		}
	}
}
