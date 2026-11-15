package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func applyTestFear(s *Service, spell uint32) {
	p := buffAura(s.selfGUID, 123, 5, spell, 8000)
	if spell != 0 {
		p[len(packedGUID(s.selfGUID))+5] ^= 0x10 | 0x80
	}
	s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAuraUpdate, Payload: p})
}

func TestObservedFearUsesLearnedBreakWithoutMovement(t *testing.T) {
	for _, test := range []struct {
		spell uint32
		legal bool
	}{
		{humanBreakSpell, true}, {7744, true}, {18499, true},
		{20589, false}, {20594, false}, {6346, false},
	} {
		s, f, _ := buffRig(t, 1, test.spell)
		f.world.Self.UnitFlags |= fleeingUnitFlag
		s.state.MotionUnconfirmed = true
		applyTestFear(s, 7399)
		if used := s.breakControlLocked(context.Background(), f.world); used != test.legal {
			t.Fatalf("spell %d used=%v, want %v", test.spell, used, test.legal)
		}
		count := 0
		if test.legal {
			count = 1
			if lastCast(f) != test.spell {
				t.Fatalf("spell %d wasn't sent", test.spell)
			}
		}
		if len(f.packets) != count || s.breakControlLocked(context.Background(), f.world) {
			t.Fatalf("spell %d repeated or emitted unrelated packets", test.spell)
		}
	}
}

func TestFearBreakUsesOnlyEquippedVerifiedTrinket(t *testing.T) {
	s, f, advance := buffRig(t, 9)
	const item = 0x4000000000012345
	f.world.SelfEquipmentKnown = true
	f.world.SelfEquipment = []*worldv1.EquipmentSlotState{{
		Slot: 13, SlotKnown: true, MetadataKnown: true, ItemGuid: item, Entry: 18854, StackCount: 1,
	}}
	ctx := context.Background()
	applyTestFear(s, 7399)
	if s.breakControlLocked(ctx, f.world) {
		t.Fatal("cast before feared unit flag")
	}
	f.world.Self.UnitFlags |= fleeingUnitFlag
	f.world.SelfEquipment[0].Slot = 6
	if s.breakControlLocked(ctx, f.world) {
		t.Fatal("used an item outside a trinket slot")
	}
	f.world.SelfEquipment[0].Slot = 13
	if !s.breakControlLocked(ctx, f.world) || len(f.packets) != 1 {
		t.Fatal("valid trinket was not used")
	}
	p := f.packets[0]
	if p.Opcode != opcode.CMSGUseItem || len(p.Payload) != 24 || p.Payload[0] != 255 || p.Payload[1] != 13 ||
		binary.LittleEndian.Uint32(p.Payload[3:]) != pvpBreakSpell || binary.LittleEndian.Uint64(p.Payload[7:]) != item {
		t.Fatal("wrong equipped item packet", p)
	}
	receipt := append(packedGUID(item), packedGUID(s.selfGUID)...)
	receipt = append(receipt, s.castCount)
	receipt = binary.LittleEndian.AppendUint32(receipt, pvpBreakSpell)
	receipt = binary.LittleEndian.AppendUint32(receipt, 0x100)
	receipt = binary.LittleEndian.AppendUint32(receipt, 17)
	receipt = append(receipt, 1)
	receipt = binary.LittleEndian.AppendUint64(receipt, s.selfGUID)
	receipt = append(receipt, 0)
	receipt = binary.LittleEndian.AppendUint32(receipt, 0)
	s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGSpellGo, Payload: receipt})
	if s.state.CastPending || !s.supportOp.view.ServerCastGo {
		t.Fatal("own implicit-self item cast did not settle")
	}
	applyTestFear(s, 0)
	advance(time.Minute)
	if s.hasObservedFearLocked() {
		t.Fatal("removed fear still seen")
	}
	applyTestFear(s, 7399)
	if s.breakControlLocked(ctx, f.world) || len(f.packets) != 1 {
		t.Fatal("retried during shared cooldown")
	}
}

func TestFearBreakRequiresTheActiveFearAura(t *testing.T) {
	s, f, advance := buffRig(t, 1, humanBreakSpell)
	f.world.Self.UnitFlags |= fleeingUnitFlag
	applyTestFear(s, 676)
	if s.breakControlLocked(context.Background(), f.world) {
		t.Fatal("unrelated negative aura selected a fear break")
	}
	applyTestFear(s, 7399)
	advance(8 * time.Second)
	if s.breakControlLocked(context.Background(), f.world) {
		t.Fatal("stale aura spent the fear break")
	}
}
