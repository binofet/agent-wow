package buddyactions

import (
	"context"
	"crypto/subtle"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) ownsExploration(token string) bool {
	return !s.closed && s.state.InteractionOwner == "exploration" && subtle.ConstantTimeCompare([]byte(token), []byte(s.interactionToken)) == 1
}

func (s *Service) explorationContextLocked(ctx context.Context, token string, mapID uint32) string {
	if !s.ownsExploration(token) || s.interactionUntil.Sub(s.now()) < time.Second {
		return "exploration_lease_changed_or_expiring"
	}
	if s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed || s.autoTarget != 0 || s.state.AutoAttacking {
		return "exploration_motion_or_spell_unconfirmed"
	}
	if ctx.Err() != nil {
		return "exploration_canceled_or_time_limit"
	}
	w, err := s.snapshot(ctx)
	if err != nil {
		return "exploration_observations_unavailable"
	}
	if reason := safeQuestCharacter(w, s.selfGUID); reason != "" {
		return "exploration_" + reason
	}
	if w.Self.MapId != mapID {
		return "exploration_map_changed_or_different"
	}
	if !s.refreshPositionLocked(w) {
		return "exploration_authoritative_position_changed"
	}
	if !w.Self.SpeedsKnown || math.IsNaN(float64(w.Self.RunSpeed)) || math.Abs(float64(w.Self.RunSpeed)-7) > .01 {
		return "exploration_normal_run_speed_required"
	}
	return ""
}

// TravelTo permits normal movement toward a finite point, never interaction or
// an assertion that a remembered NPC exists there. Only the complete verified
// route is used; no caller path, raw packets, teleport or direct fallback exists.
func (s *Service) TravelTo(ctx context.Context, r *actionsv1.TravelToRequest) (result *actionsv1.State, err error) {
	if r == nil || r.Token == "" || len(r.Token) > 128 || !finite(position{x: r.X, y: r.Y, z: r.Z}) {
		return nil, status.Error(codes.InvalidArgument, "exploration token and finite destination are required")
	}
	arrival := float64(r.ArrivalDistance)
	if math.IsNaN(arrival) || math.IsInf(arrival, 0) || arrival != 0 && (arrival < .5 || arrival > 3) {
		return nil, status.Error(codes.InvalidArgument, "arrival distance must be0(default3) or0.5..3yards")
	}
	if arrival == 0 {
		arrival = 3
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	// Capture the movement-only request once. Concurrent caller mutation cannot
	// replace the destination, map or lease token during a route.
	token, mapID, to := r.Token, r.MapId, position{x: r.X, y: r.Y, z: r.Z}
	s.mu.Lock()
	s.expireInteractionLocked()
	if !s.ownsExploration(token) || s.questApproaching {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "exclusive owned exploration lease is required")
	}
	s.questApproaching, s.state.Traveling, s.state.TravelNpcGuid = true, true, 0
	phase, moving := "exploration_planning_client_navmesh", s.state.Moving
	s.state.Reason = phase
	s.mu.Unlock()
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		defer cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.ownsExploration(token) {
			s.stopLocked(stop)
		}
		if s.ownsExploration(token) || s.state.InteractionOwner == "" {
			s.state.Traveling = false
		}
		s.questApproaching = false
		result = s.stateLocked()
	}()
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.mu.Lock()
	if !s.ownsExploration(token) {
		s.mu.Unlock()
		return nil, nil
	}
	reason := s.explorationContextLocked(call, token, mapID)
	if reason == "" && (s.state.Reason != phase || s.state.Moving != moving) {
		reason = "exploration_interrupted_by_controller_change"
	}
	if reason != "" {
		s.state.Reason = reason
		s.mu.Unlock()
		if call.Err() != nil {
			return nil, status.FromContextError(call.Err()).Err()
		}
		return nil, nil
	}
	from, revision, root := s.pos, s.positionRevision, s.navDataDir
	if distance(from, to) > 200 {
		s.state.Reason = "exploration_destination_exceeds_200_yards"
		s.mu.Unlock()
		return nil, status.Error(codes.InvalidArgument, "exploration destination must be within200yards")
	}
	if distance(from, to) <= arrival {
		s.state.Reason = "exploration_within_distance"
		s.mu.Unlock()
		return nil, nil
	}
	s.mu.Unlock()
	path, planErr := s.mapNavigationPath(call, root, mapID, from, to)
	s.mu.Lock()
	// Planning runs without the controller lock. Neither a revoked token nor
	// an accepted explicit stop may publish a plan or restart movement.
	if !s.ownsExploration(token) {
		s.mu.Unlock()
		return nil, nil
	}
	if s.state.Reason != phase || s.state.Moving != moving {
		s.state.Reason = "exploration_interrupted_by_controller_change"
		s.mu.Unlock()
		return nil, nil
	}
	if call.Err() != nil {
		s.state.Reason = "exploration_canceled_or_time_limit"
		s.mu.Unlock()
		return nil, status.FromContextError(call.Err()).Err()
	}
	if planErr != nil {
		s.state.NavAvailable, s.state.NavReason, s.state.Reason = false, navigationDiagnostic(planErr, root), "exploration_navigation_unavailable"
		s.mu.Unlock()
		if call.Err() != nil {
			return nil, status.FromContextError(call.Err()).Err()
		}
		return nil, nil
	}
	// The planner can normalize the destination onto validated ground. A
	// precise request must fit that actual endpoint; never append caller Z or
	// bypass the physical route to manufacture a closer arrival.
	if distance(path[len(path)-1], to) > arrival {
		s.state.NavAvailable = false
		s.state.NavReason = "verified_route_endpoint_outside_requested_arrival_distance"
		s.state.Reason = "exploration_navigation_endpoint_outside_arrival_distance"
		s.mu.Unlock()
		return nil, nil
	}
	s.state.NavAvailable, s.state.NavReason = true, "client_navmesh_route_verified"
	s.mu.Unlock()
	expected, index := from, 0
	for step := 0; step < 150; step++ {
		s.mu.Lock()
		if !s.ownsExploration(token) {
			s.mu.Unlock()
			return nil, nil
		}
		why := s.explorationContextLocked(call, token, mapID)
		if why == "" && (s.state.Reason != phase || s.state.Moving != moving) {
			why = "exploration_interrupted_by_controller_change"
		}
		if why == "" && (s.posMap != mapID || s.positionRevision != revision || distance(s.pos, expected) > .05) {
			why = "exploration_position_changed_after_planning"
		}
		if why == "" && distance(s.pos, to) <= arrival && routeRemaining(s.pos, path, index) <= arrival {
			why = "exploration_within_distance"
		}
		if why == "" && index >= len(path) {
			why = "exploration_route_ended_outside_range"
		}
		if why != "" {
			s.state.Reason = why
			s.mu.Unlock()
			if call.Err() != nil {
				return nil, status.FromContextError(call.Err()).Err()
			}
			return nil, nil
		}
		if !s.mapNavigationStepLocked(call, path, &index) {
			s.state.Reason = "exploration_step_or_transport_failed"
			s.mu.Unlock()
			return nil, nil
		}
		expected = s.pos
		if distance(s.pos, to) <= arrival && routeRemaining(s.pos, path, index) <= arrival {
			s.state.Reason = "exploration_within_distance"
			s.mu.Unlock()
			return nil, nil
		}
		s.state.Reason = "exploration_following_client_navmesh_estimate"
		moving, phase = s.state.Moving, s.state.Reason
		s.mu.Unlock()
		if err := s.approachDelay(call, 200*time.Millisecond); err != nil {
			s.mu.Lock()
			if s.ownsExploration(token) {
				s.state.Reason = "exploration_canceled_or_time_limit"
			}
			s.mu.Unlock()
			return nil, status.FromContextError(err).Err()
		}
	}
	s.mu.Lock()
	if s.ownsExploration(token) {
		s.state.Reason = "exploration_step_limit"
	}
	s.mu.Unlock()
	return nil, nil
}
