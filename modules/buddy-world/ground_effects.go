package buddyworld

import (
	"math"
	"sort"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

// Public Wrath DYNAMICOBJECT fields, relative to OBJECT_END (6).
const (
	groundCaster   = 6
	groundBytes    = 8
	groundSpell    = 9
	groundRadius   = 10
	groundCastTime = 11
	areaSpellType  = 1
)

func (s *observations) putGroundEffect(p *observed) {
	// Type 1 is an area spell. Other dynamic objects include farsight foci.
	if p.fields[groundBytes]&0xff != areaSpellType {
		delete(s.groundEffects, p.guid)
		return
	}
	if s.groundEffects == nil {
		s.groundEffects = make(map[uint64]*observed)
	}
	if s.groundEffects[p.guid] == nil && len(s.groundEffects) >= maxGameObjects {
		s.groundEffectsOverflow = true
		// Retain the closest observed centers and expose the loss of coverage.
		var farthest *observed
		for _, other := range s.groundEffects {
			if farthest == nil || s.groundEffectNearer(farthest, other) {
				farthest = other
			}
		}
		if !s.groundEffectNearer(p, farthest) {
			return
		}
		delete(s.groundEffects, farthest.guid)
	}
	s.groundEffects[p.guid] = p
	s.edited[p.guid] = true
}

func (s *observations) groundEffectNearer(a, b *observed) bool {
	da, ka := s.selfDiscoveryDistance(*a)
	db, kb := s.selfDiscoveryDistance(*b)
	if ka != kb {
		return ka
	}
	if ka && da != db {
		return da < db
	}
	return a.guid < b.guid
}

func (s *observations) appendGroundEffects(r *worldv1.SnapshotResponse) {
	self := &s.players[0]
	if !self.present || !self.mapKnown || s.visitRevision == 0 {
		return
	}
	r.GroundEffectsSupported = true
	r.GroundEffectsOverflow = s.groundEffectsOverflow
	for _, p := range s.groundEffects {
		radius := math.Float32frombits(p.fields[groundRadius])
		if !p.present || !p.mapKnown || p.mapID != self.mapID || !p.positionKnown ||
			p.fields[groundBytes]&0xff != areaSpellType || p.fields[groundSpell] == 0 ||
			!validFloat(radius) || radius <= 0 || p.groundEffectFirstSeen.UnixMilli() < 0 {
			continue
		}
		// Public CREATE omits zero words (including the upper half of a player GUID).
		caster := uint64(p.fields[groundCaster]) | uint64(p.fields[groundCaster+1])<<32
		effect := &worldv1.SpellGroundEffect{
			Guid: p.guid, CasterGuid: caster, SpellId: p.fields[groundSpell], RadiusYards: radius,
			X: p.x, Y: p.y, Z: p.z, FirstObservedAtUnixMs: uint64(p.groundEffectFirstSeen.UnixMilli()),
			PositionRevision: p.positionRevision,
		}
		if p.known[groundCastTime] {
			t := p.fields[groundCastTime]
			effect.ServerCastTimeMs = &t
		}
		r.GroundEffects = append(r.GroundEffects, effect)
	}
	sort.Slice(r.GroundEffects, func(i, j int) bool { return r.GroundEffects[i].Guid < r.GroundEffects[j].Guid })
}
