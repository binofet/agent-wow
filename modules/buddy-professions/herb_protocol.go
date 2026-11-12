package buddyprofessions

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"unicode/utf8"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const herbSpell uint32 = 2366
const herbSkill uint32 = 182

// Build12340 Spell.dbc d5cce1a83550dcfa9eb2f0251dbb11fd24c272534b2b1a9b230924a44d817ab3:
// row2366 effects33/118, explicit gameobject/item target23, cast14=3000ms,
// range12=5yd, no totems/categories/reagents/tools. Policy deliberately stays within3yd.
// Wire: SpellHandler::HandleCastSpellOpcode and SpellCastTargets::Read.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Spells/Spell.cpp
func herbCastPacket(count byte, target uint64) []byte {
	p := castPacket(count, herbSpell)
	binary.LittleEndian.PutUint32(p[6:], 0x800) // TARGET_FLAG_GAMEOBJECT, packedGUID.
	return append(p, herbPackedGUID(target)...)
}

func herbPackedGUID(g uint64) []byte {
	p := []byte{0}
	for i := uint(0); i < 8; i++ {
		if b := byte(g >> (i * 8)); b != 0 {
			p[0] |= 1 << i
			p = append(p, b)
		}
	}
	return p
}

type herbCastEvent struct {
	kind           string
	caster, target uint64
	count          byte
	spell          uint32
	failure        byte
}

// Herbalism receipts retain the explicit target and require the GO hit list to
// contain that exact gameobject. A generic own-spell GO cannot bind a resource.
func decodeHerbCast(code uint32, p []byte) (herbCastEvent, error) {
	r := packetReader{data: p}
	e := herbCastEvent{}
	switch code {
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo:
		first := r.guid()
		e.caster = r.guid()
		e.count, e.spell = r.u8(), r.u32()
		flags := r.u32()
		r.u32()
		if first != e.caster || flags & ^uint32(0x1|0x2|0x100|0x800|0x40000) != 0 {
			return e, errors.New("unsupported herbalism cast flags or cast item")
		}
		var hit uint64
		if code == opcode.SMSGSpellGo {
			e.kind = "go"
			if r.u8() != 1 {
				return e, errors.New("herbalism GO requires exactly one hit")
			}
			if b := r.take(8); b != nil {
				hit = binary.LittleEndian.Uint64(b)
			}
			if r.u8() != 0 {
				return e, errors.New("herbalism GO has missed targets")
			}
		} else {
			e.kind = "start"
		}
		if r.u32() != 0x800 {
			return e, errors.New("herbalism requires explicit GAMEOBJECT target")
		}
		e.target = r.guid()
		if e.target>>48 != 0xf110 || e.target&0xffffff == 0 || code == opcode.SMSGSpellGo && hit != e.target {
			return e, errors.New("herbalism target or hit GUID invalid")
		}
		if flags&0x800 != 0 {
			r.u32()
		}
	case opcode.SMSGCastFailed:
		e.kind = "failed"
		e.count, e.spell, e.failure = r.u8(), r.u32(), r.u8()
		if e.spell != herbSpell {
			return e, errors.New("foreign herbalism failure spell")
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
			return e, errors.New("invalid interrupted herbalism reason")
		}
	default:
		return e, errors.New("unsupported herbalism cast opcode")
	}
	if err := r.finish(); err != nil {
		return e, err
	}
	return e, nil
}

func decodeHerbLoot(p []byte) (lootWindow, error) {
	if len(p) < 14 || p[8] != 6 {
		return lootWindow{}, errors.New("unsupported herb loot type")
	}
	n := int(p[13])
	if n > 16 || len(p) != 14+n*22 {
		return lootWindow{}, errors.New("invalid bounded herb loot size")
	}
	w := lootWindow{guid: binary.LittleEndian.Uint64(p), gold: binary.LittleEndian.Uint32(p[9:])}
	if w.guid>>48 != 0xf110 || w.guid&0xffffff == 0 || w.gold != 0 {
		return lootWindow{}, errors.New("invalid herb GUID or herbalism money")
	}
	seen := [256]bool{}
	for i := 0; i < n; i++ {
		row := p[14+i*22 : 14+(i+1)*22]
		v := lootSlot{slot: row[0], entry: binary.LittleEndian.Uint32(row[1:]), count: binary.LittleEndian.Uint32(row[5:])}
		if seen[v.slot] || v.entry == 0 || v.count == 0 || v.count > 1000 || row[21] != 0 && row[21] != 4 {
			return lootWindow{}, errors.New("unsupported herb loot slot")
		}
		seen[v.slot] = true
		w.slots = append(w.slots, v)
	}
	return w, nil
}

type herbTemplate struct {
	entry                 uint32
	name                  string
	lockID, requiredSkill uint32
	data                  [24]uint32
}

func herbQueryPacket(entry uint32, guid uint64) []byte {
	p := make([]byte, 12)
	binary.LittleEndian.PutUint32(p, entry)
	binary.LittleEndian.PutUint64(p[4:], guid)
	return p
}

// Lock.dbc build12340 SHA256 c49bd7ed970583a3660d5d16d6917e91e46aafc9e9ed7926281f4889d45904d0:
// exactly one key case Type2(LOCK_KEY_SKILL), Index2(LOCKTYPE_HERBALISM),
// Skill=below; seven remaining cases and all Action fields are zero.
// Mixed locks259/1702/1714 and every unverified row remain unsupported.
func herbLockMinimum(lock uint32) (uint32, bool) {
	switch lock {
	case 29:
		return 0, true
	case 30:
		return 15, true
	case 8:
		return 25, true
	case 9:
		return 50, true
	case 31:
		return 70, true
	case 10:
		return 75, true
	}
	return 0, false
}

// QueryHandler::HandleGameObjectQueryOpcode: entry/type/display, name plus
// three empty names, icon/caption/unk1,24 data words,scale,six quest items.
// Public template metadata supplies no GUID/visibility/position or respawn timer.
// GameObjectData.h chest data0=LockID,1=lootID,6=event,7=trap,8=quest,15=group rules.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/QueryHandler.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/GameObject/GameObjectData.h
func decodeHerbTemplate(p []byte) (herbTemplate, error) {
	bad := errors.New("unsupported_or_invalid_bounded_herb_chest_template")
	if len(p) < 12 || len(p) > 12+3+4*4097+24*4+4+6*4 {
		return herbTemplate{}, bad
	}
	r := packetReader{data: p}
	m := herbTemplate{entry: r.u32()}
	kind := r.u32()
	r.u32()
	if m.entry == 0 || m.entry&0x80000000 != 0 || kind != 3 {
		return herbTemplate{}, bad
	}
	readString := func() (string, error) {
		remaining := r.data[r.off:]
		if len(remaining) > 4097 {
			remaining = remaining[:4097]
		}
		n := bytes.IndexByte(remaining, 0)
		if n < 0 || !utf8.Valid(remaining[:n]) {
			return "", bad
		}
		v := string(remaining[:n])
		r.take(n + 1)
		return v, nil
	}
	var err error
	if m.name, err = readString(); err != nil || m.name == "" {
		return herbTemplate{}, bad
	}
	if names := r.take(3); len(names) != 3 || names[0] != 0 || names[1] != 0 || names[2] != 0 {
		return herbTemplate{}, bad
	}
	for i := 0; i < 3; i++ {
		if _, err = readString(); err != nil {
			return herbTemplate{}, err
		}
	}
	for i := range m.data {
		m.data[i] = r.u32()
	}
	scale := float64(math.Float32frombits(r.u32()))
	if math.IsNaN(scale) || math.IsInf(scale, 0) || scale <= 0 {
		return herbTemplate{}, bad
	}
	for i := 0; i < 6; i++ {
		if r.u32() != 0 {
			return herbTemplate{}, bad
		}
	}
	if err := r.finish(); err != nil {
		return herbTemplate{}, err
	}
	m.lockID = m.data[0]
	var ok bool
	m.requiredSkill, ok = herbLockMinimum(m.lockID)
	if !ok || m.data[1] == 0 || m.data[2] != 0 || m.data[3] != 1 || m.data[6] != 0 || m.data[7] != 0 || m.data[8] != 0 || m.data[11] != 0 || m.data[15] != 0 {
		return herbTemplate{}, bad
	}
	// Deprecated min/max-success fields4/5 are scalar metadata, not booleans.
	for _, i := range []int{10, 12, 13, 16} {
		if m.data[i] > 1 {
			return herbTemplate{}, bad
		}
	}
	for i := 17; i < 24; i++ {
		if m.data[i] != 0 {
			return herbTemplate{}, bad
		}
	}
	return m, nil
}
