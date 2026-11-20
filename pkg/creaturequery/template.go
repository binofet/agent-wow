package creaturequery

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"unicode/utf8"
)

type Template struct {
	Entry, TypeFlags, CreatureType uint32
	Name                           string
}

func Request(entry uint32, guid uint64) []byte {
	return binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint32(nil, entry), guid)
}

// Decode the ordinary build 12340 creature-query reply. A template establishes
// static creature metadata only. It is never evidence of a live NPC or its GUID.
func Decode(p []byte) (Template, error) {
	bad := errors.New("invalid bounded creature query response")
	if len(p) < 4 || len(p) > 3*4097+80 {
		return Template{}, bad
	}
	m := Template{Entry: binary.LittleEndian.Uint32(p)}
	if m.Entry == 0 || m.Entry&0x80000000 != 0 {
		return Template{}, bad
	}
	p = p[4:]
	for i := 0; i < 3; i++ {
		n := bytes.IndexByte(p[:min(len(p), 4097)], 0)
		if n < 0 || !utf8.Valid(p[:n]) {
			return Template{}, bad
		}
		if i == 0 {
			if n == 0 || len(p) < n+4 || p[n+1] != 0 || p[n+2] != 0 || p[n+3] != 0 {
				return Template{}, bad
			}
			m.Name = string(p[:n])
			n += 3 // Unused alternative names.
		}
		p = p[n+1:]
	}
	// Type flags, type, family, rank, kill credits, models, multipliers,
	// racial leader, quest items and movement ID.
	if len(p) != 77 || p[48] > 1 {
		return Template{}, bad
	}
	m.TypeFlags, m.CreatureType = binary.LittleEndian.Uint32(p), binary.LittleEndian.Uint32(p[4:])
	if m.CreatureType == 0 || m.CreatureType > 13 {
		return Template{}, bad
	}
	for _, at := range []int{40, 44} {
		value := float64(math.Float32frombits(binary.LittleEndian.Uint32(p[at:])))
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return Template{}, bad
		}
	}
	return m, nil
}
