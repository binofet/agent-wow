package buddyactions

import (
	"math"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
)

func roadOwnWorld(w *worldv1.SnapshotResponse, initial bool) bool {
	if safePracticeCharacter(w, 8) != "" {
		return false
	}
	p := w.Self
	if w.MalformedPackets != 0 || w.ObservedPackets == 0 || !p.ClassKnown || p.ClassId != 9 || !p.LevelKnown || p.Level != 5 || p.PositionRevision == 0 || p.MovementFlags&0xff != 0 || !p.HealthKnown || p.MaxHealth != 113 || p.Health == 0 || p.Health > 113 || !p.PowerKnown || p.PowerType != 0 || p.MaxPower != 283 || p.Power > 283 || !p.BaseManaKnown || p.BaseMana != 113 || !p.SpeedsKnown || math.IsNaN(float64(p.RunSpeed)) || math.Abs(float64(p.RunSpeed)-7) > .01 || p.UnitFlags&(0x2000|0x20000|0x200000) != 0 || p.UnitFlags_2&1 != 0 || !p.ChannelSpellKnown || p.ChannelSpellId != 0 || !p.ChannelObjectKnown || p.ChannelObjectGuid != 0 || !p.ShapeshiftFormKnown || p.ShapeshiftForm != 0 {
		return false
	}
	if initial && (p.Health != 113 || p.Power != 283 || p.UnitFlags&0x80000 != 0) {
		return false
	}
	// Road clearing does not interpret any quest counter as1718 credit.
	if !w.SelfQuestLogKnown || w.SelfQuestLogRevision == 0 || len(w.SelfQuestLog) != 25 {
		return false
	}
	var slots [25]bool
	ids := map[uint32]bool{}
	for _, q := range w.SelfQuestLog {
		if q == nil || q.Slot >= 25 || slots[q.Slot] || q.QuestId != 0 && ids[q.QuestId] {
			return false
		}
		slots[q.Slot] = true
		if q.QuestId == 0 {
			if q.State != 0 || q.Complete || q.Failed || len(q.ObjectiveCounts) != 0 && len(q.ObjectiveCounts) != 4 {
				return false
			}
			for _, v := range q.ObjectiveCounts {
				if v != 0 {
					return false
				}
			}
			continue
		}
		ids[q.QuestId] = true
		if len(q.ObjectiveCounts) != 4 || q.Complete != (q.State&1 != 0) || q.Failed != (q.State&2 != 0) {
			return false
		}
		for _, v := range q.ObjectiveCounts {
			if v > 65535 {
				return false
			}
		}
	}
	if !w.SelfSkillsKnown || w.SelfSkillsRevision == 0 || len(w.SelfSkills) != 128 {
		return false
	}
	var skillSlots [128]bool
	skills := map[uint32]bool{}
	for _, v := range w.SelfSkills {
		if v == nil || v.Slot >= 128 || skillSlots[v.Slot] {
			return false
		}
		skillSlots[v.Slot] = true
		if v.SkillId == 0 {
			if v.Value != 0 || v.MaxValue != 0 || v.Step != 0 || v.TemporaryBonus != 0 || v.PermanentBonus != 0 {
				return false
			}
			continue
		}
		if skills[v.SkillId] || v.Value == 0 || v.MaxValue == 0 || v.Value > v.MaxValue || v.MaxValue > 450 {
			return false
		}
		skills[v.SkillId] = true
	}
	return roadInventory(w)
}

func roadInventory(w *worldv1.SnapshotResponse) bool {
	if !w.InventoryKnown || w.InventoryRevision == 0 || len(w.InventoryBags) != 5 {
		return false
	}
	var bags [5]bool
	seen := map[uint64]bool{}
	total := 0
	validGUID := func(g uint64) bool { return g>>48 == 0x4000 && uint32(g) != 0 }
	for _, b := range w.InventoryBags {
		if b == nil || b.Bag > 4 || bags[b.Bag] || !b.SlotKnown || !b.ContentsKnown || b.SlotCount > 36 || len(b.Slots) != int(b.SlotCount) {
			return false
		}
		bags[b.Bag] = true
		if b.Bag == 0 {
			if b.ContainerGuid != 0 || b.Entry != 0 || b.MetadataKnown || b.SlotCount != 16 {
				return false
			}
		} else if b.ContainerGuid == 0 {
			if b.Entry != 0 || b.MetadataKnown || b.SlotCount != 0 {
				return false
			}
		} else {
			if !validGUID(b.ContainerGuid) || seen[b.ContainerGuid] || !b.MetadataKnown || b.Entry == 0 || b.SlotCount == 0 {
				return false
			}
			seen[b.ContainerGuid] = true
		}
		total += len(b.Slots)
		if total > 160 {
			return false
		}
		var slots [36]bool
		for _, v := range b.Slots {
			if v == nil || v.Bag != b.Bag || v.Slot >= b.SlotCount || slots[v.Slot] {
				return false
			}
			slots[v.Slot] = true
			if v.ItemGuid == 0 {
				if v.Entry != 0 || v.StackCount != 0 || v.MetadataKnown {
					return false
				}
				continue
			}
			if !validGUID(v.ItemGuid) || seen[v.ItemGuid] || !v.MetadataKnown || v.Entry == 0 || v.StackCount == 0 {
				return false
			}
			seen[v.ItemGuid] = true
		}
	}
	return true
}

func roadLivingTarget(c *worldv1.CreatureState, initial bool) bool {
	if c == nil || !c.Present || !c.PositionKnown || c.PositionRevision == 0 || !roadArea(playerCreaturePosition(c)) || !c.AliveKnown || !c.Alive || !c.HealthKnown || c.Health == 0 || c.Health > c.MaxHealth || !c.UnitFlagsKnown || c.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) != 0 || c.UnitFlags_2&1 != 0 || !c.DynamicFlagsKnown || c.DynamicFlags&0x20 != 0 || c.NpcFlags != 0 || !c.TargetKnown || c.MovementFlags&^uint32(0x080001ff) != 0 {
		return false
	}
	if t := c.Trajectory; t != nil && t.Known {
		if !t.DestinationKnown || t.Cyclic || t.TransportRelative || t.TransportGuid != 0 || !roadArea(position{x: t.DestinationX, y: t.DestinationY, z: t.DestinationZ}) || initial && !t.Stopped {
			return false
		}
	}
	return !initial || c.Health == c.MaxHealth && c.DynamicFlags == 0 && c.UnitFlags&0x80000 == 0 && c.TargetGuid == 0 && (!c.AttackVictimKnown || c.AttackVictimGuid == 0) && c.MovementFlags&0x080000ff == 0
}

func roadPath(path []position, from, to position) bool {
	if len(path) < 2 || len(path) > 256 || distance(from, path[0]) > 2 || distance(to, path[len(path)-1]) > 2 {
		return false
	}
	length := distance(from, path[0])
	for i, p := range path {
		if !roadArea(p) {
			return false
		}
		if i > 0 {
			step := distance(path[i-1], p)
			if step > 2 || math.Abs(float64(path[i-1].z-p.z)) > 1 {
				return false
			}
			length += step
		}
	}
	return length <= 60
}

func roadPointSegment(p, a, b position) float64 {
	dx, dy, dz := float64(b.x-a.x), float64(b.y-a.y), float64(b.z-a.z)
	den := dx*dx + dy*dy + dz*dz
	if den == 0 {
		return distance(p, a)
	}
	t := max(0, min(1, ((float64(p.x-a.x))*dx+(float64(p.y-a.y))*dy+(float64(p.z-a.z))*dz)/den))
	return math.Sqrt(math.Pow(float64(p.x-a.x)-t*dx, 2) + math.Pow(float64(p.y-a.y)-t*dy, 2) + math.Pow(float64(p.z-a.z)-t*dz, 2))
}

func roadAdds(w *worldv1.SnapshotResponse, target uint64, path []position) bool {
	if w == nil || len(w.NearbyCreatures) > 64 || len(w.RequestedCreatures) >= 32 || len(path) < 2 {
		return false
	}
	all := append([]*worldv1.CreatureState{}, w.NearbyCreatures...)
	observed := map[uint64]*worldv1.CreatureState{}
	targetFound := false
	for _, c := range w.RequestedCreatures {
		if c == nil || c.Guid == 0 || observed[c.Guid] != nil || c.FactionTemplate != 59 || !c.Present || !c.MapKnown || c.MapId != 0 {
			return false
		}
		observed[c.Guid] = c
		if c.Guid == target {
			targetFound = true
			if w.RequestedCreature == nil || !proto.Equal(c, w.RequestedCreature) {
				return false
			}
		}
	}
	if !targetFound {
		return false
	}
	for _, c := range w.NearbyCreatures {
		if c != nil && observed[c.Guid] != nil && !proto.Equal(c, observed[c.Guid]) {
			return false
		}
	}
	for _, c := range w.RequestedCreatures {
		found := false
		for _, n := range w.NearbyCreatures {
			if n != nil && n.Guid == c.Guid {
				found = true
				break
			}
		}
		if !found {
			all = append(all, c)
		}
	}
	seen := map[uint64]bool{}
	for _, c := range all {
		if c == nil || c.Guid == 0 || seen[c.Guid] {
			return false
		}
		seen[c.Guid] = true
		if c.Guid == target || !c.Present {
			continue
		}
		if c.FactionTemplate == 0 {
			return false
		}
		if c.AttackVictimKnown && c.AttackVictimGuid == 8 && c.AttackAgeMs <= 5000 {
			return false
		}
		if c.FactionTemplate != 59 {
			continue
		}
		if !c.AliveKnown || !c.HealthKnown || !c.MapKnown || c.MapId != 0 || !c.PositionKnown || !finite(playerCreaturePosition(c)) || !c.UnitFlagsKnown || !c.DynamicFlagsKnown || !c.TargetKnown || !c.LevelKnown || c.MaxHealth == 0 {
			return false
		}
		if !c.Alive && c.Health == 0 {
			continue
		}
		if !c.Alive || c.Health == 0 || c.Health > c.MaxHealth {
			return false
		}
		points := []position{playerCreaturePosition(c)}
		if t := c.Trajectory; t != nil && t.Known {
			if !t.DestinationKnown || t.Cyclic || t.TransportRelative || t.TransportGuid != 0 || !finite(position{x: t.DestinationX, y: t.DestinationY, z: t.DestinationZ}) {
				return false
			}
			points = append(points, position{x: t.DestinationX, y: t.DestinationY, z: t.DestinationZ})
		}
		for i := 1; i < len(path); i++ {
			for _, p := range points {
				if roadPointSegment(p, path[i-1], path[i]) < 25 {
					return false
				}
			}
			// Sampling this short advertised chord conservatively catches a patrol
			// crossing the verified route; it does not predict a complete spline.
			if len(points) == 2 {
				n := int(math.Ceil(distance(points[0], points[1])))
				if n > 256 {
					return false
				}
				for j := 0; j <= max(1, n); j++ {
					t := float32(j) / float32(max(1, n))
					p := position{x: points[0].x + (points[1].x-points[0].x)*t, y: points[0].y + (points[1].y-points[0].y)*t, z: points[0].z + (points[1].z-points[0].z)*t}
					if roadPointSegment(p, path[i-1], path[i]) < 25.5 {
						return false
					}
				}
			}
		}
	}
	return true
}

func roadTrajectory(t *worldv1.CreatureTrajectory) *worldv1.CreatureTrajectory {
	if t == nil {
		return nil
	}
	c := proto.Clone(t).(*worldv1.CreatureTrajectory)
	c.AgeMs = 0
	return c
}
func roadStaticCreature(c *worldv1.CreatureState) *worldv1.CreatureState {
	n := proto.Clone(c).(*worldv1.CreatureState)
	n.PositionAgeMs, n.ObservationAgeMs, n.AttackAgeMs = 0, 0, 0
	n.DistanceToSelfObserved, n.DistanceToOwnerObserved = 0, 0
	n.DistanceToSelfObservedKnown, n.DistanceToOwnerObservedKnown = false, false
	n.Trajectory = roadTrajectory(c.Trajectory)
	return n
}
func roadHeldAdds(w *worldv1.SnapshotResponse, target uint64) (map[uint64]*worldv1.CreatureState, bool) {
	if w == nil || len(w.RequestedCreatures) >= 32 {
		return nil, false
	}
	out := map[uint64]*worldv1.CreatureState{}
	seen := map[uint64]bool{}
	targetFound := false
	for _, c := range w.RequestedCreatures {
		if c == nil || c.Guid == 0 || c.FactionTemplate != 59 || seen[c.Guid] || !c.Present || !c.MapKnown || c.MapId != 0 {
			return nil, false
		}
		seen[c.Guid] = true
		if c.Guid == target {
			if w.RequestedCreature == nil || !proto.Equal(c, w.RequestedCreature) {
				return nil, false
			}
			targetFound = true
			continue
		}
		if !c.PositionKnown || !finite(playerCreaturePosition(c)) || !c.LevelKnown || !c.HealthKnown || c.MaxHealth == 0 || !c.AliveKnown || !c.UnitFlagsKnown || !c.DynamicFlagsKnown || !c.TargetKnown {
			return nil, false
		}
		// A previous selected corpse may retain its elapsed spline and loot
		// flags. Pin every fact rather than applying live-add admission to it;
		// disappearance, respawn or any changed state still breaks equality.
		if !c.Alive && c.Health == 0 {
			out[c.Guid] = roadStaticCreature(c)
			continue
		}
		if !c.Alive || c.Health == 0 || c.Health > c.MaxHealth || c.TargetGuid != 0 || c.AttackVictimKnown && c.AttackVictimGuid != 0 || c.UnitFlags&0x80000 != 0 || c.DynamicFlags != 0 || c.MovementFlags&0x080000ff != 0 || c.Trajectory != nil && c.Trajectory.Known && !c.Trajectory.Stopped {
			return nil, false
		}
		out[c.Guid] = roadStaticCreature(c)
	}
	return out, targetFound
}
func roadHeldAddsEqual(w *worldv1.SnapshotResponse, target uint64, held map[uint64]*worldv1.CreatureState) bool {
	current, ok := roadHeldAdds(w, target)
	if !ok || len(current) != len(held) {
		return false
	}
	for id, c := range current {
		if held[id] == nil || !proto.Equal(c, held[id]) {
			return false
		}
	}
	return true
}
func roadContactSame(r *roadClearRun, c *worldv1.CreatureState) bool {
	if c == nil || c.PositionRevision != r.contactRevision || distance(playerCreaturePosition(c), r.contactRaw) > .01 || c.MovementFlags != r.contactMovement || !proto.Equal(roadTrajectory(c.Trajectory), r.contactTrajectory) {
		return false
	}
	p, known, _ := creaturePosition(c)
	return known && distance(p, r.contactAim) <= .01
}

// A terminal STOP is a new observed pose, not an update to the retained living
// contact. It is usable only for read-only death polling and exact corpse proof.
func roadTerminalPose(r *roadClearRun, c *worldv1.CreatureState) bool {
	if roadContactSame(r, c) {
		return true
	}
	if c == nil || !c.PositionKnown || c.PositionRevision <= r.contactRevision || c.PositionAgeMs > 5000 || !roadArea(playerCreaturePosition(c)) || distance(playerCreaturePosition(c), r.contactAim) > .25 || c.MovementFlags&0x080000ff != 0 {
		return false
	}
	t := c.Trajectory
	return t != nil && t.Known && t.Stopped && t.DestinationKnown && !t.Cyclic && !t.TransportRelative && t.TransportGuid == 0 && t.AgeMs <= 5000 && t.DurationMs == 0 && distance(playerCreaturePosition(c), position{x: t.DestinationX, y: t.DestinationY, z: t.DestinationZ}) <= .01
}
