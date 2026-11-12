package buddyprofessions

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const disenchantProtocolTarget uint64 = 0x400000000000002a

func disenchantProtocolPacked(guid uint64) []byte {
	p := []byte{0}
	for i := uint(0); i < 8; i++ {
		if b := byte(guid >> (8 * i)); b != 0 {
			p[0] |= 1 << i
			p = append(p, b)
		}
	}
	return p
}

// A selected ITEM has no unit/gameobject hit list entries. Its packed GUID is
// carried after the explicit ITEM0x10 mask, for both START and GO.
func disenchantProtocolCastFixture(goEvent bool, caster, target uint64, flags uint32) []byte {
	p := disenchantProtocolPacked(caster)
	p = append(p, disenchantProtocolPacked(caster)...)
	p = append(p, 9)
	p = binary.LittleEndian.AppendUint32(p, 13262)
	p = binary.LittleEndian.AppendUint32(p, flags)
	p = binary.LittleEndian.AppendUint32(p, 3000)
	if goEvent {
		p = append(p, 0, 0)
	}
	p = binary.LittleEndian.AppendUint32(p, 0x10)
	p = append(p, disenchantProtocolPacked(target)...)
	if flags&0x800 != 0 {
		p = binary.LittleEndian.AppendUint32(p, 25)
	}
	return p
}

func disenchantProtocolRejectCastPrefixes(t *testing.T, code uint32, p []byte) {
	t.Helper()
	for n := range len(p) {
		if _, err := decodeDisenchantCast(code, p[:n]); err == nil {
			t.Fatalf("accepted cast prefix%d/%d", n, len(p))
		}
	}
	if _, err := decodeDisenchantCast(code, append(append([]byte{}, p...), 0)); err == nil {
		t.Fatal("accepted trailing cast byte")
	}
}

func TestDisenchantProtocolExplicitItemRequestGoldens(t *testing.T) {
	if got := hex.EncodeToString(disenchantCastPacket(9, disenchantProtocolTarget)); got != "09ce3300000010000000812a40" {
		t.Fatal("fixed13262 explicitITEM packet", got)
	}
	if got := hex.EncodeToString(disenchantQueryPacket(1001)); got != "e9030000" {
		t.Fatal("ordinary itemquery must contain entry only", got)
	}
}

func TestDisenchantProtocolExplicitItemStartAndZeroHitGo(t *testing.T) {
	for _, goEvent := range []bool{false, true} {
		for _, flags := range []uint32{0, 1, 2, 0x100, 0x800, 0x40000, 0x40903} {
			t.Run(fmt.Sprintf("go%t_flags%x", goEvent, flags), func(t *testing.T) {
				code, kind := uint32(opcode.SMSGSpellStart), "start"
				if goEvent {
					code, kind = opcode.SMSGSpellGo, "go"
				}
				p := disenchantProtocolCastFixture(goEvent, 7, disenchantProtocolTarget, flags)
				e, err := decodeDisenchantCast(code, p)
				if err != nil || e.kind != kind || e.caster != 7 || e.target != disenchantProtocolTarget || e.count != 9 || e.spell != 13262 || e.failure != 0 {
					t.Fatal(e, err)
				}
				disenchantProtocolRejectCastPrefixes(t, code, p)
			})
		}
	}
	// The parser preserves the real actor, selected item and cast count; the
	// controller must bind those exact fields to its own pending operation.
	p := disenchantProtocolCastFixture(true, 8, disenchantProtocolTarget+1, 0x100)
	p[4] = 10
	if e, err := decodeDisenchantCast(opcode.SMSGSpellGo, p); err != nil || e.caster != 8 || e.target != disenchantProtocolTarget+1 || e.count != 10 {
		t.Fatal("identity was replaced with expected values", e, err)
	}
	for bit := uint(0); bit < 32; bit++ {
		flag := uint32(1) << bit
		if flag&(1|2|0x100|0x800|0x40000) != 0 {
			continue
		}
		if _, err := decodeDisenchantCast(opcode.SMSGSpellGo, disenchantProtocolCastFixture(true, 7, disenchantProtocolTarget, flag)); err == nil {
			t.Fatalf("unsupported cast flag%x", flag)
		}
	}
}

func TestDisenchantProtocolRejectsUnitGoTargetsAndAnyHitOrMiss(t *testing.T) {
	for _, name := range []string{"one_hit", "one_miss", "unit_mask", "go_mask", "no_mask", "mixed_mask", "unit_guid", "go_guid", "player_guid", "zero_guid", "zero_item_counter", "cast_item"} {
		t.Run(name, func(t *testing.T) {
			p := disenchantProtocolCastFixture(true, 7, disenchantProtocolTarget, 0x100)
			switch name {
			case "one_hit":
				p[17] = 1
			case "one_miss":
				p[18] = 1
			case "unit_mask":
				binary.LittleEndian.PutUint32(p[19:], 2)
			case "go_mask":
				binary.LittleEndian.PutUint32(p[19:], 0x800)
			case "no_mask":
				binary.LittleEndian.PutUint32(p[19:], 0)
			case "mixed_mask":
				binary.LittleEndian.PutUint32(p[19:], 0x810)
			case "unit_guid":
				p = disenchantProtocolCastFixture(true, 7, 0xf13000012300002a, 0x100)
			case "go_guid":
				p = disenchantProtocolCastFixture(true, 7, 0xf11000012300002a, 0x100)
			case "player_guid":
				p = disenchantProtocolCastFixture(true, 7, 3, 0x100)
			case "zero_guid":
				p = disenchantProtocolCastFixture(true, 7, 0, 0x100)
			case "zero_item_counter":
				p = disenchantProtocolCastFixture(true, 7, 0x4000000000000000, 0x100)
			case "cast_item":
				p[1] = 8
			}
			if _, err := decodeDisenchantCast(opcode.SMSGSpellGo, p); err == nil {
				t.Fatal("unsupported disenchant target/hit shape", name)
			}
		})
	}
	for _, mask := range []uint32{0, 2, 0x800, 0x810} {
		p := disenchantProtocolCastFixture(false, 7, disenchantProtocolTarget, 2)
		binary.LittleEndian.PutUint32(p[17:], mask)
		if _, err := decodeDisenchantCast(opcode.SMSGSpellStart, p); err == nil {
			t.Fatal("START accepted unsupported item target mask", mask)
		}
	}
}

func TestDisenchantProtocolFailureConditionalBodiesAndInterrupts(t *testing.T) {
	for _, tc := range []struct {
		reason byte
		tail   int
	}{{12, 0}, {29, 8}, {54, 4}, {129, 0}, {130, 0}, {131, 0}} {
		p := []byte{9, 0xce, 0x33, 0, 0, tc.reason}
		p = append(p, make([]byte, tc.tail)...)
		e, err := decodeDisenchantCast(opcode.SMSGCastFailed, p)
		if err != nil || e.kind != "failed" || e.count != 9 || e.spell != 13262 || e.failure != tc.reason {
			t.Fatal(e, err)
		}
		disenchantProtocolRejectCastPrefixes(t, opcode.SMSGCastFailed, p)
	}
	for _, reason := range []byte{130, 131} {
		if _, err := decodeDisenchantCast(opcode.SMSGCastFailed, []byte{9, 0xce, 0x33, 0, 0, reason, 166, 0, 0, 0}); err == nil {
			t.Fatal("invented totem/category metadata accepted", reason)
		}
	}
	for _, reason := range []byte{0, 188, 255} {
		if _, err := decodeDisenchantCast(opcode.SMSGCastFailed, []byte{9, 0xce, 0x33, 0, 0, reason}); err == nil {
			t.Fatal("invalid failure", reason)
		}
	}
	if _, err := decodeDisenchantCast(opcode.SMSGCastFailed, []byte{9, 0xcf, 0x33, 0, 0, 12}); err == nil {
		t.Fatal("foreign spell cast failure accepted")
	}
	p := append(disenchantProtocolPacked(7), 9, 0xce, 0x33, 0, 0, 29)
	if e, err := decodeDisenchantCast(opcode.SMSGSpellFailure, p); err != nil || e.kind != "failed" || e.caster != 7 || e.spell != 13262 || e.failure != 29 {
		t.Fatal(e, err)
	}
	disenchantProtocolRejectCastPrefixes(t, opcode.SMSGSpellFailure, p)
	if _, err := decodeDisenchantCast(opcode.SMSGSpellFailure, append(p, make([]byte, 8)...)); err == nil {
		t.Fatal("interrupted spell accepted CastFailed reason tail")
	}
}

func disenchantProtocolLootFixture(n int) []byte {
	p := binary.LittleEndian.AppendUint64(nil, disenchantProtocolTarget)
	p = append(p, 4)
	p = binary.LittleEndian.AppendUint32(p, 0)
	p = append(p, byte(n))
	for i := 0; i < n; i++ {
		row := make([]byte, 22)
		row[0] = byte(i)
		binary.LittleEndian.PutUint32(row[1:], 10940+uint32(i))
		binary.LittleEndian.PutUint32(row[5:], 2)
		if i%2 == 1 {
			row[21] = 4
		}
		p = append(p, row...)
	}
	return p
}

func TestDisenchantProtocolAutomaticItemLootWindowStrictSlots(t *testing.T) {
	for _, n := range []int{0, 1, 16} {
		p := disenchantProtocolLootFixture(n)
		w, err := decodeDisenchantLoot(p)
		if err != nil || w.guid != disenchantProtocolTarget || w.gold != 0 || len(w.slots) != n {
			t.Fatal(n, w, err)
		}
		for i, row := range w.slots {
			if row.slot != byte(i) || row.entry != 10940+uint32(i) || row.count != 2 {
				t.Fatal(row)
			}
		}
		for length := range len(p) {
			if _, err := decodeDisenchantLoot(p[:length]); err == nil {
				t.Fatal("partial itemloot", n, length)
			}
		}
		if _, err := decodeDisenchantLoot(append(append([]byte{}, p...), 0)); err == nil {
			t.Fatal("trailing itemloot bytes")
		}
	}
	for _, name := range []string{"corpse_guid", "go_guid", "player_guid", "zero_guid", "zero_counter", "normal_loot_type", "skin_loot_type", "gold", "duplicate_slot", "zero_entry", "zero_count", "oversized_stack", "seventeen_rows"} {
		t.Run(name, func(t *testing.T) {
			p := disenchantProtocolLootFixture(2)
			switch name {
			case "corpse_guid":
				binary.LittleEndian.PutUint64(p, 0xf13000012300002a)
			case "go_guid":
				binary.LittleEndian.PutUint64(p, 0xf11000012300002a)
			case "player_guid":
				binary.LittleEndian.PutUint64(p, 3)
			case "zero_guid":
				binary.LittleEndian.PutUint64(p, 0)
			case "zero_counter":
				binary.LittleEndian.PutUint64(p, 0x4000000000000000)
			case "normal_loot_type":
				p[8] = 1
			case "skin_loot_type":
				p[8] = 6
			case "gold":
				binary.LittleEndian.PutUint32(p[9:], 1)
			case "duplicate_slot":
				p[36] = 0
			case "zero_entry":
				binary.LittleEndian.PutUint32(p[15:], 0)
			case "zero_count":
				binary.LittleEndian.PutUint32(p[19:], 0)
			case "oversized_stack":
				binary.LittleEndian.PutUint32(p[19:], 1001)
			case "seventeen_rows":
				p = disenchantProtocolLootFixture(17)
			}
			if _, err := decodeDisenchantLoot(p); err == nil {
				t.Fatal("unsafe automatic itemloot accepted", name)
			}
		})
	}
	for _, kind := range []byte{1, 2, 3, 5, 255} {
		p := disenchantProtocolLootFixture(1)
		p[35] = kind
		if _, err := decodeDisenchantLoot(p); err == nil {
			t.Fatal("rolled/locked loot slot accepted", kind)
		}
	}
}
