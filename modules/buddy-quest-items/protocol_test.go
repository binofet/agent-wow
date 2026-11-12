package buddyquestitems

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func put32(p *[]byte, v uint32) { *p = append(*p, u32(v)...) }
func packed(v uint64) []byte {
	p := []byte{0}
	for i := uint(0); i < 8; i++ {
		if b := byte(v >> (i * 8)); b != 0 {
			p[0] |= 1 << i
			p = append(p, b)
		}
	}
	return p
}
func barrelFixture() []byte {
	p := []byte{}
	for _, v := range []uint32{barrelEntry, 10, 1} {
		put32(&p, v)
	}
	p = append(p, []byte("Water Barrel\x00\x00\x00\x00\x00\x00\x00")...)
	for i := range 24 {
		v := uint32(0)
		if i == 10 {
			v = createSpell
		}
		put32(&p, v)
	}
	put32(&p, math.Float32bits(1))
	p = append(p, make([]byte, 24)...)
	return p
}
func bucketFixture() []byte {
	p := []byte{}
	for _, v := range []uint32{bucketEntry, 0, 0, 0} {
		put32(&p, v)
	}
	p = append(p, []byte("Water Bucket\x00\x00\x00\x00")...)
	p = append(p, make([]byte, 22*4+8+24+28+12)...)
	for i := range 5 {
		spell, trigger, charge := uint32(0), uint32(0), uint32(0)
		if i == 0 {
			spell = throwSpell
			charge = math.MaxUint32
		}
		if i == 1 {
			spell = 42336
			trigger = 5
			charge = math.MaxUint32
		}
		for _, v := range []uint32{spell, trigger, charge, math.MaxUint32, 0, math.MaxUint32} {
			put32(&p, v)
		}
	}
	put32(&p, 0)
	p = append(p, 0)
	p = append(p, make([]byte, 29*4)...)
	return p
}
func castFixture(goEvent bool, item, caster uint64, count byte, spell uint32, dst *point) []byte {
	p := append(packed(item), packed(caster)...)
	p = append(p, count)
	put32(&p, spell)
	put32(&p, 0x40100)
	put32(&p, 0)
	if goEvent {
		p = append(p, 0, 0)
	}
	mask := uint32(0)
	if dst != nil {
		mask = 0x40
	}
	put32(&p, mask)
	if dst != nil {
		p = append(p, 0)
		for _, v := range []float32{dst.x, dst.y, dst.z} {
			put32(&p, math.Float32bits(v))
		}
		if goEvent {
			p = append(p, 0)
		}
	}
	return p
}
func pushFixture(self uint64) []byte {
	p := make([]byte, 45)
	binary.LittleEndian.PutUint64(p, self)
	binary.LittleEndian.PutUint32(p[12:], 1)
	p[20] = 255
	binary.LittleEndian.PutUint32(p[21:], 23)
	binary.LittleEndian.PutUint32(p[25:], bucketEntry)
	binary.LittleEndian.PutUint32(p[37:], 1)
	binary.LittleEndian.PutUint32(p[41:], 1)
	return p
}

func TestQuestItemPacketGoldensAndAllBagMappings(t *testing.T) {
	p, err := useBucket(0, 0, 0x4000000000000001, 7, point{1, 2, 3})
	want, _ := hex.DecodeString("ff170764a500000100000000000040000000000040000000000000803f0000004000004040")
	if err != nil || !bytes.Equal(p, want) {
		t.Fatalf("%x != %x (%v)", p, want, err)
	}
	if got := goUse(0xf11002d77a000001); hex.EncodeToString(got) != "0100007ad70210f1" {
		t.Fatalf("%x", got)
	}
	for bag := uint32(1); bag <= 4; bag++ {
		p, err = useBucket(bag, 35, 0x4000000000000001, 0, point{})
		if err != nil || p[0] != byte(18+bag) || p[1] != 35 {
			t.Fatal(bag, p, err)
		}
	}
	for _, tc := range []struct {
		bag, slot uint32
		item      uint64
		dst       point
	}{{0, 16, 0x4000000000000001, point{}}, {5, 0, 0x4000000000000001, point{}}, {1, 36, 0x4000000000000001, point{}}, {0, 0, 3, point{}}, {0, 0, 0x4000000000000001, point{x: float32(math.NaN())}}} {
		if _, err := useBucket(tc.bag, tc.slot, tc.item, 0, tc.dst); err == nil {
			t.Fatal("unsafe binding", tc)
		}
	}
}
func TestFullNormalTemplatesRejectEveryTruncationAndUnexpectedOnUse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		p      []byte
		decode func([]byte) (bool, error)
	}{{"barrel", barrelFixture(), barrelTemplate}, {"bucket", bucketFixture(), bucketTemplate}} {
		t.Run(tc.name, func(t *testing.T) {
			if ok, err := tc.decode(tc.p); err != nil || !ok {
				t.Fatal(ok, err)
			}
			for n := 0; n < len(tc.p); n++ {
				if ok, err := tc.decode(tc.p[:n]); err == nil || ok {
					t.Fatal("accepted prefix", n)
				}
			}
			if _, err := tc.decode(append(append([]byte{}, tc.p...), 0)); err == nil {
				t.Fatal("trailing byte")
			}
		})
	}
	p := bucketFixture()
	at := 16 + len("Water Bucket") + 4 + 22*4 + 8 + 24 + 28 + 12
	binary.LittleEndian.PutUint32(p[at:], 585)
	if ok, err := bucketTemplate(p); err != nil || ok {
		t.Fatal("wrong on-use authorized", ok, err)
	}
	p = bucketFixture()
	binary.LittleEndian.PutUint32(p[at+24+4:], 0)
	if ok, err := bucketTemplate(p); err != nil || ok {
		t.Fatal("second on-use authorized", ok, err)
	}
	p = barrelFixture()
	at = 12 + len("Water Barrel") + 7 + 10*4
	binary.LittleEndian.PutUint32(p[at:], throwSpell)
	if ok, err := barrelTemplate(p); err != nil || ok {
		t.Fatal("wrong barrel spell", ok, err)
	}
}
func TestGroundCastFullBodiesAndPrefixes(t *testing.T) {
	dst := point{1, 2, 3}
	for _, goEvent := range []bool{false, true} {
		op := uint32(opcode.SMSGSpellStart)
		if goEvent {
			op = opcode.SMSGSpellGo
		}
		p := castFixture(goEvent, 0x4000000000000001, 4, 7, throwSpell, &dst)
		e, err := decodeCast(op, p)
		if err != nil || e.dst != dst || e.item != 0x4000000000000001 || e.caster != 4 || e.spell != throwSpell || e.count != 7 {
			t.Fatal(e, err)
		}
		for n := range len(p) {
			if _, err := decodeCast(op, p[:n]); err == nil {
				t.Fatal("prefix accepted", n)
			}
		}
		if _, err := decodeCast(op, append(p, 0)); err == nil {
			t.Fatal("trailer")
		}
	}
	p := castFixture(true, 4, 4, 0, createSpell, nil)
	if _, err := decodeCast(opcode.SMSGSpellGo, p); err != nil {
		t.Fatal(err)
	}
}
func TestPushAndFailureAreWholePackets(t *testing.T) {
	p := pushFixture(4)
	if who, entry, err := itemPush(p); err != nil || who != 4 || entry != bucketEntry {
		t.Fatal(who, entry, err)
	}
	for n := range len(p) {
		if _, _, err := itemPush(p[:n]); err == nil {
			t.Fatal(n)
		}
	}
	p[12] = 2
	if _, _, err := itemPush(p); err == nil {
		t.Fatal("invalid boolean")
	}
	p = make([]byte, 18)
	p[0] = 50
	if code, _, _, err := inventoryFailure(p); err != nil || code != 50 {
		t.Fatal(code, err)
	}
}
func FuzzQuestItemPacketReaders(f *testing.F) {
	f.Add(barrelFixture())
	f.Add(bucketFixture())
	f.Add(castFixture(true, 0x4000000000000001, 4, 1, throwSpell, &point{1, 2, 3}))
	f.Fuzz(func(t *testing.T, p []byte) {
		if len(p) > 25000 {
			return
		}
		_, _ = barrelTemplate(p)
		_, _ = bucketTemplate(p)
		_, _ = decodeCast(opcode.SMSGSpellGo, p)
		_, _ = decodeCast(opcode.SMSGCastFailed, p)
		_, _, _ = itemPush(p)
		_, _, _, _ = inventoryFailure(p)
	})
}
