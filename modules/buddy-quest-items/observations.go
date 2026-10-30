package buddyquestitems

import (
	"errors"
	"math"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	qv1 "github.com/agent-wow/agent-wow/modules/buddy-quest-items/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

type view struct {
	world       *worldv1.SnapshotResponse
	actions     *actionsv1.State
	quest       *worldv1.QuestLogEntry
	items       map[uint64]*qv1.ItemBinding
	bucket      *qv1.ItemBinding
	count, free uint32
}

func ownView(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64) (view, error) {
	v := view{world: w, actions: a, items: map[uint64]*qv1.ItemBinding{}}
	bad := errors.New("complete actual own quest log and carried inventory required")
	if self == 0 || self>>48 != 0 || w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.Present || a == nil {
		return v, bad
	}
	if !w.SelfQuestLogKnown || w.SelfQuestLogRevision == 0 || len(w.SelfQuestLog) != 25 || !w.InventoryKnown || w.InventoryRevision == 0 || len(w.InventoryBags) != 5 {
		return v, bad
	}
	if len(w.RequestedGameObjects) > 32 || len(w.RequestedCreatures) > 32 {
		return v, bad
	}
	objects := map[uint64]bool{}
	for _, g := range w.RequestedGameObjects {
		if g == nil || objects[g.Guid] {
			return v, bad
		}
		objects[g.Guid] = true
	}
	for _, c := range w.RequestedCreatures {
		if c == nil || objects[c.Guid] {
			return v, bad
		}
		objects[c.Guid] = true
	}
	var slots [25]bool
	ids := map[uint32]bool{}
	for _, q := range w.SelfQuestLog {
		if q == nil || q.Slot >= 25 || slots[q.Slot] || len(q.ObjectiveCounts) != 4 {
			return v, bad
		}
		slots[q.Slot] = true
		if q.QuestId == 0 {
			continue
		}
		if ids[q.QuestId] {
			return v, bad
		}
		ids[q.QuestId] = true
		if q.QuestId == questID {
			v.quest = q
			v.count = q.ObjectiveCounts[0]
			if v.count > requiredCount {
				return v, bad
			}
		}
	}
	var bags [5]bool
	containers := map[uint64]bool{}
	for _, b := range w.InventoryBags {
		if b == nil || b.Bag > 4 || bags[b.Bag] || !b.SlotKnown {
			return v, bad
		}
		bags[b.Bag] = true
		if b.Bag > 0 && b.ContainerGuid == 0 {
			if b.Entry != 0 || b.SlotCount != 0 || len(b.Slots) != 0 {
				return v, bad
			}
			continue
		}
		if b.Bag == 0 {
			if b.SlotCount != 16 {
				return v, bad
			}
		} else {
			if b.ContainerGuid>>48 != 0x4000 || b.ContainerGuid&0xffffffffffff == 0 || !b.MetadataKnown || b.Entry == 0 || b.SlotCount == 0 || b.SlotCount > 36 || containers[b.ContainerGuid] {
				return v, bad
			}
			containers[b.ContainerGuid] = true
		}
		if !b.ContentsKnown || uint32(len(b.Slots)) != b.SlotCount {
			return v, bad
		}
		seen := map[uint32]bool{}
		for _, i := range b.Slots {
			if i == nil || i.Bag != b.Bag || i.Slot >= b.SlotCount || seen[i.Slot] {
				return v, bad
			}
			seen[i.Slot] = true
			if i.ItemGuid == 0 {
				if i.Entry != 0 || i.StackCount != 0 || i.MetadataKnown {
					return v, bad
				}
				v.free++
				continue
			}
			if i.ItemGuid>>48 != 0x4000 || i.ItemGuid&0xffffffffffff == 0 || !i.MetadataKnown || i.Entry == 0 || i.StackCount == 0 || v.items[i.ItemGuid] != nil {
				return v, bad
			}
			bind := &qv1.ItemBinding{Bag: i.Bag, Slot: i.Slot, ItemGuid: i.ItemGuid, Entry: i.Entry, Count: i.StackCount}
			v.items[i.ItemGuid] = bind
			if i.Entry == bucketEntry {
				if v.bucket != nil || i.StackCount != 1 {
					return v, bad
				}
				v.bucket = bind
			}
		}
	}
	return v, nil
}
func safe(v view) string {
	if v.world == nil || v.world.Self == nil || v.actions == nil {
		return "observations_unavailable"
	}
	p, a := v.world.Self, v.actions
	pos := a.Position
	if !p.Present || !p.AliveKnown || !p.Alive || !p.HealthKnown || p.Health == 0 || p.MaxHealth == 0 || p.Health > p.MaxHealth {
		return "own_living_health_unknown"
	}
	if !p.MapKnown || !p.PositionKnown || !finite(p.X) || !finite(p.Y) || !finite(p.Z) || pos == nil || !pos.Known || !pos.MapKnown || pos.MapId != p.MapId || !finite(pos.X) || !finite(pos.Y) || !finite(pos.Z) {
		return "own_current_map_or_position_unknown"
	}
	if !p.UnitFlagsKnown || p.UnitFlags&(1|4|0x40000|0x80000|0x100000|0x400000|0x800000|0x1000000|0x2000000|0x8000000) != 0 || p.DynamicFlags&0x20 != 0 {
		return "own_combat_or_unsafe_state"
	}
	if !p.MovementFlagsKnown || p.MovementFlags&^uint32(0x1ff) != 0 || p.ExtraMovementFlags != 0 {
		return "own_unsupported_movement_state"
	}
	if a.MotionUnconfirmed || a.CastPending || a.AutoAttacking || a.AutoAttackRequested || a.AutoAttackUnconfirmed || a.Traveling || a.Practicing {
		return "own_action_busy_or_unconfirmed"
	}
	if p.ChannelSpellKnown && p.ChannelSpellId != 0 {
		return "own_channel_active"
	}
	return ""
}
func here(v view) point { p := v.actions.Position; return point{p.X, p.Y, p.Z} }
func barrel(v view, guid uint64) *worldv1.GameObjectState {
	if len(v.world.RequestedGameObjects) > 32 {
		return nil
	}
	for _, g := range v.world.RequestedGameObjects {
		if g == nil || g.Guid != guid {
			continue
		}
		if g.Guid>>48 != 0xf110 || g.Guid&0xffffff == 0 || uint32(g.Guid>>24&0xffffff) != barrelEntry || g.Entry != barrelEntry || !g.Present || !g.TypeKnown || g.GameObjectType != 10 || !g.MapKnown || g.MapId != v.world.Self.MapId || !g.PositionKnown || !finite(g.X) || !finite(g.Y) || !finite(g.Z) || !g.FlagsKnown || g.Flags&0x13 != 0 {
			return nil
		}
		return g
	}
	return nil
}
func fire(v view, guid uint64) *worldv1.CreatureState {
	if len(v.world.RequestedCreatures) > 32 {
		return nil
	}
	for _, c := range v.world.RequestedCreatures {
		if c == nil || c.Guid != guid {
			continue
		}
		if c.Guid>>48 != 0xf130 || c.Guid&0xffffff == 0 || uint32(c.Guid>>24&0xffffff) != fireEntry || c.Entry != fireEntry || !c.Present || !c.MapKnown || c.MapId != v.world.Self.MapId || !c.PositionKnown || !finite(c.X) || !finite(c.Y) || !finite(c.Z) || !c.AliveKnown || !c.Alive || !c.HealthKnown || c.Health == 0 || c.MovementFlags&0x080000ff != 0 || c.Trajectory != nil && c.Trajectory.Known && (!c.Trajectory.Stopped || c.Trajectory.TransportRelative) {
			return nil
		}
		return c
	}
	return nil
}
func samePosition(a, b *actionsv1.ClientPosition) bool {
	return a != nil && b != nil && a.MapKnown && b.MapKnown && a.MapId == b.MapId && dist(point{a.X, a.Y, a.Z}, point{b.X, b.Y, b.Z}) <= .05
}
func rangeOK(v view, p point, max float64) bool {
	return dist(here(v), p) <= max && math.Abs(float64(here(v).z-p.z)) <= max
}
