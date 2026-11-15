package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

type cleansePlan struct {
	target uint64
	slot   int
	aura   observedBuffAura
	spell  spellcatalog.Spell
}

func (s *Service) observedDispelMask(a observedBuffAura) uint32 {
	if a.spell == 0 || !a.negative || a.durationKnown && a.remaining(s.now()) == 0 {
		return 0
	}
	r, ok := s.spellData.Lookup(a.spell)
	if !ok || r.DispelType < spellcatalog.DispelMagic || r.DispelType > spellcatalog.DispelPoison {
		return 0
	}
	return 1 << r.DispelType
}

func (s *Service) potentialCleanseLocked() bool {
	if len(s.buffAuras) == 0 || !s.spellbookKnown {
		return false
	}
	var mask uint32
	for _, r := range s.learnedContractsLocked() {
		if r.Capability().Kind == spellcatalog.Cleanse {
			mask |= r.FriendlyDispelMask()
		}
	}
	if mask == 0 {
		return false
	}
	for guid, set := range s.buffAuras {
		if s.independent && guid != s.selfGUID {
			continue
		}
		for _, aura := range set.slots {
			if mask&s.observedDispelMask(aura) != 0 {
				return true
			}
		}
	}
	return false
}

func (s *Service) eligibleCleanseLocked(w *worldv1.SnapshotResponse, plan cleansePlan, online map[uint64]bool, masks uint32) bool {
	self, target := w.Self, observedHealingPlayer(w, plan.target)
	r := plan.spell
	if plan.target != s.selfGUID && (s.independent || !online[plan.target]) ||
		!healablePlayer(target, self.MapId) || !target.HealthKnown || target.Health == 0 ||
		r.FriendlyDispelMask()&masks == 0 || !s.knownSpells[r.ID] || !s.casterRequirementsLocked(r) ||
		s.now().Before(s.spellReady[r.ID]) || !self.PowerKnown || self.PowerType != 0 ||
		uint64(self.Power) < spellCost(r, self) {
		return false
	}
	d := distance(s.pos, playerPosition(target))
	return target.Guid == s.selfGUID || d >= r.MinFriendly && d <= r.MaxFriendly
}

func (s *Service) cleanseTickLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	if safeSelfCombat(w, s.selfGUID) != "" || w.Self.UnitFlags&0x2000 != 0 ||
		s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed ||
		s.state.InteractionOwner != "" || s.now().Before(s.nextHeal) || s.now().Before(s.nextAssist) {
		return false
	}
	if !s.refreshPositionLocked(w) {
		return false
	}
	var online map[uint64]bool
	if !s.independent {
		online = s.healingPartyLocked(ctx)
	}
	choices := s.candidatesLocked(spellcatalog.Cleanse)
	var plan cleansePlan
	for guid, set := range s.buffAuras {
		if plan.target != 0 && (healingTiePriority(guid, s.selfGUID) > healingTiePriority(plan.target, s.selfGUID) ||
			healingTiePriority(guid, s.selfGUID) == healingTiePriority(plan.target, s.selfGUID) && guid >= plan.target) {
			continue
		}
		for slot, aura := range set.slots {
			mask := s.observedDispelMask(aura)
			if mask == 0 {
				continue
			}
			for _, r := range choices {
				p := cleansePlan{target: guid, slot: slot, aura: aura, spell: r}
				if s.eligibleCleanseLocked(w, p, online, mask) {
					plan = p
					break
				}
			}
			if plan.target == guid {
				break
			}
		}
	}
	if plan.target == 0 {
		return false
	}
	if !s.stopAutoAttackLocked(ctx) || !s.stopLocked(ctx) {
		return true
	}
	if plan.target != s.selfGUID {
		online = s.healingPartyLocked(ctx)
	}
	fresh, err := s.snapshot(ctx)
	if err != nil || safeSelfCombat(fresh, s.selfGUID) != "" || fresh.Self.UnitFlags&0x2000 != 0 ||
		s.state.MotionUnconfirmed || !s.refreshPositionLocked(fresh) {
		return false
	}
	set := s.buffAuras[plan.target]
	if set == nil {
		return false
	}
	aura := set.slots[plan.slot]
	if aura.spell != plan.aura.spell || aura.caster != plan.aura.caster ||
		!s.eligibleCleanseLocked(fresh, plan, online, s.observedDispelMask(aura)) {
		return false
	}
	s.castTargetedSupportLocked(ctx, plan.spell, plan.target, plan.target, string(spellcatalog.Cleanse))
	s.supportOp.cleanseAura = aura.spell
	return true
}

// Spell::EffectDispel sends packed victim and caster GUIDs, the dispel spell,
// one zero byte, a uint32 count, then five bytes per affected aura.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Spells/SpellEffects.cpp
func (s *Service) observeCleanseLocked(data []byte) error {
	op := s.supportOp
	if op == nil || op.cleanseAura == 0 || !s.now().Before(op.until) ||
		(op.view.Phase != "waiting_server" && op.view.Phase != "server_cast_go_effect_unverified") {
		return nil
	}
	bad := errors.New("malformed spell dispel log")
	target, n, err := readGUID(data)
	if err != nil {
		return bad
	}
	caster, m, err := readGUID(data[n:])
	if err != nil {
		return bad
	}
	data = data[n+m:]
	if len(data) < 9 {
		return bad
	}
	if caster != s.selfGUID || target != op.view.TargetGuid || binary.LittleEndian.Uint32(data) != op.view.SpellId {
		return nil
	}
	count := binary.LittleEndian.Uint32(data[5:])
	rows := data[9:]
	if data[4] != 0 || count == 0 || len(rows)%5 != 0 || uint64(count) != uint64(len(rows)/5) {
		return bad
	}
	matched := false
	for off := 0; off < len(rows); off += 5 {
		entry := binary.LittleEndian.Uint32(rows[off:])
		if entry == 0 || entry >= 1<<31 || rows[off+4] > 1 {
			return bad
		}
		matched = matched || entry == op.cleanseAura
	}
	if matched {
		op.view.EffectObserved = true
		if op.view.ServerCastGo {
			op.view.Phase = "confirmed_effect"
		}
	}
	return nil
}
