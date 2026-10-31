package buddyworld

import (
	"sort"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

const maxNearbyPlayers = 64

func publicPlayerGUID(guid uint64) bool {
	return guid != 0 && guid>>48 == 0 && uint32(guid) != 0
}

// Only the public fields needed for nearby pose/vitals triage are retained.
// Build12340 UpdateFields.h marks these PUBLIC; private own data stays self-only.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Object/Updates/UpdateFields.h
func publicPlayerField(field int) bool {
	return field == unitBytes0 || field == unitHealth || field == unitMaxHealth ||
		field >= unitPower1 && field < unitPower1+7 || field >= unitMaxPower1 && field < unitMaxPower1+7 ||
		field == unitFlags || field == unitDynamicFlags || field == playerFlags || field == 0x3c || field == 0x36
}

// Only a complete TYPEID_PLAYER CREATE admits a new public player. Movement or
// power packets can update an admitted record but cannot invent one.
func (s *observations) putNearbyPlayer(p *observed) {
	if p == nil || !publicPlayerGUID(p.guid) || player(&s.players, p.guid) != nil {
		return
	}
	if s.nearbyPlayers == nil {
		s.nearbyPlayers = make(map[uint64]*observed)
	}
	if s.nearbyPlayers[p.guid] == nil && len(s.nearbyPlayers) >= maxNearbyPlayers {
		// Retain nearest actual self observations; ties prefer recent evidence,
		// then the lower GUID. Party membership remains the consumer's gate.
		worse := func(a, b *observed) bool {
			da, ka := s.selfDiscoveryDistance(*a)
			db, kb := s.selfDiscoveryDistance(*b)
			if ka != kb {
				return !ka
			}
			if ka && da != db {
				return da > db
			}
			if !a.observedAt.Equal(b.observedAt) {
				return a.observedAt.Before(b.observedAt)
			}
			return a.guid > b.guid
		}
		var worst *observed
		for _, candidate := range s.nearbyPlayers {
			if worst == nil || worse(candidate, worst) {
				worst = candidate
			}
		}
		if worse(p, worst) {
			return
		}
		delete(s.nearbyPlayers, worst.guid)
	}
	s.nearbyPlayers[p.guid] = p
	s.edited[p.guid] = true
}

// Deliberately construct an allowlisted public view, rather than returning the
// full PlayerState as it gains more private own-character fields over time.
func nearbyPlayerSnapshot(p observed, now time.Time) *worldv1.PlayerState {
	v := snapshot(p, now)
	return &worldv1.PlayerState{
		Guid: v.Guid, Present: v.Present, MapKnown: v.MapKnown, MapId: v.MapId,
		PositionKnown: v.PositionKnown, X: v.X, Y: v.Y, Z: v.Z, Orientation: v.Orientation,
		PositionAgeMs: v.PositionAgeMs, PositionRevision: v.PositionRevision,
		MovementFlagsKnown: v.MovementFlagsKnown, MovementFlags: v.MovementFlags,
		ExtraMovementFlags: v.ExtraMovementFlags, MovementTime: v.MovementTime,
		HealthKnown: v.HealthKnown, Health: v.Health, MaxHealth: v.MaxHealth,
		PowerKnown: v.PowerKnown, Power: v.Power, MaxPower: v.MaxPower, PowerType: v.PowerType,
		UnitFlagsKnown: v.UnitFlagsKnown, UnitFlags: v.UnitFlags, UnitFlags_2: v.UnitFlags_2,
		PlayerFlags: v.PlayerFlags, DynamicFlags: v.DynamicFlags,
		AliveKnown: v.AliveKnown, Alive: v.Alive, VitalsAgeMs: v.VitalsAgeMs,
		ClassKnown: v.ClassKnown, ClassId: v.ClassId, LevelKnown: v.LevelKnown, Level: v.Level,
		RaceKnown: v.RaceKnown, RaceId: v.RaceId,
		AuraStateKnown: v.AuraStateKnown, AuraState: v.AuraState,
		ObservationAgeMs: v.ObservationAgeMs,
	}
}

func (s *observations) appendNearbyPlayers(r *worldv1.SnapshotResponse, now time.Time) {
	players := make([]*observed, 0, len(s.nearbyPlayers))
	for _, p := range s.nearbyPlayers {
		if p.present && player(&s.players, p.guid) == nil {
			players = append(players, p)
		}
	}
	sort.Slice(players, func(i, j int) bool {
		a, ka := s.selfDiscoveryDistance(*players[i])
		b, kb := s.selfDiscoveryDistance(*players[j])
		if ka != kb {
			return ka
		}
		if ka && a != b {
			return a < b
		}
		return players[i].guid < players[j].guid
	})
	for i, p := range players {
		if i >= maxNearbyPlayers {
			break
		}
		r.NearbyPlayers = append(r.NearbyPlayers, nearbyPlayerSnapshot(*p, now))
	}
}
