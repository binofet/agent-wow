package buddytraining

import (
	"encoding/binary"
	"errors"
	"unicode/utf8"

	trainingv1 "github.com/agent-wow/agent-wow/modules/buddy-training/api"
)

// NPCPackets.cpp/.h: raw trainerGUID,type,count; each spell is38bytes;
// ID,u8state,cost,2pointcosts,u8level,skill,rank,3abilities; then C-string.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Server/Packets/NPCPackets.cpp
func decodeList(p []byte) (*trainingv1.Offer, error) {
	bad := errors.New("malformed trainer list")
	if len(p) < 17 || len(p) > 32768 {
		return nil, bad
	}
	guid := binary.LittleEndian.Uint64(p)
	kind := binary.LittleEndian.Uint32(p[8:])
	count := binary.LittleEndian.Uint32(p[12:])
	if !creatureGUID(guid) || kind > 3 || count > 256 || uint64(count)*38+17 > uint64(len(p)) {
		return nil, bad
	}
	out := &trainingv1.Offer{NpcGuid: guid, TrainerType: kind}
	seen := map[uint32]bool{}
	for i := uint32(0); i < count; i++ {
		b := p[16+int(i)*38 : 16+int(i+1)*38]
		s := &trainingv1.Spell{SpellId: binary.LittleEndian.Uint32(b), Usable: uint32(b[4]), CostCopper: binary.LittleEndian.Uint32(b[5:]), PointCost: []uint32{binary.LittleEndian.Uint32(b[9:]), binary.LittleEndian.Uint32(b[13:])}, RequiredLevel: uint32(b[17]), RequiredSkill: binary.LittleEndian.Uint32(b[18:]), RequiredSkillRank: binary.LittleEndian.Uint32(b[22:]), RequiredAbilities: []uint32{binary.LittleEndian.Uint32(b[26:]), binary.LittleEndian.Uint32(b[30:]), binary.LittleEndian.Uint32(b[34:])}}
		if s.SpellId == 0 || s.SpellId > 0x7fffffff || seen[s.SpellId] || s.Usable > 2 || s.CostCopper > 0x7fffffff || s.RequiredLevel > 80 {
			return nil, bad
		}
		for _, v := range append(append([]uint32{}, s.PointCost...), append([]uint32{s.RequiredSkill, s.RequiredSkillRank}, s.RequiredAbilities...)...) {
			if v > 0x7fffffff {
				return nil, bad
			}
		}
		seen[s.SpellId] = true
		out.Spells = append(out.Spells, s)
	}
	text := p[16+int(count)*38:]
	if len(text) > 4097 || text[len(text)-1] != 0 || !utf8.Valid(text[:len(text)-1]) {
		return nil, bad
	}
	for _, b := range text[:len(text)-1] {
		if b == 0 {
			return nil, bad
		}
	}
	out.Greeting = string(text[:len(text)-1])
	return out, nil
}
func creatureGUID(g uint64) bool { return g>>48 == 0xf130 && g>>24&0xffffff != 0 && g&0xffffff != 0 }
func listPacket(g uint64) []byte { return binary.LittleEndian.AppendUint64(nil, g) }
func buyPacket(g uint64, spell uint32) []byte {
	return binary.LittleEndian.AppendUint32(listPacket(g), spell)
}
func decodeReceipt(p []byte, failed bool) (uint64, uint32, uint32, error) {
	n := 12
	if failed {
		n = 16
	}
	if len(p) != n {
		return 0, 0, 0, errors.New("malformed trainer receipt")
	}
	g, id := binary.LittleEndian.Uint64(p), binary.LittleEndian.Uint32(p[8:])
	if !creatureGUID(g) || id == 0 || id > 0x7fffffff {
		return 0, 0, 0, errors.New("invalid trainer receipt identity")
	}
	var reason uint32
	if failed {
		reason = binary.LittleEndian.Uint32(p[12:])
		if reason > 2 {
			return 0, 0, 0, errors.New("unsupported trainer failure")
		}
	}
	return g, id, reason, nil
}

// Player::SendLearnPacket / addSpell use spellID+u16zero and oldID+newID.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Player/Player.cpp
func learnedPacket(p []byte, superceded bool) (uint32, error) {
	if superceded {
		if len(p) != 8 || binary.LittleEndian.Uint32(p) == 0 {
			return 0, errors.New("invalid superseded spell")
		}
		p = p[4:]
	} else if len(p) != 6 || binary.LittleEndian.Uint16(p[4:]) != 0 {
		return 0, errors.New("invalid learned spell")
	}
	id := binary.LittleEndian.Uint32(p)
	if id == 0 || id > 0x7fffffff {
		return 0, errors.New("invalid learned spell identity")
	}
	return id, nil
}
