package buddyactions

import (
	"encoding/binary"
	"errors"
)

// Normal build12340 player talent data. Rank is the protocol's zero-based
// Talent.dbc rank index; glyph entries are GlyphProperties.dbc IDs, including0.
// Player::BuildPlayerTalentsInfoData and SendTalentsInfoData are the writers.
type talentRank struct {
	id   uint32
	rank uint8
}
type talentSpec struct {
	ranks  []talentRank
	glyphs []uint16
}
type playerTalentSnapshot struct {
	free   uint32
	active uint8
	specs  []talentSpec
}

func decodePlayerTalentBody(p []byte) (*playerTalentSnapshot, int, error) {
	bad := errors.New("malformed player talent state")
	if len(p) < 6 {
		return nil, 0, bad
	}
	out := &playerTalentSnapshot{free: binary.LittleEndian.Uint32(p), active: p[5]}
	count := int(p[4])
	if count > 2 || count == 0 && out.active != 0 || count > 0 && int(out.active) >= count {
		return nil, 0, bad
	}
	offset := 6
	for i := 0; i < count; i++ {
		if offset >= len(p) {
			return nil, 0, bad
		}
		n := int(p[offset])
		offset++
		if n > (len(p)-offset)/5 {
			return nil, 0, bad
		}
		spec := talentSpec{}
		seen := map[uint32]bool{}
		for j := 0; j < n; j++ {
			id, rank := binary.LittleEndian.Uint32(p[offset:]), p[offset+4]
			offset += 5
			if id == 0 || rank >= 5 || seen[id] {
				return nil, 0, bad
			}
			seen[id] = true
			spec.ranks = append(spec.ranks, talentRank{id, rank})
		}
		if offset >= len(p) {
			return nil, 0, bad
		}
		glyphs := int(p[offset])
		offset++
		// MAX_GLYPH_SLOT_INDEX is6 in the supported client. Counts are received
		// facts; absent slots stay absent rather than inventing empty glyphs.
		if glyphs > 6 || glyphs > (len(p)-offset)/2 {
			return nil, 0, bad
		}
		for j := 0; j < glyphs; j++ {
			spec.glyphs = append(spec.glyphs, binary.LittleEndian.Uint16(p[offset:]))
			offset += 2
		}
		out.specs = append(out.specs, spec)
	}
	return out, offset, nil
}

func decodePlayerTalentsInfo(p []byte) (*playerTalentSnapshot, error) {
	if len(p) == 0 || p[0] > 1 {
		return nil, errors.New("malformed talent state discriminator")
	}
	if p[0] == 1 {
		return nil, nil
	} // Pet talent packets cannot replace player state.
	out, n, err := decodePlayerTalentBody(p[1:])
	if err != nil {
		return nil, err
	}
	if n != len(p)-1 {
		return nil, errors.New("trailing player talent state bytes")
	}
	return out, nil
}
