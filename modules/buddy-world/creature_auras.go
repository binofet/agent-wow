package buddyworld

import (
	"maps"
	"slices"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/auraupdate"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

type creatureAuraSlot struct {
	auraupdate.Slot
	observedAt time.Time
}

type observedCreatureAuras struct {
	fullRevision uint64
	revision     uint64
	slots        map[uint8]creatureAuraSlot
}

func (s *observations) observeCreatureAuras(op uint32, payload []byte, now time.Time) (bool, error) {
	c := cursor{b: payload}
	guid := c.guid()
	if c.err != nil || guid == 0 {
		return true, errLayout
	}
	if !sameCreatureCastMap(s.creatures[guid], &s.players[0]) {
		return false, nil
	}
	all := op == opcode.SMSGAuraUpdateAll
	_, slots, err := auraupdate.Decode(payload, all)
	if err != nil {
		return true, err
	}
	p := s.target(guid)
	s.auraRevision++
	set := &observedCreatureAuras{revision: s.auraRevision}
	if old := p.auras; old != nil && !all {
		set.fullRevision = old.fullRevision
		set.slots = maps.Clone(old.slots)
	}
	if all {
		set.fullRevision = set.revision
	}
	if len(slots) != 0 && set.slots == nil {
		set.slots = make(map[uint8]creatureAuraSlot, len(slots))
	}
	for _, slot := range slots {
		if slot.SpellID == 0 && set.fullRevision != 0 {
			delete(set.slots, slot.Index)
		} else {
			set.slots[slot.Index] = creatureAuraSlot{Slot: slot, observedAt: now}
		}
	}
	p.auras = set
	return true, nil
}

// A missed aura update or object lifecycle event can supersede a full list.
// A later incremental update supplies only its own slot until another full list.
func (s *observations) invalidateCreatureAuras(op uint32, payload []byte) {
	c := cursor{b: payload}
	var guid uint64
	switch op {
	case opcode.SMSGAuraUpdate, opcode.SMSGAuraUpdateAll:
		guid = c.guid()
	case opcode.SMSGDestroyObject:
		guid = c.u64()
	case opcode.SMSGUpdateObject, opcode.SMSGCompressedUpdateObject,
		opcode.SMSGLoginVerifyWorld, opcode.SMSGTransferPending, opcode.SMSGNewWorld:
	default:
		return
	}
	if c.err == nil && guid != 0 && (op != opcode.SMSGDestroyObject || guid != s.players[0].guid) {
		if p := s.creatures[guid]; p != nil {
			p.auras = nil
		}
		return
	}
	for _, p := range s.creatures {
		p.auras = nil
	}
}

func (set *observedCreatureAuras) snapshot(now time.Time) *worldv1.VisibleCreatureAuras {
	view := &worldv1.VisibleCreatureAuras{Complete: set.fullRevision != 0, Revision: set.revision}
	if view.Complete {
		view.FullRevision = proto.Uint64(set.fullRevision)
	}
	for _, index := range slices.Sorted(maps.Keys(set.slots)) {
		slot := set.slots[index]
		if slot.SpellID == 0 {
			view.ClearedSlots = append(view.ClearedSlots, uint32(index))
			continue
		}
		aura := &worldv1.VisibleCreatureAura{
			Slot: uint32(index), SpellId: slot.SpellID, Flags: uint32(slot.Flags), CasterLevel: uint32(slot.CasterLevel),
			StacksOrCharges: uint32(slot.StacksOrCharges), Positive: slot.Positive(), Negative: slot.Negative(),
		}
		if slot.CasterGUID != 0 {
			aura.CasterGuid = proto.Uint64(slot.CasterGUID)
		}
		if slot.HasDuration() {
			elapsed := max(int64(0), now.Sub(slot.observedAt).Milliseconds())
			aura.MaxDurationMs = proto.Uint32(slot.MaxDurationMS)
			aura.RemainingMs = proto.Uint32(uint32(max(int64(0), int64(slot.RemainingMS)-elapsed)))
		}
		view.Slots = append(view.Slots, aura)
	}
	return view
}
