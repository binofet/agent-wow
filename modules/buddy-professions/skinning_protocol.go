package buddyprofessions

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"unicode/utf8"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const skinSpell uint32 = 8613
const skinSkill uint32 = 393

// Build12340 Spell.dbc d5cce1a83550dcfa9eb2f0251dbb11fd24c272534b2b1a9b230924a44d817ab3:
// row8613 effects95/118, explicit dead-unit target25, cast16=1500ms,
// range12=5yd, totemCategory166. Policy deliberately stays within3yd.
// Wire: SpellHandler::HandleCastSpellOpcode and SpellCastTargets::Read.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Spells/Spell.cpp
func skinCastPacket(count byte, target uint64) []byte {
	p := castPacket(count, skinSpell)
	binary.LittleEndian.PutUint32(p[6:], 2) // TARGET_FLAG_UNIT, packedGUID.
	return append(p, skinPackedGUID(target)...)
}

func skinPackedGUID(g uint64) []byte {
	p := []byte{0}
	for i := uint(0); i < 8; i++ {
		if b := byte(g >> (i * 8)); b != 0 {
			p[0] |= 1 << i
			p = append(p, b)
		}
	}
	return p
}

type skinCastEvent struct {
	kind           string
	caster, target uint64
	count          byte
	spell          uint32
	failure        byte
}

// Skinning receipts retain the explicit target and require the GO hit list to
// contain that exact creature. A generic own-spell GO cannot bind a corpse.
func decodeSkinCast(code uint32, p []byte) (skinCastEvent, error) {
	r := packetReader{data: p}
	e := skinCastEvent{}
	switch code {
	case opcode.SMSGSpellStart, opcode.SMSGSpellGo:
		first := r.guid()
		e.caster = r.guid()
		e.count, e.spell = r.u8(), r.u32()
		flags := r.u32()
		r.u32()
		if first != e.caster || flags & ^uint32(0x1|0x2|0x100|0x800|0x40000) != 0 {
			return e, errors.New("unsupported skinning cast flags or cast item")
		}
		var hit uint64
		if code == opcode.SMSGSpellGo {
			e.kind = "go"
			if r.u8() != 1 {
				return e, errors.New("skinning GO requires exactly one hit")
			}
			if b := r.take(8); b != nil {
				hit = binary.LittleEndian.Uint64(b)
			}
			if r.u8() != 0 {
				return e, errors.New("skinning GO has missed targets")
			}
		} else {
			e.kind = "start"
		}
		if r.u32() != 2 {
			return e, errors.New("skinning requires explicit UNIT target")
		}
		e.target = r.guid()
		if e.target>>48 != 0xf130 || uint32(e.target) == 0 || code == opcode.SMSGSpellGo && hit != e.target {
			return e, errors.New("skinning target or hit GUID invalid")
		}
		if flags&0x800 != 0 {
			r.u32()
		}
	case opcode.SMSGCastFailed:
		e.kind = "failed"
		e.count, e.spell, e.failure = r.u8(), r.u32(), r.u8()
		if e.spell != skinSpell {
			return e, errors.New("foreign skinning failure spell")
		}
		n, err := failureTailSize(7620, e.failure) // Fishing shares the absent item-limit metadata.
		if e.failure == 130 {
			n, err = 4, nil
		} // Only category166, no second category.
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
			return e, errors.New("invalid interrupted skinning reason")
		}
	default:
		return e, errors.New("unsupported skinning cast opcode")
	}
	if err := r.finish(); err != nil {
		return e, err
	}
	return e, nil
}

func decodeSkinLoot(p []byte) (lootWindow, error)         { return decodeCorpseWindow(p, 6) }
func decodeNormalCorpseLoot(p []byte) (lootWindow, error) { return decodeCorpseWindow(p, 1) }

// Spell::EffectSkinning sends the creatureGUID and LOOT_SKINNING6 directly;
// no second CMSG_LOOT is required. Rows are the normal22byte LootView form.
func decodeCorpseWindow(p []byte, kind byte) (lootWindow, error) {
	if len(p) < 14 || p[8] != kind {
		return lootWindow{}, errors.New("unsupported corpse loot type")
	}
	n := int(p[13])
	if n > 16 || len(p) != 14+n*22 {
		return lootWindow{}, errors.New("invalid bounded corpse loot size")
	}
	w := lootWindow{guid: binary.LittleEndian.Uint64(p), gold: binary.LittleEndian.Uint32(p[9:])}
	if w.guid>>48 != 0xf130 || uint32(w.guid) == 0 || kind == 6 && w.gold != 0 {
		return lootWindow{}, errors.New("invalid corpse GUID or skinning money")
	}
	seen := [256]bool{}
	for i := 0; i < n; i++ {
		row := p[14+i*22 : 14+(i+1)*22]
		v := lootSlot{slot: row[0], entry: binary.LittleEndian.Uint32(row[1:]), count: binary.LittleEndian.Uint32(row[5:])}
		if seen[v.slot] || v.entry == 0 || v.count == 0 || v.count > 1000 || row[21] != 0 && row[21] != 4 {
			return lootWindow{}, errors.New("unsupported corpse loot slot")
		}
		seen[v.slot] = true
		w.slots = append(w.slots, v)
	}
	return w, nil
}

type skinTemplate struct {
	entry, typeFlags, creatureType uint32
	name                           string
}

func skinQueryPacket(entry uint32, guid uint64) []byte {
	p := make([]byte, 12)
	binary.LittleEndian.PutUint32(p, entry)
	binary.LittleEndian.PutUint64(p[4:], guid)
	return p
}

// Public QueryHandler.cpp: static metadata only, never presence or ownership.
// Alternate harvesting flags map to herbalism/mining/engineering, not393.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/QueryHandler.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Creature/CreatureData.h
func decodeSkinTemplate(p []byte) (skinTemplate, error) {
	bad := errors.New("invalid bounded creature query response")
	if len(p) < 4 || len(p) > 3*4097+80 {
		return skinTemplate{}, bad
	}
	r := packetReader{data: p}
	m := skinTemplate{entry: r.u32()}
	if m.entry == 0 || m.entry&0x80000000 != 0 {
		return skinTemplate{}, bad
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
		return skinTemplate{}, bad
	}
	if b := r.take(3); len(b) != 3 || b[0] != 0 || b[1] != 0 || b[2] != 0 {
		return skinTemplate{}, bad
	}
	for i := 0; i < 2; i++ {
		if _, err = readString(); err != nil {
			return skinTemplate{}, err
		}
	}
	m.typeFlags, m.creatureType = r.u32(), r.u32()
	r.take(8 * 4) // family,rank,killcredit2,models4.
	for i := 0; i < 2; i++ {
		v := float64(math.Float32frombits(r.u32()))
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return skinTemplate{}, bad
		}
	}
	if r.u8() > 1 {
		return skinTemplate{}, bad
	}
	r.take(7 * 4) // questitems6,movementID.
	if err := r.finish(); err != nil {
		return skinTemplate{}, err
	}
	if m.creatureType == 0 || m.creatureType > 13 {
		return skinTemplate{}, bad
	}
	return m, nil
}
