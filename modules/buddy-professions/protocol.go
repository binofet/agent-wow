package buddyprofessions

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Cast and channel framing: HandleCastSpellOpcode/HandleCancelChanneling and
// SpellCastTargets::Write, SendSpellStart/Go, WriteCastResultInfo, SendInterrupted.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/SpellHandler.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Spells/Spell.cpp
// Flag values: https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Spells/Spell.h
func castPacket(count byte, spell uint32) []byte {
	p := make([]byte, 10)
	p[0] = count
	binary.LittleEndian.PutUint32(p[1:], spell)
	// castFlags byte and TARGET_FLAG_NONE uint32 remain zero. These learned
	// starter spells select their caster/destination implicitly, not unit2.
	return p
}

type castEvent struct {
	kind       string
	caster     uint64
	count      byte
	spell      uint32
	failure    uint8
	durationMs uint32
}

func decodeCast(op uint32, payload []byte) (castEvent, error) {
	r := packetReader{data: payload}
	e := castEvent{}
	switch op {
	case uint32(opcode.SMSGSpellStart), uint32(opcode.SMSGSpellGo):
		// The first GUID may be a cast item; the second is the real caster.
		// The controller binds the real caster, count and spell to its attempt.
		r.guid()
		e.caster = r.guid()
		e.count, e.spell = r.u8(), r.u32()
		flags, clock := r.u32(), r.u32()
		// Starter casts have no projectile, rune, missile or visual-chain body.
		// Reject unimplemented extensions rather than accept a valid prefix.
		if flags&^uint32(0x1|0x2|0x100|0x800|0x40000) != 0 {
			return castEvent{}, errors.New("unsupported cast flags")
		}
		goEvent := op == uint32(opcode.SMSGSpellGo)
		if goEvent {
			e.kind = "go"
			hits := int(r.u8())
			r.take(hits * 8) // Unpacked target GUIDs.
			misses := int(r.u8())
			for i := 0; i < misses && r.err == nil; i++ {
				r.take(8)
				miss := r.u8()
				if miss == 0 || miss > 11 {
					return castEvent{}, errors.New("invalid cast miss result")
				}
				if miss == 11 && r.u8() > 11 { // Reflect has one extra result byte.
					return castEvent{}, errors.New("invalid reflected result")
				}
			}
		} else {
			e.kind, e.durationMs = "start", clock
		}
		targetMask := r.u32()
		if targetMask&^uint32(0x42) != 0 {
			return castEvent{}, errors.New("unsupported starter cast target mask")
		}
		// SelectSpellTargets sets UNIT for an implicit caster target but
		// SelectImplicitCasterObjectTargets only adds to the hit list. The
		// explicit object GUID remains empty for our target-mask0 requests.
		if targetMask&2 != 0 && r.guid() != 0 {
			return castEvent{}, errors.New("unsupported explicit starter unit target")
		}
		if targetMask&0x40 != 0 { // Fishing chooses a water destination.
			if r.guid() != 0 {
				return castEvent{}, errors.New("unsupported cast destination transport")
			}
			for i := 0; i < 3; i++ {
				v := float64(math.Float32frombits(r.u32()))
				if math.IsNaN(v) || math.IsInf(v, 0) {
					return castEvent{}, errors.New("invalid cast destination")
				}
			}
		}
		if flags&0x800 != 0 {
			r.u32() // CAST_FLAG_POWER_LEFT_SELF.
		}
		if goEvent && targetMask&0x40 != 0 && r.u8() != 0 {
			return castEvent{}, errors.New("invalid cast destination tail")
		}
	case uint32(opcode.SMSGCastFailed):
		e.kind = "failed"
		e.count, e.spell, e.failure = r.u8(), r.u32(), r.u8()
		n, err := failureTailSize(e.spell, e.failure)
		if err != nil {
			return castEvent{}, err
		}
		r.take(n)
	case uint32(opcode.SMSGSpellFailure):
		e.kind = "failed"
		e.caster, e.count, e.spell, e.failure = r.guid(), r.u8(), r.u32(), r.u8()
		// SendInterrupted has no WriteCastResultInfo reason-specific tail.
		if !validFailure(e.failure) {
			return castEvent{}, errors.New("invalid spell failure")
		}
	case uint32(opcode.MSGChannelStart):
		e.kind = "channel_start"
		e.caster, e.spell, e.durationMs = r.guid(), r.u32(), r.u32()
	case uint32(opcode.MSGChannelUpdate):
		e.kind = "channel_update"
		e.caster, e.durationMs = r.guid(), r.u32()
	case uint32(opcode.SMSGFishNotHooked):
		e.kind = "fish_not_hooked"
	case uint32(opcode.SMSGFishEscaped):
		e.kind = "fish_escaped"
	default:
		return castEvent{}, errors.New("unsupported cast opcode")
	}
	if err := r.finish(); err != nil {
		return castEvent{}, err
	}
	return e, nil
}

// Result numbers: https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/shared/SharedDefines.h
func validFailure(reason byte) bool { return reason > 0 && reason <= 187 }

func failureTailSize(spell uint32, reason byte) (int, error) {
	if !validFailure(reason) {
		return 0, errors.New("invalid cast failure")
	}
	switch reason {
	case 29, 30, 31, 55, 150: // Equipment class/subclass, items, minimum skill.
		return 8, nil
	case 54, 100, 101, 102, 147, 172, 181:
		return 4, nil
	case 130, 131: // Variable totem/category lists require known spell metadata.
		switch spell {
		case 3275, 2538, 2540, 7620: // All four verified starter rows have none.
			return 0, nil
		}
		return 0, errors.New("unsupported cast failure totem metadata")
	case 129: // ItemLimitCategory is conditional on the created item template.
		if spell == 7620 { // Fishing has no create-item effect.
			return 0, nil
		}
		return 0, errors.New("unsupported cast failure item-limit metadata")
	default:
		return 0, nil
	}
}

type itemPush struct {
	player            uint64
	received, created bool
	bag               byte
	slot, entry       uint32
	added, total      uint32
}

// SendNewItem writes three uint32 booleans, including the chat flag, then the
// actual bag/slot (0xffffffff slot means an existing stack), suffix and property.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Player/PlayerStorage.cpp
func decodePush(p []byte) (itemPush, error) {
	if len(p) != 45 {
		return itemPush{}, errors.New("invalid item push size")
	}
	received, created, chat := binary.LittleEndian.Uint32(p[8:]), binary.LittleEndian.Uint32(p[12:]), binary.LittleEndian.Uint32(p[16:])
	if received > 1 || created > 1 || chat > 1 {
		return itemPush{}, errors.New("invalid item push flags")
	}
	e := itemPush{player: binary.LittleEndian.Uint64(p), received: received == 1, created: created == 1, bag: p[20], slot: binary.LittleEndian.Uint32(p[21:]), entry: binary.LittleEndian.Uint32(p[25:]), added: binary.LittleEndian.Uint32(p[37:]), total: binary.LittleEndian.Uint32(p[41:])}
	if e.entry == 0 || e.added == 0 || e.total < e.added {
		return itemPush{}, errors.New("invalid item push counts")
	}
	return e, nil
}

type lootSlot struct {
	slot         byte
	entry, count uint32
}

type lootWindow struct {
	guid  uint64
	gold  uint32
	slots []lootSlot
}

// Player::SendLoot uses wire LOOT_FISHING3 for both normal and junk fishing.
// LootItem/LootView serialization supplies 22 bytes per slot; this narrow path
// accepts normal slots only and never turns a locked/rolled slot into normal.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Player/Player.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Loot/LootMgr.cpp
func decodeFishingLoot(p []byte) (lootWindow, error) {
	if len(p) < 14 || p[8] != 3 {
		return lootWindow{}, errors.New("unsupported fishing loot response")
	}
	n := int(p[13])
	if n > 16 || len(p) != 14+n*22 {
		return lootWindow{}, errors.New("invalid fishing loot size")
	}
	w := lootWindow{guid: binary.LittleEndian.Uint64(p), gold: binary.LittleEndian.Uint32(p[9:])}
	seen := [256]bool{}
	for i := 0; i < n; i++ {
		row := p[14+i*22 : 14+(i+1)*22]
		s := lootSlot{slot: row[0], entry: binary.LittleEndian.Uint32(row[1:]), count: binary.LittleEndian.Uint32(row[5:])}
		// displayID, suffix factor and random-property ID occupy bytes9..20.
		if seen[s.slot] || s.entry == 0 || s.count == 0 || row[21] != 0 {
			return lootWindow{}, errors.New("invalid or unsupported fishing loot slot")
		}
		seen[s.slot] = true
		w.slots = append(w.slots, s)
	}
	return w, nil
}

// Public request handlers read a full GUID (not a packed GUID), one loot-slot
// byte, and one channel spell uint32 respectively.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/SpellHandler.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/LootHandler.cpp
func gameObjectUsePacket(guid uint64) []byte { return fullGUIDPacket(guid) }
func storeLootPacket(slot byte) []byte       { return []byte{slot} }
func releaseLootPacket(guid uint64) []byte   { return fullGUIDPacket(guid) }
func cancelChannelPacket(spell uint32) []byte {
	p := make([]byte, 4)
	binary.LittleEndian.PutUint32(p, spell)
	return p
}

func fullGUIDPacket(guid uint64) []byte {
	p := make([]byte, 8)
	binary.LittleEndian.PutUint64(p, guid)
	return p
}

type packetReader struct {
	data []byte
	off  int
	err  error
}

func (r *packetReader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || n > len(r.data)-r.off {
		r.err = errors.New("truncated profession packet")
		return nil
	}
	p := r.data[r.off : r.off+n]
	r.off += n
	return p
}
func (r *packetReader) u8() byte {
	if p := r.take(1); p != nil {
		return p[0]
	}
	return 0
}
func (r *packetReader) u32() uint32 {
	if p := r.take(4); p != nil {
		return binary.LittleEndian.Uint32(p)
	}
	return 0
}
func (r *packetReader) guid() uint64 {
	mask := r.u8()
	var guid uint64
	for i := uint(0); i < 8; i++ {
		if mask&(1<<i) != 0 {
			guid |= uint64(r.u8()) << (i * 8)
		}
	}
	return guid
}
func (r *packetReader) finish() error {
	if r.err != nil {
		return r.err
	}
	if r.off != len(r.data) {
		return errors.New("trailing profession packet data")
	}
	return nil
}
