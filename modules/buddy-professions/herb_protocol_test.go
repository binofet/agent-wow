package buddyprofessions

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const herbProtocolTarget uint64 = 0xf11000012300002a

func herbProtocolPacked(guid uint64) []byte {
	p := []byte{0}
	for i := uint(0); i < 8; i++ {
		if b := byte(guid >> (8 * i)); b != 0 {
			p[0] |= 1 << i
			p = append(p, b)
		}
	}
	return p
}

// Independent explicit GAMEOBJECT receipt: unpacked GO hit, then target mask
// 0x800 and the packed target GUID. UNIT2 is not this spell's target form.
func herbProtocolCastFixture(goEvent bool, caster, target uint64, flags uint32) []byte {
	p := herbProtocolPacked(caster)
	p = append(p, herbProtocolPacked(caster)...)
	p = append(p, 9)
	p = binary.LittleEndian.AppendUint32(p, 2366)
	p = binary.LittleEndian.AppendUint32(p, flags)
	p = binary.LittleEndian.AppendUint32(p, 1500)
	if goEvent {
		p = append(p, 1)
		p = binary.LittleEndian.AppendUint64(p, target)
		p = append(p, 0)
	}
	p = binary.LittleEndian.AppendUint32(p, 0x800)
	p = append(p, herbProtocolPacked(target)...)
	if flags&0x800 != 0 {
		p = binary.LittleEndian.AppendUint32(p, 25)
	}
	return p
}

func herbProtocolRejectCastPrefixes(t *testing.T, code uint32, p []byte) {
	t.Helper()
	for n := range len(p) {
		if _, err := decodeHerbCast(code, p[:n]); err == nil {
			t.Fatalf("accepted cast prefix%d/%d", n, len(p))
		}
	}
	if _, err := decodeHerbCast(code, append(append([]byte{}, p...), 0)); err == nil {
		t.Fatal("accepted trailing cast byte")
	}
}

func TestHerbProtocolExplicitGameObjectRequestGoldens(t *testing.T) {
	if got := hex.EncodeToString(herbCastPacket(9, herbProtocolTarget)); got != "093e0900000000080000d92a230110f1" {
		t.Fatal("fixed2366 GOtarget packet", got)
	}
	if got := hex.EncodeToString(herbQueryPacket(291, herbProtocolTarget)); got != "230100002a000023010010f1" {
		t.Fatal("normal fullGUID query packet", got)
	}
}

func TestHerbProtocolExplicitGoStartAndGoReceipts(t *testing.T) {
	for _, goEvent := range []bool{false, true} {
		for _, flags := range []uint32{0, 1, 2, 0x100, 0x800, 0x40000, 0x40903} {
			t.Run(fmt.Sprintf("go%t_flags%x", goEvent, flags), func(t *testing.T) {
				code, kind := uint32(opcode.SMSGSpellStart), "start"
				if goEvent {
					code, kind = opcode.SMSGSpellGo, "go"
				}
				p := herbProtocolCastFixture(goEvent, 7, herbProtocolTarget, flags)
				e, err := decodeHerbCast(code, p)
				if err != nil || e.kind != kind || e.caster != 7 || e.target != herbProtocolTarget || e.count != 9 || e.spell != 2366 || e.failure != 0 {
					t.Fatal(e, err)
				}
				herbProtocolRejectCastPrefixes(t, code, p)
			})
		}
	}
	if e, err := decodeHerbCast(opcode.SMSGSpellGo, herbProtocolCastFixture(true, 8, herbProtocolTarget, 0x100)); err != nil || e.caster != 8 {
		t.Fatal("foreign real caster was rewritten", e, err)
	}
	for bit := uint(0); bit < 32; bit++ {
		flag := uint32(1) << bit
		if flag&(1|2|0x100|0x800|0x40000) != 0 {
			continue
		}
		if _, err := decodeHerbCast(opcode.SMSGSpellGo, herbProtocolCastFixture(true, 7, herbProtocolTarget, flag)); err == nil {
			t.Fatalf("unsupported flag%x", flag)
		}
	}
	for _, name := range []string{"no_hits", "two_hits", "miss", "different_hit", "unit_mask", "no_mask", "unit_target", "item_target", "zero_target", "zero_go_counter", "cast_item"} {
		t.Run(name, func(t *testing.T) {
			p := herbProtocolCastFixture(true, 7, herbProtocolTarget, 0x100)
			switch name {
			case "no_hits":
				p[17] = 0
			case "two_hits":
				p[17] = 2
			case "miss":
				p[26] = 1
			case "different_hit":
				binary.LittleEndian.PutUint64(p[18:], herbProtocolTarget+1)
			case "unit_mask":
				binary.LittleEndian.PutUint32(p[27:], 2)
			case "no_mask":
				binary.LittleEndian.PutUint32(p[27:], 0)
			case "unit_target":
				p = herbProtocolCastFixture(true, 7, 0xf13000012300002a, 0x100)
			case "item_target":
				p = herbProtocolCastFixture(true, 7, 0x400000000000002a, 0x100)
			case "zero_target":
				p = herbProtocolCastFixture(true, 7, 0, 0x100)
			case "zero_go_counter":
				p = herbProtocolCastFixture(true, 7, 0xf110000123000000, 0x100)
			case "cast_item":
				p[1] = 8
			}
			if _, err := decodeHerbCast(opcode.SMSGSpellGo, p); err == nil {
				t.Fatal("unsupported target/hit shape accepted", name)
			}
		})
	}
}

func TestHerbProtocolFailureMetadataHasNoTotemsOrCategoryTail(t *testing.T) {
	for _, reason := range []byte{12, 129, 130, 131} {
		p := []byte{9, 0x3e, 9, 0, 0, reason}
		e, err := decodeHerbCast(opcode.SMSGCastFailed, p)
		if err != nil || e.kind != "failed" || e.spell != 2366 || e.count != 9 || e.failure != reason {
			t.Fatal(e, err)
		}
		herbProtocolRejectCastPrefixes(t, opcode.SMSGCastFailed, p)
	}
	for _, reason := range []byte{130, 131} {
		if _, err := decodeHerbCast(opcode.SMSGCastFailed, []byte{9, 0x3e, 9, 0, 0, reason, 166, 0, 0, 0}); err == nil {
			t.Fatal("invented totem metadata accepted", reason)
		}
	}
	for _, reason := range []byte{0, 188, 255} {
		if _, err := decodeHerbCast(opcode.SMSGCastFailed, []byte{9, 0x3e, 9, 0, 0, reason}); err == nil {
			t.Fatal("invalid failure", reason)
		}
	}
	p := append(herbProtocolPacked(7), 9, 0x3e, 9, 0, 0, 130)
	if e, err := decodeHerbCast(opcode.SMSGSpellFailure, p); err != nil || e.kind != "failed" || e.caster != 7 || e.spell != 2366 || e.failure != 130 {
		t.Fatal(e, err)
	}
	herbProtocolRejectCastPrefixes(t, opcode.SMSGSpellFailure, p)
}

func herbProtocolLootFixture(n int) []byte {
	p := binary.LittleEndian.AppendUint64(nil, herbProtocolTarget)
	p = append(p, 6)
	p = binary.LittleEndian.AppendUint32(p, 0)
	p = append(p, byte(n))
	for i := 0; i < n; i++ {
		row := make([]byte, 22)
		row[0] = byte(i)
		binary.LittleEndian.PutUint32(row[1:], 2447+uint32(i))
		binary.LittleEndian.PutUint32(row[5:], 2)
		if i%2 == 1 {
			row[21] = 4
		}
		p = append(p, row...)
	}
	return p
}

func TestHerbProtocolLootNormalOwnedSlotsOnly(t *testing.T) {
	for _, n := range []int{0, 1, 16} {
		p := herbProtocolLootFixture(n)
		w, err := decodeHerbLoot(p)
		if err != nil || w.guid != herbProtocolTarget || w.gold != 0 || len(w.slots) != n {
			t.Fatal(n, w, err)
		}
		for i, row := range w.slots {
			if row.slot != byte(i) || row.entry != 2447+uint32(i) || row.count != 2 {
				t.Fatal(row)
			}
		}
		for length := range len(p) {
			if _, err := decodeHerbLoot(p[:length]); err == nil {
				t.Fatal("partial herb loot", n, length)
			}
		}
		if _, err := decodeHerbLoot(append(append([]byte{}, p...), 0)); err == nil {
			t.Fatal("trailing herb loot")
		}
	}
	for _, name := range []string{"corpse_guid", "item_guid", "zero_guid", "normal_loot_type", "gold", "rolled", "locked", "duplicate_slot", "zero_entry", "zero_count", "seventeen_slots"} {
		t.Run(name, func(t *testing.T) {
			p := herbProtocolLootFixture(2)
			switch name {
			case "corpse_guid":
				binary.LittleEndian.PutUint64(p, 0xf13000012300002a)
			case "item_guid":
				binary.LittleEndian.PutUint64(p, 0x400000000000002a)
			case "zero_guid":
				binary.LittleEndian.PutUint64(p, 0)
			case "normal_loot_type":
				p[8] = 1
			case "gold":
				binary.LittleEndian.PutUint32(p[9:], 1)
			case "rolled":
				p[35] = 1
			case "locked":
				p[35] = 2
			case "duplicate_slot":
				p[36] = 0
			case "zero_entry":
				binary.LittleEndian.PutUint32(p[15:], 0)
			case "zero_count":
				binary.LittleEndian.PutUint32(p[19:], 0)
			case "seventeen_slots":
				p = herbProtocolLootFixture(17)
			}
			if _, err := decodeHerbLoot(p); err == nil {
				t.Fatal("unsafe herb loot accepted", name)
			}
		})
	}
}

func herbProtocolTemplateFixture(lock uint32, name string) ([]byte, int) {
	p := binary.LittleEndian.AppendUint32(nil, 291)
	p = binary.LittleEndian.AppendUint32(p, 3)
	p = binary.LittleEndian.AppendUint32(p, 100)
	p = append(p, []byte(name)...)
	p = append(p, 0, 0, 0, 0, 0, 0, 0)
	offset := len(p)
	data := [24]uint32{0: lock, 1: 12345, 3: 1, 4: 17, 5: 19, 10: 1, 12: 1, 13: 1, 14: 123, 16: 1}
	for _, word := range data {
		p = binary.LittleEndian.AppendUint32(p, word)
	}
	p = binary.LittleEndian.AppendUint32(p, math.Float32bits(1))
	p = append(p, make([]byte, 6*4)...)
	return p, offset
}

func TestHerbProtocolVerifiedPureHerbLockSubsetAndActualData(t *testing.T) {
	for lock, minimum := range map[uint32]uint32{29: 0, 30: 15, 8: 25, 9: 50, 31: 70, 10: 75} {
		for _, name := range []string{"Peacebloom", "Ore Vein", "Chest"} {
			p, offset := herbProtocolTemplateFixture(lock, name)
			m, err := decodeHerbTemplate(p)
			if err != nil || m.entry != 291 || m.name != name || m.lockID != lock || m.requiredSkill != minimum {
				t.Fatal(lock, name, m, err)
			}
			for i, word := range m.data {
				if word != binary.LittleEndian.Uint32(p[offset+4*i:]) {
					t.Fatal("actual24word metadata not retained", i, word)
				}
			}
		}
	}
	for _, lock := range []uint32{0, 1, 2, 3, 7, 11, 28, 32, 259, 1702, 1714, 0xffffffff} {
		p, _ := herbProtocolTemplateFixture(lock, "Peacebloom")
		if _, err := decodeHerbTemplate(p); err == nil {
			t.Fatal("unknown/nonherb/mixed lock accepted by display name", lock)
		}
	}
	// Deprecated scalar fields4/5 are not boolean flags and cannot be read as
	// skill requirements. Only the verified Lock.dbc row authorizes the skill.
	p, offset := herbProtocolTemplateFixture(29, "Herb")
	binary.LittleEndian.PutUint32(p[offset+4*4:], math.MaxUint32)
	binary.LittleEndian.PutUint32(p[offset+5*4:], math.MaxUint32)
	if m, err := decodeHerbTemplate(p); err != nil || m.requiredSkill != 0 {
		t.Fatal("deprecated scalar fields misclassified", m, err)
	}
}

func TestHerbProtocolChestTemplateRejectsMixedOrSideEffectMetadata(t *testing.T) {
	for _, tc := range []struct {
		name        string
		word, value uint32
	}{
		{"loot_absent", 1, 0}, {"restock", 2, 1}, {"not_consumable", 3, 0}, {"invalid_consumable", 3, 2}, {"event", 6, 1}, {"linked_trap", 7, 1}, {"quest", 8, 1}, {"los_boolean", 10, 2}, {"leave_loot", 11, 1}, {"combat_boolean", 12, 2}, {"log_boolean", 13, 2}, {"group_loot", 15, 1}, {"tooltip_boolean", 16, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, offset := herbProtocolTemplateFixture(29, "Herb")
			binary.LittleEndian.PutUint32(p[offset+int(tc.word)*4:], tc.value)
			if _, err := decodeHerbTemplate(p); err == nil {
				t.Fatal("unsafe chest data accepted", tc.name)
			}
		})
	}
	for i := 17; i < 24; i++ {
		p, offset := herbProtocolTemplateFixture(29, "Herb")
		binary.LittleEndian.PutUint32(p[offset+i*4:], 1)
		if _, err := decodeHerbTemplate(p); err == nil {
			t.Fatal("unknown extension data accepted", i)
		}
	}
	for i := 0; i < 6; i++ {
		p, offset := herbProtocolTemplateFixture(29, "Herb")
		binary.LittleEndian.PutUint32(p[offset+24*4+4+i*4:], 1)
		if _, err := decodeHerbTemplate(p); err == nil {
			t.Fatal("quest item metadata accepted", i)
		}
	}
	for _, word := range []int{10, 12, 13, 16} {
		p, offset := herbProtocolTemplateFixture(29, "Herb")
		binary.LittleEndian.PutUint32(p[offset+word*4:], 0)
		if _, err := decodeHerbTemplate(p); err != nil {
			t.Fatal("canonicalfalse chest boolean rejected", word, err)
		}
	}
}

func TestHerbProtocolChestTemplateCompleteBodyAndStringBounds(t *testing.T) {
	p, offset := herbProtocolTemplateFixture(29, "Herb")
	for n := range len(p) {
		if _, err := decodeHerbTemplate(p[:n]); err == nil {
			t.Fatal("partial completeGO query accepted", n)
		}
	}
	if _, err := decodeHerbTemplate(append(append([]byte{}, p...), 0)); err == nil {
		t.Fatal("trailing GOquery bytes")
	}
	for _, kind := range []uint32{0, 1, 8, 17, 19} {
		q := append([]byte{}, p...)
		binary.LittleEndian.PutUint32(q[4:], kind)
		if _, err := decodeHerbTemplate(q); err == nil {
			t.Fatal("nonchest type accepted", kind)
		}
	}
	for _, name := range []string{"entry_zero", "entry_missing", "name_empty", "name_utf8", "alternate_name", "name_oversize", "scale_zero", "scale_negative", "scale_nan", "scale_infinite", "packet_oversize"} {
		t.Run(name, func(t *testing.T) {
			q := append([]byte{}, p...)
			switch name {
			case "entry_zero":
				binary.LittleEndian.PutUint32(q, 0)
			case "entry_missing":
				binary.LittleEndian.PutUint32(q, 0x80000123)
			case "name_empty":
				q, _ = herbProtocolTemplateFixture(29, "")
			case "name_utf8":
				q[12] = 0xff
			case "alternate_name":
				q[12+len("Herb")+1] = 1
			case "name_oversize":
				q, _ = herbProtocolTemplateFixture(29, strings.Repeat("x", 4097))
			case "scale_zero":
				binary.LittleEndian.PutUint32(q[offset+24*4:], 0)
			case "scale_negative":
				binary.LittleEndian.PutUint32(q[offset+24*4:], math.Float32bits(-1))
			case "scale_nan":
				binary.LittleEndian.PutUint32(q[offset+24*4:], math.Float32bits(float32(math.NaN())))
			case "scale_infinite":
				binary.LittleEndian.PutUint32(q[offset+24*4:], math.Float32bits(float32(math.Inf(1))))
			case "packet_oversize":
				q = make([]byte, 12+3+4*4097+24*4+4+6*4+1)
			}
			if _, err := decodeHerbTemplate(q); err == nil {
				t.Fatal("malformed herb template accepted", name)
			}
		})
	}
	q, _ := herbProtocolTemplateFixture(29, strings.Repeat("x", 4096))
	if m, err := decodeHerbTemplate(q); err != nil || len(m.name) != 4096 {
		t.Fatal("bounded4096byte name rejected", len(m.name), err)
	}
}
