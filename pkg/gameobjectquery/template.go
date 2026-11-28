package gameobjectquery

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"unicode/utf8"
)

const MaxStringBytes = 4096
const MaxPacketBytes = 12 + 3 + 4*(MaxStringBytes+1) + 24*4 + 4 + 6*4

type Template struct {
	Entry, Type, DisplayID uint32
	Data                   [24]uint32
	Scale                  float32
}

func Request(entry uint32, guid uint64) []byte {
	return binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint32(nil, entry), guid)
}

// HandleGameObjectQueryOpcode replies with static template data, keyed only
// by entry. Instance identity and presence belong to the caller's World view.
func Decode(p []byte) (Template, error) {
	bad := errors.New("invalid game object query response")
	if len(p) < 12 || len(p) > MaxPacketBytes {
		return Template{}, bad
	}
	m := Template{Entry: binary.LittleEndian.Uint32(p), Type: binary.LittleEndian.Uint32(p[4:]), DisplayID: binary.LittleEndian.Uint32(p[8:])}
	if m.Entry == 0 || m.Entry&0x80000000 != 0 {
		return Template{}, bad
	}
	p = p[12:]
	for n := 0; n < 4; n++ { // name, icon, localized caption, unknown string
		i := bytes.IndexByte(p[:min(len(p), MaxStringBytes+1)], 0)
		if i < 0 || !utf8.Valid(p[:i]) {
			return Template{}, bad
		}
		p = p[i+1:]
		if n == 0 {
			// The three alternative name fields are sent as literal zero bytes.
			if len(p) < 3 || p[0] != 0 || p[1] != 0 || p[2] != 0 {
				return Template{}, bad
			}
			p = p[3:]
		}
	}
	if len(p) != 24*4+4+6*4 {
		return Template{}, bad
	}
	for i := range m.Data {
		m.Data[i] = binary.LittleEndian.Uint32(p[4*i:])
	}
	m.Scale = math.Float32frombits(binary.LittleEndian.Uint32(p[24*4:]))
	if math.IsNaN(float64(m.Scale)) || math.IsInf(float64(m.Scale), 0) || m.Scale <= 0 {
		return Template{}, bad
	}
	return m, nil
}
