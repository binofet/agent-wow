package buddyactions

// Named-task transport and exact native admission. Fargodeep spell selection
// reads the shared Lua selectors; ordinary encounter combat has its own writer.
import (
	"context"
	"encoding/binary"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"math"
	"time"
)

func (s *Service) starterAssistObservedTargetLocked(ctx context.Context, w *worldv1.SnapshotResponse, target *worldv1.CreatureState) bool {
	fail := func(reason string) bool {
		if !s.fargodeepAttemptCurrentLocked(ctx) {
			return false
		}
		s.state.Reason = reason
		s.stopAutoAttackLocked(ctx)
		return false
	}
	if s.blockProtectedOffenseLocked(ctx, target.Guid) {
		return false
	}
	self := w.Self
	s.updateClassLocked()
	if !self.ClassKnown || self.ClassId == 0 {
		return fail("assist_class_unknown_or_unsupported")
	}
	if kind := target.Guid >> 48; kind != 0xf130 && kind != 0xf150 {
		return fail("assist_requires_creature_target")
	}
	if target.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) != 0 || target.UnitFlags_2&1 != 0 || target.DynamicFlags&0x20 != 0 {
		return fail("assist_target_not_attackable")
	}
	if s.failedAssistTarget == target.Guid {
		return fail("starter_target_failed_no_retry")
	}
	targetPosition, known, _ := creaturePosition(target)
	if s.fargodeep.active && s.fargodeep.target == target.Guid {
		targetPosition, known = fargodeepCreaturePosition(target)
	}
	if !known {
		return fail("assist_target_outside_conservative_range")
	}
	decision := s.starterSpellDecisionLocked(ctx, w, target, targetPosition)
	if !s.fargodeepAttemptCurrentLocked(ctx) {
		return false
	}
	if !decision.valid {
		return fail(s.starterSpells.failure())
	}
	if decision.kind == "wait" {
		return fail("no_affordable_learned_damage_spell_in_range")
	}
	if decision.kind == "melee" {
		if self.CombatReachKnown && target.CombatReachKnown && distance(s.pos, targetPosition) > math.Max(5, float64(self.CombatReach+target.CombatReach)+4.0/3.0)-0.5 {
			return fail("self_defense_melee_target_outside_range")
		}
		return s.starterRogueAssistLocked(ctx, w, target, targetPosition)
	}
	r := decision.spell
	if !s.stopAutoAttackLocked(ctx) {
		s.state.Reason = "auto_attack_stop_waiting_for_server"
		return false
	}
	if self.UnitFlags&0x2000 != 0 {
		return fail("caster_silenced")
	}
	if s.state.CastPending || s.now().Before(s.nextAssist) || s.now().Before(s.nextHeal) {
		s.state.Reason = "assist_throttled_or_pending"
		return false
	}
	if distance(s.pos, targetPosition) < r.MinHostile || distance(s.pos, targetPosition) > r.MaxHostile {
		return fail("assist_target_outside_conservative_range")
	}
	cost := spellCost(r, self)
	if !self.PowerKnown || int32(self.PowerType) != r.Power || cost == 0 || uint64(self.Power) < cost {
		return fail("assist_exact_spell_resources_unavailable")
	}
	if !s.stopLocked(ctx) {
		s.state.Reason = "movement_stop_unconfirmed_assist_aborted"
		return false
	}
	if !s.faceEnemyLocked(ctx, targetPosition) {
		return false
	}
	return s.castStarterOffenseLocked(ctx, r.ID, target.Guid, max(1500*time.Millisecond, time.Duration(r.GcdMs)*time.Millisecond))
}

func (s *Service) castStarterOffenseLocked(ctx context.Context, spell uint32, target uint64, throttle time.Duration) bool {
	if s.starterSpells.uncertainStrike != nil {
		s.state.Reason = "named_offense_receipt_unconfirmed_no_retry"
		return false
	}
	if s.blockProtectedOffenseLocked(ctx, target) {
		return false
	}
	if r := s.learnedContractsLocked()[spell]; instantWeaponStrike(r) {
		return s.castStarterWeaponLocked(ctx, r, target)
	}
	rank := spellRankFor(spell)
	if rank.id == 0 || !s.knownSpells[spell] {
		s.state.Reason = "offensive_rank_not_in_known_spells"
		return false
	}
	if s.fargodeep.active && !s.fargodeepWriteAllowedLocked(ctx, target, spell) {
		return false
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
	s.protectedCastCancelAttempted = false
	s.state.CastPending = true
	s.state.AssistAttempts++
	s.state.LastCastFailure = 0
	s.state.LastCastSpellId = spell
	s.state.LastCastTargetGuid = target
	if !s.sendLocked(ctx, opcode.CMSGCastSpell, spellPacket(s.castCount, spell, target)) {
		s.state.CastPending = false
		s.pendingSpell = 0
		s.pendingTarget = 0
		s.nextAssist = s.now().Add(5 * time.Second)
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

func (s *Service) starterRogueAssistLocked(ctx context.Context, w *worldv1.SnapshotResponse, target *worldv1.CreatureState, targetPosition position) bool {
	if s.blockProtectedOffenseLocked(ctx, target.Guid) {
		return false
	}
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
		if op := s.supportOp; op != nil && op.owner == target.Guid && op.view.Capability == string(spellcatalog.Stealth) &&
			(op.view.Phase == "waiting_server" || s.now().Before(op.until) && op.view.ServerCastGo) {
			return true
		}
		decision := s.starterWeaponDecisionLocked(ctx, w, target, targetPosition)
		if !decision.valid || decision.kind == "wait" {
			s.state.Reason = s.starterSpells.failure()
			return true
		}
		if decision.kind == "cast_self" {
			return s.castStarterSelfLocked(ctx, w, target.Guid, decision)
		}
		if !s.stopAutoAttackLocked(ctx) {
			return true
		}
		return s.starterApproachMeleeLocked(ctx, w, targetPosition, d, math.Min(3.5, gameRange-0.75))
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
	if s.failedAssistTarget == target.Guid {
		s.state.Reason = "starter_target_failed_no_retry"
		s.stopAutoAttackLocked(ctx)
		return true
	}
	decision := s.starterWeaponDecisionLocked(ctx, w, target, targetPosition)
	if !s.fargodeepAttemptCurrentLocked(ctx) {
		return false
	}
	if !decision.valid || decision.kind == "wait" {
		s.state.Reason = s.starterSpells.failure()
		return true
	}
	if decision.kind == "cast" && decision.spell.Attrs[0]&0x20000 != 0 {
		s.castStarterOffenseLocked(ctx, decision.spell.ID, target.Guid, time.Second)
		return true
	}
	if s.autoTarget == 0 {
		// A pure selection callback may have changed the solo mission facts.
		if s.state.Practicing {
			if _, _, _, valid := s.practiceWeaponAuthorityLocked(ctx, target.Guid); !valid {
				s.state.Reason = "starter_strike_mission_observations_changed"
				return false
			}
		}
		if s.blockProtectedOffenseLocked(ctx, target.Guid) {
			return false
		}
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
	if decision.kind == "cast" {
		s.castStarterOffenseLocked(ctx, decision.spell.ID, target.Guid, time.Second)
	} else {
		s.state.Reason = "rogue_auto_attack_only"
	}
	return true
}

// This is bounded assistance to an already hostile engaged target, never an
// autonomous pull or a general navigator. Normal server range checks still run.

func (s *Service) starterApproachMeleeLocked(ctx context.Context, w *worldv1.SnapshotResponse, target position, d, stopDistance float64) bool {
	{
		if d > 35 || math.Abs(float64(target.z-s.pos.z)) > 12 || observedRunSpeed(w.Self) == 0 {
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

func (s *Service) starterSelfSpellAvailableLocked(w *worldv1.SnapshotResponse) bool {
	self := w.GetSelf()
	return s.starterSpells.uncertainStrike == nil && s.state.Practicing && !s.fargodeep.active &&
		safeSelfCombat(w, s.selfGUID) == "" && self.ClassKnown && self.ClassId == 4 && self.UnitFlagsKnown &&
		self.UnitFlags&0x80000 == 0 && s.autoTarget == 0 && !s.state.CastPending &&
		!s.state.AutoAttackUnconfirmed && !s.stealthedLocked()
}

// Execute the exact self-targeted rank selected by the same accepted weapon
// policy. STOP and subsequent callbacks cannot retain an old task or choice.
func (s *Service) castStarterSelfLocked(ctx context.Context, w *worldv1.SnapshotResponse, target uint64, chosen starterSpellDecision) bool {
	binding, source := s.practiceLifecycleBindingLocked(w), s.starterSpells.revision
	if !s.stopLocked(ctx) {
		return false
	}
	fresh, creature, point, valid := s.practiceWeaponAuthorityLocked(ctx, target)
	if !valid || !binding.current(s, ctx, fresh) {
		return false
	}
	selected := s.starterWeaponDecisionLocked(ctx, fresh, creature, point)
	if !selected.valid || selected.kind != "cast_self" || selected.spell.ID != chosen.spell.ID || s.starterSpells.revision != source {
		return false
	}
	fresh, creature, point, valid = s.practiceWeaponAuthorityLocked(ctx, target)
	if !valid || !binding.current(s, ctx, fresh) || !s.practiceLifecycleSourceCurrentLocked(source) ||
		!s.starterSpellFactsMatchLocked(selected, fresh, creature, point) || !s.starterSelfSpellAvailableLocked(fresh) ||
		!s.encounterRankResourcesAvailable(selected.spell, fresh.Self) || fresh.Self.MovementFlags&0xff != 0 {
		return false
	}
	s.castSupportLocked(ctx, selected.spell, s.selfGUID, target)
	if !s.supportOp.view.TransportWritten {
		s.starterSpells.uncertainStrike = s.supportOp
	}
	return true
}
