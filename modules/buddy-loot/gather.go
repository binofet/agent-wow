package buddyloot

import (
	"context"
	"encoding/binary"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/types/known/emptypb"
)

const harvestQuestID uint32 = 3904
const harvestObjectEntry uint32 = 161557
const harvestItemID uint32 = 11119
const harvestOpenSpell uint32 = 6478

// Public quest_template3904 requires item11119x8; gameobject_template161557
// is a chest(type3), loot10119, lock43. Build12340 Lock.dbc43 requires
// OPEN_KNEELING13; Spell6478 opens that lock in1s at5yd. GO_USE/REPORT_USE
// do not open an ordinary chest. SpellHandler.cpp+SpellEffects::EffectOpenLock:
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/SpellHandler.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Spells/SpellEffects.cpp
func harvestQuest(world *worldv1.SnapshotResponse, self uint64, incomplete bool) bool {
	if world == nil || world.Self == nil || world.Self.Guid != self || !world.SelfQuestLogKnown || len(world.SelfQuestLog) != 25 {
		return false
	}
	var seen [25]bool
	found := false
	for _, q := range world.SelfQuestLog {
		if q == nil || q.Slot >= 25 || seen[q.Slot] {
			return false
		}
		seen[q.Slot] = true
		if q.QuestId == harvestQuestID && !q.Failed && q.State&2 == 0 && (!incomplete || !q.Complete && q.State&1 == 0) {
			found = true
		}
	}
	return found
}
func harvestContext(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64, incomplete bool) bool {
	if !harvestQuest(w, self, incomplete) || a == nil || a.Position == nil || a.MotionUnconfirmed || a.AutoAttackUnconfirmed || a.AutoAttacking || a.CastPending {
		return false
	}
	p, pos := w.Self, a.Position
	if !p.Present || !p.AliveKnown || !p.Alive || !p.HealthKnown || p.Health == 0 || !p.MapKnown || p.MapId != 0 || !p.UnitFlagsKnown || p.UnitFlags&(0x1|0x4|0x2000|0x40000|0x80000|0x100000|0x400000|0x800000|0x1000000|0x2000000|0x8000000) != 0 || !p.MovementFlagsKnown || p.MovementFlags&^uint32(0x1ff) != 0 || p.ExtraMovementFlags != 0 {
		return false
	}
	if !pos.Known || !pos.MapKnown || pos.MapId != p.MapId {
		return false
	}
	for _, n := range []float32{pos.X, pos.Y, pos.Z} {
		if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) || math.Abs(float64(n)) > 40000 {
			return false
		}
	}
	return true
}
func harvestObject(p *actionsv1.ClientPosition, g *worldv1.GameObjectState) bool {
	if p == nil || g == nil || g.Guid>>48 != 0xf110 || g.Guid&0xffffff == 0 || uint32(g.Guid>>24&0xffffff) != harvestObjectEntry || g.Entry != harvestObjectEntry || !g.Present || !g.TypeKnown || g.GameObjectType != 3 || !g.MapKnown || g.MapId != p.MapId || !g.PositionKnown || !g.FlagsKnown || g.Flags&(1|8|16) != 0 {
		return false
	}
	for _, n := range []float32{g.X, g.Y, g.Z} {
		if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) || math.Abs(float64(n)) > 40000 {
			return false
		}
	}
	dx, dy, dz := float64(g.X-p.X), float64(g.Y-p.Y), float64(g.Z-p.Z)
	return dx*dx+dy*dy+dz*dz <= 4.5*4.5 && math.Abs(dz) <= 1.5
}
func gatherOpenPacket(count byte, guid uint64) []byte {
	p := make([]byte, 10)
	p[0] = count
	binary.LittleEndian.PutUint32(p[1:], harvestOpenSpell)
	binary.LittleEndian.PutUint32(p[6:], 0x800) // TARGET_FLAG_GAMEOBJECT +packedGUID.
	mask := byte(0)
	var packed []byte
	for i := uint(0); i < 8; i++ {
		if b := byte(guid >> (i * 8)); b != 0 {
			mask |= 1 << i
			packed = append(packed, b)
		}
	}
	return append(append(p, mask), packed...)
}
func (s *Service) pendingGUID() uint64 {
	if s.questGather {
		return s.state.PendingGameObjectGuid
	}
	return s.state.PendingCorpseGuid
}
func (s *Service) observeGatherQuest(w *worldv1.SnapshotResponse) {
	r := s.state.LastGather
	if !s.questGather || r == nil || !harvestQuest(w, s.selfGUID, false) {
		return
	}
	if w.SelfQuestLogRevision >= r.ObservedQuestLogRevision {
		r.ObservedQuestLogRevision = w.SelfQuestLogRevision
		for _, q := range w.SelfQuestLog {
			if q.QuestId == harvestQuestID {
				r.OwnQuestCompleteObserved = q.Complete || q.State&1 != 0
			}
		}
	}
}
func (s *Service) tryGatherLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	if !harvestQuest(w, s.selfGUID, true) || len(w.QuestObjects) == 0 || len(w.QuestObjects) > 32 || len(s.gatherSeen) >= 128 {
		return false
	}
	a := &actionsv1.State{}
	if s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, a) != nil || !harvestContext(w, a, s.selfGUID, true) {
		return false
	}
	var selected *worldv1.GameObjectState
	best := math.Inf(1)
	for _, g := range w.QuestObjects {
		if g == nil || s.gatherSeen[g.Guid] || !harvestObject(a.Position, g) {
			continue
		}
		d := math.Pow(float64(g.X-a.Position.X), 2) + math.Pow(float64(g.Y-a.Position.Y), 2) + math.Pow(float64(g.Z-a.Position.Z), 2)
		if d < best || d == best && g.Guid < selected.Guid {
			selected, best = g, d
		}
	}
	if selected == nil {
		return false
	}
	l := &actionsv1.InteractionLease{}
	if s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "loot", LeaseMs: 15000}, l) != nil {
		return false
	}
	if l.Token == "" || l.State == nil || l.State.InteractionOwner != "loot" {
		s.state.Reason = "gather_interaction_lease_unconfirmed"
		return true
	}
	s.token = l.Token
	fresh := &worldv1.SnapshotResponse{}
	valid := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, fresh) == nil && !l.State.Moving && harvestContext(fresh, l.State, s.selfGUID, true)
	var current *worldv1.GameObjectState
	if valid && len(fresh.QuestObjects) <= 32 {
		for _, g := range fresh.QuestObjects {
			if g != nil && g.Guid == selected.Guid {
				if current != nil {
					valid = false
					break
				}
				current = g
			}
		}
	}
	if !valid || current == nil || current.PositionRevision != selected.PositionRevision || fresh.SelfQuestLogRevision != w.SelfQuestLogRevision || !harvestObject(l.State.Position, current) {
		s.releaseLease(ctx)
		s.state.Reason = "gather_observation_changed_after_stop"
		return true
	}
	id := uint64(1)
	if s.state.LastGather != nil {
		id = s.state.LastGather.Id + 1
	}
	s.questGather = true
	if s.gatherSeen == nil {
		s.gatherSeen = map[uint64]bool{}
	}
	s.gatherSeen[current.Guid] = true
	s.state.PendingGameObjectGuid = current.Guid
	s.state.LastGather = &lootv1.GatherReceipt{Id: id, ObjectGuid: current.Guid, ObjectEntry: harvestObjectEntry, QuestId: harvestQuestID, ItemId: harvestItemID, Status: lootv1.GatherStatus_GATHER_REQUESTED, BaselineQuestLogRevision: fresh.SelfQuestLogRevision, ObservedQuestLogRevision: fresh.SelfQuestLogRevision, Reason: "opening_requested_waiting_for_server"}
	s.transactionUntil = s.now().Add(12 * time.Second)
	s.moneySent = false
	s.moneyCleared = false
	s.openLocked(ctx)
	return true
}

func gatherObjectCurrent(w *worldv1.SnapshotResponse, a *actionsv1.State, guid uint64) bool {
	if w == nil || a == nil || len(w.QuestObjects) > 32 {
		return false
	}
	var object *worldv1.GameObjectState
	for _, g := range w.QuestObjects {
		if g != nil && g.Guid == guid {
			if object != nil {
				return false
			}
			object = g
		}
	}
	return harvestObject(a.Position, object)
}

// Item.h EQUIP_ERR_INVENTORY_FULL50 is a rejected store, not an ambiguous
// write. Resume only after a complete own backpack snapshot proves free space.
func ownKnownFreeSlot(w *worldv1.SnapshotResponse, self uint64) bool {
	if w == nil || w.Self == nil || w.Self.Guid != self || !w.BackpackKnown || len(w.Backpack) != 16 {
		return false
	}
	var seen [16]bool
	free := false
	for _, item := range w.Backpack {
		if item == nil || item.Bag != 0 || item.Slot >= 16 || seen[item.Slot] {
			return false
		}
		seen[item.Slot] = true
		free = free || item.ItemGuid == 0
	}
	return free
}
