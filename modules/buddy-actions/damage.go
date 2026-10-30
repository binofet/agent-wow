package buddyactions

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
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

// Unit::SendAttackStateUpdate: full ordinary melee damage packet. Optional
// absorb/resist/block/rage words depend on HitInfo; debug extensions fail closed.
func decodeMeleeDamage(data []byte) (attacker, target uint64, damage uint32, err error) {
	bad := errors.New("invalid ordinary melee damage log")
	if len(data) < 4 {
		return 0, 0, 0, bad
	}
	flags := binary.LittleEndian.Uint32(data)
	if flags&0xfe000001 != 0 {
		return 0, 0, 0, bad
	}
	at := 4
	attacker, n, e := readGUID(data[at:])
	if e != nil || attacker == 0 || !bytes.Equal(packedGUID(attacker), data[at:at+n]) {
		return 0, 0, 0, bad
	}
	at += n
	target, n, e = readGUID(data[at:])
	if e != nil || target == 0 || !bytes.Equal(packedGUID(target), data[at:at+n]) {
		return 0, 0, 0, bad
	}
	at += n
	if len(data)-at < 9 {
		return 0, 0, 0, bad
	}
	damage = binary.LittleEndian.Uint32(data[at:])
	if damage > 0 && flags&0x01000010 != 0 { // Fake damage and a miss are not positive-damage evidence.
		return 0, 0, 0, bad
	}
	count := int(data[at+8])
	at += 9
	if count < 1 || count > 2 || len(data)-at < count*12 {
		return 0, 0, 0, bad
	}
	var sum uint64
	for i := 0; i < count; i++ {
		school := binary.LittleEndian.Uint32(data[at:])
		f := math.Float32frombits(binary.LittleEndian.Uint32(data[at+4:]))
		d := binary.LittleEndian.Uint32(data[at+8:])
		at += 12
		if school == 0 || school&^uint32(0x7f) != 0 || float32(d) != f {
			return 0, 0, 0, bad
		}
		sum += uint64(d)
	}
	if sum != uint64(damage) {
		return 0, 0, 0, bad
	}
	if flags&0x60 != 0 {
		at += 4 * count
	}
	if flags&0x180 != 0 {
		at += 4 * count
	}
	if len(data)-at < 9 {
		return 0, 0, 0, bad
	}
	victimState := data[at]
	unknown, spell := binary.LittleEndian.Uint32(data[at+1:]), binary.LittleEndian.Uint32(data[at+5:])
	at += 9
	if victimState > 8 || unknown != 0 || spell != 0 {
		return 0, 0, 0, bad
	}
	if flags&0x2000 != 0 {
		at += 4
	}
	if flags&0x800000 != 0 {
		at += 4
	}
	if at != len(data) {
		return 0, 0, 0, bad
	}
	return attacker, target, damage, nil
}
