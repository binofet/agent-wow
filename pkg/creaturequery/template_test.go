package creaturequery

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

func TestRequestAndDecodeBuild12340Template(t *testing.T) {
	wantRequest := []byte{0x2b, 0x02, 0, 0, 1, 0, 0, 0x2b, 0x02, 0, 0x30, 0xf1}
	if got := Request(555, 0xf13000022b000001); !bytes.Equal(got, wantRequest) {
		t.Fatalf("request = %x, want %x", got, wantRequest)
	}
	prefix := binary.LittleEndian.AppendUint32(nil, 555)
	prefix = append(prefix, "Creature\x00\x00\x00\x00Subname\x00Icon\x00"...)
	tail := make([]byte, 77)
	binary.LittleEndian.PutUint32(tail, 4)
	binary.LittleEndian.PutUint32(tail[4:], 7)
	binary.LittleEndian.PutUint32(tail[8:], 21)
	binary.LittleEndian.PutUint32(tail[12:], 2)
	binary.LittleEndian.PutUint32(tail[40:], math.Float32bits(1))
	binary.LittleEndian.PutUint32(tail[44:], math.Float32bits(1))
	packet := append(prefix, tail...)
	want := Template{Entry: 555, Name: "Creature", SubName: "Subname", TypeFlags: 4, CreatureType: 7, Family: 21, Rank: 2}
	if got, err := Decode(packet); err != nil || got != want {
		t.Fatalf("template = %+v, err = %v, want %+v", got, err, want)
	}

	for n := range len(packet) {
		if got, err := Decode(packet[:n]); err == nil || got != (Template{}) {
			t.Fatalf("truncated template accepted at %d bytes: %+v, err = %v", n, got, err)
		}
	}
	for _, tc := range []struct {
		name string
		edit func([]byte) []byte
	}{
		{"not-found entry flag", func(p []byte) []byte { p[3] |= 0x80; return p }},
		{"invalid UTF-8", func(p []byte) []byte { p[4] = 0xff; return p }},
		{"unknown creature type", func(p []byte) []byte { p[len(prefix)+4] = 14; return p }},
		{"nonfinite multiplier", func(p []byte) []byte {
			binary.LittleEndian.PutUint32(p[len(prefix)+40:], math.Float32bits(float32(math.Inf(1))))
			return p
		}},
		{"unexpected trailing bytes", func(p []byte) []byte { return append(p, 0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Decode(tc.edit(bytes.Clone(packet))); err == nil || got != (Template{}) {
				t.Fatalf("invalid template accepted: %+v, err = %v", got, err)
			}
		})
	}
}
