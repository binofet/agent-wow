package buddyworld

import (
	"maps"
	"math"
	"sort"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

const (
	maxCreatures       = 128
	maxItems           = 256
	maxCreatureResults = 64
	objectEntry        = 3
	unitNPCFlags       = 0x52 // OBJECT_END6 + UNIT_NPC_FLAGS0x4c
	questLogFirst      = 0x9e
	backpackFirst      = 0x172
	itemOwner          = 6
	itemStack          = 0xe
	playerCoinage      = 0x492 // UNIT_END0x94 + PLAYER_FIELD_COINAGE0x3fe, PRIVATE own-only.
	playerSkillsFirst  = 0x27c // UNIT_END0x94 + PLAYER_SKILL_INFO_1_1 0x1e8, 384 private words.
	playerFreePrimary  = 0x3fd // UNIT_END0x94 + PLAYER_CHARACTER_POINTS2 0x369.
	playerOffhand      = 0x164 // PLAYER_FIELD_INV_SLOT_HEAD0x144 + equipment slot16 *2.
)

// Maps are shallow-cloned per packet. target copies each touched object once,
// so malformed packet rollback never mutates previously committed observations.
type observations struct {
	players             [2]observed
	creatures           map[uint64]*observed
	items               map[uint64]*observed
	gameObjects         map[uint64]*observed
	edited              map[uint64]bool
	itemRevision        uint64
	copperRevision      uint64
	skillsRevision      uint64
	freePrimaryRevision uint64
	offhandRevision     uint64
	inventoryRevision   uint64
}

func (s observations) clone() observations {
	s.creatures = maps.Clone(s.creatures)
	s.items = maps.Clone(s.items)
	s.gameObjects = maps.Clone(s.gameObjects)
	s.edited = make(map[uint64]bool)
	return s
}
func (s *observations) target(guid uint64) *observed {
	if p := player(&s.players, guid); p != nil {
		return p
	}
	objects := s.creatures
	if objects[guid] == nil {
		objects = s.items
	}
	if objects[guid] == nil {
		objects = s.gameObjects
	}
	p := objects[guid]
	if p == nil {
		return nil
	}
	if !s.edited[guid] {
		copy := *p
		p = &copy
		objects[guid] = p
		s.edited[guid] = true
	}
	return p
}
func (s *observations) remove(guid uint64, now time.Time) {
	if p := player(&s.players, guid); p != nil {
		markAbsent(p, now)
	}
	delete(s.creatures, guid)
	delete(s.gameObjects, guid)
	if s.items[guid] != nil {
		delete(s.items, guid)
		s.itemRevision++
	}
}
func creatureGUID(guid uint64) bool { return guid>>48 == 0xf130 && guid&0xffffff != 0 }
func itemGUID(guid uint64) bool     { return guid>>48 == 0x4000 && uint32(guid) != 0 }
func (s *observations) reference() observed {
	p := s.players[1]
	if p.present && p.mapKnown && p.positionKnown {
		return p
	}
	return s.players[0]
}
func observedDistance(a, b observed) (float64, bool) {
	if !a.present || !b.present || !a.mapKnown || !b.mapKnown || a.mapID != b.mapID || !a.positionKnown || !b.positionKnown {
		return 0, false
	}
	dx, dy, dz := float64(a.x-b.x), float64(a.y-b.y), float64(a.z-b.z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz), true
}
func interesting(p *observed) bool {
	return p.fields[unitNPCFlags]&2 != 0 || p.fields[unitDynamicFlags]&1 != 0 || p.attackVictimKnown
}
func (s *observations) putCreature(p *observed) {
	if s.creatures[p.guid] == nil && len(s.creatures) >= maxCreatures {
		var worst *observed
		ref := s.reference()
		for _, candidate := range s.creatures {
			if worst == nil {
				worst = candidate
				continue
			}
			if interesting(candidate) != interesting(worst) {
				if !interesting(candidate) {
					worst = candidate
				}
				continue
			}
			d, known := observedDistance(*candidate, ref)
			old, oldKnown := observedDistance(*worst, ref)
			if known && oldKnown && d != old {
				if d > old {
					worst = candidate
				}
				continue
			}
			if candidate.observedAt.Before(worst.observedAt) || candidate.observedAt.Equal(worst.observedAt) && candidate.guid < worst.guid {
				worst = candidate
			}
		}
		if !interesting(p) && interesting(worst) {
			return
		}
		if interesting(p) == interesting(worst) {
			d, k := observedDistance(*p, ref)
			old, ok := observedDistance(*worst, ref)
			if k && ok && d > old {
				return
			}
		}
		delete(s.creatures, worst.guid)
	}
	s.creatures[p.guid] = p
	s.edited[p.guid] = true
}
func (s *observations) putItem(p *observed) {
	if p.fields[itemOwner]|p.fields[itemOwner+1] == 0 {
		return
	}
	owner := uint64(p.fields[itemOwner]) | uint64(p.fields[itemOwner+1])<<32
	if owner != s.players[0].guid {
		return
	}
	if s.items[p.guid] == nil && len(s.items) >= maxItems {
		// Keep actual carried bag references, contents and equipment.
		var oldest *observed
		for _, candidate := range s.items {
			if s.protectedItem(candidate.guid) {
				continue
			}
			if oldest == nil || candidate.observedAt.Before(oldest.observedAt) || candidate.observedAt.Equal(oldest.observedAt) && candidate.guid < oldest.guid {
				oldest = candidate
			}
		}
		if oldest == nil {
			return
		}
		delete(s.items, oldest.guid)
	}
	s.items[p.guid] = p
	s.edited[p.guid] = true
	s.itemRevision++
}

func creatureSnapshot(p *observed, s *observations, now time.Time) *worldv1.CreatureState {
	healthKnown := p.known[unitHealth] && p.known[unitMaxHealth] && p.fields[unitMaxHealth] > 0 && p.fields[unitHealth] <= p.fields[unitMaxHealth]
	r := &worldv1.CreatureState{Guid: p.guid, Entry: p.fields[objectEntry], NpcFlags: p.fields[unitNPCFlags], Present: p.present, MapKnown: p.mapKnown, MapId: p.mapID, PositionKnown: p.positionKnown, X: p.x, Y: p.y, Z: p.z, Orientation: p.orientation, PositionAgeMs: age(now, p.positionAt), PositionRevision: p.positionRevision, ObservationAgeMs: age(now, p.observedAt), HealthKnown: healthKnown, Health: p.fields[unitHealth], MaxHealth: p.fields[unitMaxHealth], AliveKnown: healthKnown, Alive: healthKnown && p.fields[unitHealth] > 0 && p.fields[unitDynamicFlags]&0x20 == 0, UnitFlags: p.fields[unitFlags], DynamicFlags: p.fields[unitDynamicFlags], MovementFlags: p.movementFlags}
	r.FactionTemplate = p.fields[0x37]
	r.UnitFlagsKnown = p.known[unitFlags]
	r.DynamicFlagsKnown = p.known[unitDynamicFlags]
	r.UnitFlags_2 = p.fields[0x3c]
	r.TargetKnown = p.known[0x12] && p.known[0x13]
	r.TargetGuid = uint64(p.fields[0x12]) | uint64(p.fields[0x13])<<32
	r.AttackVictimKnown = p.attackVictimKnown
	r.AttackVictimGuid = p.attackVictim
	r.AttackAgeMs = age(now, p.attackAt)
	r.CombatReach = math.Float32frombits(p.fields[0x42])
	r.CombatReachKnown = p.known[0x42] && validFloat(r.CombatReach) && r.CombatReach >= 0 && r.CombatReach <= 50
	r.Level = p.fields[0x36]
	r.LevelKnown = p.known[0x36] && r.Level > 0 && r.Level <= 80
	if t := p.trajectory; t.known {
		r.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: t.duration, AgeMs: uint64(t.elapsed) + age(now, t.at), DestinationKnown: t.destinationKnown, DestinationX: t.destination[0], DestinationY: t.destination[1], DestinationZ: t.destination[2], Cyclic: t.cyclic, TransportRelative: t.relative, TransportGuid: t.transport, TransportSeat: int32(t.seat), Stopped: t.stopped, Flags: t.flags}
	}
	d, k := observedDistance(*p, s.players[0])
	r.DistanceToSelfObservedKnown = k
	r.DistanceToSelfObserved = float32(d)
	d, k = observedDistance(*p, s.players[1])
	r.DistanceToOwnerObservedKnown = k
	r.DistanceToOwnerObserved = float32(d)
	return r
}
func sortCreatures(items []*worldv1.CreatureState) {
	rank := func(p *worldv1.CreatureState) int {
		if p.DistanceToOwnerObservedKnown {
			return 0
		}
		if p.DistanceToSelfObservedKnown {
			return 1
		}
		return 2
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		ra, rb := rank(a), rank(b)
		if ra != rb {
			return ra < rb
		}
		if ra == 0 && a.DistanceToOwnerObserved != b.DistanceToOwnerObserved {
			return a.DistanceToOwnerObserved < b.DistanceToOwnerObserved
		}
		if ra == 1 && a.DistanceToSelfObserved != b.DistanceToSelfObserved {
			return a.DistanceToSelfObserved < b.DistanceToSelfObserved
		}
		return a.Guid < b.Guid
	})
}
func (s *observations) appendSnapshot(r *worldv1.SnapshotResponse, now time.Time) {
	s.appendGameObjects(r, now)
	owner := s.players[1]
	target := uint64(owner.fields[0x12]) | uint64(owner.fields[0x13])<<32
	if owner.present && owner.known[0x12] && owner.known[0x13] {
		if p := s.creatures[target]; p != nil && p.present {
			r.OwnerTarget = creatureSnapshot(p, s, now)
		}
	}
	for _, p := range s.creatures {
		if !p.present || !p.known[objectEntry] || p.fields[objectEntry] == 0 {
			continue
		}
		r.NearbyCreatures = append(r.NearbyCreatures, creatureSnapshot(p, s, now))
		if p.known[unitNPCFlags] && p.fields[unitNPCFlags]&2 != 0 {
			r.QuestGivers = append(r.QuestGivers, creatureSnapshot(p, s, now))
		}
		if p.known[unitDynamicFlags] && p.fields[unitDynamicFlags]&1 != 0 && p.known[unitHealth] && p.fields[unitHealth] == 0 && p.known[unitMaxHealth] && p.fields[unitMaxHealth] > 0 {
			r.LootableCorpses = append(r.LootableCorpses, creatureSnapshot(p, s, now))
		}
		if p.known[unitFlags] && p.fields[unitFlags]&0x04000000 != 0 && p.known[unitDynamicFlags] && p.fields[unitDynamicFlags]&1 == 0 && p.known[unitHealth] && p.fields[unitHealth] == 0 && p.known[unitMaxHealth] && p.fields[unitMaxHealth] > 0 {
			r.SkinnableCorpses = append(r.SkinnableCorpses, creatureSnapshot(p, s, now))
		}
	}
	sortCreatures(r.QuestGivers)
	sortCreatures(r.LootableCorpses)
	sortCreatures(r.SkinnableCorpses)
	sort.Slice(r.NearbyCreatures, func(i, j int) bool {
		a, b := r.NearbyCreatures[i], r.NearbyCreatures[j]
		if a.DistanceToSelfObservedKnown != b.DistanceToSelfObservedKnown {
			return a.DistanceToSelfObservedKnown
		}
		if a.DistanceToSelfObserved != b.DistanceToSelfObserved {
			return a.DistanceToSelfObserved < b.DistanceToSelfObserved
		}
		return a.Guid < b.Guid
	})
	if len(r.NearbyCreatures) > 32 {
		r.NearbyCreatures = r.NearbyCreatures[:32]
	}
	if len(r.QuestGivers) > maxCreatureResults {
		r.QuestGivers = r.QuestGivers[:maxCreatureResults]
	}
	if len(r.LootableCorpses) > maxCreatureResults {
		r.LootableCorpses = r.LootableCorpses[:maxCreatureResults]
	}
	if len(r.SkinnableCorpses) > 32 {
		r.SkinnableCorpses = r.SkinnableCorpses[:32]
	}
	s.appendInventory(r)
	p := s.players[0]
	r.SelfCopperKnown, r.SelfCopper, r.SelfCopperRevision = p.copperKnown, p.copper, s.copperRevision
	r.SelfSkillsKnown, r.SelfSkillsRevision = p.skillsKnown, s.skillsRevision
	r.SelfFreePrimarySlotsKnown, r.SelfFreePrimarySlots, r.SelfFreePrimarySlotsRevision = p.freePrimaryKnown, p.freePrimary, s.freePrimaryRevision
	if p.skillsKnown {
		for slot, fields := range p.skills {
			r.SelfSkills = append(r.SelfSkills, &worldv1.SkillState{Slot: uint32(slot), SkillId: fields[0] & 0xffff, Step: fields[0] >> 16, Value: fields[1] & 0xffff, MaxValue: fields[1] >> 16, TemporaryBonus: int32(int16(fields[2] & 0xffff)), PermanentBonus: int32(int16(fields[2] >> 16))})
		}
	}
	r.SelfQuestLogKnown = p.questLogKnown
	r.SelfQuestLogRevision = p.questRevision
	if p.questLogKnown {
		for slot, fields := range p.questLog {
			r.SelfQuestLog = append(r.SelfQuestLog, &worldv1.QuestLogEntry{Slot: uint32(slot), QuestId: fields[0], State: fields[1], Complete: fields[1]&1 != 0, Failed: fields[1]&2 != 0, ObjectiveCounts: []uint32{fields[2] & 0xffff, fields[2] >> 16, fields[3] & 0xffff, fields[3] >> 16}, ExpireTime: fields[4]})
		}
	}
	r.Self.Mainhand = &worldv1.EquippedWeapon{SlotKnown: p.mainhandKnown, ItemGuid: p.mainhand}
	if item := s.items[p.mainhand]; item != nil {
		owner := uint64(item.fields[itemOwner]) | uint64(item.fields[itemOwner+1])<<32
		weapon := r.Self.Mainhand
		weapon.Entry = item.fields[objectEntry]
		weapon.MetadataKnown = item.known[objectEntry] && item.known[itemOwner] && item.known[itemOwner+1] && owner == p.guid && weapon.Entry > 0
		weapon.Durability = item.fields[0x3c]
		weapon.MaxDurability = item.fields[0x3d]
		weapon.DurabilityKnown = item.known[0x3c] && item.known[0x3d] && weapon.Durability <= weapon.MaxDurability
	}
	r.Self.Offhand, r.Self.OffhandRevision = s.offhandSnapshot(), s.offhandRevision
	r.BackpackKnown = p.backpackKnown
	r.BackpackRevision = p.backpackRevision + s.itemRevision
	if p.backpackKnown {
		for slot, guid := range p.backpack {
			item := &worldv1.BackpackSlot{Bag: 0, Slot: uint32(slot), ItemGuid: guid}
			if obj := s.items[guid]; obj != nil {
				owner := uint64(obj.fields[itemOwner]) | uint64(obj.fields[itemOwner+1])<<32
				item.Entry, item.StackCount = obj.fields[objectEntry], obj.fields[itemStack]
				item.MetadataKnown = obj.known[objectEntry] && obj.known[itemStack] && obj.known[itemOwner] && obj.known[itemOwner+1] && owner == p.guid && item.Entry > 0 && item.StackCount > 0
			}
			r.Backpack = append(r.Backpack, item)
		}
	}
}
