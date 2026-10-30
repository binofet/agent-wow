package buddyworld

import (
	"sort"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

// Primary UpdateFields.h: OBJECT_END6 + GAMEOBJECT_BYTES_1 offset0xb.
// GameObject::GetGoType reads byte1, and SharedDefines MAILBOX is type19.
const gameObjectBytes = 0x11
const gameObjectFlags = 0x9
const gameObjectDynamic = 0xe
const maxGameObjects = 128

func gameObjectGUID(guid uint64) bool { return guid>>48 == 0xf110 && guid&0xffffff != 0 }
func mailbox(p *observed) bool {
	return p.known[gameObjectBytes] && (p.fields[gameObjectBytes]>>8)&0xff == 19
}
func harvestCrate(p *observed) bool {
	return p.known[objectEntry] && p.fields[objectEntry] == 161557 && p.known[gameObjectBytes] && (p.fields[gameObjectBytes]>>8)&0xff == 3
}
func importantGameObject(p *observed) bool { return mailbox(p) || harvestCrate(p) }

func (s *observations) putGameObject(p *observed) {
	if s.gameObjects == nil {
		s.gameObjects = make(map[uint64]*observed)
	}
	if s.gameObjects[p.guid] == nil && len(s.gameObjects) >= maxGameObjects {
		var worst *observed
		for _, v := range s.gameObjects {
			if worst == nil {
				worst = v
				continue
			}
			if importantGameObject(v) != importantGameObject(worst) {
				if !importantGameObject(v) {
					worst = v
				}
				continue
			}
			if v.observedAt.Before(worst.observedAt) || v.observedAt.Equal(worst.observedAt) && v.guid < worst.guid {
				worst = v
			}
		}
		if !importantGameObject(p) && importantGameObject(worst) {
			return
		}
		delete(s.gameObjects, worst.guid)
	}
	s.gameObjects[p.guid] = p
	s.edited[p.guid] = true
}
func (s *observations) appendGameObjects(r *worldv1.SnapshotResponse, now time.Time) {
	for _, p := range s.gameObjects {
		if !p.present || !p.known[objectEntry] || p.fields[objectEntry] == 0 || !importantGameObject(p) {
			continue
		}
		v := &worldv1.GameObjectState{Guid: p.guid, Entry: p.fields[objectEntry], Present: p.present, MapKnown: p.mapKnown, MapId: p.mapID, PositionKnown: p.positionKnown, X: p.x, Y: p.y, Z: p.z, Orientation: p.orientation, PositionRevision: p.positionRevision, PositionAgeMs: age(now, p.positionAt), ObservationAgeMs: age(now, p.observedAt), TypeKnown: true, GameObjectType: (p.fields[gameObjectBytes] >> 8) & 0xff, FlagsKnown: p.known[gameObjectFlags], Flags: p.fields[gameObjectFlags], DynamicFlagsKnown: p.known[gameObjectDynamic], DynamicFlags: p.fields[gameObjectDynamic]}
		if mailbox(p) {
			r.Mailboxes = append(r.Mailboxes, v)
		} else {
			r.QuestObjects = append(r.QuestObjects, v)
		}
	}
	sortObjects := func(objects []*worldv1.GameObjectState) {
		sort.Slice(objects, func(i, j int) bool {
			a, b := s.gameObjects[objects[i].Guid], s.gameObjects[objects[j].Guid]
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
	}
	sortObjects(r.Mailboxes)
	sortObjects(r.QuestObjects)
	if len(r.Mailboxes) > 32 {
		r.Mailboxes = r.Mailboxes[:32]
	}
	if len(r.QuestObjects) > 32 {
		r.QuestObjects = r.QuestObjects[:32]
	}
}
