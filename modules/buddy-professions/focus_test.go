package buddyprofessions

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"testing"
)

func focusResponseFixture(text [4]string) (p []byte, dataOffset int) {
	p = binary.LittleEndian.AppendUint32(p, 123456)
	p = binary.LittleEndian.AppendUint32(p, 8)
	p = binary.LittleEndian.AppendUint32(p, 987)
	p = append(p, text[0]...)
	p = append(p, 0, 0, 0, 0) // Name terminator and three empty name fields.
	for _, value := range text[1:] {
		p = append(p, value...)
		p = append(p, 0)
	}
	dataOffset = len(p)
	words := [24]uint32{4, 17, 123, 0, 3904, 1, 1}
	for i := 7; i < len(words); i++ {
		words[i] = uint32(i * 101) // Unused words still occupy the full frame.
	}
	for _, word := range words {
		p = binary.LittleEndian.AppendUint32(p, word)
	}
	p = binary.LittleEndian.AppendUint32(p, math.Float32bits(1.25))
	for _, item := range []uint32{117, 0, 2589, 769, 0, 2672} {
		p = binary.LittleEndian.AppendUint32(p, item)
	}
	return p, dataOffset
}

func TestFocusQueryRequestGoldenBytes(t *testing.T) {
	p := focusQueryPacket(0x12345678, 0xf110010203040506)
	want := []byte{0x78, 0x56, 0x34, 0x12, 6, 5, 4, 3, 2, 1, 0x10, 0xf1}
	if !bytes.Equal(p, want) {
		t.Fatalf("query = %x, want %x", p, want)
	}
}

func TestFocusTemplateActualDataIgnoresDisplayText(t *testing.T) {
	want := focusTemplate{entry: 123456, focusID: 4, radius: 17}
	for _, text := range [][4]string{
		{"Localized fire", "Interact", "Crafting", "extra"},
		{"Feu de camp — 焚き火", "", "Cuisiner", ""},
		{"Mailbox with misleading name", "Attack", "No focus here", "356"},
		{},
	} {
		p, _ := focusResponseFixture(text)
		if got, err := decodeFocusTemplate(p); err != nil || got != want {
			t.Fatalf("template = %+v, %v, text=%q", got, err, text)
		}
	}
	// The decoder preserves actual data0/data1, including zero values. Matching
	// a needed focus and deciding whether its range is sufficient is separate.
	p, dataOffset := focusResponseFixture([4]string{"Cooking Fire"})
	binary.LittleEndian.PutUint32(p[dataOffset:], 0)
	binary.LittleEndian.PutUint32(p[dataOffset+4:], 0)
	if got, err := decodeFocusTemplate(p); err != nil || got != (focusTemplate{entry: 123456}) {
		t.Fatalf("zero focus/radius rewritten: %+v %v", got, err)
	}
}

func TestFocusTemplateRequiresEntireResponse(t *testing.T) {
	p, _ := focusResponseFixture([4]string{"Fire", "Interact", "Cook", "extra"})
	for i := range p {
		if got, err := decodeFocusTemplate(p[:i]); err == nil || got != (focusTemplate{}) {
			t.Fatalf("accepted prefix length%d or leaked partial template: %+v %v", i, got, err)
		}
	}
	for _, suffix := range [][]byte{{0}, {1, 2, 3, 4}, make([]byte, 24)} {
		bad := append(append([]byte{}, p...), suffix...)
		if got, err := decodeFocusTemplate(bad); err == nil || got != (focusTemplate{}) {
			t.Fatalf("accepted trailing data: %+v %v", got, err)
		}
	}
}

func TestFocusTemplateRejectsUnavailableOrWrongType(t *testing.T) {
	for _, entry := range []uint32{0, 0x80000000, 0x8001e240, 0xffffffff} {
		p, _ := focusResponseFixture([4]string{"Fire"})
		binary.LittleEndian.PutUint32(p, entry)
		if _, err := decodeFocusTemplate(p); err == nil {
			t.Fatalf("accepted invalid entry%x", entry)
		}
	}
	for _, kind := range []uint32{0, 3, 17, 19, 25, 36, 0xffffffff} {
		p, _ := focusResponseFixture([4]string{"Fire"})
		binary.LittleEndian.PutUint32(p[4:], kind)
		if _, err := decodeFocusTemplate(p); err == nil {
			t.Fatalf("accepted non-focus type%d", kind)
		}
	}
	// The server's four-byte negative response cannot establish a template.
	if _, err := decodeFocusTemplate([]byte{0x40, 0xe2, 1, 0x80}); err == nil {
		t.Fatal("accepted missing-template response")
	}
}

func TestFocusTemplateFlagsAndScale(t *testing.T) {
	for _, index := range []int{3, 5, 6} {
		for _, value := range []uint32{2, 0x80000000, 0xffffffff} {
			p, dataOffset := focusResponseFixture([4]string{"Fire"})
			binary.LittleEndian.PutUint32(p[dataOffset+index*4:], value)
			if _, err := decodeFocusTemplate(p); err == nil {
				t.Fatalf("accepted unsupported data%d flag%x", index, value)
			}
		}
	}
	for _, bits := range []uint32{0x7fc00000, 0x7f800000, 0xff800000, 0xbf800000, 0, 0x80000000} {
		p, dataOffset := focusResponseFixture([4]string{"Fire"})
		binary.LittleEndian.PutUint32(p[dataOffset+24*4:], bits)
		if _, err := decodeFocusTemplate(p); err == nil {
			t.Fatalf("accepted invalid scale%x", bits)
		}
	}
}

func TestFocusTemplateStringBoundsAndFraming(t *testing.T) {
	// Every meaningful string may use its full byte allowance, including UTF-8.
	for i := 0; i < 4; i++ {
		text := [4]string{}
		text[i] = strings.Repeat("é", focusMaxStringBytes/2)
		p, _ := focusResponseFixture(text)
		if _, err := decodeFocusTemplate(p); err != nil {
			t.Fatalf("valid bounded string%d rejected: %v", i, err)
		}
		text[i] += "x"
		p, _ = focusResponseFixture(text)
		if _, err := decodeFocusTemplate(p); err == nil {
			t.Fatalf("accepted overlong string%d", i)
		}
		text[i] = string([]byte{0xff})
		p, _ = focusResponseFixture(text)
		if _, err := decodeFocusTemplate(p); err == nil {
			t.Fatalf("accepted invalid UTF-8 string%d", i)
		}
	}
	p, _ := focusResponseFixture([4]string{"Fire", "Interact", "Cook", "extra"})
	for _, i := range []int{17, 18, 19} { // Name2/name3/name4 must stay empty.
		bad := append([]byte{}, p...)
		bad[i] = 'x'
		if _, err := decodeFocusTemplate(bad); err == nil {
			t.Fatalf("accepted unsupported nonempty name at%d", i)
		}
	}
	if _, err := decodeFocusTemplate(bytes.Repeat([]byte{'x'}, focusMaxPacketBytes+1)); err == nil {
		t.Fatal("accepted oversized response")
	}
	// No NUL terminator within the bounded field, despite a valid header.
	unterminated := append(append([]byte{}, p[:12]...), bytes.Repeat([]byte{'x'}, focusMaxStringBytes+1)...)
	if _, err := decodeFocusTemplate(unterminated); err == nil {
		t.Fatal("accepted unterminated name")
	}
}

func FuzzFocusTemplate(f *testing.F) {
	p, _ := focusResponseFixture([4]string{"Feu de camp", "", "Cuisiner", ""})
	f.Add(p)
	f.Add([]byte{0x40, 0xe2, 1, 0x80})
	f.Fuzz(func(t *testing.T, p []byte) {
		got, err := decodeFocusTemplate(p)
		if err != nil && got != (focusTemplate{}) {
			t.Fatal(fmt.Sprintf("partial template escaped malformed frame: %+v", got))
		}
	})
}
