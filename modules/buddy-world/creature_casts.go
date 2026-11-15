package buddyworld

import (
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// The longest cast time in the build 12340 spell catalog is five minutes.
const maxCreatureCastMs = 300000

type observedCreatureCast struct {
	spell    uint32
	count    byte
	target   uint64
	revision uint64
	until    time.Time
}

func (c observedCreatureCast) snapshot(now time.Time) *worldv1.CreatureCast {
	remaining := c.until.Sub(now).Milliseconds()
	if c.spell == 0 || remaining <= 0 {
		return nil
	}
	return &worldv1.CreatureCast{SpellId: c.spell, CastCount: uint32(c.count), RemainingMs: uint32(remaining), TargetGuid: c.target, Revision: c.revision}
}

func sameCreatureCastMap(creature, self *observed) bool {
	return creature != nil && creature.present && creature.mapKnown && self.present && self.mapKnown && creature.mapID == self.mapID
}

func observedCreatureDead(p *observed) bool {
	return p.known[unitHealth] && p.fields[unitHealth] == 0 || p.fields[unitDynamicFlags]&0x20 != 0
}

func (s *observations) observeCreatureCast(op uint32, c *cursor, now time.Time) (bool, error) {
	caster := c.guid()
	fullCast := op == opcode.SMSGSpellStart || op == opcode.SMSGSpellGo
	if fullCast {
		// The first packed GUID identifies the cast source (e.g. an item).
		// The following GUID identifies the unit actually doing the cast.
		caster = c.guid()
	}
	count, spell := c.u8(), c.u32()
	if c.err != nil {
		return true, c.err
	}
	p := s.creatures[caster]
	if !sameCreatureCastMap(p, &s.players[0]) {
		return false, nil
	}
	if spell == 0 {
		return true, errLayout
	}
	if op == opcode.SMSGSpellStart {
		c.u32() // cast flags
		duration := c.u32()
		var target uint64
		if c.u32()&2 != 0 { // TARGET_FLAG_UNIT is serialized first.
			target = c.guid()
		}
		if c.err != nil {
			return true, c.err
		}
		if duration != 0 && !observedCreatureDead(p) {
			s.castRevision++
			s.target(caster).cast = observedCreatureCast{spell: spell, count: count, target: target, revision: s.castRevision,
				until: now.Add(time.Duration(min(duration, maxCreatureCastMs)) * time.Millisecond)}
		}
		return true, nil
	}
	if op == opcode.SMSGSpellGo {
		c.skip(8) // cast flags and server clock
		c.skip(int(c.u8()) * 8)
		for misses := c.u8(); misses != 0 && c.err == nil; misses-- {
			c.skip(8)
			if c.u8() == 11 { // reflected spells include a second result byte
				c.u8()
			}
		}
		c.u32() // target mask precedes optional target and flag-specific data
		if c.err != nil {
			return true, c.err
		}
	} else {
		c.u8() // interruption result
		if err := c.done(); err != nil {
			return true, err
		}
	}
	if p.cast.spell == spell && p.cast.count == count {
		s.target(caster).cast = observedCreatureCast{}
	}
	return true, nil
}
