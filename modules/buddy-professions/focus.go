package buddyprofessions

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"unicode/utf8"
)

type focusTemplate struct {
	entry, focusID, radius uint32
}

const focusMaxStringBytes = 4096
const focusMaxPacketBytes = 12 + 3 + 4*(focusMaxStringBytes+1) + 24*4 + 4 + 6*4

// HandleGameObjectQueryOpcode reads entry uint32 followed by a full GUID.
// The caller must bind these to a currently observed spell-focus object.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/QueryHandler.cpp
func focusQueryPacket(entry uint32, guid uint64) []byte {
	p := make([]byte, 12)
	binary.LittleEndian.PutUint32(p, entry)
	binary.LittleEndian.PutUint64(p[4:], guid)
	return p
}

// QueryHandler::HandleGameObjectQueryOpcode produces the complete response:
// entry/type/display, four names (the last three empty), icon/caption/unk1,
// 24 data words, float scale, and six quest-item words. There is no GUID or
// position in the response, so it does not establish visibility or proximity.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/QueryHandler.cpp
// GameObjectTemplate::spellFocus has focusId at data0 and dist at data1.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/GameObject/GameObjectData.h
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/shared/SharedDefines.h
func decodeFocusTemplate(p []byte) (focusTemplate, error) {
	if len(p) < 4 || len(p) > focusMaxPacketBytes {
		return focusTemplate{}, errors.New("invalid spell focus query size")
	}
	r := packetReader{data: p}
	entry, kind := r.u32(), r.u32()
	r.u32() // Display ID is not a focus requirement.
	if entry == 0 || entry&0x80000000 != 0 || kind != 8 {
		return focusTemplate{}, errors.New("unsupported or unavailable spell focus template")
	}
	if err := skipFocusString(&r); err != nil {
		return focusTemplate{}, err
	}
	// AzerothCore writes literal empty name2/name3/name4, not more locales.
	if names := r.take(3); len(names) != 3 || names[0] != 0 || names[1] != 0 || names[2] != 0 {
		return focusTemplate{}, errors.New("unsupported spell focus name fields")
	}
	for i := 0; i < 3; i++ { // IconName, localized CastBarCaption, unk1.
		if err := skipFocusString(&r); err != nil {
			return focusTemplate{}, err
		}
	}
	data := [24]uint32{}
	for i := range data {
		data[i] = r.u32()
	}
	// Only the canonical forms of serverOnly, large and floatingTooltip are
	// supported. Linked-trap and quest IDs are scalar metadata, not flags.
	if data[3] > 1 || data[5] > 1 || data[6] > 1 {
		return focusTemplate{}, errors.New("unsupported spell focus flags")
	}
	scale := float64(math.Float32frombits(r.u32()))
	if math.IsNaN(scale) || math.IsInf(scale, 0) || scale <= 0 {
		return focusTemplate{}, errors.New("invalid spell focus scale")
	}
	r.take(6 * 4) // Six fixed-width quest-item fields, including empty entries.
	if err := r.finish(); err != nil {
		return focusTemplate{}, err
	}
	return focusTemplate{entry: entry, focusID: data[0], radius: data[1]}, nil
}

func skipFocusString(r *packetReader) error {
	if r.err != nil {
		return r.err
	}
	remaining := r.data[r.off:]
	if len(remaining) > focusMaxStringBytes+1 {
		remaining = remaining[:focusMaxStringBytes+1]
	}
	n := bytes.IndexByte(remaining, 0)
	if n < 0 || !utf8.Valid(remaining[:n]) {
		return errors.New("invalid spell focus string")
	}
	r.take(n + 1)
	return r.err
}
