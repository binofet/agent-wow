package buddyactions

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strconv"
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
	return planColdridgeRegion(ctx, root, buddynav.RegionColdridge, from, to)
}

func planColdridge182Navigation(ctx context.Context, root string, from, to position) ([]position, error) {
	return planColdridgeRegion(ctx, root, buddynav.RegionColdridgeTrollCave, from, to)
}

func planColdridgeRegion(ctx context.Context, root string, region buddynav.Region, from, to position) ([]position, error) {
	points, err := buddynav.PlanRegionContext(ctx, root, 0, region, buddynav.Point{X: from.x, Y: from.y, Z: from.z}, buddynav.Point{X: to.x, Y: to.y, Z: to.z})
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
	return s.coldridgeGoalNavigationPath(ctx, coldridgePracticeGoal{questID: 179}, from, to)
}

func (s *Service) coldridgeGoalNavigationPath(ctx context.Context, goal coldridgePracticeGoal, from, to position) ([]position, error) {
	plan := s.planColdridgePath
	if goal.questID == 182 {
		plan = s.planColdridge182Path
	}
	if s.navDataDir == "" || plan == nil || !goal.contains(from) || !goal.contains(to) {
		return nil, errors.New("Coldridge client navigation data and bounded endpoints required")
	}
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	path, err := plan(ctx, s.navDataDir, from, to)
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
		if !goal.contains(p) {
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
	if !s.refreshPositionLocked(w) {
		return nil, status.Error(codes.FailedPrecondition, "current Coldridge client baseline required")
	}
	chosen, fresh, owned := s.coldridgeLifecycleLocked(ctx, w, "start")
	if !owned || !chosen.Valid || chosen.Kind != "start" {
		return nil, status.Error(codes.FailedPrecondition, "current Lua Coldridge lifecycle did not select a goal")
	}
	goal := coldridgePracticeGoal{}
	goal.applyLifecycle(chosen)
	w = fresh
	incomplete := chosen.Active && chosen.Needed
	if !incomplete || safeQuestCharacter(w, s.selfGUID) != "" || !w.Self.MapKnown || w.Self.MapId != 0 || !goal.baselineContains(playerPosition(w.Self)) || !s.state.KnownShadowBolt {
		return nil, status.Error(codes.FailedPrecondition, "Coldridge practice requires actual level1..5 warlock (quest182 level4..6), knownShadowBolt and own supported incompletequest179,170,183 or182")
	}
	if goal.questID == 182 && (!s.refreshPositionLocked(w) || !goal.contains(s.pos)) {
		return nil, status.Error(codes.FailedPrecondition, "quest182 practice requires a stopped client position in its verified troll area")
	}
	binding, previousGoal := chosen.binding, s.coldridgeGoal
	phase, epoch := s.state.PracticePhase, s.hostEncounterEpoch
	facts, _ := s.coldridgeLifecycleFacts(w, "start")
	current := func() bool {
		return binding.sameTask(s, ctx) && s.coldridgeGoal == previousGoal && s.practiceLifecycleSourceCurrentLocked(chosen.source) &&
			s.state.PracticePhase == phase && s.hostEncounterEpoch == epoch && !s.state.CastPending && !s.state.MotionUnconfirmed && !s.state.Traveling &&
			!s.bodyMoving && !s.questApproaching && !s.clientControlLost && !s.clientControlRestoring && !s.recovery.state.Active
	}
	if !current() || !s.stopAutoAttackLocked(ctx) || !current() || !s.stopLocked(ctx) || !current() {
		return nil, status.Error(codes.FailedPrecondition, "practice stop or original task binding is unconfirmed")
	}
	now := s.now()
	observed, err := s.snapshot(ctx)
	latest, known := s.coldridgeLifecycleFacts(observed, "start")
	if err != nil || !known || !current() || !reflect.DeepEqual(practiceLifecycleStable(facts), practiceLifecycleStable(latest)) {
		return nil, status.Error(codes.FailedPrecondition, "Coldridge start observations changed during stop")
	}
	if !reflect.DeepEqual(chosen.motionSources, practiceLifecycleMotionSources(observed, chosen.binding.target)) {
		return nil, status.Error(codes.FailedPrecondition, "Coldridge start motion source changed during stop")
	}
	// An attached source can change during STOP callbacks without altering the
	// native cache. Read the same pure selector once more before binding a task.
	confirmed, _, owned := s.coldridgeLifecycleLocked(ctx, observed, "start")
	if !owned || !confirmed.Valid || !current() || confirmed.source != chosen.source || confirmed.Kind != "start" ||
		!sameColdridgeLifecycleChoice(confirmed, chosen) || !reflect.DeepEqual(chosen.motionSources, confirmed.motionSources) ||
		!reflect.DeepEqual(practiceLifecycleStable(chosen.facts), practiceLifecycleStable(confirmed.facts)) {
		return nil, status.Error(codes.FailedPrecondition, "Coldridge start source changed during stop")
	}
	s.state.Practicing, s.state.PracticePhase = true, "waiting_for_safe_coldridge_pull"
	s.coldridgeGoal = goal
	if goal.questID == 170 {
		s.state.PracticePhase = "waiting_for_safe_quest_170_pull"
	} else if goal.questID == 183 {
		s.state.PracticePhase = "waiting_for_safe_quest_183_pull"
	} else if goal.questID == 182 {
		s.state.PracticePhase = "waiting_for_safe_quest_182_pull"
	}
	s.practiceUntil, s.practiceQuestGoal = now.Add(time.Duration(chosen.LeaseMS)*time.Millisecond), true
	s.practiceTarget, s.state.PracticeTargetGuid = 0, 0
	s.practiceRoute, s.practicePullStarted, s.failedAssistTarget = nil, false, 0
	s.lastStep = now
	return s.stateLocked(), nil
}

func (s *Service) coldridgePracticeTickLocked(ctx context.Context, w *worldv1.SnapshotResponse) {
	s.coldridgeObservedPracticeTickLocked(ctx, w, nil, nil)
}

// A target-free Lua read is followed once by these same complete native gates
// on its fresh World response. Readiness never survives into another tick.
func (s *Service) coldridgeObservedPracticeTickLocked(ctx context.Context, w *worldv1.SnapshotResponse, readiness *starterSpellDecision, current func() bool) {
	if current != nil && !current() {
		return
	}
	self := w.Self
	goal := &s.coldridgeGoal
	// A new authoritative baseline invalidates any route and pull selected from
	// the old client estimate. Rebase before stopping so STOP cannot undo it.
	if s.posKnown && self.PositionRevision != s.positionRevision {
		s.refreshPositionLocked(w)
		s.stopPracticeLocked(ctx, "coldridge_authoritative_position_changed")
		return
	}
	if safePracticeCharacter(w, s.selfGUID) != "" || !self.ClassKnown || self.ClassId != 9 || !self.LevelKnown || self.Level < 1 || !self.HealthKnown || !self.PowerKnown || !self.BaseManaKnown || self.BaseMana == 0 || self.PowerType != 0 || !self.SpeedsKnown || math.Abs(float64(self.RunSpeed)-7) > .01 || !goal.baselineContains(playerPosition(self)) || !s.refreshPositionLocked(w) || !goal.contains(s.pos) || s.state.MotionUnconfirmed || self.UnitFlags&0x2000 != 0 || !s.state.KnownShadowBolt {
		s.stopPracticeLocked(ctx, "coldridge_own_state_unknown_or_unsafe")
		return
	}
	target := practiceTargetObservation(w, s.practiceTarget)
	lifecycle, fresh, owned := s.coldridgeLifecycleLocked(ctx, w, "tick")
	if !owned {
		return
	}
	if !lifecycle.Valid {
		s.rejectColdridgeLifecycleLocked(ctx, lifecycle)
		return
	}
	w, self = fresh, fresh.Self
	target = practiceTargetObservation(w, s.practiceTarget)
	goal.applyLifecycle(lifecycle)
	finishing := lifecycle.Finishing
	if finishing && (!s.practicePullStarted || self.UnitFlags&0x80000 == 0 || !goal.eligible(target, self) || !target.AttackVictimKnown || target.AttackVictimGuid != s.selfGUID || target.AttackAgeMs > 30000) {
		s.pauseColdridgeLifecycleLocked(ctx, lifecycle.binding)
		return
	}
	if lifecycle.Kind == "stop" {
		s.stopPracticeLocked(ctx, lifecycle.Phase)
		return
	}
	if lifecycle.Kind == "wait" {
		s.pauseColdridgeLifecycleLocked(ctx, lifecycle.binding)
		return
	}
	if finishing {
		session, selfGUID, until, questID, targetGUID := s.sessionID, s.selfGUID, s.practiceUntil, goal.questID, s.practiceTarget
		defer func() {
			if s.state.Practicing && s.sessionID == session && s.selfGUID == selfGUID && s.practiceUntil == until &&
				s.coldridgeGoal.questID == questID && s.practiceTarget == targetGUID {
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
	if lifecycle.Kind == "target_dead" && (target == nil || !target.HealthKnown || target.Health != 0 || !target.AliveKnown || target.Alive) ||
		lifecycle.Kind == "clear_target" && (target == nil || s.practicePullStarted) {
		s.pauseColdridgeLifecycleLocked(ctx, lifecycle.binding)
		return
	}
	if lifecycle.Kind == "target_dead" && target != nil && target.HealthKnown && target.Health == 0 && target.AliveKnown && !target.Alive {
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
	if lifecycle.Kind == "clear_target" && target != nil && !s.practicePullStarted {
		s.stopLocked(ctx)
		s.practiceTarget, s.state.PracticeTargetGuid = 0, 0
		s.practiceRoute = nil
		s.state.PracticePhase = "selected_quest_objective_already_satisfied"
		return
	}
	if s.practiceTarget == 0 {
		if current == nil {
			current = s.coldridgePracticeCurrentLocked(ctx, 0)
		}
		if !current() {
			return
		}
		stopped := s.stopLocked(ctx)
		if current != nil && !current() {
			return
		}
		attackStopped := false
		if stopped && self.UnitFlags&0x80000 == 0 {
			attackStopped = s.stopAutoAttackLocked(ctx)
		}
		if current != nil && !current() {
			return
		}
		if !stopped || self.UnitFlags&0x80000 != 0 || !attackStopped {
			s.state.PracticePhase = "waiting_out_of_combat"
			return
		}
		if readiness == nil {
			selected, fresh := s.starterColdridgePullDecisionLocked(ctx, w)
			if !current() {
				return
			}
			if !selected.valid || selected.kind != "ready" {
				s.state.PracticePhase = "recovering_before_next_pull"
				if !selected.valid {
					s.state.Reason = s.starterSpells.failure()
				}
				return
			}
			s.coldridgeObservedPracticeTickLocked(ctx, fresh, &selected, current)
			return
		}
		if !current() || !s.starterSpellFactsMatchLocked(*readiness, w, nil, position{}) {
			return
		}
		selected, observed, owned := s.coldridgeLifecycleLocked(ctx, w, "targets")
		if !owned || !current() {
			return
		}
		if !selected.Valid {
			s.rejectColdridgeLifecycleLocked(ctx, selected)
			return
		}
		w, self = observed, observed.Self
		goal.applyLifecycle(selected)
		if selected.Kind == "stop" {
			s.stopPracticeLocked(ctx, selected.Phase)
			return
		}
		if selected.Kind != "select" {
			s.state.PracticePhase = selected.Phase
			return
		}
		guid, _ := strconv.ParseUint(selected.TargetGUID, 10, 64)
		target = practiceTargetObservation(w, guid)
		if target == nil || !goal.eligible(target, self) || !goal.positionEvidenceWithin(target) || !goal.targetNeeded(target.Entry) || !unclaimedColdridgePull(target) {
			s.pauseColdridgeLifecycleLocked(ctx, selected.binding)
			return
		}
		point, known, _ := creaturePosition(target)
		if !known || !goal.contains(point) {
			s.pauseColdridgeLifecycleLocked(ctx, selected.binding)
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
	if !known || !goal.contains(p) || !goal.positionEvidenceWithin(target) {
		s.stopLocked(ctx)
		s.state.PracticePhase = "waiting_for_observed_wolf_position"
		if goal.questID == 170 {
			s.state.PracticePhase = "waiting_for_observed_quest_170_target_position"
		} else if goal.questID == 183 {
			s.state.PracticePhase = "waiting_for_observed_quest_183_target_position"
		} else if goal.questID == 182 {
			s.state.PracticePhase = "waiting_for_observed_quest_182_target_position"
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
			path, err := s.coldridgeGoalNavigationPath(ctx, *goal, s.pos, p)
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
		if s.practiceRouteIndex >= len(s.practiceRoute) || !s.navigationStepWithinLocked(ctx, s.practiceRoute, &s.practiceRouteIndex, goal.contains) {
			s.stopPracticeLocked(ctx, "navigation_step_failed")
			return
		}
		s.state.PracticePhase = "approaching_observed_coldridge_wolf_on_navmesh"
		if goal.questID == 170 {
			s.state.PracticePhase = "approaching_observed_quest_170_target_on_navmesh"
		} else if goal.questID == 183 {
			s.state.PracticePhase = "approaching_observed_quest_183_target_on_navmesh"
		} else if goal.questID == 182 {
			s.state.PracticePhase = "approaching_observed_quest_182_target_on_navmesh"
		}
		return
	}
	if !s.stopLocked(ctx) {
		s.stopPracticeLocked(ctx, "movement_stop_unconfirmed")
		return
	}
	if !s.practicePullStarted {
		if !goal.targetNeeded(target.Entry) || self.UnitFlags&0x80000 != 0 {
			s.stopPracticeLocked(ctx, "pull_prerequisites_changed")
			return
		}
	}
	if !s.starterColdridgeCasterLocked(ctx, w, target, p) {
		return
	}
	s.state.PracticePhase = "fighting_observed_coldridge_wolf"
	if goal.questID == 170 {
		s.state.PracticePhase = "fighting_observed_quest_170_target"
	} else if goal.questID == 183 {
		s.state.PracticePhase = "fighting_observed_quest_183_target"
	} else if goal.questID == 182 {
		s.state.PracticePhase = "fighting_observed_quest_182_target"
	}
	if finishing {
		s.state.PracticePhase = "finishing_current_fight_to_goal"
	}
}

// Facing itself obtains clock/world observations. Use a final requested-target
// snapshot so neither that newer evidence nor discovery truncation can leave a
// stale goal, claim or physical baseline immediately before cast dispatch.
func (s *Service) coldridgeCastObservationsLocked(ctx context.Context, original *worldv1.SnapshotResponse, aimed position, current func() bool) (*worldv1.SnapshotResponse, *worldv1.CreatureState, position, bool) {
	reject := func(reason string) (*worldv1.SnapshotResponse, *worldv1.CreatureState, position, bool) {
		if current() {
			s.stopPracticeLocked(ctx, "coldridge_precast_"+reason)
		}
		return nil, nil, position{}, false
	}
	if !current() {
		return nil, nil, position{}, false
	}
	w, err := s.snapshot(ctx, s.practiceTarget)
	if !current() {
		return nil, nil, position{}, false
	}
	if err != nil || safePracticeCharacter(w, s.selfGUID) != "" {
		return reject("own_observations_unavailable_or_unsafe")
	}
	self := w.Self
	goal := &s.coldridgeGoal
	if original == nil || original.Self == nil || self.MapId != original.Self.MapId || self.PositionRevision != original.Self.PositionRevision ||
		distance(playerPosition(self), playerPosition(original.Self)) > .05 || s.posMap != self.MapId || s.positionRevision != self.PositionRevision ||
		!goal.baselineContains(playerPosition(self)) || !goal.contains(s.pos) || s.state.MotionUnconfirmed {
		return reject("physical_baseline_changed")
	}
	if !self.PowerKnown || !self.BaseManaKnown || self.BaseMana == 0 || self.PowerType != 0 || !self.SpeedsKnown || math.Abs(float64(self.RunSpeed)-7) > .01 || self.UnitFlags&0x2000 != 0 || !s.state.KnownShadowBolt {
		return reject("own_combat_prerequisites_changed")
	}
	source := s.starterSpells.revision
	active, needed := s.observeColdridgeGoalLocked(ctx, w)
	if !current() || source != s.starterSpells.revision {
		return nil, nil, position{}, false
	}
	if !active {
		return reject("bound_goal_absent_or_invalid")
	}
	target := practiceTargetObservation(w, s.practiceTarget)
	if !goal.eligible(target, self) || len(w.NearbyCreatures) > 32 {
		return reject("target_observations_unavailable_or_unsafe")
	}
	if !s.practicePullStarted {
		if !needed || !goal.targetNeeded(target.Entry) || !unclaimedColdridgePull(target) || self.UnitFlags&0x80000 != 0 {
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
	point, known, _ := creaturePosition(target)
	if !known || !goal.contains(point) || !goal.positionEvidenceWithin(target) || distance(s.pos, point) > 25 || distance(aimed, point) > 2 {
		// A roaming target may need another bounded route/facing cycle. This
		// grants no pull authority and never reuses the earlier facing now.
		s.practiceRoute = nil
		s.state.PracticePhase = "waiting_for_fresh_coldridge_target_position"
		return nil, nil, position{}, false
	}
	return w, target, point, true
}
