package buddyactions

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// decodeSpellDamage reads the ordinary 3.3.5a SMSG_SPELLNONMELEEDAMAGELOG
// payload (opcode0x250). Target precedes caster, unlike the return order.
// Layout: AzerothCore Unit::SendSpellNonMeleeDamageLog and
// WorldObject::SendSpellNonMeleeDamageLog:
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Unit/Unit.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Object/Object.cpp
// A decoded positive damage value is a server combat-log observation, not a
// cast transport acknowledgement. Zero damage (e.g. full absorb) stays zero.
// Debug extensions are deliberately unsupported and fail locally.
func decodeSpellDamage(data []byte) (caster uint64, target uint64, spell uint32, damage uint32, err error) {
	bad := errors.New("invalid ordinary spell damage log")
	target, n, err := readGUID(data)
	if err != nil || target == 0 || !bytes.Equal(packedGUID(target), data[:n]) {
		return 0, 0, 0, 0, bad
	}
	caster, m, err := readGUID(data[n:])
	if err != nil || caster == 0 || !bytes.Equal(packedGUID(caster), data[n:n+m]) {
		return 0, 0, 0, 0, bad
	}
	tail := data[n+m:]
	if len(tail) != 32 {
		return 0, 0, 0, 0, bad
	}
	spell = binary.LittleEndian.Uint32(tail)
	// schoolMask contains the seven ordinary schools; physicalLog and unused
	// are serialized bools. The last byte requests unsupported debug data.
	if spell == 0 || tail[12] == 0 || tail[12]&0x80 != 0 || tail[21] > 1 || tail[22] > 1 || tail[31] != 0 {
		return 0, 0, 0, 0, bad
	}
	damage = binary.LittleEndian.Uint32(tail[4:])
	return caster, target, spell, damage, nil
}
