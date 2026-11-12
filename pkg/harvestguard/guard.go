// Package harvestguard provides pure, bounded observation gates shared by
// non-destructive harvest movement and its final profession world read.
package harvestguard

import (
	"math"
	"sort"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
)

func playerGUID(g uint64) bool { return g != 0 && g>>48 == 0 }

func finite(p *worldv1.PlayerState) bool {
	for _, v := range []float32{p.X, p.Y, p.Z} {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || math.Abs(float64(v)) > 40000 {
			return false
		}
	}
	return true
}

// WorldReason consumes actual world observations plus the protected GUID set
// from the SAME exact guarded lease renewal. That set is not party discovery:
// actions obtained its online membership via this body's buddy-party module.
// Stationary health is update-on-change; age alone does not revoke known health.
func WorldReason(w *worldv1.SnapshotResponse, self uint64, protected []uint64) string {
	if w == nil || w.Self == nil || !playerGUID(self) || w.Self.Guid != self || !w.Self.Present || !w.Self.MapKnown || !w.Self.PositionKnown || !finite(w.Self) || !w.Self.AliveKnown || !w.Self.Alive || !w.Self.HealthKnown || w.Self.Health == 0 || w.Self.MaxHealth == 0 || w.Self.Health > w.Self.MaxHealth || !w.Self.UnitFlagsKnown || w.Self.PlayerFlags&0x10 != 0 || w.Self.DynamicFlags&0x20 != 0 {
		return "harvest_own_living_combat_observation_required"
	}
	if !w.Self.MovementFlagsKnown || !movementflags.GroundCompatible(w.Self.MovementFlags, w.Self.ExtraMovementFlags) {
		return "harvest_own_ordinary_ground_movement_required"
	}
	const unsafeSelf = uint32(0x1 | 0x4 | 0x00040000 | 0x00100000 | 0x00400000 | 0x00800000 | 0x01000000 | 0x02000000 | 0x08000000)
	if w.Self.UnitFlags&unsafeSelf != 0 {
		return "harvest_own_safe_unit_state_required"
	}
	if len(protected) == 0 || len(protected) > 42 || len(w.NearbyPlayers) > 64 || len(w.NearbyCreatures) > 32 || len(w.RequestedCreatures) > 32 {
		return "harvest_bounded_party_and_threat_observations_required"
	}
	members := make(map[uint64]bool, len(protected))
	for _, guid := range protected {
		if !playerGUID(guid) || members[guid] {
			return "harvest_protected_guid_proof_invalid"
		}
		members[guid] = true
	}
	if !members[self] {
		return "harvest_protected_guid_proof_missing_self"
	}
	// Fixed owner3 remains a combat priority even outside a current group.
	members[3] = true
	players := map[uint64]*worldv1.PlayerState{self: w.Self}
	if w.Owner != nil && w.Owner.Guid == 3 && w.Owner.Guid != self {
		players[3] = w.Owner
	}
	for _, p := range w.NearbyPlayers {
		if p == nil || !members[p.Guid] || p.Guid == self || p.Guid == 3 {
			continue
		}
		if players[p.Guid] != nil {
			return "harvest_duplicate_protected_player_observation"
		}
		players[p.Guid] = p
	}
	order := make([]uint64, 0, len(players))
	for guid := range players {
		order = append(order, guid)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	for _, guid := range order {
		p := players[guid]
		if p == nil || !p.Present || !p.MapKnown || p.MapId != w.Self.MapId {
			continue
		}
		if !p.UnitFlagsKnown {
			return "harvest_protected_combat_flags_unknown"
		}
		if p.UnitFlags&0x80000 != 0 {
			return "harvest_observed_self_owner_or_party_combat"
		}
		// The ordinary automatic-healing threshold is below85%, not every old
		// scratch. A raw own pose can lag the client movement estimate, so do
		// not use its distance to suppress healing priority. A distant observed
		// injury may conservatively defer this harvest opportunity. Other
		// players must still be alive/selectable with a usable stationary or
		// fresh moving pose; AFK/stale moving actors grant no healing proof.
		const unsafe = uint32(0x1 | 0x4 | 0x00040000 | 0x00100000 | 0x00400000 | 0x00800000 | 0x01000000 | 0x02000000 | 0x08000000)
		if p.HealthKnown && p.MaxHealth > 0 && p.Health > 0 && p.Health <= p.MaxHealth && p.AliveKnown && p.Alive && uint64(p.Health)*100 < uint64(p.MaxHealth)*85 && p.PositionKnown && finite(p) && p.PlayerFlags&(0x10|2) == 0 && p.DynamicFlags&0x20 == 0 && p.UnitFlags&unsafe == 0 {
			if p.Guid == self || p.MovementFlagsKnown && movementflags.GroundCompatible(p.MovementFlags, p.ExtraMovementFlags) && (p.MovementFlags&0xff == 0 || p.PositionAgeMs <= 2000) {
				return "harvest_observed_healing_need_takes_priority"
			}
		}
	}
	for _, list := range [][]*worldv1.CreatureState{w.NearbyCreatures, w.RequestedCreatures, {w.OwnerTarget, w.RequestedCreature}} {
		for _, c := range list {
			if c == nil || !c.Present || !c.MapKnown || c.MapId != w.Self.MapId || !c.AttackVictimKnown || !members[c.AttackVictimGuid] {
				continue
			}
			kind := c.Guid >> 48
			if kind != 0xf130 && kind != 0xf150 || c.Guid&0xffffff == 0 {
				continue
			}
			inCombat := players[c.AttackVictimGuid] != nil && players[c.AttackVictimGuid].UnitFlagsKnown && players[c.AttackVictimGuid].UnitFlags&0x80000 != 0
			if c.AttackAgeMs > 30000 || !inCombat && c.AttackAgeMs > 5000 {
				continue
			}
			if !c.AliveKnown || !c.HealthKnown {
				return "harvest_fresh_attacker_living_state_unknown"
			}
			if c.Alive && c.Health > 0 {
				return "harvest_fresh_actual_attacker_takes_priority"
			}
		}
	}
	return ""
}
