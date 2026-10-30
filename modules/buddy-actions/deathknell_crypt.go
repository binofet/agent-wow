package buddyactions

import (
	"context"
	"crypto/subtle"
	"errors"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type cryptExitPlanner func(context.Context, string, position, position) ([]position, error)

func deathknellCrypt(p position) bool {
	return finite(p) && p.x >= 1642 && p.x <= 1684 && p.y >= 1662 && p.y <= 1681 && p.z >= 118 && p.z <= 145
}

// This static landmark came from an ordinary observed surface creature position
// and a complete independently checked crypt/stair route. It is not an NPC
// location claim, quest interaction, or permission to travel onward to Sarvis.
func cryptExitLandmark() position {
	return position{x: 1681.1973, y: 1671.4958, z: 136.40988}
}

func planCryptExit(ctx context.Context, root string, from, to position) ([]position, error) {
	points, err := buddynav.PlanRegionContext(ctx, root, 0, buddynav.RegionDeathknellCrypt,
		buddynav.Point{X: from.x, Y: from.y, Z: from.z}, buddynav.Point{X: to.x, Y: to.y, Z: to.z})
	if err != nil {
		return nil, err
	}
	path := make([]position, len(points))
	for i, p := range points {
		path[i] = position{x: p.X, y: p.Y, z: p.Z}
	}
	return path, nil
}

func validCryptExitPath(path []position, from, to position) bool {
	if len(path) < 2 || len(path) > buddynav.MaxPoints || distance(path[0], from) > 2 || distance(path[len(path)-1], to) > 2 {
		return false
	}
	length := distance(from, path[0])
	for i, p := range path {
		if !deathknellCrypt(p) {
			return false
		}
		if i > 0 {
			step := distance(path[i-1], p)
			if step > 2 || math.Abs(float64(p.z-path[i-1].z)) > 1 {
				return false
			}
			length += step
		}
	}
	return length <= buddynav.MaxRouteLength
}

func ownCryptQuest(w *worldv1.SnapshotResponse) bool {
	if w == nil || !w.SelfQuestLogKnown || len(w.SelfQuestLog) != 25 {
		return false
	}
	seen := [25]bool{}
	found := 0
	for _, q := range w.SelfQuestLog {
		if q == nil || q.Slot >= 25 || seen[q.Slot] {
			return false
		}
		seen[q.Slot] = true
		if q.QuestId == 363 {
			if q.Failed || q.State&2 != 0 {
				return false
			}
			found++
		}
	}
	return found == 1
}

func (s *Service) ownsCryptExit(r *actionsv1.ExitCryptRequest) bool {
	return !s.closed && s.state.InteractionOwner == "quests" && subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) == 1
}

func (s *Service) cryptExitContextLocked(ctx context.Context, r *actionsv1.ExitCryptRequest) string {
	s.expireInteractionLocked()
	if !s.ownsCryptExit(r) || s.interactionUntil.Sub(s.now()) < time.Second {
		return "crypt_exit_lease_changed_or_expiring"
	}
	if !s.independent {
		return "crypt_exit_independent_mode_required"
	}
	if s.state.Following || s.state.Healing || s.state.Assisting || s.state.Practicing || s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed || s.state.AutoAttacking || s.autoTarget != 0 {
		return "crypt_exit_controller_not_idle"
	}
	w, err := s.snapshot(ctx)
	if err != nil {
		return "crypt_exit_observations_unavailable"
	}
	if reason := safeQuestCharacter(w, s.selfGUID); reason != "" {
		return "crypt_exit_" + reason
	}
	p := w.Self
	if !p.ClassKnown || p.ClassId != 8 || !p.LevelKnown || p.Level < 1 || p.Level > 5 || !p.HealthKnown || p.Health == 0 {
		return "crypt_exit_observed_starter_mage_required"
	}
	if !ownCryptQuest(w) {
		return "crypt_exit_own_quest_363_required"
	}
	if !deathknellCrypt(playerPosition(p)) || !p.SpeedsKnown || math.IsNaN(float64(p.RunSpeed)) || math.IsInf(float64(p.RunSpeed), 0) || math.Abs(float64(p.RunSpeed)-7) > 0.01 {
		return "crypt_exit_verified_region_and_normal_speed_required"
	}
	if !s.refreshPositionLocked(w) {
		return "crypt_exit_authoritative_position_changed"
	}
	if !s.posKnown || s.posMap != 0 || !deathknellCrypt(s.pos) {
		return "crypt_exit_client_position_outside_region"
	}
	return ""
}

// ExitCrypt moves only an independent starter mage with its own quest363
// through the fixed measured crypt exit. Completion is a stopped client
// position estimate, not a server movement receipt or Sarvis observation.
func (s *Service) ExitCrypt(ctx context.Context, r *actionsv1.ExitCryptRequest) (*actionsv1.State, error) {
	return s.exitCrypt(ctx, r, planCryptExit)
}

func (s *Service) exitCrypt(ctx context.Context, r *actionsv1.ExitCryptRequest, planner cryptExitPlanner) (result *actionsv1.State, err error) {
	if r == nil || r.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "owned quests token is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	s.expireInteractionLocked()
	if !s.independent || !s.ownsCryptExit(r) || s.questApproaching || s.state.Moving || s.interactionUntil.Sub(s.now()) < 26*time.Second {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "independent idle character and exclusive quests lease with at least26s remaining are required")
	}
	s.questApproaching, s.state.Traveling = true, true
	s.state.TravelNpcGuid = 0
	s.state.Reason = "crypt_exit_planning_verified_route"
	s.mu.Unlock()
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		defer cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.ownsCryptExit(r) {
			s.stopLocked(stop)
		}
		s.questApproaching, s.state.Traveling = false, false
		result = s.stateLocked()
	}()
	call, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	s.mu.Lock()
	if !s.ownsCryptExit(r) {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "quests interaction lease changed")
	}
	observe, done := context.WithTimeout(call, 500*time.Millisecond)
	why := s.cryptExitContextLocked(observe, r)
	done()
	if why == "" && s.state.Reason != "crypt_exit_planning_verified_route" {
		why = "crypt_exit_interrupted_by_controller_change"
	}
	if why != "" || s.navDataDir == "" || planner == nil {
		if why == "" {
			why = "crypt_exit_navigation_unavailable"
		}
		s.state.Reason = why
		s.mu.Unlock()
		return nil, nil
	}
	from, to, revision, root := s.pos, cryptExitLandmark(), s.positionRevision, s.navDataDir
	s.mu.Unlock()
	plan, finishPlan := context.WithTimeout(call, 250*time.Millisecond)
	path, planErr := planner(plan, root, from, to)
	if planErr == nil {
		planErr = plan.Err()
	}
	finishPlan()
	if planErr == nil && !validCryptExitPath(path, from, to) {
		planErr = errors.New("crypt route failed endpoint, region, sample or length validation")
	}
	expected, moving, phase, index := from, false, "crypt_exit_planning_verified_route", 0
	for step := 0; step < 150; step++ {
		s.mu.Lock()
		if !s.ownsCryptExit(r) {
			s.mu.Unlock()
			return nil, status.Error(codes.FailedPrecondition, "quests interaction lease changed")
		}
		if err := call.Err(); err != nil {
			s.state.Reason = "crypt_exit_canceled_or_time_limit"
			s.mu.Unlock()
			return nil, status.FromContextError(err).Err()
		}
		iteration, finish := context.WithTimeout(call, 500*time.Millisecond)
		why = s.cryptExitContextLocked(iteration, r)
		if why == "" && (s.positionRevision != revision || distance(s.pos, expected) > 0.05) {
			why = "crypt_exit_position_changed"
		}
		if why == "" && (s.state.Moving != moving || s.state.Reason != phase) {
			why = "crypt_exit_interrupted_by_controller_change"
		}
		if why == "" && planErr != nil {
			s.state.NavAvailable, s.state.NavReason = false, navigationDiagnostic(planErr, root)
			why = "crypt_exit_navigation_unavailable"
		}
		if why == "" && iteration.Err() != nil {
			why = "crypt_exit_canceled_or_time_limit"
		}
		if why == "" && index >= len(path) {
			why = "crypt_exit_route_completed_client_estimate_requires_observation"
		}
		if why != "" {
			s.state.Reason = why
			finish()
			s.mu.Unlock()
			return nil, nil
		}
		s.state.NavAvailable, s.state.NavReason = true, "deathknell_crypt_physical_route_verified"
		if !s.navigationStepWithinLocked(iteration, path, &index, deathknellCrypt) {
			s.state.Reason = "crypt_exit_step_or_transport_failed"
			finish()
			s.mu.Unlock()
			return nil, nil
		}
		finish()
		if routeRemaining(s.pos, path, index) <= 0.05 && distance(s.pos, path[len(path)-1]) <= 0.05 {
			s.state.Reason = "crypt_exit_route_completed_client_estimate_requires_observation"
			s.mu.Unlock()
			return nil, nil
		}
		s.state.Reason = "crypt_exit_following_verified_route_client_estimate"
		expected, moving, phase = s.pos, s.state.Moving, s.state.Reason
		s.mu.Unlock()
		if err := s.approachDelay(call, 200*time.Millisecond); err != nil {
			return nil, status.FromContextError(err).Err()
		}
	}
	s.mu.Lock()
	if s.ownsCryptExit(r) {
		s.state.Reason = "crypt_exit_step_limit"
	}
	s.mu.Unlock()
	return nil, nil
}
