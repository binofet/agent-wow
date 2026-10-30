package buddyprofessions

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func protocolCastFixture(goEvent bool, caster uint64, spell, flags, mask uint32) []byte {
	p := protocolPackedFixture(caster)
	p = append(p, protocolPackedFixture(caster)...)
	p = append(p, 9)
	p = binary.LittleEndian.AppendUint32(p, spell)
	p = binary.LittleEndian.AppendUint32(p, flags)
	p = binary.LittleEndian.AppendUint32(p, 1500)
	if goEvent {
		p = append(p, 1)
		p = binary.LittleEndian.AppendUint64(p, caster)
		p = append(p, 0) // No misses.
	}
	p = binary.LittleEndian.AppendUint32(p, mask)
	if mask&2 != 0 {
		p = append(p, 0) // Server-added UNIT flag, empty explicit object GUID.
	}
	if mask&0x40 != 0 {
		p = append(p, 0) // No destination transport.
		for _, v := range []float32{12.5, -31.25, 9} {
			p = binary.LittleEndian.AppendUint32(p, math.Float32bits(v))
		}
	}
	if flags&0x800 != 0 {
		p = binary.LittleEndian.AppendUint32(p, 25)
	}
	if goEvent && mask&0x40 != 0 {
		p = append(p, 0)
	}
	return p
}

func protocolPackedFixture(guid uint64) []byte {
	mask := byte(0)
	body := []byte{}
	for i := uint(0); i < 8; i++ {
		if b := byte(guid >> (8 * i)); b != 0 {
			mask |= 1 << i
			body = append(body, b)
		}
	}
	return append([]byte{mask}, body...)
}

func protocolFailureFixture(reason byte, tail int) []byte {
	p := []byte{9, 0xc4, 0x1d, 0, 0, reason} // Fishing7620.
	return append(p, make([]byte, tail)...)
}

func TestProtocolRequestGoldenBytes(t *testing.T) {
	if got := castPacket(9, 7620); !bytes.Equal(got, []byte{9, 0xc4, 0x1d, 0, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("implicit-self cast = %x", got)
	}
	const guid = uint64(0x1020304050607080)
	wantGUID := []byte{0x80, 0x70, 0x60, 0x50, 0x40, 0x30, 0x20, 0x10}
	if got := gameObjectUsePacket(guid); !bytes.Equal(got, wantGUID) {
		t.Fatalf("game object use = %x", got)
	}
	if got := releaseLootPacket(guid); !bytes.Equal(got, wantGUID) {
		t.Fatalf("loot release = %x", got)
	}
	if got := storeLootPacket(255); !bytes.Equal(got, []byte{255}) {
		t.Fatalf("store slot = %x", got)
	}
	if got := cancelChannelPacket(7620); !bytes.Equal(got, []byte{0xc4, 0x1d, 0, 0}) {
		t.Fatalf("cancel channel = %x", got)
	}
}

func TestProtocolCastReceipts(t *testing.T) {
	for _, tc := range []struct {
		name        string
		op          uint32
		goEvent     bool
		spell, flag uint32
		mask        uint32
		kind        string
		duration    uint32
	}{
		{"bandage start", opcode.SMSGSpellStart, false, 3275, 2, 0, "start", 1500},
		{"cooking go", opcode.SMSGSpellGo, true, 2538, 0x40100, 0, "go", 0},
		{"implicit caster go", opcode.SMSGSpellGo, true, 3275, 0x40100, 2, "go", 0},
		{"power start", opcode.SMSGSpellStart, false, 2540, 0x802, 0, "start", 1500},
		{"fishing start", opcode.SMSGSpellStart, false, 7620, 2, 0, "start", 1500},
		{"fishing destination", opcode.SMSGSpellGo, true, 7620, 0x40900, 0x40, "go", 0},
		{"fishing caster and destination", opcode.SMSGSpellGo, true, 7620, 0x40100, 0x42, "go", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := protocolCastFixture(tc.goEvent, 0x0100000000000007, tc.spell, tc.flag, tc.mask)
			want := castEvent{kind: tc.kind, caster: 0x0100000000000007, count: 9, spell: tc.spell, durationMs: tc.duration}
			if got, err := decodeCast(tc.op, p); err != nil || got != want {
				t.Fatalf("receipt = %+v, %v; want %+v", got, err, want)
			}
			protocolRejectCastPrefixes(t, tc.op, p)
		})
	}
}

func protocolRejectCastPrefixes(t *testing.T, op uint32, p []byte) {
	t.Helper()
	for i := range p {
		if got, err := decodeCast(op, p[:i]); err == nil || got != (castEvent{}) {
			t.Fatalf("accepted partial length%d: %+v, %v", i, got, err)
		}
	}
	if got, err := decodeCast(op, append(append([]byte{}, p...), 0)); err == nil || got != (castEvent{}) {
		t.Fatalf("accepted trailing byte: %+v, %v", got, err)
	}
}

func TestProtocolCastPreservesBindingFields(t *testing.T) {
	p := protocolCastFixture(false, 8, 3275, 2, 0)
	p[1] = 7 // Cast-item/source GUID differs from the real caster in GUID2.
	got, err := decodeCast(opcode.SMSGSpellStart, p)
	if err != nil || got.caster != 8 || got.count != 9 || got.spell != 3275 {
		t.Fatalf("real caster/count/spell lost: %+v %v", got, err)
	}
	// Decode does not manufacture the expected actor7/count10/spell7620. The
	// controller must reject these mismatches against its pending attempt.
	if got.caster == 7 || got.count == 10 || got.spell == 7620 {
		t.Fatalf("receipt rewritten to expected identity: %+v", got)
	}
}

func TestProtocolCastRejectsUnsupportedFlagsAndTargets(t *testing.T) {
	for bit := uint(0); bit < 32; bit++ {
		flag := uint32(1) << bit
		if flag&(0x1|0x2|0x100|0x800|0x40000) != 0 {
			continue
		}
		for _, goEvent := range []bool{false, true} {
			op := uint32(opcode.SMSGSpellStart)
			if goEvent {
				op = opcode.SMSGSpellGo
			}
			if _, err := decodeCast(op, protocolCastFixture(goEvent, 7, 7620, flag, 0)); err == nil {
				t.Fatalf("accepted extension flag%x go=%v", flag, goEvent)
			}
		}
	}
	for _, mask := range []uint32{1, 2, 0x20, 0x42, 0x100, 0xffffffff} {
		p := protocolCastFixture(false, 7, 7620, 2, mask)
		p = append(p, 1, 7) // Unit packed GUID cannot make mask2 an implicit cast.
		if _, err := decodeCast(opcode.SMSGSpellStart, p); err == nil {
			t.Fatalf("accepted target mask%x", mask)
		}
	}
	p := protocolCastFixture(false, 7, 7620, 2, 0)
	if _, err := decodeCast(opcode.SMSGSpellStart, append(p, 1, 7)); err == nil {
		t.Fatal("accepted a unit GUID after target mask0")
	}
	// A server-added UNIT flag has an empty explicit GUID. A foreign unit or
	// an explicit self GUID is not substituted for that implicit target.
	for _, target := range []byte{7, 8} {
		p := protocolCastFixture(true, 7, 3275, 0x100, 2)
		p = append(p[:len(p)-1], 1, target)
		if _, err := decodeCast(opcode.SMSGSpellGo, p); err == nil {
			t.Fatalf("accepted explicit target%d in implicit starter receipt", target)
		}
	}
	if _, err := decodeCast(0xffffffff, nil); err == nil {
		t.Fatal("accepted unknown opcode")
	}
}

func TestProtocolFishingDestinationValidation(t *testing.T) {
	p := protocolCastFixture(true, 7, 7620, 0x100, 0x40)
	// Two packed GUIDs(4), count/spell/flags/time(13), hit list(10), mask(4).
	const transportOffset = 31
	if p[transportOffset] != 0 {
		t.Fatal("bad destination fixture")
	}
	transport := append([]byte{}, p[:transportOffset]...)
	transport = append(transport, 1, 8)
	transport = append(transport, p[transportOffset+1:]...)
	if _, err := decodeCast(opcode.SMSGSpellGo, transport); err == nil {
		t.Fatal("accepted destination on an unsupported transport")
	}
	for _, bits := range []uint32{0x7fc00000, 0x7f800000, 0xff800000} {
		bad := append([]byte{}, p...)
		binary.LittleEndian.PutUint32(bad[transportOffset+1:], bits)
		if _, err := decodeCast(opcode.SMSGSpellGo, bad); err == nil {
			t.Fatal("accepted nonfinite destination")
		}
	}
	bad := append([]byte{}, p...)
	bad[len(bad)-1] = 1
	if _, err := decodeCast(opcode.SMSGSpellGo, bad); err == nil {
		t.Fatal("accepted nonzero destination tail")
	}
}

func TestProtocolGoMissAndReflectFraming(t *testing.T) {
	p := protocolCastFixture(true, 7, 7620, 0x100, 0)
	// Replace zero misses after one hit with one reflected target.
	prefix := append([]byte{}, p[:26]...)
	prefix = append(prefix, 1)
	prefix = binary.LittleEndian.AppendUint64(prefix, 8)
	prefix = append(prefix, 11, 0)
	p = append(prefix, p[27:]...)
	if _, err := decodeCast(opcode.SMSGSpellGo, p); err != nil {
		t.Fatalf("valid reflected miss rejected: %v", err)
	}
	protocolRejectCastPrefixes(t, opcode.SMSGSpellGo, p)
	for _, offset := range []int{35, 36} {
		bad := append([]byte{}, p...)
		bad[offset] = 12
		if _, err := decodeCast(opcode.SMSGSpellGo, bad); err == nil {
			t.Fatalf("accepted invalid miss/reflected result at%d", offset)
		}
	}
}

func TestProtocolFailureReasonTails(t *testing.T) {
	for _, tc := range []struct {
		reason byte
		tail   int
	}{{40, 0}, {29, 8}, {30, 8}, {31, 8}, {55, 8}, {150, 8}, {54, 4}, {100, 4}, {101, 4}, {102, 4}, {147, 4}, {172, 4}, {181, 4}, {129, 0}, {130, 0}, {131, 0}} {
		t.Run(fmt.Sprint(tc.reason), func(t *testing.T) {
			p := protocolFailureFixture(tc.reason, tc.tail)
			got, err := decodeCast(opcode.SMSGCastFailed, p)
			if err != nil || got != (castEvent{kind: "failed", count: 9, spell: 7620, failure: tc.reason}) {
				t.Fatalf("failure = %+v, %v", got, err)
			}
			protocolRejectCastPrefixes(t, opcode.SMSGCastFailed, p)
		})
	}
	for _, reason := range []byte{0, 188, 255} {
		if _, err := decodeCast(opcode.SMSGCastFailed, protocolFailureFixture(reason, 0)); err == nil {
			t.Fatalf("accepted nonfailure reason%d", reason)
		}
	}
	for _, reason := range []byte{129, 130, 131} {
		p := protocolFailureFixture(reason, 0)
		binary.LittleEndian.PutUint32(p[1:], 42)
		if _, err := decodeCast(opcode.SMSGCastFailed, p); err == nil {
			t.Fatalf("guessed conditional failure tail for unknown spell reason%d", reason)
		}
	}
}

func TestProtocolChannelAndFishingTerminalEvents(t *testing.T) {
	start := []byte{1, 8, 0xc4, 0x1d, 0, 0, 0x30, 0x75, 0, 0}
	update := []byte{1, 8, 0, 0, 0, 0}
	interrupted := []byte{1, 8, 9, 0xc4, 0x1d, 0, 0, 29}
	for _, tc := range []struct {
		op   uint32
		p    []byte
		want castEvent
	}{
		{opcode.MSGChannelStart, start, castEvent{kind: "channel_start", caster: 8, spell: 7620, durationMs: 30000}},
		{opcode.MSGChannelUpdate, update, castEvent{kind: "channel_update", caster: 8}},
		{opcode.SMSGSpellFailure, interrupted, castEvent{kind: "failed", caster: 8, count: 9, spell: 7620, failure: 29}},
		{opcode.SMSGFishNotHooked, nil, castEvent{kind: "fish_not_hooked"}},
		{opcode.SMSGFishEscaped, nil, castEvent{kind: "fish_escaped"}},
	} {
		if got, err := decodeCast(tc.op, tc.p); err != nil || got != tc.want {
			t.Fatalf("opcode%x = %+v, %v", tc.op, got, err)
		}
		protocolRejectCastPrefixes(t, tc.op, tc.p)
	}
	// Unlike SMSG_CAST_FAILED reason29, SMSG_SPELL_FAILURE has no equipment tail.
	if _, err := decodeCast(opcode.SMSGSpellFailure, append(interrupted, make([]byte, 8)...)); err == nil {
		t.Fatal("accepted WriteCastResultInfo tail on interruption")
	}
}

func protocolPushFixture() []byte {
	p := make([]byte, 45)
	binary.LittleEndian.PutUint64(p, 8)
	binary.LittleEndian.PutUint32(p[12:], 1) // Crafted, not looted.
	binary.LittleEndian.PutUint32(p[16:], 1)
	p[20] = 255
	binary.LittleEndian.PutUint32(p[21:], 0xffffffff) // Added to existing stack.
	binary.LittleEndian.PutUint32(p[25:], 1251)
	binary.LittleEndian.PutUint32(p[29:], 77)
	binary.LittleEndian.PutUint32(p[33:], 0xfffffff9) // Signed random property.
	binary.LittleEndian.PutUint32(p[37:], 1)
	binary.LittleEndian.PutUint32(p[41:], 3)
	return p
}

func TestProtocolItemPushPreservesCraftAndStorageEvidence(t *testing.T) {
	p := protocolPushFixture()
	want := itemPush{player: 8, created: true, bag: 255, slot: 0xffffffff, entry: 1251, added: 1, total: 3}
	if got, err := decodePush(p); err != nil || got != want {
		t.Fatalf("crafted push = %+v %v", got, err)
	}
	binary.LittleEndian.PutUint32(p[8:], 1)
	binary.LittleEndian.PutUint32(p[12:], 0)
	want.received, want.created = true, false
	if got, err := decodePush(p); err != nil || got != want {
		t.Fatalf("received push = %+v %v", got, err)
	}
	for i := range p {
		if got, err := decodePush(p[:i]); err == nil || got != (itemPush{}) {
			t.Fatalf("accepted partial push length%d: %+v %v", i, got, err)
		}
	}
	if _, err := decodePush(append(p, 0)); err == nil {
		t.Fatal("accepted trailing push data")
	}
}

func TestProtocolItemPushRejectsFlagsAndCounts(t *testing.T) {
	for _, offset := range []int{8, 12, 16} {
		p := protocolPushFixture()
		binary.LittleEndian.PutUint32(p[offset:], 2)
		if got, err := decodePush(p); err == nil || got != (itemPush{}) {
			t.Fatalf("accepted malformed flag at%d: %+v %v", offset, got, err)
		}
	}
	for _, offset := range []int{25, 37, 41} {
		p := protocolPushFixture()
		binary.LittleEndian.PutUint32(p[offset:], 0)
		if _, err := decodePush(p); err == nil {
			t.Fatalf("accepted invalid entry/count at%d", offset)
		}
	}
}

func protocolFishingLootFixture(n int) []byte {
	p := make([]byte, 14+n*22)
	binary.LittleEndian.PutUint64(p, 0xf110008b07000009)
	p[8] = 3
	binary.LittleEndian.PutUint32(p[9:], 17)
	p[13] = byte(n)
	for i := 0; i < n; i++ {
		row := p[14+i*22:]
		row[0] = byte(i)
		binary.LittleEndian.PutUint32(row[1:], uint32(6291+i))
		binary.LittleEndian.PutUint32(row[5:], uint32(i+1))
		binary.LittleEndian.PutUint32(row[9:], 4811)
		binary.LittleEndian.PutUint32(row[13:], 77)
		binary.LittleEndian.PutUint32(row[17:], 0xfffffff9)
	}
	return p
}

func TestProtocolFishingLootNormalSlots(t *testing.T) {
	p := protocolFishingLootFixture(2)
	want := lootWindow{guid: 0xf110008b07000009, gold: 17, slots: []lootSlot{{slot: 0, entry: 6291, count: 1}, {slot: 1, entry: 6292, count: 2}}}
	if got, err := decodeFishingLoot(p); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("fishing window = %+v %v", got, err)
	}
	for _, n := range []int{0, 16} {
		if got, err := decodeFishingLoot(protocolFishingLootFixture(n)); err != nil || len(got.slots) != n {
			t.Fatalf("valid slot count%d = %+v %v", n, got, err)
		}
	}
	for i := range p {
		if got, err := decodeFishingLoot(p[:i]); err == nil || !reflect.DeepEqual(got, lootWindow{}) {
			t.Fatalf("accepted partial window length%d: %+v %v", i, got, err)
		}
	}
	if _, err := decodeFishingLoot(append(p, 0)); err == nil {
		t.Fatal("accepted trailing fishing data")
	}
}

func TestProtocolFishingLootRejectsTypesAndUnsafeSlots(t *testing.T) {
	for _, lootType := range []byte{0, 1, 2, 6, 20, 22, 255} {
		p := protocolFishingLootFixture(1)
		p[8] = lootType
		if _, err := decodeFishingLoot(p); err == nil {
			t.Fatalf("coerced wire loot type%d into fishing", lootType)
		}
	}
	if _, err := decodeFishingLoot(protocolFishingLootFixture(17)); err == nil {
		t.Fatal("accepted more than16 fishing slots")
	}
	for _, flag := range []byte{1, 2, 3, 4, 5, 255} {
		p := protocolFishingLootFixture(1)
		p[35] = flag
		if _, err := decodeFishingLoot(p); err == nil {
			t.Fatalf("accepted non-normal fishing slot flag%d", flag)
		}
	}
	for _, offset := range []int{15, 19} {
		p := protocolFishingLootFixture(1)
		binary.LittleEndian.PutUint32(p[offset:], 0)
		if _, err := decodeFishingLoot(p); err == nil {
			t.Fatalf("accepted zero entry/count at%d", offset)
		}
	}
	p := protocolFishingLootFixture(2)
	p[36] = 0
	if got, err := decodeFishingLoot(p); err == nil || !reflect.DeepEqual(got, lootWindow{}) {
		t.Fatalf("accepted duplicate slot or leaked partial window: %+v %v", got, err)
	}
}
