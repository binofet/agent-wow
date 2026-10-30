package buddyactions

import (
	"context"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func safePracticeCharacter(w *worldv1.SnapshotResponse, selfGUID uint64) string {
	if w == nil || w.Self == nil {
		return "character_unknown"
	}
	// An active practice fight may set IN_COMBAT. All other own safety
	// conditions are identical to local quest travel and still fail closed.
	copy := proto.Clone(w.Self).(*worldv1.PlayerState)
	copy.UnitFlags &^= 0x80000
	return safeQuestCharacter(&worldv1.SnapshotResponse{Self: copy}, selfGUID)
}

func ownWolfQuest(w *worldv1.SnapshotResponse) (active, incomplete bool) {
	if w == nil || !w.SelfQuestLogKnown || len(w.SelfQuestLog) != 25 {
		return false, false
	}
	seen := [25]bool{}
	for _, q := range w.SelfQuestLog {
		if q == nil || q.Slot >= 25 || seen[q.Slot] {
			return false, false
		}
		seen[q.Slot] = true
		if q.QuestId == 33 && !q.Failed && q.State&2 == 0 {
			active, incomplete = true, !q.Complete && q.State&1 == 0
		}
	}
	return active, incomplete
}

func practicePullBudget(p *worldv1.PlayerState) uint64 {
	if p.ClassId == 5 {
		return manaBudget(p, 45)
	}
	return manaBudget(p, 40)
}

func (s *Service) stopPracticeLocked(ctx context.Context, why string) {
	s.state.Practicing = false
	s.state.PracticePhase = why
	s.state.Reason = "practice_" + why
	s.practiceRoute = nil
	s.practiceTarget = 0
	s.state.PracticeTargetGuid = 0
	s.practicePullStarted = false
	s.stopLocked(ctx)
	s.stopAutoAttackLocked(ctx)
	s.lastStep = s.now()
}

func (s *Service) Practice(ctx context.Context, r *actionsv1.ToggleRequest) (*actionsv1.State, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "practice toggle is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, status.Error(codes.FailedPrecondition, "session is closing")
	}
	if !r.Enabled {
		s.autoStopping, s.state.AutoAttackUnconfirmed = false, false
		s.stopPracticeLocked(ctx, "disabled")
		return s.stateLocked(), nil
	}
	if s.state.InteractionOwner != "" || s.questApproaching || s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed {
		return nil, status.Error(codes.FailedPrecondition, "practice requires confirmed idle action ownership")
	}
	w, err := s.snapshot(ctx)
	if err != nil || safeQuestCharacter(w, s.selfGUID) != "" || !w.Self.LevelKnown || w.Self.Level < 1 || w.Self.Level > 5 || !w.Self.ClassKnown || (w.Self.ClassId != 4 && w.Self.ClassId != 5 && w.Self.ClassId != 8) || !northshire(playerPosition(w.Self)) {
		return nil, status.Error(codes.FailedPrecondition, "practice requires observed level1..5 supported class safely in Northshire")
	}
	active33, incomplete33 := ownWolfQuest(w)
	if !incomplete33 && (active33 || w.Self.Level != 1 || w.Self.ClassId == 5) {
		return nil, status.Error(codes.FailedPrecondition, "practice requires own incompletequest33 or level1 mage/rogue XP goal")
	}
	if !s.stopAutoAttackLocked(ctx) || !s.stopLocked(ctx) {
		return nil, status.Error(codes.FailedPrecondition, "practice stop is unconfirmed")
	}
	s.state.Practicing, s.state.PracticePhase = true, "waiting_for_safe_pull"
	s.practiceUntil = s.now().Add(10 * time.Minute)
	s.practiceQuestGoal = incomplete33
	s.practiceTarget, s.state.PracticeTargetGuid = 0, 0
	s.practiceRoute = nil
	s.practicePullStarted = false
	s.failedAssistTarget = 0
	s.lastStep = s.now()
	return s.stateLocked(), nil
}

func eligiblePracticeWolf(c *worldv1.CreatureState) bool {
	return c != nil && c.Guid>>48 == 0xf130 && c.Entry == 299 && c.Present && c.MapKnown && c.MapId == 0 && c.LevelKnown && c.Level == 1 && c.HealthKnown && c.MaxHealth > 0 && c.Health > 0 && c.AliveKnown && c.Alive && c.NpcFlags == 0 && c.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) == 0 && c.UnitFlags_2&1 == 0
}

func (s *Service) practiceTickLocked(ctx context.Context) {
	if !s.now().Before(s.practiceUntil) {
		s.stopPracticeLocked(ctx, "goal_expired")
		return
	}
	w, err := s.snapshot(ctx)
	if err != nil || safePracticeCharacter(w, s.selfGUID) != "" || !w.Self.LevelKnown || !w.Self.ClassKnown || (w.Self.ClassId != 4 && w.Self.ClassId != 5 && w.Self.ClassId != 8) {
		s.stopPracticeLocked(ctx, "own_state_unknown_or_unsafe")
		return
	}
	self := w.Self
	active33, incomplete33 := ownWolfQuest(w)
	questNeeded := incomplete33 && self.Level >= 1 && self.Level <= 5
	xpNeeded := !active33 && self.Level == 1 && self.ClassId != 5 && !s.practiceQuestGoal
	if active33 {
		s.practiceQuestGoal = true
	}
	finishing := false
	if !questNeeded && !xpNeeded {
		if s.practicePullStarted && self.UnitFlags&0x80000 != 0 && len(w.NearbyCreatures) <= 32 {
			for _, c := range w.NearbyCreatures {
				if eligiblePracticeWolf(c) && c.Guid == s.practiceTarget && c.AttackVictimKnown && c.AttackVictimGuid == s.selfGUID && c.AttackAgeMs <= 30000 {
					finishing = true
				}
			}
		}
		if !finishing {
			why := "level_two_observed"
			if s.practiceQuestGoal {
				why = "quest_33_completion_observed"
				if incomplete33 {
					why = "quest_33_level_limit"
				}
				if !active33 {
					why = "own_quest_33_absent_or_unknown"
				}
			}
			s.stopPracticeLocked(ctx, why)
			return
		}
		phase := "finishing_current_fight_to_level2"
		if s.practiceQuestGoal {
			phase = "finishing_current_fight_to_goal"
		}
		s.state.PracticePhase = phase
		defer func() {
			if s.state.Practicing {
				s.state.PracticePhase = phase
			}
		}()
	}
	if !questNeeded && !xpNeeded && !finishing || !self.HealthKnown || !self.PowerKnown || !northshire(playerPosition(self)) || !self.SpeedsKnown || math.Abs(float64(self.RunSpeed)-7) > 0.01 || !s.refreshPositionLocked(w) || !northshire(s.pos) || s.state.MotionUnconfirmed {
		s.stopPracticeLocked(ctx, "own_state_unknown_or_unsafe")
		return
	}
	if s.state.AutoAttackUnconfirmed {
		s.stopLocked(ctx)
		s.state.PracticePhase = "waiting_for_server_attack_stop"
		return
	}
	if self.ClassId == 8 && !s.state.KnownFireball || self.ClassId == 5 && !s.state.KnownSmite || self.ClassId == 4 && (!s.state.KnownAutoAttack || !s.state.KnownSinisterStrike) {
		s.stopPracticeLocked(ctx, "required_spell_unknown")
		return
	}
	if self.ClassId != 4 && self.PowerType != 0 || self.ClassId == 4 && self.PowerType != 3 {
		s.stopPracticeLocked(ctx, "class_resource_type_unknown")
		return
	}
	if self.ClassId != 4 && self.UnitFlags&0x2000 != 0 || self.ClassId == 4 && (!self.CombatReachKnown || !self.BaseAttackTimeKnown || self.UnitFlags&(0x20000|0x200000) != 0) {
		s.stopPracticeLocked(ctx, "class_combat_state_unknown_or_unsafe")
		return
	}
	if self.ClassId == 4 && !s.weaponReady(self) {
		s.stopLocked(ctx)
		s.stopAutoAttackLocked(ctx)
		s.requestWeaponLocked(ctx, self)
		s.state.PracticePhase = "verifying_equipped_weapon"
		return
	}
	var target *worldv1.CreatureState
	if len(w.NearbyCreatures) > 32 {
		s.stopPracticeLocked(ctx, "creature_observations_invalid")
		return
	}
	for _, c := range w.NearbyCreatures {
		if c != nil && c.Entry != 299 && c.Present && c.AliveKnown && c.Alive && c.AttackVictimKnown && c.AttackVictimGuid == s.selfGUID && c.AttackAgeMs <= 30000 {
			s.stopPracticeLocked(ctx, "unexpected_hostile_requires_help")
			return
		}
	}
	for _, c := range w.NearbyCreatures {
		if c != nil && c.Guid == s.practiceTarget {
			target = c
			break
		}
	}
	if s.practiceTarget != 0 && target != nil && target.HealthKnown && target.Health == 0 && target.AliveKnown && !target.Alive {
		s.stopLocked(ctx)
		s.stopAutoAttackLocked(ctx)
		s.practiceTarget, s.state.PracticeTargetGuid = 0, 0
		s.practiceRoute, s.practicePullStarted = nil, false
		s.state.PracticePhase = "target_death_observed"
		return
	}
	if s.practiceTarget == 0 {
		s.stopLocked(ctx)
		if self.UnitFlags&0x80000 != 0 || !s.stopAutoAttackLocked(ctx) {
			s.state.PracticePhase = "waiting_out_of_combat"
			return
		}
		ready := self.MaxHealth > 0 && uint64(self.Health)*100 >= uint64(self.MaxHealth)*95
		if self.ClassId != 4 {
			ready = ready && self.PowerType == 0 && uint64(self.Power) >= practicePullBudget(self)
		} else {
			ready = ready && self.PowerType == 3 && self.Power >= 90
		}
		if !ready {
			s.state.PracticePhase = "recovering_before_next_pull"
			return
		}
		best := float64(120)
		for _, c := range w.NearbyCreatures {
			if !eligiblePracticeWolf(c) || c.UnitFlags&0x80000 != 0 || c.DynamicFlags != 0 || c.AttackVictimKnown || c.TargetKnown && c.TargetGuid != 0 {
				continue
			}
			p, known, _ := creaturePosition(c)
			if d := distance(s.pos, p); known && northshire(p) && d < best {
				best, target = d, c
			}
		}
		if target == nil {
			s.state.PracticePhase = "waiting_for_observed_level_one_young_wolf"
			return
		}
		s.practiceTarget, s.state.PracticeTargetGuid = target.Guid, target.Guid
		s.practiceRoute, s.practicePullStarted, s.practiceReplans = nil, false, 0
		s.practiceKillCredited = false
	}
	if !eligiblePracticeWolf(target) || target.AttackVictimKnown && target.AttackVictimGuid != s.selfGUID || target.DynamicFlags&4 != 0 && target.DynamicFlags&8 == 0 || s.failedAssistTarget == s.practiceTarget {
		s.stopPracticeLocked(ctx, "target_unknown_claimed_or_cast_failed")
		return
	}
	if self.ClassId == 4 && !target.CombatReachKnown {
		s.stopPracticeLocked(ctx, "target_melee_range_unknown")
		return
	}
	if self.UnitFlags&0x80000 != 0 && !s.practicePullStarted {
		s.stopPracticeLocked(ctx, "unexpected_combat_before_pull")
		return
	}
	targetPosition, known, _ := creaturePosition(target)
	if !known || !northshire(targetPosition) {
		s.stopLocked(ctx)
		s.state.PracticePhase = "waiting_for_observed_wolf_position"
		return
	}
	if s.state.CastPending {
		s.stopLocked(ctx)
		if !s.now().Before(s.castUntil) {
			s.stopPracticeLocked(ctx, "cast_timeout_no_retry")
		}
		return
	}
	standRange := float64(3.5)
	if self.ClassId != 4 {
		standRange = 25
	}
	needsApproach := distance(s.pos, targetPosition) > standRange
	if s.practicePullStarted && !needsApproach {
		s.practiceRoute = nil
	}
	if !s.practicePullStarted || needsApproach {
		if s.practiceRoute == nil || distance(s.practiceRouteTarget, targetPosition) > 2 {
			s.stopLocked(ctx)
			if s.practiceReplans >= 3 {
				s.stopPracticeLocked(ctx, "target_moved_beyond_route_budget")
				return
			}
			path, err := s.navigationPath(ctx, s.navDataDir, s.pos, targetPosition)
			if err != nil {
				s.state.NavAvailable, s.state.NavReason = false, navigationDiagnostic(err, s.navDataDir)
				s.stopPracticeLocked(ctx, "navigation_unavailable")
				return
			}
			s.state.NavAvailable, s.state.NavReason = true, "client_navmesh_route_verified"
			s.practiceRoute, s.practiceRouteIndex, s.practiceRouteTarget = path, 0, targetPosition
			s.practiceReplans++
		}
	}
	if needsApproach || s.practiceRoute != nil && (routeRemaining(s.pos, s.practiceRoute, s.practiceRouteIndex) > standRange || !directRemainingRoute(s.pos, targetPosition, s.practiceRoute, s.practiceRouteIndex)) {
		if !s.stopAutoAttackLocked(ctx) {
			return
		}
		if s.practiceRouteIndex >= len(s.practiceRoute) {
			s.stopPracticeLocked(ctx, "navigation_ended_outside_range")
			return
		}
		if !s.navigationStepLocked(ctx, s.practiceRoute, &s.practiceRouteIndex) {
			s.stopPracticeLocked(ctx, "navigation_step_failed")
			return
		}
		s.state.PracticePhase = "approaching_observed_wolf_on_navmesh"
		return
	}
	if !s.stopLocked(ctx) {
		s.stopPracticeLocked(ctx, "movement_stop_unconfirmed")
		return
	}
	if !s.practicePullStarted {
		// Recheck the pull prerequisites after approach/plan time; a previously
		// healthy ready observation does not authorize a later weakened pull.
		if self.UnitFlags&0x80000 != 0 || uint64(self.Health)*100 < uint64(self.MaxHealth)*95 || self.ClassId != 4 && uint64(self.Power) < practicePullBudget(self) || self.ClassId == 4 && self.Power < 90 {
			s.stopPracticeLocked(ctx, "pull_prerequisites_changed")
			return
		}
		s.practicePullStarted = true
		s.practiceRoute = nil
		s.state.PracticePulls++
	}
	if self.ClassId == 4 {
		s.rogueAssistLocked(ctx, w, target, targetPosition)
	} else {
		spell, cost := fireball, uint64(8)
		if self.ClassId == 5 {
			spell, cost = smite, 9
		}
		if !s.now().Before(s.nextAssist) && !s.now().Before(s.nextHeal) && uint64(self.Power) >= manaBudget(self, cost) && self.UnitFlags&0x2000 == 0 {
			if s.faceEnemyLocked(ctx, targetPosition) {
				s.castOffenseLocked(ctx, spell, target.Guid, 3*time.Second)
			}
		}
	}
	if finishing {
		if s.practiceQuestGoal {
			s.state.PracticePhase = "finishing_current_fight_to_goal"
		} else {
			s.state.PracticePhase = "finishing_current_fight_to_level2"
		}
	} else {
		s.state.PracticePhase = "fighting_observed_young_wolf"
	}
}
