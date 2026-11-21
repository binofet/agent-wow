package buddyactions

import (
	"time"

	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

// These are the exact local deadlines contributed by one attempted cast, not
// observed cooldowns. Keep them attached to that native attempt's identity.
type cooldownReservation struct {
	spell         uint32
	before, until time.Time
	categories    map[uint32]cooldownDeadline
	visit         uint64
}
type cooldownDeadline struct{ before, until time.Time }

func (s *Service) reserveControlBreakCooldownLocked(spell spellcatalog.Spell, visit uint64) *cooldownReservation {
	c := &cooldownReservation{spell: spell.ID, before: s.spellReady[spell.ID], categories: map[uint32]cooldownDeadline{}, visit: visit}
	s.reserveSpellCooldownLocked(spell)
	c.until = s.spellReady[spell.ID]
	if s.categoryReady == nil {
		s.categoryReady = map[uint32]time.Time{}
	}
	reserve := func(r spellcatalog.Spell) {
		if r.Category == 0 || r.CategoryCooldownMs == 0 {
			return
		}
		prior := s.categoryReady[r.Category]
		until := s.now().Add(time.Duration(r.CategoryCooldownMs) * time.Millisecond)
		if !until.After(prior) {
			return
		}
		if existing, ok := c.categories[r.Category]; ok {
			prior = existing.before
		}
		c.categories[r.Category] = cooldownDeadline{before: prior, until: until}
		s.categoryReady[r.Category] = until
	}
	reserve(spell)
	if linked, ok := s.spellData.Lookup(controlBreakCooldownTrigger(spell.ID)); ok {
		reserve(linked)
	}
	return c
}

func laterDeadline(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// A proven non-dispatch or an exactly correlated definitive rejection may
// remove only this attempt's still-owned estimates. A newer observed server
// baseline remains binding even when it was shorter than the estimate.
func (s *Service) releaseControlBreakCooldownLocked(c *cooldownReservation) {
	if c == nil {
		return
	}
	if s.spellReady[c.spell].Equal(c.until) {
		s.spellReady[c.spell] = laterDeadline(c.before, s.serverSpellReady[c.spell])
	}
	for category, owned := range c.categories {
		if s.categoryReady[category].Equal(owned.until) {
			s.categoryReady[category] = laterDeadline(owned.before, s.serverCategoryReady[category])
		}
	}
}

func (s *Service) rejectControlBreakCooldownLocked(op *supportAttempt) bool {
	c := op.cooldown
	if op != s.supportOp || c == nil || op.view.ServerCastGo || !s.encounterVisit.bound || s.encounterVisit.revision != c.visit ||
		s.pendingSpell != op.view.SpellId || s.castCount != op.count || s.pendingTarget != s.selfGUID {
		return false
	}
	set := s.buffAuras[s.selfGUID]
	if set == nil {
		return false
	}
	aura := set.slots[op.breakAuraSlot]
	if aura.revision != op.breakAuraRevision || aura.spell != op.breakAura.spell || aura.caster != op.breakAura.caster {
		return false
	}
	s.releaseControlBreakCooldownLocked(c)
	op.cooldown = nil
	return true
}
