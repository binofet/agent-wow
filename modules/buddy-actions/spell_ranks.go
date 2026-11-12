package buddyactions

import (
	"encoding/binary"
	"errors"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"time"
)

// Player rank chains verified against public AzerothCore spell_ranks.sql;
// costs, cast times and equal-family ranges verified in client build12340 DBCs.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/data/sql/base/db_world/spell_ranks.sql
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/shared/DataStores/DBCStructure.h
// Actual learned spell IDs remain the authority for use.
// Spell.dbc SHA256: d5cce1a83550dcfa9eb2f0251dbb11fd24c272534b2b1a9b230924a44d817ab3.
type spellRank struct {
	id, family, rank, manaPercent uint32
	castTime                      time.Duration
}

// Rank relationships and resources come from generated game data. A newly
// learned family or rank requires no controller-table edit.
func spellRankFor(id uint32) spellRank {
	r, ok := spellcatalog.Default().Lookup(id)
	if !ok {
		return spellRank{}
	}
	return spellRank{id: r.ID, family: r.Family, rank: r.Rank, manaPercent: r.CostPercent, castTime: time.Duration(r.CastMs) * time.Millisecond}
}
func spellFamily(id, family uint32) bool { return spellRankFor(id).family == family }
func highestKnownSpell(known map[uint32]bool, family uint32) spellRank {
	var best spellRank
	for id, on := range known {
		if on {
			r := spellRankFor(id)
			if r.family == family && (r.rank > best.rank || r.rank == best.rank && r.id < best.id) {
				best = r
			}
		}
	}
	return best
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
