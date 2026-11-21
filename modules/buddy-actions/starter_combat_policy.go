package buddyactions

// Pending named-starter migration owned separately: only Practice and the
// explicit Fargodeep mission call these legacy policy helpers. Ordinary combat
// never calls them; its sole selector is the attached shared Lua policy.
import (
	"context"
	"encoding/binary"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	contract "github.com/agent-wow/agent-wow/pkg/behaviorcontract"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"math"
	"time"
)

func (s *Service) starterAssistObservedTargetLocked(ctx context.Context, w *worldv1.SnapshotResponse, target *worldv1.CreatureState) bool {
	fail := func(reason string) bool { s.state.Reason = reason; s.stopAutoAttackLocked(ctx); return false }
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
	if s.starterRejectOverlevelTargetLocked(w, target) {
		return fail("assist_target_level_exceeds_self_plus_three")
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
	finishHealth := uint32(0)
	if target.HealthKnown && target.MaxHealth > 0 && uint64(target.Health)*100 <= uint64(target.MaxHealth)*25 {
		finishHealth = target.Health
	}
	r := s.starterBestDamageLocked(self, targetPosition, finishHealth, target.Guid)
	if self.ClassId != 4 && r.ID == 0 && !s.state.KnownAutoAttack {
		return fail("no_affordable_learned_damage_spell_in_range")
	}
	if self.ClassId == 4 || r.ID == 0 {
		if self.CombatReachKnown && target.CombatReachKnown && distance(s.pos, targetPosition) > math.Max(5, float64(self.CombatReach+target.CombatReach)+4.0/3.0)-0.5 {
			return fail("self_defense_melee_target_outside_range")
		}
		return s.starterRogueAssistLocked(ctx, w, target, targetPosition)
	}
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
	if distance(s.pos, targetPosition) > r.MaxHostile-2 {
		return fail("assist_target_outside_conservative_range")
	}
	cost, reserve := spellCost(r, self), s.starterHealingReserveLocked(self)
	if !self.PowerKnown || self.PowerType != 0 || cost == 0 || uint64(self.Power) < cost+reserve {
		return fail("assist_mana_reserved_for_healing")
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
	if s.blockProtectedOffenseLocked(ctx, target) {
		return false
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

func (s *Service) starterHealingReserveLocked(p *worldv1.PlayerState) uint64 {
	reserve := uint64(0)
	for _, r := range s.candidatesLocked(spellcatalog.Heal) {
		if r.Power == 0 {
			cost := spellCost(r, p)
			if cost > reserve {
				reserve = cost
			}
		}
	}
	return reserve
}

func (s *Service) starterBestDamageLocked(p *worldv1.PlayerState, target position, finishHealth uint32, targetGUID uint64) spellcatalog.Spell {
	var best spellcatalog.Spell
	var choices []contract.Spell
	eligible := map[uint32]spellcatalog.Spell{}
	score := float64(-1)
	targetRange := distance(s.pos, target)
	interruptedHere := targetGUID != 0 && s.state.LastCastTargetGuid == targetGUID &&
		(s.state.LastCastFailure == spellFailedInterrupted || s.state.LastCastFailure == spellFailedInterruptedCombat)
	for _, r := range s.candidatesLocked(spellcatalog.Damage) {
		if r.Power != 0 || !p.PowerKnown || p.PowerType != 0 || uint64(p.Power) < spellCost(r, p)+s.starterHealingReserveLocked(p) ||
			s.now().Before(s.spellReady[r.ID]) || targetRange > r.MaxHostile-2 || targetRange < r.MinHostile {
			continue
		}
		damage := directDamage(r)
		periodic := periodicDamage(r)
		if periodic > 0 && targetGUID != 0 && s.hasOwnDamageAuraLocked(targetGUID, r.Family) {
			continue
		}
		if periodic > 0 && (targetGUID == 0 || finishHealth != 0) && damage == 0 {
			continue // A late or position-only cast needs immediate damage.
		}
		amount := damage
		if finishHealth == 0 && targetGUID != 0 {
			amount += periodic
		}
		value := amount / math.Max(1.5, float64(r.CastMs)/1000)
		if interruptedHere && r.CastMs != 0 {
			value *= 0.5 // A long cast just failed on this target.
		}
		// Prefer the fastest affordable spell expected to finish this enemy.
		// Otherwise keep the usual damage-per-second policy.
		kills := finishHealth > 0 && damage >= float64(finishHealth)
		bestKills := finishHealth > 0 && directDamage(best) >= float64(finishHealth)
		if kills && !bestKills || kills && bestKills && (r.CastMs < best.CastMs || r.CastMs == best.CastMs && value > score) ||
			!kills && !bestKills && value > score {
			best, score = r, value
		}
		eligible[r.ID] = r
		choices = append(choices, contract.Spell{SpellId: r.ID, Cost: uint32(spellCost(r, p)), Amount: amount, CastMs: r.CastMs, Score: value})
	}
	if len(choices) > 0 && finishHealth == 0 && (s.behavior != nil || s.dungeonTrace != nil && s.dungeonTrace.Active()) {
		decision := s.behaviorDecisionLocked(contract.Input{Kind: "damage_spell", Self: combatant(p), Spells: choices})
		if decision.SpellId != 0 {
			if r, ok := eligible[decision.SpellId]; ok {
				return r
			}
			s.behaviorRejection = "unknown_or_ineligible_damage_spell_using_native_decision"
		}
	}
	return best
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
	if d > gameRange-0.5 && s.starterStealthApproachLocked(ctx, w, target.Guid) {
		return true
	}
	if d > gameRange-0.5 {
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
	if r := s.starterWeaponStrikeLocked(self, targetPosition); r.ID != 0 && r.Attrs[0]&0x20000 != 0 && s.stealthedLocked() {
		s.castStarterOffenseLocked(ctx, r.ID, target.Guid, time.Second)
		return true
	}
	if s.autoTarget == 0 {
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
	if !s.state.KnownSinisterStrike {
		s.state.Reason = "rogue_auto_attack_only"
		return true
	}
	s.updateClassLocked()
	if !self.PowerKnown || self.PowerType != 3 || self.Power < s.state.SinisterStrikeEnergyCost {
		s.state.Reason = "rogue_auto_attack_waiting_for_energy"
		return true
	}
	r := s.starterWeaponStrikeLocked(self, targetPosition)
	if r.ID != 0 {
		s.castStarterOffenseLocked(ctx, r.ID, target.Guid, time.Second)
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

func (s *Service) starterStealthApproachLocked(ctx context.Context, w *worldv1.SnapshotResponse, target uint64) bool {
	if w.Self.UnitFlags&0x80000 != 0 || s.autoTarget != 0 || s.state.CastPending || s.state.AutoAttackUnconfirmed || s.stealthedLocked() {
		return false
	}
	if op := s.supportOp; op != nil && op.view.Capability == string(spellcatalog.Stealth) && op.owner == target {
		return op.view.Phase == "waiting_server" || s.now().Before(op.until) && op.view.ServerCastGo
	}
	for _, r := range s.candidatesLocked(spellcatalog.Stealth) {
		if s.now().Before(s.spellReady[r.ID]) || !s.stopLocked(ctx) {
			continue
		}
		fresh, err := s.snapshot(ctx)
		if err != nil || safeSelfCombat(fresh, s.selfGUID) != "" || fresh.Self.UnitFlags&0x80000 != 0 || !s.casterRequirementsLocked(r) {
			return false
		}
		return s.castSupportLocked(ctx, r, s.selfGUID, target)
	}
	return false
}

func (s *Service) starterWeaponStrikeLocked(p *worldv1.PlayerState, target position) spellcatalog.Spell {
	var best spellcatalog.Spell
	score := float64(-1)
	for _, r := range s.candidatesLocked(spellcatalog.WeaponStrike) {
		cost := spellCost(r, p)
		if r.Family == sinisterStrike {
			cost = uint64(s.state.SinisterStrikeEnergyCost)
		}
		if !p.PowerKnown || int32(p.PowerType) != r.Power || uint64(p.Power) < cost || s.now().Before(s.spellReady[r.ID]) || r.Behind && !behindEnemy(s.pos, target) || r.Attrs[0]&0x20000 != 0 && !s.stealthedLocked() {
			continue
		}
		proof := s.weapons[p.GetMainhand().GetEntry()]
		if r.ItemClass >= 0 && (!proof.valid || int32(proof.class) != r.ItemClass || proof.subclass >= 32 || r.SubclassMask != 0 && r.SubclassMask&(1<<proof.subclass) == 0 || proof.inventory >= 32 || r.InventoryMask != 0 && r.InventoryMask&(1<<proof.inventory) == 0) {
			continue
		}
		amount := float64(0)
		for _, e := range r.Effects {
			if e.Kind != 80 {
				amount += float64(e.Points) + float64(e.Dice)/2
			}
		}
		if amount > score {
			best, score = r, amount
		}
	}
	return best
}

func (s *Service) starterRejectOverlevelTargetLocked(w *worldv1.SnapshotResponse, target *worldv1.CreatureState) bool {
	self := w.Self
	if !self.LevelKnown || !target.LevelKnown || uint64(target.Level) <= uint64(self.Level)+3 {
		return false
	}
	return true
}
