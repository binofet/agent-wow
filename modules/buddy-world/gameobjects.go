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
const gameObjectCreatedBy = 6
const unitChannelObject = 0x14
const unitChannelSpell = 0x16
const unitShapeshiftBytes = 0x7a // OBJECT_END6 + UNIT_FIELD_BYTES_2 0x74, form in byte3.
const maxGameObjects = 128

func gameObjectGUID(guid uint64) bool { return guid>>48 == 0xf110 && guid&0xffffff != 0 }
func mailbox(p *observed) bool {
	return p.known[gameObjectBytes] && (p.fields[gameObjectBytes]>>8)&0xff == 19
}
func harvestCrate(p *observed) bool {
	return p.known[objectEntry] && p.fields[objectEntry] == 161557 && p.known[gameObjectBytes] && (p.fields[gameObjectBytes]>>8)&0xff == 3
}
func spellFocus(p *observed) bool {
	return p.known[gameObjectBytes] && (p.fields[gameObjectBytes]>>8)&0xff == 8
}
func fishingBobber(p *observed) bool {
	return p.guid>>24&0xffffff == 35591 && p.known[objectEntry] && p.fields[objectEntry] == 35591 && p.known[gameObjectBytes] && (p.fields[gameObjectBytes]>>8)&0xff == 17
}
func ownFishingBobber(p *observed, self uint64) bool {
	return self != 0 && fishingBobber(p) && p.known[gameObjectCreatedBy] && p.known[gameObjectCreatedBy+1] && uint64(p.fields[gameObjectCreatedBy])|uint64(p.fields[gameObjectCreatedBy+1])<<32 == self
}
func importantGameObject(p *observed) bool {
	return mailbox(p) || harvestCrate(p) || spellFocus(p) || fishingBobber(p)
}
func gameObjectPriority(p *observed, self uint64) int {
	if ownFishingBobber(p, self) {
		return 3
	}
	if mailbox(p) || harvestCrate(p) || spellFocus(p) {
		return 2
	}
	if fishingBobber(p) {
		return 1
	}
	return 0
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
			if gameObjectPriority(v, s.players[0].guid) != gameObjectPriority(worst, s.players[0].guid) {
				if gameObjectPriority(v, s.players[0].guid) < gameObjectPriority(worst, s.players[0].guid) {
					worst = v
				}
				continue
			}
			if v.observedAt.Before(worst.observedAt) || v.observedAt.Equal(worst.observedAt) && v.guid < worst.guid {
				worst = v
			}
		}
		if gameObjectPriority(p, s.players[0].guid) < gameObjectPriority(worst, s.players[0].guid) {
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
		v := gameObjectSnapshot(p, now)
		if mailbox(p) {
			r.Mailboxes = append(r.Mailboxes, v)
		} else if harvestCrate(p) {
			r.QuestObjects = append(r.QuestObjects, v)
		} else if spellFocus(p) {
			r.SpellFocuses = append(r.SpellFocuses, v)
		} else if ownFishingBobber(p, s.players[0].guid) {
			r.OwnFishingBobbers = append(r.OwnFishingBobbers, v)
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
	sortObjects(r.SpellFocuses)
	sortObjects(r.OwnFishingBobbers)
	if len(r.Mailboxes) > 32 {
		r.Mailboxes = r.Mailboxes[:32]
	}
	if len(r.QuestObjects) > 32 {
		r.QuestObjects = r.QuestObjects[:32]
	}
	if len(r.SpellFocuses) > 32 {
		r.SpellFocuses = r.SpellFocuses[:32]
	}
	if len(r.OwnFishingBobbers) > 4 {
		r.OwnFishingBobbers = r.OwnFishingBobbers[:4]
	}
}

func clearCastingContext(p *observed) {
	for _, f := range []int{unitChannelObject, unitChannelObject + 1, unitChannelSpell, unitShapeshiftBytes} {
		p.known[f], p.fields[f] = false, 0
	}
}
