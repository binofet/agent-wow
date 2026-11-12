package buddyprofessions

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const skinProtocolTarget uint64 = 0xf13000012300002a

// Independent 3.3.5a explicit-UNIT receipt fixture. GO target hits are unpacked;
// SpellCastTargets uses the packed object GUID after its UNIT2 target mask.
func skinProtocolCastFixture(goEvent bool, caster, target uint64, flags uint32) []byte {
	p := protocolPackedFixture(caster)
	p = append(p, protocolPackedFixture(caster)...)
	p = append(p, 9)
	p = binary.LittleEndian.AppendUint32(p, 8613)
	p = binary.LittleEndian.AppendUint32(p, flags)
	p = binary.LittleEndian.AppendUint32(p, 1500)
	if goEvent {
		p = append(p, 1)
		p = binary.LittleEndian.AppendUint64(p, target)
		p = append(p, 0)
	}
	p = binary.LittleEndian.AppendUint32(p, 2)
	p = append(p, protocolPackedFixture(target)...)
	if flags&0x800 != 0 {
		p = binary.LittleEndian.AppendUint32(p, 25)
	}
	return p
}

func skinProtocolRejectCastPrefixes(t *testing.T, code uint32, p []byte) {
	t.Helper()
	for n := range len(p) {
		if _, err := decodeSkinCast(code, p[:n]); err == nil {
			t.Fatalf("accepted cast prefix%d/%d", n, len(p))
		}
	}
	if _, err := decodeSkinCast(code, append(append([]byte{}, p...), 0)); err == nil {
		t.Fatal("accepted trailing cast byte")
	}
}

func TestSkinProtocolExplicitRequestGoldens(t *testing.T) {
	if got := hex.EncodeToString(skinCastPacket(9, skinProtocolTarget)); got != "09a52100000002000000d92a230130f1" {
		t.Fatal("explicit UNIT skin8613 request", got)
	}
	if got := hex.EncodeToString(skinQueryPacket(291, skinProtocolTarget)); got != "230100002a000023010030f1" {
		t.Fatal("normal creature query request", got)
	}
}

func TestSkinProtocolTargetBoundStartAndGoStrictBodies(t *testing.T) {
	for _, goEvent := range []bool{false, true} {
		for _, flags := range []uint32{0, 1, 2, 0x100, 0x800, 0x40000, 0x40903} {
			t.Run(fmt.Sprintf("go%t_flags%x", goEvent, flags), func(t *testing.T) {
				code, kind := uint32(opcode.SMSGSpellStart), "start"
				if goEvent {
					code, kind = opcode.SMSGSpellGo, "go"
				}
				p := skinProtocolCastFixture(goEvent, 7, skinProtocolTarget, flags)
				e, err := decodeSkinCast(code, p)
				if err != nil || e.kind != kind || e.caster != 7 || e.target != skinProtocolTarget || e.count != 9 || e.spell != 8613 || e.failure != 0 {
					t.Fatal(e, err)
				}
				skinProtocolRejectCastPrefixes(t, code, p)
			})
		}
	}
	// A foreign real caster remains foreign; the controller must bind it to the
	// pending actor, rather than substituting the expected actor into a receipt.
	if e, err := decodeSkinCast(opcode.SMSGSpellGo, skinProtocolCastFixture(true, 8, skinProtocolTarget, 0x100)); err != nil || e.caster != 8 {
		t.Fatal(e, err)
	}
	for bit := uint(0); bit < 32; bit++ {
		flag := uint32(1) << bit
		if flag&(1|2|0x100|0x800|0x40000) != 0 {
			continue
		}
		if _, err := decodeSkinCast(opcode.SMSGSpellGo, skinProtocolCastFixture(true, 7, skinProtocolTarget, flag)); err == nil {
			t.Fatalf("accepted unsupported cast flag%x", flag)
		}
	}
	for _, name := range []string{"zero_hits", "two_hits", "miss", "wrong_hit", "wrong_mask", "zero_target", "player_target", "cast_item"} {
		t.Run(name, func(t *testing.T) {
			p := skinProtocolCastFixture(true, 7, skinProtocolTarget, 0x100)
			// Two packed caster GUIDs occupy4bytes, then count1+spell4+flags4+clock4.
			switch name {
			case "zero_hits":
				p[17] = 0
			case "two_hits":
				p[17] = 2
			case "miss":
				p[26] = 1
			case "wrong_hit":
				binary.LittleEndian.PutUint64(p[18:], skinProtocolTarget+1)
			case "wrong_mask":
				binary.LittleEndian.PutUint32(p[27:], 0x42)
			case "zero_target":
				p = append(p[:31], 0)
			case "player_target":
				p = append(p[:31], protocolPackedFixture(3)...)
			case "cast_item":
				p[1] = 8
			}
			if _, err := decodeSkinCast(opcode.SMSGSpellGo, p); err == nil {
				t.Fatal("accepted unsupported/foreign target receipt", name)
			}
		})
	}
}

func TestSkinProtocolFailureConditionalTotemCategoryAndInterrupts(t *testing.T) {
	for _, tc := range []struct {
		reason byte
		tail   []byte
	}{{29, make([]byte, 8)}, {54, make([]byte, 4)}, {130, []byte{166, 0, 0, 0}}, {131, nil}, {129, nil}, {12, nil}} {
		p := []byte{9, 0xa5, 0x21, 0, 0, tc.reason}
		p = append(p, tc.tail...)
		e, err := decodeSkinCast(opcode.SMSGCastFailed, p)
		if err != nil || e.kind != "failed" || e.count != 9 || e.spell != 8613 || e.failure != tc.reason {
			t.Fatal(e, err)
		}
		skinProtocolRejectCastPrefixes(t, opcode.SMSGCastFailed, p)
	}
	if _, err := decodeSkinCast(opcode.SMSGCastFailed, []byte{9, 0xa5, 0x21, 0, 0, 130}); err == nil {
		t.Fatal("missing totem category166 payload accepted")
	}
	if _, err := decodeSkinCast(opcode.SMSGCastFailed, []byte{9, 0xa5, 0x21, 0, 0, 131, 0, 0, 0, 0}); err == nil {
		t.Fatal("TOTEMS131 invented item-totem payload")
	}
	for _, reason := range []byte{0, 188, 255} {
		if _, err := decodeSkinCast(opcode.SMSGCastFailed, []byte{9, 0xa5, 0x21, 0, 0, reason}); err == nil {
			t.Fatal("invalid failure accepted", reason)
		}
	}
	interrupt := append(protocolPackedFixture(7), 9, 0xa5, 0x21, 0, 0, 130)
	if e, err := decodeSkinCast(opcode.SMSGSpellFailure, interrupt); err != nil || e.caster != 7 || e.kind != "failed" || e.failure != 130 {
		t.Fatal(e, err)
	}
	skinProtocolRejectCastPrefixes(t, opcode.SMSGSpellFailure, interrupt)
	if _, err := decodeSkinCast(opcode.SMSGSpellFailure, append(interrupt, 166, 0, 0, 0)); err == nil {
		t.Fatal("interrupted packet accepted CastFailed-only reason tail")
	}
}

func skinProtocolLootFixture(kind byte, n int) []byte {
	p := binary.LittleEndian.AppendUint64(nil, skinProtocolTarget)
	p = append(p, kind)
	p = binary.LittleEndian.AppendUint32(p, 0)
	p = append(p, byte(n))
	for i := 0; i < n; i++ {
		row := make([]byte, 22)
		row[0] = byte(i)
		binary.LittleEndian.PutUint32(row[1:], 2318+uint32(i))
		binary.LittleEndian.PutUint32(row[5:], 2)
		if i%2 != 0 {
			row[21] = 4
		}
		p = append(p, row...)
	}
	return p
}

func TestSkinProtocolNormalAndSkinLootStrictActualSlots(t *testing.T) {
	for _, kind := range []byte{1, 6} {
		decode := decodeNormalCorpseLoot
		if kind == 6 {
			decode = decodeSkinLoot
		}
		for _, n := range []int{0, 1, 16} {
			p := skinProtocolLootFixture(kind, n)
			w, err := decode(p)
			if err != nil || w.guid != skinProtocolTarget || w.gold != 0 || len(w.slots) != n {
				t.Fatal(kind, n, w, err)
			}
			for i, row := range w.slots {
				if row.slot != byte(i) || row.entry != 2318+uint32(i) || row.count != 2 {
					t.Fatal(row)
				}
			}
			for length := range len(p) {
				if _, err := decode(p[:length]); err == nil {
					t.Fatal("partial corpse window accepted", kind, n, length)
				}
			}
			if _, err := decode(append(append([]byte{}, p...), 0)); err == nil {
				t.Fatal("trailing corpse window")
			}
		}
	}
	p := skinProtocolLootFixture(1, 0)
	binary.LittleEndian.PutUint32(p[9:], 123)
	if w, err := decodeNormalCorpseLoot(p); err != nil || w.gold != 123 {
		t.Fatal("normal corpse money lost", w, err)
	}
	p[8] = 6
	if _, err := decodeSkinLoot(p); err == nil {
		t.Fatal("skinning loot accepted gold")
	}
	for _, name := range []string{"wrong_type", "locked", "rolled", "duplicate", "zero_entry", "zero_count", "oversized_stack", "foreign_guid", "zero_guid", "seventeen_rows"} {
		t.Run(name, func(t *testing.T) {
			p := skinProtocolLootFixture(6, 2)
			switch name {
			case "wrong_type":
				p[8] = 3
			case "locked":
				p[14+21] = 2
			case "rolled":
				p[14+21] = 1
			case "duplicate":
				p[14+22] = 0
			case "zero_entry":
				binary.LittleEndian.PutUint32(p[15:], 0)
			case "zero_count":
				binary.LittleEndian.PutUint32(p[19:], 0)
			case "oversized_stack":
				binary.LittleEndian.PutUint32(p[19:], 1001)
			case "foreign_guid":
				binary.LittleEndian.PutUint64(p, 3)
			case "zero_guid":
				binary.LittleEndian.PutUint64(p, 0)
			case "seventeen_rows":
				p = skinProtocolLootFixture(6, 17)
			}
			if _, err := decodeSkinLoot(p); err == nil {
				t.Fatal("unsafe skin loot accepted", name)
			}
		})
	}
}

func skinProtocolTemplateFixture(name, title, icon string) []byte {
	p := binary.LittleEndian.AppendUint32(nil, 291)
	p = append(p, []byte(name)...)
	p = append(p, 0, 0, 0, 0)
	p = append(p, []byte(title)...)
	p = append(p, 0)
	p = append(p, []byte(icon)...)
	p = append(p, 0)
	for _, word := range []uint32{0x4000, 1, 0, 0, 0, 0, 100, 0, 0, 0} {
		p = binary.LittleEndian.AppendUint32(p, word)
	}
	p = binary.LittleEndian.AppendUint32(p, math.Float32bits(1))
	p = binary.LittleEndian.AppendUint32(p, math.Float32bits(1))
	p = append(p, 0)
	return append(p, make([]byte, 7*4)...)
}

func TestSkinProtocolCreatureTemplateExactPublicShapeAndBounds(t *testing.T) {
	p := skinProtocolTemplateFixture("Dire Wolf", "", "")
	m, err := decodeSkinTemplate(p)
	if err != nil || m.entry != 291 || m.name != "Dire Wolf" || m.typeFlags != 0x4000 || m.creatureType != 1 {
		t.Fatal(m, err)
	}
	for n := range len(p) {
		if _, err := decodeSkinTemplate(p[:n]); err == nil {
			t.Fatal("partial creature query accepted", n)
		}
	}
	if _, err := decodeSkinTemplate(append(append([]byte{}, p...), 0)); err == nil {
		t.Fatal("trailing creature query")
	}
	if m, err := decodeSkinTemplate(skinProtocolTemplateFixture(strings.Repeat("a", 4096), "", "")); err != nil || len(m.name) != 4096 {
		t.Fatal("bounded4096byte name", len(m.name), err)
	}
	for _, name := range []string{"missing_name", "overlong_name", "invalid_utf8", "alternate_name", "entry_missing", "entry_unknown", "type_missing", "type_unknown", "nan_health", "infinite_mana", "negative_health", "leader_invalid", "oversize_packet"} {
		t.Run(name, func(t *testing.T) {
			q := append([]byte{}, p...)
			fixed := 4 + len("Dire Wolf") + 1 + 3 + 1 + 1
			switch name {
			case "missing_name":
				q = skinProtocolTemplateFixture("", "", "")
			case "overlong_name":
				q = skinProtocolTemplateFixture(strings.Repeat("a", 4097), "", "")
			case "invalid_utf8":
				q[4] = 0xff
			case "alternate_name":
				q[4+len("Dire Wolf")+1] = 1
			case "entry_missing":
				binary.LittleEndian.PutUint32(q, 0)
			case "entry_unknown":
				binary.LittleEndian.PutUint32(q, 0x80000123)
			case "type_missing":
				binary.LittleEndian.PutUint32(q[fixed+4:], 0)
			case "type_unknown":
				binary.LittleEndian.PutUint32(q[fixed+4:], 14)
			case "nan_health":
				binary.LittleEndian.PutUint32(q[fixed+40:], math.Float32bits(float32(math.NaN())))
			case "infinite_mana":
				binary.LittleEndian.PutUint32(q[fixed+44:], math.Float32bits(float32(math.Inf(1))))
			case "negative_health":
				binary.LittleEndian.PutUint32(q[fixed+40:], math.Float32bits(-1))
			case "leader_invalid":
				q[fixed+48] = 2
			case "oversize_packet":
				q = bytes.Repeat([]byte{'x'}, 3*4097+81)
			}
			if _, err := decodeSkinTemplate(q); err == nil {
				t.Fatal("invalid creature template accepted", name)
			}
		})
	}
}
