package buddyprofessions

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// This fixture follows the public ItemHandler field order independently of
// the decoder: two stats, two damages, five complete spells, then 29 tail words.
func disenchantMetadataFixture(entry, class, quality, required uint32) ([]byte, int, int) {
	p := []byte{}
	put := func(v uint32) { p = binary.LittleEndian.AppendUint32(p, v) }
	put(entry)
	put(class)
	put(0)
	put(0)
	p = append(p, []byte("Explicit selected test item")...)
	p = append(p, 0, 0, 0, 0)
	prefixAt := len(p)
	prefix := [22]uint32{1, quality, 0, 0, 100, 20, 13, ^uint32(0), ^uint32(0), 5, 1, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 2}
	for _, v := range prefix {
		put(v)
	}
	for _, v := range []uint32{3, 1, 4, 2, 0, 0, math.Float32bits(1), math.Float32bits(2), 0, 0, 0, 0} {
		put(v)
	}
	for range 7 {
		put(0)
	}
	put(1500)
	put(0)
	put(math.Float32bits(1))
	for range 5 {
		for _, v := range []uint32{0, 0, 0, ^uint32(0), 0, ^uint32(0)} {
			put(v)
		}
	}
	put(1)
	p = append(p, []byte("Selected deliberately by the operator")...)
	p = append(p, 0)
	tailAt := len(p)
	for i := range 29 {
		v := uint32(0)
		if i == 24 {
			v = required
		}
		put(v)
	}
	return p, prefixAt, tailAt
}

func TestDisenchantMetadataCompleteRequiredSkillIsTail24(t *testing.T) {
	p, _, tail := disenchantMetadataFixture(1001, 2, 3, 75)
	// Adjacent words must not become a guessed minimum or DisenchantID.
	binary.LittleEndian.PutUint32(p[tail+23*4:], 999)
	binary.LittleEndian.PutUint32(p[tail+26*4:], 456)
	m, err := decodeDisenchantTemplate(p)
	if err != nil || !m.known || m.entry != 1001 || m.class != 2 || m.quality != 3 || m.requiredSkill != 75 || m.tail[23] != 999 || m.tail[26] != 456 {
		t.Fatal(m, err)
	}
	for n := range len(p) {
		if _, err := decodeDisenchantTemplate(p[:n]); err == nil {
			t.Fatalf("accepted item prefix%d/%d", n, len(p))
		}
	}
	if _, err := decodeDisenchantTemplate(append(p, 0)); err == nil {
		t.Fatal("accepted trailing item data")
	}
}

func TestDisenchantMetadataUnavailableAndMinusOneRemainExplicit(t *testing.T) {
	m, err := decodeDisenchantTemplate(binary.LittleEndian.AppendUint32(nil, 1001|0x80000000))
	if err != nil || m.known || m.entry != 1001 {
		t.Fatal(m, err)
	}
	p, _, _ := disenchantMetadataFixture(1001, 4, 2, ^uint32(0))
	m, err = decodeDisenchantTemplate(p)
	if err != nil || !m.known || m.requiredSkill != ^uint32(0) {
		t.Fatal("-1 sentinel was lost", m, err)
	}
	if _, err := decodeDisenchantTemplate(append(binary.LittleEndian.AppendUint32(nil, 1001|0x80000000), 0)); err == nil {
		t.Fatal("unavailable response accepted extra body")
	}
}

func TestDisenchantMetadataRejectsMalformedCountsStringsAndFloats(t *testing.T) {
	for _, mode := range []string{"entry_zero", "class", "quality", "stats", "name_utf8", "name_long", "alternate_name", "bonding", "damage_nan", "range_inf", "modifier_nan", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			p, at, tail := disenchantMetadataFixture(1001, 2, 2, 1)
			switch mode {
			case "entry_zero":
				binary.LittleEndian.PutUint32(p, 0)
			case "class":
				binary.LittleEndian.PutUint32(p[4:], 17)
			case "quality":
				binary.LittleEndian.PutUint32(p[at+4:], 8)
			case "stats":
				binary.LittleEndian.PutUint32(p[at+21*4:], 11)
			case "name_utf8":
				p[16] = 0xff
			case "name_long":
				p = append(append(append([]byte{}, p[:16]...), []byte(strings.Repeat("a", 256))...), p[16:]...)
			case "alternate_name":
				p[at-1] = 'x'
			case "bonding":
				binary.LittleEndian.PutUint32(p[tail-1-len("Selected deliberately by the operator")-4:], 6)
			case "damage_nan":
				binary.LittleEndian.PutUint32(p[at+22*4+16+8:], math.Float32bits(float32(math.NaN())))
			case "range_inf":
				binary.LittleEndian.PutUint32(p[at+22*4+16+8+24+28+8:], math.Float32bits(float32(math.Inf(1))))
			case "modifier_nan":
				binary.LittleEndian.PutUint32(p[tail+25*4:], math.Float32bits(float32(math.NaN())))
			case "oversize":
				p = make([]byte, 5501)
			}
			if _, err := decodeDisenchantTemplate(p); err == nil {
				t.Fatal("malformed complete metadata accepted", mode)
			}
		})
	}
}
