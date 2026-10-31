package buddyactions

import (
	"context"
	"math"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

// The class and learned-spell gates select a known rank of one offensive ability.
// Normal owner assistance still requires separate hostile ATTACKSTART evidence.
func (s *Service) assistLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	defer func() { s.state.AssistReason = s.state.Reason }()
	if !s.state.Assisting {
		return false
	}
	fail := func(reason string) bool { s.state.Reason = reason; s.stopAutoAttackLocked(ctx); return false }
	if reason := safePlayers(w, s.selfGUID); reason != "" {
		return fail(reason)
	}
	if !s.refreshPositionLocked(w) || s.state.MotionUnconfirmed {
		return fail("assist_position_unconfirmed")
	}
	self, owner, target := w.Self, w.Owner, w.OwnerTarget
	if !owner.TargetKnown || owner.TargetGuid == 0 || target == nil || target.Guid != owner.TargetGuid || !target.Present || !target.MapKnown || target.MapId != self.MapId || !target.HealthKnown || !target.AliveKnown || !target.Alive || target.Health == 0 {
		return fail("assist_requires_observed_living_owner_target")
	}
	return s.assistObservedTargetLocked(ctx, w, target, false)
}

// Self-defense consumes a tick only for an actual current attacker. Selecting
// a player or creature target alone never authorizes an attack. Active normal
// interaction leases retain priority in Tick, and this path never chases.
func (s *Service) selfDefenseLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	if !s.state.Assisting || safeSelfCombat(w, s.selfGUID) != "" || !s.refreshPositionLocked(w) {
		return false
	}
	target := s.selfAttackerLocked(w)
	if target == nil {
		return false
	}
	defer func() { s.state.AssistReason = s.state.Reason }()
	if s.state.CastPending {
		s.stopLocked(ctx)
		return true
	}
	s.assistObservedTargetLocked(ctx, w, target, true)
	// Even a class/mana/range/level refusal must stop a prior owned approach
	// rather than fall through into a trip or owner-follow movement in combat.
	s.stopLocked(ctx)
	return true
}

func safeSelfCombat(w *worldv1.SnapshotResponse, selfGUID uint64) string {
	if w == nil || w.Self == nil || w.Self.Guid != selfGUID {
		return "player_identity_mismatch"
	}
	p := w.Self
	if !p.Present || !p.MapKnown || !p.PositionKnown || !finite(playerPosition(p)) || !p.AliveKnown || !p.Alive || !p.HealthKnown || p.Health == 0 {
		return "self_defense_character_unknown_or_dead"
	}
	if !p.MovementFlagsKnown || !movementflags.GroundCompatible(p.MovementFlags, p.ExtraMovementFlags) {
		return "unsupported_movement_state"
	}
	const unsafe = uint32(0x1 | 0x4 | 0x00040000 | 0x00100000 | 0x00400000 | 0x00800000 | 0x01000000 | 0x02000000 | 0x08000000)
	if !p.UnitFlagsKnown || p.UnitFlags&unsafe != 0 {
		return "unsafe_unit_state"
	}
	return ""
}

func (s *Service) selfAttackerLocked(w *worldv1.SnapshotResponse) *worldv1.CreatureState {
	if len(w.NearbyCreatures) > 32 {
		return nil
	}
	var selected *worldv1.CreatureState
	best := math.Inf(1)
	for _, view := range [][]*worldv1.CreatureState{w.NearbyCreatures, {w.OwnerTarget, w.RequestedCreature}} {
		for _, c := range view {
			if c == nil || !c.Present || !c.MapKnown || c.MapId != w.Self.MapId || !c.HealthKnown || !c.AliveKnown || !c.Alive || c.Health == 0 || !c.AttackVictimKnown || c.AttackVictimGuid != s.selfGUID || !ongoingAttack(c, w.Self) {
				continue
			}
			if kind := c.Guid >> 48; kind != 0xf130 && kind != 0xf150 {
				continue
			}
			p, known, _ := creaturePosition(c)
			if !known {
				continue
			}
			d := distance(s.pos, p)
			if d < best || d == best && (selected == nil || c.Guid < selected.Guid) {
				selected, best = c, d
			}
		}
	}
	return selected
}

func (s *Service) assistObservedTargetLocked(ctx context.Context, w *worldv1.SnapshotResponse, target *worldv1.CreatureState, defense bool) bool {
	fail := func(reason string) bool { s.state.Reason = reason; s.stopAutoAttackLocked(ctx); return false }
	self, owner := w.Self, w.Owner
	s.updateClassLocked()
	if !self.ClassKnown || (self.ClassId != 5 && self.ClassId != 8 && self.ClassId != 4 && self.ClassId != 9) {
		return fail("assist_class_unknown_or_unsupported")
	}
	if !defense && self.ClassId == 5 && (!owner.HealthKnown || owner.MaxHealth == 0 || uint64(owner.Health)*100 < uint64(owner.MaxHealth)*85) {
		return fail("assist_deferred_healing_needed_or_unknown")
	}
	if kind := target.Guid >> 48; kind != 0xf130 && kind != 0xf150 {
		return fail("assist_requires_creature_target")
	}
	if !defense {
		partyAttackStarted := target.AttackVictimGuid != ownerGUID && target.AttackVictimGuid != s.selfGUID && target.AttackAgeMs <= 5000
		if !target.AttackVictimKnown || !s.defendsVictimLocked(ctx, target.AttackVictimGuid) || target.AttackAgeMs > 30000 || self.UnitFlags&0x80000 == 0 && owner.UnitFlags&0x80000 == 0 && !partyAttackStarted {
			return fail("assist_requires_enemy_attacking_companions")
		}
	}
	if target.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) != 0 || target.UnitFlags_2&1 != 0 || target.DynamicFlags&0x20 != 0 {
		return fail("assist_target_not_attackable")
	}
	if self.LevelKnown && target.LevelKnown && uint64(target.Level) > uint64(self.Level)+3 {
		return fail("assist_target_level_exceeds_self_plus_three")
	}
	if s.failedAssistTarget == target.Guid && (!defense || s.failedAssistCastTarget != target.Guid ||
		s.state.Practicing || s.fargodeep.active || s.now().Before(s.nextAssist)) {
		return fail("assist_target_failed_no_retry")
	}
	targetPosition, known, _ := creaturePosition(target)
	if s.fargodeep.active && s.fargodeep.target == target.Guid {
		targetPosition, known = fargodeepCreaturePosition(target)
	}
	if !known {
		return fail("assist_target_outside_conservative_range")
	}
	if self.ClassId == 4 {
		if defense && s.partyDefenseTarget == 0 && self.CombatReachKnown && target.CombatReachKnown && distance(s.pos, targetPosition) > math.Max(5, float64(self.CombatReach+target.CombatReach)+4.0/3.0)-0.5 {
			return fail("self_defense_melee_target_outside_range")
		}
		return s.rogueAssistLocked(ctx, w, target, targetPosition)
	}
	if !s.stopAutoAttackLocked(ctx) {
		s.state.Reason = "auto_attack_stop_waiting_for_server"
		return false
	}
	r := s.bestDamageLocked(self, targetPosition)
	if r.ID == 0 {
		return fail("no_affordable_learned_damage_spell_in_range")
	}
	if self.UnitFlags&0x2000 != 0 {
		return fail("caster_silenced")
	}
	if s.state.CastPending || s.now().Before(s.nextAssist) || s.now().Before(s.nextHeal) {
		s.state.Reason = "assist_throttled_or_pending"
		return false
	}
	if distance(s.pos, targetPosition) > r.MaxHostile-2 {
		return fail("assist_target_outside_conservative_range")
	}
	cost, reserve := spellCost(r, self), s.healingReserveLocked(self)
	if !self.PowerKnown || self.PowerType != 0 || cost == 0 || uint64(self.Power) < cost+reserve {
		return fail("assist_mana_reserved_for_healing")
	}
	if !s.stopLocked(ctx) {
		s.state.Assisting = false
		s.state.Reason = "movement_stop_unconfirmed_assist_aborted"
		return false
	}
	if !s.faceEnemyLocked(ctx, targetPosition) {
		return false
	}
	return s.castOffenseLocked(ctx, r.ID, target.Guid, max(1500*time.Millisecond, time.Duration(r.GcdMs)*time.Millisecond))
}

func (s *Service) faceEnemyLocked(ctx context.Context, target position) bool {
	facing := s.pos
	facing.o = float32(math.Atan2(float64(target.y-facing.y), float64(target.x-facing.x)))
	if facing.o < 0 {
		facing.o += 2 * math.Pi
	}
	if !s.movementLocked(ctx, opcode.MSGMoveSetFacing, facing, false) {
		s.state.Assisting = false
		s.stopAutoAttackLocked(ctx)
		return false
	}
	return true
}
func (s *Service) castOffenseLocked(ctx context.Context, spell uint32, target uint64, throttle time.Duration) bool {
	rank := spellRankFor(spell)
	if rank.id == 0 || !s.knownSpells[spell] {
		s.state.Reason = "offensive_rank_not_in_known_spells"
		return false
	}
	if s.fargodeep.active && !s.fargodeepWriteAllowedLocked(ctx, target, spell) {
		return false
	}
	if s.partyDefenseTarget != 0 || s.failedAssistTarget == target {
		allowed := false
		if s.failedAssistTarget == target {
			allowed = s.failedDefenseRetryAllowedLocked(ctx, target, spell)
		} else {
			allowed = s.partyDefenseOffenseAllowedLocked(ctx, target, spell)
		}
		if !allowed {
			if s.state.Reason != "party_defense_resources_changed_before_cast" {
				s.stopAutoAttackLocked(ctx)
			}
			return false
		}
	}
	now := s.now()
	if r, ok := s.learnedContractsLocked()[spell]; ok {
		s.reserveSpellCooldownLocked(r)
	}
	s.castCount++
	s.nextAssist = now.Add(throttle)
	s.nextHeal = now.Add(1500 * time.Millisecond)
	if spellFamily(spell, sinisterStrike) {
		s.nextHeal = now.Add(time.Second)
	}
	s.castUntil = now.Add(max(5*time.Second, rank.castTime+1500*time.Millisecond))
	s.pendingSpell = spell
	s.pendingTarget = target
	s.state.CastPending = true
	s.state.AssistAttempts++
	s.state.LastCastFailure = 0
	s.state.LastCastSpellId = spell
	s.state.LastCastTargetGuid = target
	if !s.sendLocked(ctx, opcode.CMSGCastSpell, spellPacket(s.castCount, spell, target)) {
		s.state.CastPending = false
		s.pendingSpell = 0
		s.pendingTarget = 0
		return false
	}
	switch spellRankFor(spell).family {
	case smite:
		s.state.Reason = "smite_transport_written_waiting_for_server"
	case fireball:
		s.state.Reason = "fireball_transport_written_waiting_for_server"
	case shadowBolt:
		s.state.Reason = "shadow_bolt_transport_written_waiting_for_server"
	case sinisterStrike:
		s.state.Reason = "sinister_strike_transport_written_waiting_for_server"
	}
	return true
}

// NPC movement packets advertise a route endpoint and duration. Once an
// ordinary noncyclic route has elapsed we may use that endpoint as a client
// estimate, while keeping the actual observation and its age unchanged.
func creaturePosition(c *worldv1.CreatureState) (position, bool, bool) {
	if c == nil {
		return position{}, false, false
	}
	p := position{c.X, c.Y, c.Z, c.Orientation}
	t := c.Trajectory
	if t != nil && t.Known {
		if t.TransportRelative {
			return p, false, false
		}
		if t.Stopped {
			return p, c.PositionKnown && finite(p), false
		}
		if !t.Cyclic && t.DestinationKnown && t.AgeMs >= uint64(t.DurationMs) {
			p = position{t.DestinationX, t.DestinationY, t.DestinationZ, c.Orientation}
			return p, finite(p), true
		}
		return p, false, false // Wait for arrival rather than using a stale start.
	}
	if c.MovementFlags&0x08000000 != 0 || c.MovementFlags&0xff != 0 && c.PositionAgeMs > 1500 {
		return p, false, false
	}
	return p, c.PositionKnown && finite(p), false
}

func (s *Service) castFailedLocked(code uint32, reason string) {
	s.state.LastCastFailure = code
	s.state.CastPending = false
	if s.spellKindLocked(s.pendingSpell) != spellcatalog.Heal {
		s.failedAssistTarget = s.pendingTarget
		s.failedAssistCastTarget = s.pendingTarget
		s.nextAssist = s.now().Add(5 * time.Second)
		if spellFamily(s.pendingSpell, smite) {
			s.state.Reason = "server_smite_failed_target_paused"
		} else {
			s.state.Reason = "server_offensive_spell_failed_target_paused"
		}
	} else {
		// A definite rejection settles this cast, not the user's healing opt-in.
		// After the cooldown, triage must select again from fresh observations.
		// Leave an explicit disable untouched; never replay this pending cast.
		s.nextHeal = s.now().Add(5 * time.Second)
		s.state.Reason = reason
	}
	s.pendingSpell = 0
	s.pendingTarget = 0
}
