package buddyactions

import (
	"context"
	"math"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

// An actual attack relation may precede combat flags briefly. Older relations
// require both players' current combat state, and after thirty seconds also the
// creature's current selected victim. A selected target alone grants nothing.
func ongoingAttack(c *worldv1.CreatureState, victim *worldv1.PlayerState) bool {
	if c == nil || victim == nil || !c.AttackVictimKnown || c.AttackVictimGuid != victim.Guid {
		return false
	}
	if c.TargetKnown && c.TargetGuid != victim.Guid {
		return false
	}
	if c.AttackAgeMs <= 5000 {
		return true
	}
	if !victim.UnitFlagsKnown || victim.UnitFlags&0x80000 == 0 {
		return false
	}
	if c.AttackAgeMs <= 30000 {
		return true
	}
	return c.TargetKnown && c.TargetGuid == victim.Guid && c.UnitFlagsKnown && c.UnitFlags&0x80000 != 0
}

func partyDefenseCreature(w *worldv1.SnapshotResponse, guid uint64) *worldv1.CreatureState {
	if w == nil || len(w.NearbyCreatures) > 32 {
		return nil
	}
	var found *worldv1.CreatureState
	for _, view := range [][]*worldv1.CreatureState{w.NearbyCreatures, {w.OwnerTarget, w.RequestedCreature}} {
		for _, c := range view {
			if c != nil && c.Guid == guid {
				found = c
			}
		}
	}
	return found
}

func partyDefenseEligible(w *worldv1.SnapshotResponse, c *worldv1.CreatureState, online map[uint64]bool) bool {
	if w == nil || w.Self == nil || c == nil || !online[c.AttackVictimGuid] || c.AttackVictimGuid == w.Self.Guid {
		return false
	}
	victim := observedHealingPlayer(w, c.AttackVictimGuid)
	if !safeHealingPlayer(victim, w.Self.MapId) || !victim.HealthKnown || victim.Health == 0 || !ongoingAttack(c, victim) {
		return false
	}
	kind := c.Guid >> 48
	if kind != 0xf130 && kind != 0xf150 {
		return false
	}
	if !c.Present || !c.MapKnown || c.MapId != w.Self.MapId || !c.AliveKnown || !c.Alive || !c.HealthKnown || c.Health == 0 {
		return false
	}
	_, known, _ := creaturePosition(c)
	return known
}

// Defend current online party members even while the owner is offline. This is
// separate from owner target assistance: only an actual hostile attack relation
// can select a target, and independent profiles acquire no new group authority.
func (s *Service) partyDefenseLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	if s.independent || !s.state.Assisting || safeSelfCombat(w, s.selfGUID) != "" || !s.refreshPositionLocked(w) || s.state.MotionUnconfirmed || len(w.NearbyCreatures) > 32 {
		return false
	}
	online := s.healingPartyLocked(ctx)
	var target *worldv1.CreatureState
	best := math.Inf(1)
	for _, c := range w.NearbyCreatures {
		if !partyDefenseEligible(w, c, online) {
			continue
		}
		p, _, _ := creaturePosition(c)
		d := distance(s.pos, p)
		if d < best || d == best && (target == nil || c.Guid < target.Guid) {
			target, best = c, d
		}
	}
	if target == nil {
		return false
	}
	s.partyDefenseTarget, s.partyDefenseVictim = target.Guid, target.AttackVictimGuid
	defer func() { s.partyDefenseTarget, s.partyDefenseVictim = 0, 0; s.state.AssistReason = s.state.Reason }()
	s.assistObservedTargetLocked(ctx, w, target, true)
	// Caster range/mana refusals and failed melee routes must not leave prior
	// approach movement running or fall through into ordinary owner following.
	if !s.state.Moving || s.state.Reason != "rogue_approaching_engaged_target_nav" {
		s.stopLocked(ctx)
	}
	return true
}

// Recheck after clock/planning/facing callbacks, immediately before a new
// movement, attack, or cast. The current lease remains separately enforced.
func (s *Service) partyDefenseCurrentLocked(ctx context.Context) (*worldv1.SnapshotResponse, bool) {
	// Membership callbacks may themselves coincide with packet updates. Fetch
	// membership first, then the final world snapshot used to authorize a write.
	online := s.healingPartyLocked(ctx)
	w, err := s.snapshot(ctx)
	if err != nil || s.independent || s.partyDefenseTarget == 0 || safeSelfCombat(w, s.selfGUID) != "" || w.Self.MapId != s.posMap || w.Self.PositionRevision != s.positionRevision {
		return nil, false
	}
	c := partyDefenseCreature(w, s.partyDefenseTarget)
	return w, c != nil && c.AttackVictimGuid == s.partyDefenseVictim && partyDefenseEligible(w, c, online)
}

func (s *Service) partyDefenseOffenseAllowedLocked(ctx context.Context, target uint64, spell uint32) bool {
	s.state.Reason = "party_defense_authority_changed_before_cast"
	w, ok := s.partyDefenseCurrentLocked(ctx)
	if !ok || target != s.partyDefenseTarget {
		return false
	}
	return s.defenseOffenseAllowedLocked(w, target, spell)
}

// A definite correlated spell failure can settle one defensive cast without
// retiring defense against a still-attacking enemy. Initial pulls and uncertain
// casts keep their existing quarantine. The final observation follows facing.
func (s *Service) failedDefenseRetryAllowedLocked(ctx context.Context, target uint64, spell uint32) bool {
	s.state.Reason = "defense_retry_requires_current_attacker"
	if target == 0 || s.failedAssistTarget != target || s.failedAssistCastTarget != target ||
		s.state.Practicing || s.fargodeep.active || s.now().Before(s.nextAssist) {
		return false
	}
	if s.partyDefenseTarget != 0 {
		return target == s.partyDefenseTarget && s.partyDefenseOffenseAllowedLocked(ctx, target, spell)
	}
	w, err := s.snapshot(ctx)
	if err != nil || safeSelfCombat(w, s.selfGUID) != "" || w.Self.MapId != s.posMap || w.Self.PositionRevision != s.positionRevision {
		return false
	}
	c := partyDefenseCreature(w, target)
	if c == nil || !c.Present || !c.MapKnown || c.MapId != w.Self.MapId || !c.AliveKnown || !c.Alive || !c.HealthKnown || c.Health == 0 || !ongoingAttack(c, w.Self) {
		return false
	}
	if kind := c.Guid >> 48; kind != 0xf130 && kind != 0xf150 {
		return false
	}
	return s.defenseOffenseAllowedLocked(w, target, spell)
}

func (s *Service) defenseOffenseAllowedLocked(w *worldv1.SnapshotResponse, target uint64, spell uint32) bool {
	c := partyDefenseCreature(w, target)
	p, known, _ := creaturePosition(c)
	self := w.Self
	if !known || !self.ClassKnown || c.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) != 0 || c.UnitFlags_2&1 != 0 || c.DynamicFlags&0x20 != 0 || self.LevelKnown && c.LevelKnown && uint64(c.Level) > uint64(self.Level)+3 {
		return false
	}
	limit := float64(28)
	if self.ClassId == 8 {
		limit = 33
	}
	if self.ClassId == 4 {
		if !self.BaseAttackTimeKnown || !self.CombatReachKnown || !c.CombatReachKnown || self.UnitFlags&(0x20000|0x200000) != 0 || !s.weaponReady(self) {
			return false
		}
		limit = math.Max(5, float64(self.CombatReach+c.CombatReach)+4.0/3.0) - 0.5
		if spell != 0 {
			if !spellFamily(spell, sinisterStrike) {
				return false
			}
			if !self.PowerKnown || self.PowerType != 3 || self.Power < s.state.SinisterStrikeEnergyCost {
				s.state.Reason = "party_defense_resources_changed_before_cast"
				return false
			}
		}
	} else {
		family := uint32(0)
		switch self.ClassId {
		case 5:
			family = smite
		case 8:
			family = fireball
		case 9:
			family = shadowBolt
		}
		if family == 0 || !spellFamily(spell, family) || self.UnitFlags&0x2000 != 0 || !self.PowerKnown || self.PowerType != 0 {
			return false
		}
		rank := spellRankFor(spell)
		cost, reserve := manaBudget(self, uint64(rank.manaPercent)), uint64(0)
		if self.ClassId == 5 {
			percent := uint64(s.selectedSpellLocked(lesserHeal).manaPercent)
			if percent == 0 {
				percent = 16
			}
			reserve = manaBudget(self, percent)
		}
		if cost == 0 || uint64(self.Power) < cost+reserve {
			s.state.Reason = "party_defense_resources_changed_before_cast"
			return false
		}
	}
	return distance(s.pos, p) <= limit
}
