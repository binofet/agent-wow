package buddyactions

import (
	"encoding/binary"
	"errors"
	"math"
)

const lesserHeal = uint32(2050)
const ownerGUID = uint64(3)

// Wire formats: AzerothCore SpellHandler.cpp HandleCastSpellOpcode,
// Spell.cpp SpellCastTargets::Read and WorldSession.cpp ReadMovementInfo.
// https://github.com/azerothcore/azerothcore-wotlk/tree/master/src/server/game
// Spell2050/rank1 verified from the downloaded build12340 Spell.dbc:
// 1500ms cast, 1500ms GCD, 40-yard friendly range, 16% base-mana cost.
// DBC field indices: src/server/shared/DataStores/DBCStructure.h.
func packedGUID(guid uint64) []byte {
	result := []byte{0}
	for i := uint(0); i < 8; i++ {
		if b := byte(guid >> (i * 8)); b != 0 {
			result[0] |= 1 << i
			result = append(result, b)
		}
	}
	return result
}

func readGUID(data []byte) (uint64, int, error) {
	if len(data) == 0 {
		return 0, 0, errors.New("missing packed GUID")
	}
	var result uint64
	n := 1
	for i := uint(0); i < 8; i++ {
		if data[0]&(1<<i) != 0 {
			if n >= len(data) {
				return 0, 0, errors.New("truncated packed GUID")
			}
			result |= uint64(data[n]) << (i * 8)
			n++
		}
	}
	return result, n, nil
}

func castPacket(count byte) []byte {
	// castCount, spellId, castFlags0, TARGET_FLAG_UNIT2, packed JoelGUID3.
	p := make([]byte, 10)
	p[0] = count
	binary.LittleEndian.PutUint32(p[1:], lesserHeal)
	binary.LittleEndian.PutUint32(p[6:], 2)
	return append(p, packedGUID(ownerGUID)...)
}

type position struct{ x, y, z, o float32 }

func finite(p position) bool {
	for _, value := range []float32{p.x, p.y, p.z, p.o} {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || math.Abs(float64(value)) > 40000 {
			return false
		}
	}
	return true
}

func movementPacket(guid uint64, clock uint32, p position, moving bool) []byte {
	result := packedGUID(guid)
	data := make([]byte, 30) // uint32 flags,uint16 flags2,uint32 time,4float,uint32 fallTime
	if moving {
		binary.LittleEndian.PutUint32(data, 1)
	} // normal forward run
	binary.LittleEndian.PutUint32(data[6:], clock)
	for i, v := range []float32{p.x, p.y, p.z, p.o} {
		binary.LittleEndian.PutUint32(data[10+i*4:], math.Float32bits(v))
	}
	return append(result, data...)
}

func initialKnown(data []byte) (bool, error) {
	if len(data) < 5 {
		return false, errors.New("truncated initial spells")
	}
	count := int(binary.LittleEndian.Uint16(data[1:]))
	if count > 4096 || len(data) < 3+count*6+2 {
		return false, errors.New("invalid initial spell count")
	}
	known := false
	for i := 0; i < count; i++ {
		if binary.LittleEndian.Uint32(data[3+i*6:]) == lesserHeal {
			known = true
		}
	}
	// Cooldown records are fixed16bytes. Reject malformed tails atomically.
	end := 3 + count*6
	cooldowns := int(binary.LittleEndian.Uint16(data[end:]))
	if len(data) != end+2+cooldowns*16 {
		return false, errors.New("invalid initial cooldowns")
	}
	return known, nil
}

func castEvent(data []byte) (guid uint64, count byte, spell uint32, offset int, err error) {
	_, n, err := readGUID(data)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	guid, m, err := readGUID(data[n:])
	if err != nil {
		return 0, 0, 0, 0, err
	}
	offset = n + m
	if len(data) < offset+13 {
		return 0, 0, 0, 0, errors.New("truncated cast event")
	}
	return guid, data[offset], binary.LittleEndian.Uint32(data[offset+1:]), offset + 5, nil
}

// Spell2050 uses one unit target. Check every mandatory target-list/body field
// before accepting a start/go observation; optional flags may add trailing data.
func validCastBody(data []byte, offset int, goEvent bool) bool {
	pos := offset + 8 // uint32 castFlags + uint32 castTime/timestamp
	if pos > len(data) {
		return false
	}
	flags := binary.LittleEndian.Uint32(data[offset:])
	// Only flag bodies possible for this ordinary level1 mana heal are
	// supported. Unknown projectile/rune/trajectory extensions fail closed.
	if flags&^uint32(0x1|0x2|0x100|0x800|0x40000) != 0 {
		return false
	}
	if goEvent {
		if pos >= len(data) {
			return false
		}
		hits := int(data[pos])
		pos++
		if hits > (len(data)-pos)/8 {
			return false
		}
		pos += hits * 8
		if pos >= len(data) {
			return false
		}
		misses := int(data[pos])
		pos++
		for i := 0; i < misses; i++ {
			if len(data)-pos < 9 {
				return false
			}
			reason := data[pos+8]
			pos += 9
			if reason == 11 { // SPELL_MISS_REFLECT carries a reflected result byte.
				if pos >= len(data) {
					return false
				}
				pos++
			}
		}
	}
	if len(data)-pos < 4 || binary.LittleEndian.Uint32(data[pos:]) != 2 {
		return false
	}
	target, n, err := readGUID(data[pos+4:])
	if err != nil || target != ownerGUID {
		return false
	}
	pos += 4 + n
	if flags&0x800 != 0 {
		pos += 4
	} // CAST_FLAG_POWER_LEFT_SELF remaining mana
	return pos == len(data)
}
