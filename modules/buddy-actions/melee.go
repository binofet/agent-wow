package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func (s *Service) stopAutoAttackLocked(ctx context.Context) bool {
	if s.autoStopping || s.state.AutoAttackUnconfirmed {
		return false
	}
	if s.autoTarget == 0 && !s.state.AutoAttacking {
		return true
	}
	s.autoStopping = true
	s.state.AutoAttackRequested = false
	s.state.AutoAttackUnconfirmed = true
	if !s.sendLocked(ctx, opcode.CMSGAttackstop, nil) {
		s.state.Reason = "auto_attack_stop_unconfirmed"
	}
	return false // Only a matching server ATTACKSTOP confirms the stop.
}

func (s *Service) rogueAssistLocked(ctx context.Context, w *worldv1.SnapshotResponse, target *worldv1.CreatureState, targetPosition position) bool {
	self := w.Self
	if !s.state.KnownAutoAttack {
		s.state.Reason = "auto_attack_not_in_known_spells"
		s.stopAutoAttackLocked(ctx)
		return false
	}
	if self.UnitFlags&(0x20000|0x200000) != 0 {
		s.state.Reason = "rogue_pacified_or_disarmed"
		s.stopAutoAttackLocked(ctx)
		return false
	}
	if !s.weaponReady(self) {
		s.stopAutoAttackLocked(ctx)
		s.requestWeaponLocked(ctx, self)
		s.state.Reason = "mainhand_weapon_not_verified"
		return false
	}
	if !self.BaseAttackTimeKnown || !self.CombatReachKnown || !target.CombatReachKnown {
		s.state.Reason = "melee_range_or_attack_time_unknown"
		s.stopAutoAttackLocked(ctx)
		return false
	}
	if s.autoStopping || s.state.AutoAttackUnconfirmed {
		s.state.Reason = "auto_attack_stop_waiting_for_server"
		return true
	}
	gameRange := math.Max(5, float64(self.CombatReach+target.CombatReach)+4.0/3.0)
	d := distance(s.pos, targetPosition)
	if d > gameRange-0.5 {
		if !s.stopAutoAttackLocked(ctx) {
			return true
		}
		return s.approachMeleeLocked(ctx, w, targetPosition, d, math.Min(3.5, gameRange-0.75))
	}
	if !s.stopLocked(ctx) {
		s.state.Reason = "movement_stop_unconfirmed_assist_aborted"
		return true
	}
	if s.autoTarget != 0 && s.autoTarget != target.Guid {
		s.stopAutoAttackLocked(ctx)
		return true
	}
	if !s.faceEnemyLocked(ctx, targetPosition) {
		return true
	}
	if s.autoTarget == 0 {
		s.autoTarget = target.Guid
		s.state.AutoAttackRequested = true
		if !s.sendLocked(ctx, opcode.CMSGAttackswing, binary.LittleEndian.AppendUint64(nil, target.Guid)) {
			s.state.AutoAttackUnconfirmed = true
			return true
		}
	}
	if s.state.CastPending || s.now().Before(s.nextAssist) || s.now().Before(s.nextHeal) {
		s.state.Reason = "rogue_auto_attack_gcd_wait"
		return true
	}
	if !s.state.KnownSinisterStrike {
		s.state.Reason = "rogue_auto_attack_only"
		return true
	}
	s.updateClassLocked()
	if !self.PowerKnown || self.PowerType != 3 || self.Power < s.state.SinisterStrikeEnergyCost {
		s.state.Reason = "rogue_auto_attack_waiting_for_energy"
		return true
	}
	s.castOffenseLocked(ctx, s.selectedSpellLocked(sinisterStrike).id, target.Guid, time.Second)
	return true
}

// This is bounded assistance to an already hostile engaged target, never an
// autonomous pull or a general navigator. Normal server range checks still run.
func (s *Service) approachMeleeLocked(ctx context.Context, w *worldv1.SnapshotResponse, target position, d, stopDistance float64) bool {
	{
		if d > 35 || math.Abs(float64(target.z-s.pos.z)) > 12 || !w.Self.SpeedsKnown || math.Abs(float64(w.Self.RunSpeed)-7) > 0.01 {
			s.state.Reason = "rogue_target_outside_bounded_ground_approach"
			s.stopLocked(ctx)
			return false
		}
		if elapsed := s.now().Sub(s.lastStep); elapsed < 0 || elapsed > 600*time.Millisecond {
			s.stopLocked(ctx)
			s.state.Reason = "rogue_approach_tick_late"
			return true
		}
		// A separate verified route survives the ordinary formation-cache reset
		// after combat ticks. Failed navigation never falls through to a line.
		s.followNav, s.meleeNav = s.meleeNav, s.followNav
		defer func() { s.followNav, s.meleeNav = s.meleeNav, s.followNav }()
		if s.followFormationStepLocked(ctx, target, stopDistance) {
			s.state.Reason = "rogue_approaching_engaged_target_nav"
		}
		return true
	}
}
