package buddyactions

import (
	"encoding/binary"
	"errors"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"time"
)

// Player rank chains verified against public AzerothCore spell_ranks.sql;
// costs, cast times and equal-family ranges verified in client build12340 DBCs.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/data/sql/base/db_world/spell_ranks.sql
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/shared/DataStores/DBCStructure.h
// NPC-only same-name spells are deliberately absent from this fixed catalog.
// Spell.dbc SHA256: d5cce1a83550dcfa9eb2f0251dbb11fd24c272534b2b1a9b230924a44d817ab3.
type spellRank struct {
	id, family, rank, manaPercent uint32
	castTime                      time.Duration
}

var spellRankCatalog = []spellRank{
	{id: 2050, family: 2050, rank: 1, manaPercent: 16, castTime: 1500 * time.Millisecond},
	{id: 2052, family: 2050, rank: 2, manaPercent: 21, castTime: 2000 * time.Millisecond},
	{id: 2053, family: 2050, rank: 3, manaPercent: 27, castTime: 2500 * time.Millisecond},
	{id: 585, family: 585, rank: 1, manaPercent: 9, castTime: 1500 * time.Millisecond},
	{id: 591, family: 585, rank: 2, manaPercent: 12, castTime: 2000 * time.Millisecond},
	{id: 598, family: 585, rank: 3, manaPercent: 15, castTime: 2500 * time.Millisecond},
	{id: 984, family: 585, rank: 4, manaPercent: 15, castTime: 2500 * time.Millisecond},
	{id: 1004, family: 585, rank: 5, manaPercent: 15, castTime: 2500 * time.Millisecond},
	{id: 6060, family: 585, rank: 6, manaPercent: 15, castTime: 2500 * time.Millisecond},
	{id: 10933, family: 585, rank: 7, manaPercent: 15, castTime: 2500 * time.Millisecond},
	{id: 10934, family: 585, rank: 8, manaPercent: 15, castTime: 2500 * time.Millisecond},
	{id: 25363, family: 585, rank: 9, manaPercent: 15, castTime: 2500 * time.Millisecond},
	{id: 25364, family: 585, rank: 10, manaPercent: 15, castTime: 2500 * time.Millisecond},
	{id: 48122, family: 585, rank: 11, manaPercent: 15, castTime: 2500 * time.Millisecond},
	{id: 48123, family: 585, rank: 12, manaPercent: 15, castTime: 2500 * time.Millisecond},
	{id: 133, family: 133, rank: 1, manaPercent: 8, castTime: 1500 * time.Millisecond},
	{id: 143, family: 133, rank: 2, manaPercent: 11, castTime: 2000 * time.Millisecond},
	{id: 145, family: 133, rank: 3, manaPercent: 14, castTime: 2500 * time.Millisecond},
	{id: 3140, family: 133, rank: 4, manaPercent: 16, castTime: 3000 * time.Millisecond},
	{id: 8400, family: 133, rank: 5, manaPercent: 19, castTime: 3500 * time.Millisecond},
	{id: 8401, family: 133, rank: 6, manaPercent: 19, castTime: 3500 * time.Millisecond},
	{id: 8402, family: 133, rank: 7, manaPercent: 19, castTime: 3500 * time.Millisecond},
	{id: 10148, family: 133, rank: 8, manaPercent: 19, castTime: 3500 * time.Millisecond},
	{id: 10149, family: 133, rank: 9, manaPercent: 19, castTime: 3500 * time.Millisecond},
	{id: 10150, family: 133, rank: 10, manaPercent: 19, castTime: 3500 * time.Millisecond},
	{id: 10151, family: 133, rank: 11, manaPercent: 19, castTime: 3500 * time.Millisecond},
	{id: 25306, family: 133, rank: 12, manaPercent: 19, castTime: 3500 * time.Millisecond},
	{id: 27070, family: 133, rank: 13, manaPercent: 19, castTime: 3500 * time.Millisecond},
	{id: 38692, family: 133, rank: 14, manaPercent: 19, castTime: 3500 * time.Millisecond},
	{id: 42832, family: 133, rank: 15, manaPercent: 19, castTime: 3500 * time.Millisecond},
	{id: 42833, family: 133, rank: 16, manaPercent: 19, castTime: 3500 * time.Millisecond},
	{id: 1752, family: 1752, rank: 1, manaPercent: 0, castTime: 0 * time.Millisecond},
	{id: 1757, family: 1752, rank: 2, manaPercent: 0, castTime: 0 * time.Millisecond},
	{id: 1758, family: 1752, rank: 3, manaPercent: 0, castTime: 0 * time.Millisecond},
	{id: 1759, family: 1752, rank: 4, manaPercent: 0, castTime: 0 * time.Millisecond},
	{id: 1760, family: 1752, rank: 5, manaPercent: 0, castTime: 0 * time.Millisecond},
	{id: 8621, family: 1752, rank: 6, manaPercent: 0, castTime: 0 * time.Millisecond},
	{id: 11293, family: 1752, rank: 7, manaPercent: 0, castTime: 0 * time.Millisecond},
	{id: 11294, family: 1752, rank: 8, manaPercent: 0, castTime: 0 * time.Millisecond},
	{id: 26861, family: 1752, rank: 9, manaPercent: 0, castTime: 0 * time.Millisecond},
	{id: 26862, family: 1752, rank: 10, manaPercent: 0, castTime: 0 * time.Millisecond},
	{id: 48637, family: 1752, rank: 11, manaPercent: 0, castTime: 0 * time.Millisecond},
	{id: 48638, family: 1752, rank: 12, manaPercent: 0, castTime: 0 * time.Millisecond},
}

func spellRankFor(id uint32) spellRank {
	for _, r := range spellRankCatalog {
		if r.id == id {
			return r
		}
	}
	return spellRank{}
}
func spellFamily(id, family uint32) bool { return spellRankFor(id).family == family }
func highestKnownSpell(known map[uint32]bool, family uint32) spellRank {
	var best spellRank
	for _, r := range spellRankCatalog {
		if r.family == family && known[r.id] && r.rank > best.rank {
			best = r
		}
	}
	return best
}

func trackedSpellIDs() []uint32 {
	ids := make([]uint32, 0, len(spellRankCatalog)+3)
	for _, r := range spellRankCatalog {
		ids = append(ids, r.id)
	}
	return append(ids, autoAttack, 13732, 13863)
}

// Player::SendLearnPacket emits exact6/4 bytes; rank replacement emits old,new.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Player/Player.cpp
func spellKnowledgePacket(op uint32, p []byte) (remove, add uint32, err error) {
	bad := errors.New("malformed normal spell knowledge packet")
	valid := func(id uint32) bool { return id != 0 && id <= 0x7fffffff }
	switch op {
	case opcode.SMSGLearnedSpell:
		if len(p) != 6 || binary.LittleEndian.Uint16(p[4:]) != 0 {
			return 0, 0, bad
		}
		add = binary.LittleEndian.Uint32(p)
	case opcode.SMSGRemovedSpell:
		if len(p) != 4 {
			return 0, 0, bad
		}
		remove = binary.LittleEndian.Uint32(p)
	case opcode.SMSGSupercededSpell:
		if len(p) != 8 {
			return 0, 0, bad
		}
		remove = binary.LittleEndian.Uint32(p)
		add = binary.LittleEndian.Uint32(p[4:])
	default:
		return 0, 0, bad
	}
	if remove != 0 && !valid(remove) || add != 0 && !valid(add) || remove == 0 && add == 0 {
		return 0, 0, bad
	}
	if op == opcode.SMSGSupercededSpell && (!valid(remove) || !valid(add) || remove == add) {
		return 0, 0, bad
	}
	return remove, add, nil
}
