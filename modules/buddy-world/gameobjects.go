package buddyworld

import (
	"sort"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

// Primary UpdateFields.h: OBJECT_END6 + GAMEOBJECT_BYTES_1 offset0xb.
// GameObject::GetGoType reads byte1, and SharedDefines MAILBOX is type19.
const gameObjectBytes = 0x11
const maxGameObjects = 128

func gameObjectGUID(guid uint64) bool { return guid>>48 == 0xf110 && guid&0xffffff != 0 }
func mailbox(p *observed) bool {
	return p.known[gameObjectBytes] && (p.fields[gameObjectBytes]>>8)&0xff == 19
}

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
			if mailbox(v) != mailbox(worst) {
				if !mailbox(v) {
					worst = v
				}
				continue
			}
			if v.observedAt.Before(worst.observedAt) || v.observedAt.Equal(worst.observedAt) && v.guid < worst.guid {
				worst = v
			}
		}
		if !mailbox(p) && mailbox(worst) {
			return
		}
		delete(s.gameObjects, worst.guid)
	}
	s.gameObjects[p.guid] = p
	s.edited[p.guid] = true
}
func (s *observations) appendMailboxes(r *worldv1.SnapshotResponse, now time.Time) {
	for _, p := range s.gameObjects {
		if !p.present || !p.known[objectEntry] || p.fields[objectEntry] == 0 || !mailbox(p) {
			continue
		}
		r.Mailboxes = append(r.Mailboxes, &worldv1.GameObjectState{Guid: p.guid, Entry: p.fields[objectEntry], Present: p.present, MapKnown: p.mapKnown, MapId: p.mapID, PositionKnown: p.positionKnown, X: p.x, Y: p.y, Z: p.z, Orientation: p.orientation, PositionRevision: p.positionRevision, PositionAgeMs: age(now, p.positionAt), ObservationAgeMs: age(now, p.observedAt), TypeKnown: true, GameObjectType: 19})
	}
	sort.Slice(r.Mailboxes, func(i, j int) bool {
		a, b := s.gameObjects[r.Mailboxes[i].Guid], s.gameObjects[r.Mailboxes[j].Guid]
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
	if len(r.Mailboxes) > 32 {
		r.Mailboxes = r.Mailboxes[:32]
	}
}
