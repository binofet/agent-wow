package modrt

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestPolicyControlBreakDefinitiveRejectionAllowsSameVMRetunedChoice(t *testing.T) {
	m, native, cb := friendlyEncounterManager(t)
	runtimeControlBook(t, native, 59752)
	cb.world.Self.UnitFlags |= 0x800000
	cb.world.SelfEquipmentKnown = true
	const itemGUID = uint64(0x4000000000000059)
	cb.world.SelfEquipment = []*worldv1.EquipmentSlotState{{Slot: 12, SlotKnown: true, MetadataKnown: true, ItemGuid: itemGUID, Entry: 18854, StackCount: 1}}
	runtimeControlFear(t, native, 7399)
	source := controlBreakRuntimeSource(t)
	p, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	first, _ := controlBreakStep(t, m, p)
	if first == nil || first.Outcome != behavior.ActionTransportAccepted || len(cb.writes) != 1 || cb.writes[0].Opcode != opcode.CMSGCastSpell || binary.LittleEndian.Uint32(cb.writes[0].Payload[1:]) != 59752 {
		t.Fatal("default actual Lua learned choice not dispatched", first, cb.writes)
	}
	failure := binary.LittleEndian.AppendUint32([]byte{cb.writes[0].Payload[0]}, 59752)
	failure = append(failure, 47)
	if _, err = native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: failure}); err != nil {
		t.Fatal(err)
	}
	// Selection remains in editable Lua. A changed same-session policy now
	// prefers the equipped source sharing the rejected cast's category.
	tuned := strings.Replace(source, "spell_weights={}", "spell_weights={[42292]=10}", 1)
	if tuned == source {
		t.Fatal("actual Lua preference was not edited")
	}
	before := p.vm.Status()
	if _, err = p.vm.Reload(context.Background(), tuned, before.Revision); err != nil {
		t.Fatal(err)
	}
	next, out := controlBreakStep(t, m, p)
	if next == nil || next.Outcome != behavior.ActionTransportAccepted || len(cb.writes) != 2 || cb.writes[1].Opcode != opcode.CMSGUseItem || binary.LittleEndian.Uint64(cb.writes[1].Payload[7:]) != itemGUID || p.vm.Status().SessionID != before.SessionID {
		t.Fatal("local rejected cooldown hid Lua's new exact choice", next, out, cb.writes, p.vm.Status())
	}
	if next.Payload["server_cast_go"] != false || next.Payload["effect_observed"] != false {
		t.Fatal("retry transport claimed a game effect", next)
	}
}

func TestPolicyControlBreakRejectionKeepsNewObservedServerCooldown(t *testing.T) {
	m, native, cb := friendlyEncounterManager(t)
	runtimeControlBook(t, native, 59752)
	cb.world.Self.UnitFlags |= 0x800000
	runtimeControlFear(t, native, 7399)
	p, err := m.StartPolicy(context.Background(), controlBreakRuntimeSource(t))
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	first, _ := controlBreakStep(t, m, p)
	if first == nil || first.Outcome != behavior.ActionTransportAccepted || len(cb.writes) != 1 {
		t.Fatal(first, cb.writes)
	}
	// An actual complete initial-spell frame supplies a shorter server cooldown
	// while the optimistic two-minute deadline is still held by this attempt.
	book := binary.LittleEndian.AppendUint32([]byte{0, 1, 0}, 59752)
	book = append(book, 0, 0, 1, 0)
	book = binary.LittleEndian.AppendUint32(book, 59752)
	book = binary.LittleEndian.AppendUint16(book, 0)
	book = binary.LittleEndian.AppendUint16(book, 1182)
	book = binary.LittleEndian.AppendUint32(book, 30000)
	book = binary.LittleEndian.AppendUint32(book, 30000)
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInitialSpells, Payload: book})
	failure := binary.LittleEndian.AppendUint32([]byte{cb.writes[0].Payload[0]}, 59752)
	failure = append(failure, 47)
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: failure})
	next, out := controlBreakStep(t, m, p)
	if next != nil || len(cb.writes) != 1 || p.vm.Status().Pending != 0 {
		t.Fatal("definitive rejection erased real cooldown or stranded pending", next, out, cb.writes, p.vm.Status())
	}
	observed, err := native.EncounterObservation(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, o := range observed.GetSupport().GetControlRecovery().GetOptions() {
		if o.SpellId == 59752 {
			found = true
			if o.Ready {
				t.Fatal("server cooldown lost", o)
			}
		}
	}
	if !found {
		t.Fatal("observed cooldown hid learned option")
	}
}
