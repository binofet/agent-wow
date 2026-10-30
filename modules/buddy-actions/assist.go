package buddyactions

import (
	"context"
	"math"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// The class and learned-spell gates select a known rank of one offensive ability. The
// owner-selected target still needs separate hostile ATTACKSTART evidence.
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
	s.updateClassLocked()
	if !self.ClassKnown || (self.ClassId != 5 && self.ClassId != 8 && self.ClassId != 4) {
		return fail("assist_class_unknown_or_unsupported")
	}
	if self.ClassId == 5 && (!owner.HealthKnown || owner.MaxHealth == 0 || uint64(owner.Health)*100 < uint64(owner.MaxHealth)*85) {
		return fail("assist_deferred_healing_needed_or_unknown")
	}
	if !owner.TargetKnown || owner.TargetGuid == 0 || target == nil || target.Guid != owner.TargetGuid || !target.Present || !target.MapKnown || target.MapId != self.MapId || !target.HealthKnown || !target.AliveKnown || !target.Alive || target.Health == 0 {
		return fail("assist_requires_observed_living_owner_target")
	}
	if kind := target.Guid >> 48; kind != 0xf130 && kind != 0xf150 {
		return fail("assist_requires_creature_target")
	}
	partyAttackStarted := target.AttackVictimGuid != ownerGUID && target.AttackVictimGuid != s.selfGUID && target.AttackAgeMs <= 5000
	if !target.AttackVictimKnown || !s.defendsVictimLocked(ctx, target.AttackVictimGuid) || target.AttackAgeMs > 30000 || self.UnitFlags&0x80000 == 0 && owner.UnitFlags&0x80000 == 0 && !partyAttackStarted {
		return fail("assist_requires_enemy_attacking_companions")
	}
	if target.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) != 0 || target.UnitFlags_2&1 != 0 || target.DynamicFlags&0x20 != 0 {
		return fail("assist_target_not_attackable")
	}
	if s.failedAssistTarget == target.Guid {
		return fail("assist_target_failed_no_retry")
	}
	targetPosition, known, _ := creaturePosition(target)
	if !known {
		return fail("assist_target_outside_conservative_range")
	}
	if self.ClassId == 4 {
		return s.rogueAssistLocked(ctx, w, target, targetPosition)
	}
	if !s.stopAutoAttackLocked(ctx) {
		s.state.Reason = "auto_attack_stop_waiting_for_server"
		return false
	}
	rank, maxRange := s.selectedSpellLocked(smite), float64(28)
	if self.ClassId == 8 {
		rank, maxRange = s.selectedSpellLocked(fireball), 33
		if !s.state.KnownFireball {
			return fail("fireball_not_in_known_spells")
		}
	} else if !s.state.KnownSmite {
		return fail("smite_not_in_known_spells")
	}
	if self.UnitFlags&0x2000 != 0 {
		return fail("caster_silenced")
	}
	if s.state.CastPending || s.now().Before(s.nextAssist) || s.now().Before(s.nextHeal) {
		s.state.Reason = "assist_throttled_or_pending"
		return false
	}
	if distance(s.pos, targetPosition) > maxRange {
		return fail("assist_target_outside_conservative_range")
	}
	cost, reserve := manaBudget(self, uint64(rank.manaPercent)), uint64(0)
	if self.ClassId == 5 {
		percent := uint64(s.selectedSpellLocked(lesserHeal).manaPercent)
		if percent == 0 {
			percent = 16
		} // Preserve the existing reserve when healing knowledge is absent.
		reserve = manaBudget(self, percent)
	}
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
	return s.castOffenseLocked(ctx, rank.id, target.Guid, 3*time.Second)
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
	now := s.now()
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
	if !spellFamily(s.pendingSpell, lesserHeal) {
		s.failedAssistTarget = s.pendingTarget
		s.nextAssist = s.now().Add(5 * time.Second)
		if spellFamily(s.pendingSpell, smite) {
			s.state.Reason = "server_smite_failed_target_paused"
		} else {
			s.state.Reason = "server_offensive_spell_failed_target_paused"
		}
	} else {
		s.state.Healing = false
		s.nextHeal = s.now().Add(5 * time.Second)
		s.state.Reason = reason
	}
	s.pendingSpell = 0
	s.pendingTarget = 0
}
