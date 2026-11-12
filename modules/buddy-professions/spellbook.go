package buddyprofessions

import (
	"encoding/binary"
	"errors"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// spellBook is protected by its owning service's mutex. A complete login list
// establishes the baseline; skill acquisition or a training wrapper never does.
type spellBook struct {
	known  bool
	spells map[uint32]bool
}

func (b *spellBook) has(id uint32) bool {
	_, supported := recipeForSpell(id)
	return b.known && supported && b.spells[id]
}

func validSpellID(id uint32) bool { return id != 0 && id < 0x80000000 }

func (b *spellBook) invalidate() {
	b.known = false
	b.spells = nil
}

// Packet layouts follow normal Player::SendInitialSpells/SendLearnPacket and
// rank replacement. The same framing is covered by buddy-actions' spell tests.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Player/Player.cpp
// No IDs from packet bytes authorize a spell outside the explicit catalog.
func (b *spellBook) onPacket(op uint32, payload []byte) error {
	bad := func() error {
		// A malformed removal may have removed a recipe we previously knew.
		// Require a new complete baseline instead of retaining uncertain access.
		b.invalidate()
		return errors.New("malformed profession spell knowledge packet")
	}
	if op == opcode.SMSGInitialSpells {
		b.invalidate()
		if len(payload) < 5 {
			return bad()
		}
		count := int(binary.LittleEndian.Uint16(payload[1:]))
		end := 3 + count*6
		if count > 4096 || len(payload) < end+2 {
			return bad()
		}
		cooldowns := int(binary.LittleEndian.Uint16(payload[end:]))
		if len(payload) != end+2+cooldowns*16 {
			return bad()
		}
		next := make(map[uint32]bool, len(starterRecipes))
		seen := make(map[uint32]bool, count)
		for i := 0; i < count; i++ {
			id := binary.LittleEndian.Uint32(payload[3+i*6:])
			if !validSpellID(id) || seen[id] {
				return bad()
			}
			seen[id] = true
			if _, supported := recipeForSpell(id); supported {
				next[id] = true
			}
		}
		b.spells, b.known = next, true
		return nil
	}

	var remove, add uint32
	switch op {
	case opcode.SMSGLearnedSpell:
		if len(payload) != 6 || binary.LittleEndian.Uint16(payload[4:]) != 0 {
			return bad()
		}
		add = binary.LittleEndian.Uint32(payload)
		if !validSpellID(add) {
			return bad()
		}
	case opcode.SMSGRemovedSpell:
		if len(payload) != 4 {
			return bad()
		}
		remove = binary.LittleEndian.Uint32(payload)
		if !validSpellID(remove) {
			return bad()
		}
	case opcode.SMSGSupercededSpell:
		if len(payload) != 8 {
			return bad()
		}
		remove, add = binary.LittleEndian.Uint32(payload), binary.LittleEndian.Uint32(payload[4:])
		if !validSpellID(remove) || !validSpellID(add) || remove == add {
			return bad()
		}
	default:
		return nil
	}
	if !b.known {
		// A valid incremental event cannot supply the missing complete baseline.
		return nil
	}
	delete(b.spells, remove)
	if _, supported := recipeForSpell(add); supported {
		b.spells[add] = true
	}
	return nil
}
