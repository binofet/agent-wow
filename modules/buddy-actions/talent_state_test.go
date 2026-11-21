package buddyactions

import (
	"encoding/binary"
	"testing"
)

func playerTalentPacket(free uint32, active byte, specs ...talentSpec) []byte {
	p := binary.LittleEndian.AppendUint32([]byte{0}, free)
	p = append(p, byte(len(specs)), active)
	for _, spec := range specs {
		p = append(p, byte(len(spec.ranks)))
		for _, r := range spec.ranks {
			p = binary.LittleEndian.AppendUint32(p, r.id)
			p = append(p, r.rank)
		}
		p = append(p, byte(len(spec.glyphs)))
		for _, g := range spec.glyphs {
			p = binary.LittleEndian.AppendUint16(p, g)
		}
	}
	return p
}
func TestPlayerTalentStateDecodesActualWriterFieldsNotPacketLength(t *testing.T) {
	// Synthetic writer-shaped21-byte frame, explicitly not a live capture.
	p := playerTalentPacket(31, 0, talentSpec{glyphs: make([]uint16, 6)})
	if len(p) != 21 {
		t.Fatal(len(p))
	}
	got, err := decodePlayerTalentsInfo(p)
	if err != nil || got == nil || got.free != 31 || got.active != 0 || len(got.specs) != 1 || len(got.specs[0].ranks) != 0 || len(got.specs[0].glyphs) != 6 {
		t.Fatal(got, err)
	}
	p[1] = 7
	changed, err := decodePlayerTalentsInfo(p)
	if err != nil || changed.free != 7 {
		t.Fatal("equal length cannot prove the free-point count", changed, err)
	}
	dual := playerTalentPacket(4, 1, talentSpec{ranks: []talentRank{{123, 2}}, glyphs: []uint16{0, 456}}, talentSpec{ranks: []talentRank{{123, 0}, {987, 4}}, glyphs: []uint16{321}})
	got, err = decodePlayerTalentsInfo(dual)
	if err != nil || got.active != 1 || len(got.specs) != 2 || got.specs[0].ranks[0].rank != 2 || got.specs[1].ranks[1].rank != 4 || got.specs[1].glyphs[0] != 321 {
		t.Fatal(got, err)
	}
}
func TestPlayerTalentStateRejectsPartialAmbiguousOrForeignFrames(t *testing.T) {
	p := playerTalentPacket(31, 0, talentSpec{ranks: []talentRank{{123, 2}}, glyphs: make([]uint16, 6)})
	for n := 0; n < len(p); n++ {
		if got, err := decodePlayerTalentsInfo(p[:n]); err == nil || got != nil {
			t.Fatal("accepted truncated player frame", n, got, err)
		}
	}
	for _, bad := range [][]byte{append(append([]byte(nil), p...), 0), playerTalentPacket(31, 1, talentSpec{}), playerTalentPacket(31, 0, talentSpec{ranks: []talentRank{{123, 1}, {123, 2}}}), playerTalentPacket(31, 0, talentSpec{ranks: []talentRank{{123, 5}}}), playerTalentPacket(31, 0, talentSpec{glyphs: make([]uint16, 7)})} {
		if got, err := decodePlayerTalentsInfo(bad); err == nil || got != nil {
			t.Fatal(got, err)
		}
	}
	if got, err := decodePlayerTalentsInfo([]byte{1, 99}); got != nil || err != nil {
		t.Fatal("pet frame replaced player state", got, err)
	}
}
