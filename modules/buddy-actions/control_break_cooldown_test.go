package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func rejectedControlFixture(t *testing.T, item bool) (*Service, *fakeSession, *actionsv1.BreakControlRequest, func(time.Duration)) {
	t.Helper()
	s, f, advance := buffRig(t, 1, humanBreakSpell)
	activateEncounterWriter(t, s, f)
	f.world.Self.UnitFlags |= fleeingUnitFlag
	applyTestFear(s, 7399)
	r := exactFearRequest(s, humanBreakSpell)
	if item {
		f.world.SelfEquipmentKnown = true
		f.world.SelfEquipment = []*worldv1.EquipmentSlotState{{Slot: 12, SlotKnown: true, ItemGuid: 0x4000000000000091, Entry: 18854, MetadataKnown: true, StackCount: 1}}
		r.SpellId, r.ItemGuid, r.ItemSlot = pvpBreakSpell, f.world.SelfEquipment[0].ItemGuid, 12
	}
	return s, f, r, advance
}
func rejectControlPacket(t *testing.T, s *Service, count byte, spell uint32) {
	t.Helper()
	p := binary.LittleEndian.AppendUint32([]byte{count}, spell)
	p = append(p, 47)
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCastFailed, Payload: p}); err != nil {
		t.Fatal(err)
	}
}
func observeControlCooldown(t *testing.T, s *Service, spell, category, ms uint32) {
	t.Helper()
	p := binary.LittleEndian.AppendUint32([]byte{0, 1, 0}, humanBreakSpell)
	p = append(p, 0, 0, 1, 0)
	p = binary.LittleEndian.AppendUint32(p, spell)
	p = binary.LittleEndian.AppendUint16(p, 0)
	p = binary.LittleEndian.AppendUint16(p, uint16(category))
	p = binary.LittleEndian.AppendUint32(p, ms)
	p = binary.LittleEndian.AppendUint32(p, ms)
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInitialSpells, Payload: p}); err != nil {
		t.Fatal(err)
	}
}
func TestControlBreakDefinitiveRejectionReleasesOnlyOwnOptimisticCooldown(t *testing.T) {
	for _, item := range []bool{false, true} {
		t.Run(map[bool]string{false: "learned", true: "equipped"}[item], func(t *testing.T) {
			s, f, r, _ := rejectedControlFixture(t, item)
			gcd := s.now().Add(1500 * time.Millisecond)
			s.nextHeal, s.nextAssist = gcd, gcd
			if _, err := s.BreakControl(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			if !s.spellReady[r.SpellId].After(s.now()) || !s.categoryReady[1182].After(s.now()) || !s.categoryReady[1166].After(s.now()) {
				t.Fatal("fixture did not reserve actual spell/item/linked category", s.spellReady, s.categoryReady)
			}
			rejectControlPacket(t, s, s.castCount, r.SpellId)
			if !s.nextHeal.Equal(gcd) || !s.nextAssist.Equal(gcd) {
				t.Fatal("rejection changed unrelated real GCD", s.nextHeal, s.nextAssist)
			}
			if s.state.CastPending || s.spellReady[r.SpellId].After(s.now()) || s.categoryReady[1182].After(s.now()) || s.categoryReady[1166].After(s.now()) || len(s.controlBreakBindings) != 0 {
				t.Fatal("rejected cast imposed a local retry timer", s.stateLocked(), s.spellReady, s.categoryReady)
			}
			if s.supportOp.view.EffectObserved || s.supportOp.view.ServerCastGo {
				t.Fatal("rejection invented success")
			}
			if _, err := s.BreakControl(context.Background(), r); err != nil || len(f.packets) != 2 {
				t.Fatal("exact caller could not choose fresh same-application action", err, f.packets)
			}
		})
	}
}
func TestControlBreakRejectedEstimatePreservesObservedServerCooldown(t *testing.T) {
	for _, item := range []bool{false, true} {
		t.Run(map[bool]string{false: "learned", true: "equipped"}[item], func(t *testing.T) {
			s, _, r, _ := rejectedControlFixture(t, item)
			if _, err := s.BreakControl(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			optimistic := s.spellReady[r.SpellId]
			observeControlCooldown(t, s, r.SpellId, 1182, 30000)
			observed := s.now().Add(30 * time.Second)
			if !optimistic.After(observed) || !s.spellReady[r.SpellId].Equal(optimistic) || !s.serverSpellReady[r.SpellId].Equal(observed) {
				t.Fatal("server fact was conflated with local estimate")
			}
			rejectControlPacket(t, s, s.castCount, r.SpellId)
			if !s.spellReady[r.SpellId].Equal(observed) || !s.categoryReady[1182].Equal(observed) || s.categoryReady[1166].After(s.now()) {
				t.Fatal("rollback erased server cooldown or retained unrelated estimate", s.spellReady, s.categoryReady)
			}
		})
	}
}
func TestControlBreakUnknownForeignRefreshAndVisitCannotReleaseCooldown(t *testing.T) {
	for _, mode := range []string{"unknown", "foreign_caster", "foreign_source", "wrong_count", "wrong_spell", "aura_refresh", "stale_visit", "newer_reservation"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r, advance := rejectedControlFixture(t, false)
			if mode == "unknown" {
				f.failSend = true
			}
			_, err := s.BreakControl(context.Background(), r)
			if mode != "unknown" && err != nil {
				t.Fatal(err)
			}
			spell, category, linked := s.spellReady[r.SpellId], s.categoryReady[1182], s.categoryReady[1166]
			count := s.castCount
			switch mode {
			case "unknown":
				advance(4 * time.Second)
				s.supportSnapshotLocked(f.world)
			case "foreign_caster":
				p := append(packedGUID(9), count)
				p = binary.LittleEndian.AppendUint32(p, r.SpellId)
				p = append(p, 47)
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellFailure, Payload: p})
			case "foreign_source":
				p := smiteEvent(true, count, s.selfGUID)
				_, _, _, off, _ := castEvent(p)
				binary.LittleEndian.PutUint32(p[off-4:], r.SpellId)
				p[1] = 9
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: p})
			case "wrong_count":
				rejectControlPacket(t, s, count+1, r.SpellId)
			case "wrong_spell":
				rejectControlPacket(t, s, count, r.SpellId+1)
			case "aura_refresh":
				applyTestFear(s, 7399)
				rejectControlPacket(t, s, count, r.SpellId)
			case "stale_visit":
				s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGNewWorld, Payload: make([]byte, 20)})
				rejectControlPacket(t, s, count, r.SpellId)
			case "newer_reservation":
				spell = s.now().Add(5 * time.Minute)
				category = spell
				linked = spell
				s.spellReady[r.SpellId], s.categoryReady[1182], s.categoryReady[1166] = spell, category, linked
				rejectControlPacket(t, s, count, r.SpellId)
			}
			if !s.spellReady[r.SpellId].Equal(spell) || !s.categoryReady[1182].Equal(category) || !s.categoryReady[1166].Equal(linked) {
				t.Fatal("unsettled/foreign/stale event erased another deadline", mode, s.spellReady, s.categoryReady)
			}
			if mode != "newer_reservation" && len(s.controlBreakBindings) == 0 {
				t.Fatal("unsettled application lost source custody", mode)
			}
		})
	}
}
