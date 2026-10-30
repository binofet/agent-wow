package buddyactions

import (
	"context"
	"math"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Smite585/rank1, verified from build12340 Spell.dbc: 1500ms cast/GCD,
// 30yd range, 9% base mana. A selected NPC alone is never hostility evidence.
func (s *Service) assistLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	if !s.state.Assisting {
		return false
	}
	if reason := safePlayers(w, s.selfGUID); reason != "" {
		s.state.Reason = reason
		return false
	}
	if !s.refreshPositionLocked(w) || s.state.MotionUnconfirmed {
		s.state.Reason = "assist_position_unconfirmed"
		return false
	}
	self, owner, target := w.Self, w.Owner, w.OwnerTarget
	if !owner.HealthKnown || owner.MaxHealth == 0 || uint64(owner.Health)*100 < uint64(owner.MaxHealth)*85 {
		s.state.Reason = "assist_deferred_healing_needed_or_unknown"
		return false
	}
	if !s.state.KnownSmite {
		s.state.Reason = "smite_not_in_known_spells"
		return false
	}
	if !owner.TargetKnown || owner.TargetGuid == 0 || target == nil || target.Guid != owner.TargetGuid || !target.Present || !target.MapKnown || target.MapId != self.MapId || !target.PositionKnown || !target.HealthKnown || !target.AliveKnown || !target.Alive || target.Health == 0 {
		s.state.Reason = "assist_requires_observed_living_owner_target"
		return false
	}
	if kind := target.Guid >> 48; kind != 0xf130 && kind != 0xf150 {
		s.state.Reason = "assist_requires_creature_target"
		return false
	}
	// This is separate from the unit's selected target: ATTACKSTART proves an
	// actual hostile engagement, and ATTACKSTOP/death clear it in buddy-world.
	if !target.AttackVictimKnown || (target.AttackVictimGuid != ownerGUID && target.AttackVictimGuid != s.selfGUID) || target.AttackAgeMs > 30000 || self.UnitFlags&0x80000 == 0 && owner.UnitFlags&0x80000 == 0 {
		s.state.Reason = "assist_requires_enemy_attacking_companions"
		return false
	}
	if target.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) != 0 || target.UnitFlags_2&1 != 0 || target.DynamicFlags&0x20 != 0 {
		s.state.Reason = "assist_target_not_attackable"
		return false
	}
	if self.UnitFlags&0x2000 != 0 {
		s.state.Reason = "caster_silenced"
		return false
	}
	if s.failedAssistTarget == target.Guid {
		s.state.Reason = "assist_target_failed_no_retry"
		return false
	}
	if s.state.CastPending || s.now().Before(s.nextAssist) || s.now().Before(s.nextHeal) {
		s.state.Reason = "assist_throttled_or_pending"
		return false
	}
	position := position{target.X, target.Y, target.Z, target.Orientation}
	if !finite(position) || distance(s.pos, position) > 28 || target.MovementFlags&0x08000000 != 0 || target.MovementFlags&0xff != 0 && target.PositionAgeMs > 1500 {
		s.state.Reason = "assist_target_outside_conservative_range"
		return false
	}
	// Budget with max mana (at least ordinary base mana) and preserve one
	// Lesser Heal. Never spend the healing reserve on offensive casts.
	cost, reserve := (uint64(self.MaxPower)*9+99)/100, (uint64(self.MaxPower)*16+99)/100
	if !self.PowerKnown || self.PowerType != 0 || cost == 0 || uint64(self.Power) < cost+reserve {
		s.state.Reason = "assist_mana_reserved_for_healing"
		return false
	}
	if !s.stopLocked(ctx) {
		s.state.Assisting = false
		s.state.Reason = "movement_stop_unconfirmed_assist_aborted"
		return false
	}
	// Face this observed enemy at our current position before the ordinary
	// cast request; a facing write is not a spell/range/line-of-sight receipt.
	facing := s.pos
	facing.o = float32(math.Atan2(float64(position.y-facing.y), float64(position.x-facing.x)))
	if facing.o < 0 {
		facing.o += 2 * math.Pi
	}
	if !s.movementLocked(ctx, opcode.MSGMoveSetFacing, facing, false) {
		s.state.Assisting = false
		return false
	}
	now := s.now()
	s.castCount++
	s.nextAssist = now.Add(3 * time.Second)
	s.nextHeal = now.Add(1500 * time.Millisecond)
	s.castUntil = now.Add(5 * time.Second)
	s.pendingSpell = smite
	s.pendingTarget = target.Guid
	s.state.CastPending = true
	s.state.AssistAttempts++
	s.state.LastCastFailure = 0
	s.state.LastCastSpellId = smite
	s.state.LastCastTargetGuid = target.Guid
	if !s.sendLocked(ctx, opcode.CMSGCastSpell, spellPacket(s.castCount, smite, target.Guid)) {
		s.state.CastPending = false
		s.pendingSpell = 0
		s.pendingTarget = 0
		return false
	}
	s.state.Reason = "smite_transport_written_waiting_for_server"
	return true
}

func (s *Service) castFailedLocked(code uint32, reason string) {
	s.state.LastCastFailure = code
	s.state.CastPending = false
	if s.pendingSpell == smite {
		s.failedAssistTarget = s.pendingTarget
		s.nextAssist = s.now().Add(5 * time.Second)
		s.state.Reason = "server_smite_failed_target_paused"
	} else {
		s.state.Healing = false
		s.nextHeal = s.now().Add(5 * time.Second)
		s.state.Reason = reason
	}
	s.pendingSpell = 0
	s.pendingTarget = 0
}
