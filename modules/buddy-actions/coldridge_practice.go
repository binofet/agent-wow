package buddyactions

import (
	"context"
	"errors"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func coldridge(p position) bool {
	return finite(p) && p.x >= -6399 && p.x <= -6000 && p.y >= 0 && p.y <= 700 && p.z >= 320 && p.z <= 450
}

func ownColdridgeQuest(w *worldv1.SnapshotResponse, self uint64) (active, incomplete bool) {
	if w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.ClassKnown || w.Self.ClassId != 9 || !w.Self.LevelKnown || w.Self.Level < 1 || w.Self.Level > 5 || !w.SelfQuestLogKnown || len(w.SelfQuestLog) != 25 {
		return false, false
	}
	var slots [25]bool
	for _, q := range w.SelfQuestLog {
		if q == nil || q.Slot >= 25 || slots[q.Slot] {
			return false, false
		}
		slots[q.Slot] = true
		if q.QuestId == 179 && !q.Failed && q.State&2 == 0 {
			active, incomplete = true, !q.Complete && q.State&1 == 0
		}
	}
	return active, incomplete
}

func planColdridgeNavigation(ctx context.Context, root string, from, to position) ([]position, error) {
	points, err := buddynav.PlanRegionContext(ctx, root, 0, buddynav.RegionColdridge, buddynav.Point{X: from.x, Y: from.y, Z: from.z}, buddynav.Point{X: to.x, Y: to.y, Z: to.z})
	if err != nil {
		return nil, err
	}
	path := make([]position, len(points))
	for i, p := range points {
		path[i] = position{x: p.X, y: p.Y, z: p.Z}
	}
	return path, nil
}

func (s *Service) coldridgeNavigationPath(ctx context.Context, from, to position) ([]position, error) {
	if s.navDataDir == "" || s.planColdridgePath == nil || !coldridge(from) || !coldridge(to) {
		return nil, errors.New("Coldridge client navigation data and bounded endpoints required")
	}
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	path, err := s.planColdridgePath(ctx, s.navDataDir, from, to)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(path) < 2 || len(path) > buddynav.MaxPoints || distance(path[0], from) > 2 || distance(path[len(path)-1], to) > 2 {
		return nil, errors.New("Coldridge route endpoint or point bound failed")
	}
	length := distance(from, path[0])
	for i, p := range path {
		if !coldridge(p) {
			return nil, errors.New("Coldridge route escaped sealed bounds")
		}
		if i > 0 {
			d, dz := distance(path[i-1], p), math.Abs(float64(path[i-1].z-p.z))
			if d > 2 || dz > 1 {
				return nil, errors.New("Coldridge route exceeds physical segment limits")
			}
			length += d
		}
	}
	if length > buddynav.MaxRouteLength {
		return nil, errors.New("Coldridge route exceeds240yd budget")
	}
	return path, nil
}

func eligibleColdridgeWolf(c *worldv1.CreatureState) bool {
	return c != nil && c.Guid>>48 == 0xf130 && c.Guid&0xffffff != 0 && uint32(c.Guid>>24&0xffffff) == 705 && c.Entry == 705 && c.Present && c.MapKnown && c.MapId == 0 && c.LevelKnown && c.Level == 1 && c.HealthKnown && c.MaxHealth > 0 && c.Health > 0 && c.AliveKnown && c.Alive && c.NpcFlags == 0 && c.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) == 0 && c.UnitFlags_2&1 == 0
}

func unclaimedColdridgePull(c *worldv1.CreatureState) bool {
	return c != nil && c.UnitFlags&0x80000 == 0 && c.DynamicFlags == 0 && !c.AttackVictimKnown && (!c.TargetKnown || c.TargetGuid == 0)
}

func (s *Service) startColdridgePracticeLocked(ctx context.Context, w *worldv1.SnapshotResponse) (*actionsv1.State, error) {
	goal, incomplete := selectColdridgePracticeGoal(w, s.selfGUID)
	if !incomplete || safeQuestCharacter(w, s.selfGUID) != "" || !w.Self.MapKnown || w.Self.MapId != 0 || !coldridge(playerPosition(w.Self)) || !s.state.KnownShadowBolt {
		return nil, status.Error(codes.FailedPrecondition, "Coldridge practice requires actual level1..5 warlock, knownShadowBolt and own supported incompletequest179 or170")
	}
	if !s.stopAutoAttackLocked(ctx) || !s.stopLocked(ctx) {
		return nil, status.Error(codes.FailedPrecondition, "practice stop is unconfirmed")
	}
	s.state.Practicing, s.state.PracticePhase = true, "waiting_for_safe_coldridge_pull"
	s.coldridgeGoal = goal
	if goal.questID == 170 {
		s.state.PracticePhase = "waiting_for_safe_quest_170_pull"
	}
	s.practiceUntil, s.practiceQuestGoal = s.now().Add(10*time.Minute), true
	s.practiceTarget, s.state.PracticeTargetGuid = 0, 0
	s.practiceRoute, s.practicePullStarted, s.failedAssistTarget = nil, false, 0
	s.lastStep = s.now()
	return s.stateLocked(), nil
}

func (s *Service) coldridgePracticeTickLocked(ctx context.Context, w *worldv1.SnapshotResponse) {
	self := w.Self
	// A new authoritative baseline invalidates any route and pull selected from
	// the old client estimate. Rebase before stopping so STOP cannot undo it.
	if s.posKnown && self.PositionRevision != s.positionRevision {
		s.refreshPositionLocked(w)
		s.stopPracticeLocked(ctx, "coldridge_authoritative_position_changed")
		return
	}
	if safePracticeCharacter(w, s.selfGUID) != "" || !self.ClassKnown || self.ClassId != 9 || !self.LevelKnown || self.Level < 1 || !self.HealthKnown || !self.PowerKnown || !self.BaseManaKnown || self.BaseMana == 0 || self.PowerType != 0 || !self.SpeedsKnown || math.Abs(float64(self.RunSpeed)-7) > .01 || !coldridge(playerPosition(self)) || !s.refreshPositionLocked(w) || !coldridge(s.pos) || s.state.MotionUnconfirmed || self.UnitFlags&0x2000 != 0 || !s.state.KnownShadowBolt {
		s.stopPracticeLocked(ctx, "coldridge_own_state_unknown_or_unsafe")
		return
	}
	target := practiceTargetObservation(w, s.practiceTarget)
	goal := &s.coldridgeGoal
	active, needed := goal.observe(w, s.selfGUID)
	selectedObjectiveComplete := target != nil && !goal.targetNeeded(target.Entry)
	finishing := active && (!needed || selectedObjectiveComplete) && s.practicePullStarted && self.UnitFlags&0x80000 != 0 && goal.eligible(target, self) && target.AttackVictimKnown && target.AttackVictimGuid == s.selfGUID && target.AttackAgeMs <= 30000
	if !needed && !finishing {
		why := "quest_179_complete_absent_or_level_limit"
		if goal.questID == 170 {
			why = "quest_170_complete_absent_or_invalid"
		}
		s.stopPracticeLocked(ctx, why)
		return
	}
	if needed && selectedObjectiveComplete && s.practicePullStarted && !finishing {
		s.stopPracticeLocked(ctx, "selected_quest_objective_complete_without_owned_fight")
		return
	}
	if finishing {
		defer func() {
			if s.state.Practicing {
				s.state.PracticePhase = "finishing_current_fight_to_goal"
			}
		}()
	}
	if s.state.AutoAttackUnconfirmed {
		s.stopLocked(ctx)
		s.state.PracticePhase = "waiting_for_server_attack_stop"
		return
	}
	if len(w.NearbyCreatures) > 32 || s.practiceTarget != 0 && target == nil {
		s.stopPracticeLocked(ctx, "coldridge_target_observation_unavailable")
		return
	}
	for _, c := range w.NearbyCreatures {
		if c != nil && c.Guid != s.practiceTarget && c.Present && c.AliveKnown && c.Alive && c.AttackVictimKnown && c.AttackVictimGuid == s.selfGUID && c.AttackAgeMs <= 30000 {
			s.stopPracticeLocked(ctx, "unexpected_hostile_requires_help")
			return
		}
	}
	if target != nil && target.HealthKnown && target.Health == 0 && target.AliveKnown && !target.Alive {
		s.stopLocked(ctx)
		s.stopAutoAttackLocked(ctx)
		s.practiceTarget, s.state.PracticeTargetGuid = 0, 0
		s.practiceRoute, s.practicePullStarted = nil, false
		s.state.PracticePhase = "target_death_observed"
		return
	}
	// Another actual objective update may satisfy this target while a route is
	// being planned. A selected but unpulled target never creates authority to
	// begin an unnecessary fight, even when the other objective still needs work.
	if target != nil && !s.practicePullStarted && !goal.targetNeeded(target.Entry) {
		s.stopLocked(ctx)
		s.practiceTarget, s.state.PracticeTargetGuid = 0, 0
		s.practiceRoute = nil
		s.state.PracticePhase = "selected_quest_objective_already_satisfied"
		return
	}
	if s.practiceTarget == 0 {
		if !s.stopLocked(ctx) || self.UnitFlags&0x80000 != 0 || !s.stopAutoAttackLocked(ctx) {
			s.state.PracticePhase = "waiting_out_of_combat"
			return
		}
		if self.MaxHealth == 0 || uint64(self.Health)*100 < uint64(self.MaxHealth)*95 || uint64(self.Power) < manaBudget(self, 60) {
			s.state.PracticePhase = "recovering_before_next_pull"
			return
		}
		best := float64(120)
		for _, c := range w.NearbyCreatures {
			if !goal.eligible(c, self) || !goal.targetNeeded(c.Entry) || !unclaimedColdridgePull(c) {
				continue
			}
			p, known, _ := creaturePosition(c)
			if d := distance(s.pos, p); known && coldridge(p) && d < best {
				best, target = d, c
			}
		}
		if target == nil {
			s.state.PracticePhase = "waiting_for_observed_level_one_ragged_young_wolf"
			if goal.questID == 170 {
				s.state.PracticePhase = "waiting_for_observed_required_quest_170_target"
			}
			return
		}
		s.practiceTarget, s.state.PracticeTargetGuid = target.Guid, target.Guid
		s.practiceRoute, s.practicePullStarted, s.practiceReplans, s.practiceKillCredited = nil, false, 0, false
	}
	if !goal.eligible(target, self) || !s.practicePullStarted && !unclaimedColdridgePull(target) || target.AttackVictimKnown && target.AttackVictimGuid != s.selfGUID || target.DynamicFlags&4 != 0 && target.DynamicFlags&8 == 0 || s.failedAssistTarget == s.practiceTarget || self.UnitFlags&0x80000 != 0 && !s.practicePullStarted {
		s.stopPracticeLocked(ctx, "coldridge_target_claimed_unsafe_or_cast_failed")
		return
	}
	p, known, _ := creaturePosition(target)
	if !known || !coldridge(p) {
		s.stopLocked(ctx)
		s.state.PracticePhase = "waiting_for_observed_wolf_position"
		if goal.questID == 170 {
			s.state.PracticePhase = "waiting_for_observed_quest_170_target_position"
		}
		return
	}
	if s.state.CastPending {
		s.stopLocked(ctx)
		if !s.now().Before(s.castUntil) {
			s.stopPracticeLocked(ctx, "cast_timeout_no_retry")
		}
		return
	}
	if !s.practicePullStarted || distance(s.pos, p) > 25 {
		if s.practiceRoute == nil || distance(s.practiceRouteTarget, p) > 2 {
			if !s.stopLocked(ctx) || s.practiceReplans >= 3 {
				s.stopPracticeLocked(ctx, "coldridge_target_moved_beyond_route_budget")
				return
			}
			path, err := s.coldridgeNavigationPath(ctx, s.pos, p)
			if err != nil {
				s.state.NavAvailable, s.state.NavReason = false, navigationDiagnostic(err, s.navDataDir)
				s.stopPracticeLocked(ctx, "navigation_unavailable")
				return
			}
			s.state.NavAvailable, s.state.NavReason = true, "coldridge_client_navmesh_route_verified"
			s.practiceRoute, s.practiceRouteIndex, s.practiceRouteTarget = path, 0, p
			s.practiceReplans++
			// Fetch a new own/target/quest snapshot on the next tick before
			// either walking this route or beginning a pull.
			s.state.PracticePhase = "coldridge_route_verified_rechecking_observations"
			return
		}
	}
	if distance(s.pos, p) > 25 || s.practiceRoute != nil && (routeRemaining(s.pos, s.practiceRoute, s.practiceRouteIndex) > 25 || !directRemainingRoute(s.pos, p, s.practiceRoute, s.practiceRouteIndex)) {
		if s.practiceRouteIndex >= len(s.practiceRoute) || !s.navigationStepWithinLocked(ctx, s.practiceRoute, &s.practiceRouteIndex, coldridge) {
			s.stopPracticeLocked(ctx, "navigation_step_failed")
			return
		}
		s.state.PracticePhase = "approaching_observed_coldridge_wolf_on_navmesh"
		if goal.questID == 170 {
			s.state.PracticePhase = "approaching_observed_quest_170_target_on_navmesh"
		}
		return
	}
	if !s.stopLocked(ctx) {
		s.stopPracticeLocked(ctx, "movement_stop_unconfirmed")
		return
	}
	if !s.practicePullStarted {
		if !goal.targetNeeded(target.Entry) || self.UnitFlags&0x80000 != 0 || uint64(self.Health)*100 < uint64(self.MaxHealth)*95 || uint64(self.Power) < manaBudget(self, 60) {
			s.stopPracticeLocked(ctx, "pull_prerequisites_changed")
			return
		}
	}
	rank := s.selectedSpellLocked(shadowBolt)
	if rank.id != shadowBolt {
		s.stopPracticeLocked(ctx, "required_spell_unknown")
		return
	}
	if !s.now().Before(s.nextAssist) && !s.now().Before(s.nextHeal) && uint64(self.Power) >= manaBudget(self, uint64(rank.manaPercent)) && s.faceEnemyLocked(ctx, p) {
		if !s.coldridgeCastReadyLocked(ctx, w, p, rank) {
			return
		}
		if !s.practicePullStarted {
			// Only dispatching the first cast reserves an owned pull. Waiting
			// for cooldown, mana or facing cannot waive fresh claim checks.
			s.practicePullStarted, s.practiceRoute = true, nil
			s.state.PracticePulls++
		}
		s.castOffenseLocked(ctx, rank.id, target.Guid, 2*time.Second)
	}
	s.state.PracticePhase = "fighting_observed_coldridge_wolf"
	if goal.questID == 170 {
		s.state.PracticePhase = "fighting_observed_quest_170_target"
	}
	if finishing {
		s.state.PracticePhase = "finishing_current_fight_to_goal"
	}
}

// Facing itself obtains clock/world observations. Use a final requested-target
// snapshot so neither that newer evidence nor discovery truncation can leave a
// stale goal, claim or physical baseline immediately before cast dispatch.
func (s *Service) coldridgeCastReadyLocked(ctx context.Context, original *worldv1.SnapshotResponse, aimed position, rank spellRank) bool {
	reject := func(reason string) bool {
		s.stopPracticeLocked(ctx, "coldridge_precast_"+reason)
		return false
	}
	w, err := s.snapshot(ctx, s.practiceTarget)
	if err != nil || safePracticeCharacter(w, s.selfGUID) != "" {
		return reject("own_observations_unavailable_or_unsafe")
	}
	self := w.Self
	if original == nil || original.Self == nil || self.MapId != original.Self.MapId || self.PositionRevision != original.Self.PositionRevision || !coldridge(playerPosition(self)) || !coldridge(s.pos) || s.state.MotionUnconfirmed {
		return reject("physical_baseline_changed")
	}
	if !self.PowerKnown || !self.BaseManaKnown || self.BaseMana == 0 || self.PowerType != 0 || !self.SpeedsKnown || math.Abs(float64(self.RunSpeed)-7) > .01 || self.UnitFlags&0x2000 != 0 || uint64(self.Power) < manaBudget(self, uint64(rank.manaPercent)) {
		return reject("own_combat_prerequisites_changed")
	}
	goal := &s.coldridgeGoal
	active, needed := goal.observe(w, s.selfGUID)
	if !active {
		return reject("bound_goal_absent_or_invalid")
	}
	target := practiceTargetObservation(w, s.practiceTarget)
	if !goal.eligible(target, self) || len(w.NearbyCreatures) > 32 {
		return reject("target_observations_unavailable_or_unsafe")
	}
	if !s.practicePullStarted {
		if !needed || !goal.targetNeeded(target.Entry) || !unclaimedColdridgePull(target) || self.UnitFlags&0x80000 != 0 || uint64(self.Health)*100 < uint64(self.MaxHealth)*95 || uint64(self.Power) < manaBudget(self, 60) {
			return reject("fresh_pull_authority_changed")
		}
	} else {
		if target.AttackVictimKnown && target.AttackVictimGuid != s.selfGUID || target.DynamicFlags&4 != 0 && target.DynamicFlags&8 == 0 {
			return reject("started_target_claim_changed")
		}
		if (!needed || !goal.targetNeeded(target.Entry)) && (self.UnitFlags&0x80000 == 0 || !target.AttackVictimKnown || target.AttackVictimGuid != s.selfGUID || target.AttackAgeMs > 30000) {
			return reject("completed_objective_without_owned_fight")
		}
	}
	for _, other := range w.NearbyCreatures {
		if other != nil && other.Guid != target.Guid && other.Present && other.AliveKnown && other.Alive && other.AttackVictimKnown && other.AttackVictimGuid == s.selfGUID && other.AttackAgeMs <= 30000 {
			return reject("unexpected_hostile_requires_help")
		}
	}
	position, known, _ := creaturePosition(target)
	if !known || !coldridge(position) || distance(s.pos, position) > 25 || distance(aimed, position) > 2 {
		// A roaming target may need another bounded route/facing cycle. This
		// grants no pull authority and never reuses the earlier facing now.
		s.practiceRoute = nil
		s.state.PracticePhase = "waiting_for_fresh_coldridge_target_position"
		return false
	}
	return true
}
