package buddyworld

import (
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

// Build12340 UpdateFields.h + Corpse.h: OWNER6/7, FLAGS6+0x1b,
// DYNAMIC_FLAGS6+0x1c, CORPSE_FLAG_BONES1. Corpse::Create sets actual owner
// GUID and stationary position; only a TYPEID_CORPSE7 CREATE admits a record.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Corpse/Corpse.cpp
const (
	corpseOwner   = 6
	corpseFlags   = 0x21
	corpseDynamic = 0x22
	maxCorpses    = 32
)

func corpseGUID(guid uint64) bool { return guid>>48 == 0xf101 && uint32(guid) != 0 }

func ownReclaimableCorpse(p *observed, self uint64) bool {
	return self != 0 && p != nil && p.present && corpseGUID(p.guid) && p.known[corpseOwner] && p.known[corpseOwner+1] &&
		uint64(p.fields[corpseOwner])|uint64(p.fields[corpseOwner+1])<<32 == self &&
		p.known[corpseFlags] && p.fields[corpseFlags]&1 == 0
}

func (s *observations) putCorpse(p *observed) {
	if s.corpses == nil {
		s.corpses = make(map[uint64]*observed)
	}
	if s.corpses[p.guid] == nil && len(s.corpses) >= maxCorpses {
		var oldest *observed
		for _, v := range s.corpses {
			if ownReclaimableCorpse(v, s.players[0].guid) {
				continue
			}
			if oldest == nil || v.observedAt.Before(oldest.observedAt) || v.observedAt.Equal(oldest.observedAt) && v.guid < oldest.guid {
				oldest = v
			}
		}
		if oldest == nil {
			return
		}
		delete(s.corpses, oldest.guid)
	}
	s.corpses[p.guid] = p
	s.edited[p.guid] = true
}

func (s *observations) ownCorpseSnapshot(now time.Time) *worldv1.CorpseState {
	self := s.players[0]
	if !self.isSelf || !self.present || !self.mapKnown {
		return nil
	}
	var found *observed
	for _, p := range s.corpses {
		if !ownReclaimableCorpse(p, self.guid) || !p.mapKnown || p.mapID != self.mapID {
			continue
		}
		// Old bones are excluded using actual flags; two remaining own bodies
		// are ambiguous, and cannot authorize an arbitrary reclaim GUID.
		if found != nil {
			return nil
		}
		found = p
	}
	if found == nil {
		return nil
	}
	p := found
	return &worldv1.CorpseState{Guid: p.guid, OwnerGuid: self.guid, Present: p.present, MapKnown: p.mapKnown, MapId: p.mapID,
		PositionKnown: p.positionKnown, X: p.x, Y: p.y, Z: p.z, Orientation: p.orientation, PositionRevision: p.positionRevision,
		PositionAgeMs: age(now, p.positionAt), ObservationAgeMs: age(now, p.observedAt), FlagsKnown: p.known[corpseFlags], Flags: p.fields[corpseFlags], DynamicFlagsKnown: p.known[corpseDynamic], DynamicFlags: p.fields[corpseDynamic]}
}
