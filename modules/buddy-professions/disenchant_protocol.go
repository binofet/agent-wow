package buddyprofessions

import (
	"encoding/binary"
	"errors"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const disenchantSpell uint32 = 13262
const disenchantSkill uint32 = 333

// Build12340 Spell.dbc d5cce1a83550dcfa9eb2f0251dbb11fd24c272534b2b1a9b230924a44d817ab3:
// row13262 effects99/0/0, explicit ITEM target16, cast14=3000ms,
// no implicit targets, totems/categories/reagents/tools or power cost.
// Wire: SpellHandler::HandleCastSpellOpcode and SpellCastTargets::Read.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Spells/Spell.cpp
func disenchantCastPacket(count byte, target uint64) []byte {
	p := castPacket(count, disenchantSpell)
	binary.LittleEndian.PutUint32(p[6:], 0x10) // TARGET_FLAG_ITEM, packedGUID.
	return append(p, disenchantPackedGUID(target)...)
}

func disenchantPackedGUID(g uint64) []byte {
	p := []byte{0}
	for i := uint(0); i < 8; i++ {
		if b := byte(g >> (i * 8)); b != 0 {
			p[0] |= 1 << i
			p = append(p, b)
		}
	}
	return p
}

type disenchantCastEvent struct {
	kind           string
	caster, target uint64
	count          byte
	spell          uint32
	failure        byte
}

// Item targets are absent from the GO hit list. The explicit ITEM mask and
// packed owned-item GUID bind START/GO to this selected source.
func decodeDisenchantCast(code uint32, p []byte) (disenchantCastEvent, error) {
	r := packetReader{data: p}
	e := disenchantCastEvent{}
	switch code {
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo:
		first := r.guid()
		e.caster = r.guid()
		e.count, e.spell = r.u8(), r.u32()
		flags := r.u32()
		r.u32()
		if first != e.caster || flags & ^uint32(0x1|0x2|0x100|0x800|0x40000) != 0 {
			return e, errors.New("unsupported disenchant cast flags or cast item")
		}
		if code == opcode.SMSGSpellGo {
			e.kind = "go"
			if r.u8() != 0 || r.u8() != 0 {
				return e, errors.New("disenchant GO requires zero unit/gameobject hits and misses")
			}
		} else {
			e.kind = "start"
		}
		if r.u32() != 0x10 {
			return e, errors.New("disenchant requires explicit ITEM target")
		}
		e.target = r.guid()
		if !carriedItemGUID(e.target) {
			return e, errors.New("disenchant target item GUID invalid")
		}
		if flags&0x800 != 0 {
			r.u32()
		}
	case opcode.SMSGCastFailed:
		e.kind = "failed"
		e.count, e.spell, e.failure = r.u8(), r.u32(), r.u8()
		if e.spell != disenchantSpell {
			return e, errors.New("foreign disenchant failure spell")
		}
		n, err := failureTailSize(7620, e.failure) // Fishing shares the absent item-limit metadata.
		if e.failure == 130 {
			n, err = 0, nil
		} // No category requirement.
		if e.failure == 131 {
			n, err = 0, nil
		} // No item-totem requirements.
		if err != nil {
			return e, err
		}
		r.take(n)
	case opcode.SMSGSpellFailure:
		e.kind = "failed"
		e.caster, e.count, e.spell, e.failure = r.guid(), r.u8(), r.u32(), r.u8()
		if !validFailure(e.failure) {
			return e, errors.New("invalid interrupted disenchant reason")
		}
	default:
		return e, errors.New("unsupported disenchant cast opcode")
	}
	if err := r.finish(); err != nil {
		return e, err
	}
	return e, nil
}

func decodeDisenchantLoot(p []byte) (lootWindow, error) {
	if len(p) < 14 || p[8] != 4 {
		return lootWindow{}, errors.New("unsupported disenchant loot type")
	}
	n := int(p[13])
	if n > 16 || len(p) != 14+n*22 {
		return lootWindow{}, errors.New("invalid bounded disenchant loot size")
	}
	w := lootWindow{guid: binary.LittleEndian.Uint64(p), gold: binary.LittleEndian.Uint32(p[9:])}
	if !carriedItemGUID(w.guid) || w.gold != 0 {
		return lootWindow{}, errors.New("invalid disenchant GUID or disenchant money")
	}
	seen := [256]bool{}
	for i := 0; i < n; i++ {
		row := p[14+i*22 : 14+(i+1)*22]
		v := lootSlot{slot: row[0], entry: binary.LittleEndian.Uint32(row[1:]), count: binary.LittleEndian.Uint32(row[5:])}
		if seen[v.slot] || v.entry == 0 || v.count == 0 || v.count > 1000 || row[21] != 0 && row[21] != 4 {
			return lootWindow{}, errors.New("unsupported disenchant loot slot")
		}
		seen[v.slot] = true
		w.slots = append(w.slots, v)
	}
	return w, nil
}

// ItemHandler::HandleItemQuerySingleOpcode reads only the entry uint32.
func disenchantQueryPacket(entry uint32) []byte {
	return binary.LittleEndian.AppendUint32(nil, entry)
}
