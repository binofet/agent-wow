package buddyactions

import (
	"context"
	"crypto/subtle"
	"errors"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/harvestguard"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (s *Service) ownsExploration(token string) bool {
	return !s.closed && s.state.InteractionOwner == "exploration" && subtle.ConstantTimeCompare([]byte(token), []byte(s.interactionToken)) == 1
}

type flightmasterApproachKey struct{}

// Idle-only leases remain bound to the body/visit which admitted them. Legacy
// acquisition has no such binding and keeps its existing behavior.
func (s *Service) explorationIdleBindingCurrentLocked(token string, w *worldv1.SnapshotResponse) bool {
	b := s.idleInteraction
	if b.sessionID == "" && b.selfGUID == 0 && b.visit == 0 && b.token == "" {
		return true
	}
	return b.sessionID == s.sessionID && b.selfGUID == s.selfGUID && b.visit != 0 && b.token == token &&
		s.encounterVisit.bound && s.encounterVisit.revision == b.visit &&
		(w == nil || s.matchesCurrentEncounterVisitLocked(w) && w.VisitRevision == b.visit)
}

func (s *Service) explorationContextLocked(ctx context.Context, token string, mapID uint32) string {
	if !s.ownsExploration(token) || s.interactionUntil.Sub(s.now()) < time.Second {
		return "exploration_lease_changed_or_expiring"
	}
	if !s.explorationIdleBindingCurrentLocked(token, nil) {
		return "exploration_idle_body_or_visit_changed"
	}
	if s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed || s.autoTarget != 0 || s.state.AutoAttacking {
		return "exploration_motion_or_spell_unconfirmed"
	}
	if ctx.Err() != nil {
		return "exploration_canceled_or_time_limit"
	}
	if s.state.InteractionHarvest {
		_, guids, reason := s.harvestContextLocked(ctx)
		if reason != "" {
			s.revokeHarvestLocked(ctx, reason)
			return reason
		}
		s.state.HarvestGuardGuids, s.harvestGuardAt = guids, s.now()
	}
	target, _ := ctx.Value(flightmasterApproachKey{}).(*actionsv1.FlightmasterTarget)
	w, err := s.snapshot(ctx, target.GetGuid())
	if err != nil {
		return "exploration_observations_unavailable"
	}
	if s.state.InteractionHarvest {
		if reason := harvestguard.WorldReason(w, s.selfGUID, s.state.HarvestGuardGuids); reason != "" {
			s.revokeHarvestLocked(ctx, reason)
			return reason
		}
	}
	d, _ := ctx.Value(destinationOwnerKey{}).(destinationOwner)
	if reason := safeQuestCharacterFlags(w, s.selfGUID, func(f, x uint32) bool { return destinationSelfMode(w, f, x) || d.gravity.current(w) }, s.groundTravelUnitFlags(w.Self)); reason != "" {
		return "exploration_" + reason
	}
	if w.Self.MapId != mapID {
		return "exploration_map_changed_or_different"
	}
	if !s.refreshPositionLocked(w) {
		return "exploration_authoritative_position_changed"
	}
	if observedRunSpeed(w.Self) == 0 {
		return "exploration_observed_run_speed_required"
	}
	if !s.explorationIdleBindingCurrentLocked(token, w) {
		return "exploration_idle_body_or_visit_changed"
	}
	if target != nil && (!movementflags.GroundCompatible(w.Self.MovementFlags, w.Self.ExtraMovementFlags) || !s.currentFlightmasterLocked(w, target)) {
		return "exploration_flightmaster_or_owner_intent_changed"
	}
	return ""
}

// TravelTo permits normal movement toward a finite point, never interaction or
// an assertion that a remembered NPC exists there. Only the complete verified
// route is used; no caller path, raw packets, teleport or direct fallback exists.
func (s *Service) TravelTo(ctx context.Context, r *actionsv1.TravelToRequest) (result *actionsv1.State, err error) {
	ctx = modcb.ForwardPolicySpell(ctx)
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
	if r.Flightmaster != nil {
		r = proto.Clone(r).(*actionsv1.TravelToRequest)
		if r.Flightmaster.Guid>>48 != 0xf130 || r.Flightmaster.PositionRevision == 0 {
			return nil, status.Error(codes.InvalidArgument, "exact observed flightmaster position required")
		}
		ctx = context.WithValue(ctx, flightmasterApproachKey{}, r.Flightmaster)
	}
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
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 750*time.Millisecond)
		defer cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.ownsExploration(token) && s.explorationIdleBindingCurrentLocked(token, nil) {
			if s.state.InteractionHarvest {
				s.stopHarvestMovementLocked(stop)
			} else {
				s.stopLocked(stop)
			}
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
	if s.idleInteraction.token != "" {
		scope, err := s.newIdleMotionScopeLocked(call, token, s.lastWorld, false)
		if err != nil {
			s.state.Reason = "exploration_idle_scope_unavailable"
			s.mu.Unlock()
			return nil, nil
		}
		ctx = context.WithValue(ctx, idleMotionScopeKey{}, scope)
		call = context.WithValue(call, idleMotionScopeKey{}, scope)
	}
	from, revision, root := s.pos, s.positionRevision, s.navDataDir
	swimming, allowWater := s.swimming, r.Flightmaster == nil && !s.state.InteractionHarvest && root != "" && s.lastWorld.GetSelf().GetUnitFlags()&movementflags.Mounted == 0 && waterMovementAllowed(s.lastWorld)
	if r.Flightmaster != nil {
		npc := s.lastWorld.GetRequestedCreature()
		if s.idleInteraction.token != token || swimming || distance(from, to) > 16 || npc.GetGuid() != r.Flightmaster.Guid ||
			distance(to, position{npc.X, npc.Y, npc.Z, npc.Orientation}) > .05 {
			s.state.Reason = "exploration_flightmaster_ground_approach_unavailable"
			s.mu.Unlock()
			return nil, nil
		}
	}
	// Navmesh geometry is speed-independent. Retain the observed speed across
	// its unlocked planning interval; every following checkpoint refreshes it.
	intervalSpeed := observedRunSpeed(s.lastWorld.GetSelf())
	gravity := destinationGravitySource(s.lastWorld, s.pos)
	scope := s.travelMapLocked(mapID)
	open := s.openTravel
	ground := r.Flightmaster == nil && !swimming && !gravity.falling && movementflags.GroundCompatible(s.lastWorld.GetSelf().GetMovementFlags(), s.lastWorld.GetSelf().GetExtraMovementFlags())
	cached, preview := s.travelCandidateLocked(scope, from, to, true)
	if distance(from, to) <= arrival {
		s.state.Reason = "exploration_within_distance"
		s.mu.Unlock()
		return nil, nil
	}
	s.mu.Unlock()
	var mixed *swimFollowRoute
	var strip *travelStrip
	var path []position
	var planErr error
	if ground {
		if preview != nil {
			strip, path = cached, preview
		} else {
			strip, path, planErr = prepareTravelStrip(call, open, scope, from, to, cached)
		}
	} else if r.Flightmaster != nil {
		path, planErr = s.mapNavigationPath(call, root, mapID, from, to)
	} else {
		planErr = errors.New("current body requires a measured surface or landing route")
	}
	if allowWater && (planErr != nil || swimming) && call.Err() == nil {
		r, e := s.planMixedDestination(call, root, mapID, from, to, intervalSpeed, swimming, gravity, nil)
		if e == nil {
			r.owner, r.revision = destinationOwner{token: token, target: to, gravity: r.owner.gravity}, revision
			mixed, path, planErr, strip = &r, r.path, nil, nil
		} else if swimming {
			planErr = e
		}
	}
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
	if r.Flightmaster != nil && routeRemaining(from, path, 0) > 20 {
		s.state.Reason = "exploration_flightmaster_route_too_long"
		s.mu.Unlock()
		return nil, nil
	}
	// The planner can normalize the destination onto validated ground. A
	// precise request must fit that actual endpoint; never append caller Z or
	// bypass the physical route to manufacture a closer arrival.
	endpoint := path[len(path)-1]
	if strip != nil && preview == nil {
		strip.mu.Lock()
		endpoint = navPosition(strip.surface.Destination())
		strip.mu.Unlock()
	}
	if mixed != nil && mixed.drop != nil {
		terminal := mixed.drop
		for terminal.Next != nil {
			terminal = terminal.Next
		}
		endpoint = navPosition(terminal.Onward[len(terminal.Onward)-1].Point)
	}
	if distance(endpoint, to) > arrival {
		s.state.NavAvailable = false
		s.state.NavReason = "verified_route_endpoint_outside_requested_arrival_distance"
		s.state.Reason = "exploration_navigation_endpoint_outside_arrival_distance"
		s.mu.Unlock()
		return nil, nil
	}
	if mixed != nil {
		call = context.WithValue(call, destinationOwnerKey{}, mixed.owner)
	}
	if mixed != nil && s.idleInteraction.token != "" {
		scoped := context.WithValue(call, destinationOwnerKey{}, mixed.owner)
		motion, e := s.newBodyMotionScopeLocked(scoped, token, s.lastWorld, false, "destination")
		if e != nil {
			s.state.Reason = "exploration_idle_scope_unavailable"
			s.mu.Unlock()
			return nil, nil
		}
		call = context.WithValue(scoped, idleMotionScopeKey{}, motion)
		ctx = context.WithValue(ctx, idleMotionScopeKey{}, motion)
	}
	if strip != nil {
		// A read-issued local endpoint ends this request; a direct goal may
		// need successive prefixes. Both use their originally searched strip.
		call = context.WithValue(call, travelGroundStrideKey{}, scope)
		if preview == nil {
			s.travelPreview = travelPreview{strip: strip}
		}
	}
	s.state.NavAvailable, s.state.NavReason = true, "client_navmesh_route_verified"
	s.mu.Unlock()
	expected, index := from, 0
	for step := 0; strip != nil || step < 150; step++ {
		s.mu.Lock()
		if !s.ownsExploration(token) {
			s.mu.Unlock()
			return nil, nil
		}
		why := s.explorationContextLocked(call, token, mapID)
		if why == "" && (s.state.Reason != phase || s.state.Moving != moving) {
			why = "exploration_interrupted_by_controller_change"
		}
		if why == "" && (s.travelMapLocked(mapID) != scope || s.posMap != mapID || s.positionRevision != revision || distance(s.pos, expected) > .05) {
			why = "exploration_position_changed_after_planning"
		}
		// Hand control back to the everyday policy after a short ordinary
		// ground input. The next leg requires another observed target and
		// owner decision. The existing deferred STOP retires this input.
		if why == "" && r.Flightmaster != nil && step > 0 && (distance(from, s.pos) >= 1.5 || step >= 3) {
			why = "exploration_flightmaster_checkpoint_reached"
		}
		if why == "" && strip != nil && preview == nil && s.interactionUntil.Sub(s.now()) <= 1500*time.Millisecond &&
			distance(s.pos, from) > 1 && distance(s.pos, to) > arrival {
			why = "exploration_ground_progress_checkpoint"
		}
		terminal := strip == nil || preview != nil || distance(path[len(path)-1], endpoint) <= .05
		if why == "" && !terminal && index >= len(path) {
			// Release the movement lock during geometry. The next iteration
			// rechecks lease, control, visit, source revision and actual pose.
			progressAt := s.pos
			s.mu.Unlock()
			next, err := strip.next(call, progressAt)
			if err != nil {
				s.mu.Lock()
				if s.ownsExploration(token) && s.state.Reason == phase {
					s.state.NavAvailable = false
					s.state.NavReason = navigationDiagnostic(err, root)
					s.state.Reason = "exploration_navigation_unavailable"
				}
				s.mu.Unlock()
				return nil, nil
			}
			path, index = next, 0
			continue
		}
		if why == "" && terminal && (mixed == nil || mixed.drop == nil) && distance(s.pos, to) <= arrival && routeRemaining(s.pos, path, index) <= arrival {
			why = "exploration_within_distance"
		}
		if why == "" && (mixed == nil || mixed.drop == nil) && index >= len(path) {
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
		speed := observedRunSpeed(s.lastWorld.GetSelf())
		// A speed-change packet does not timestamp its exact position within
		// the elapsed interval. Never apply a newly faster speed retroactively.
		intervalSpeed = math.Min(intervalSpeed, speed)
		before := s.posAt
		var advanced bool
		var stepReason string
		if mixed == nil {
			advanced, stepReason = s.navigationStepResultLocked(call, path, &index, finite, intervalSpeed, true)
		} else {
			advanced = s.advanceDestinationLocked(call, mixed, s.now().Sub(s.posAt).Seconds())
			path, index, stepReason = mixed.path, mixed.index, s.state.Reason
		}
		if !advanced {
			s.state.Reason = "exploration_" + stepReason
			s.mu.Unlock()
			return nil, nil
		}
		if !s.posAt.Equal(before) {
			intervalSpeed = speed
		}
		expected = s.pos
		if terminal && (mixed == nil || mixed.drop == nil) && distance(s.pos, to) <= arrival && routeRemaining(s.pos, path, index) <= arrival {
			s.state.Reason = "exploration_within_distance"
			s.mu.Unlock()
			return nil, nil
		}
		s.state.Reason = "exploration_following_client_navmesh_estimate"
		if stepReason == "checkpoint_delay_bounded" {
			s.state.Reason = "exploration_checkpoint_delay_bounded"
		}
		moving, phase = s.state.Moving, s.state.Reason
		delay := explorationCheckpointDelay(speed)
		if strip != nil {
			delay = travelCheckpointDelay(s.pos, path, index, speed)
		}
		s.mu.Unlock()
		if err := s.approachDelay(call, delay); err != nil {
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

// Preserve the ordinary ~1.4-yard checkpoint spacing while respecting the
// shared movement actuator's150ms minimum and the existing200ms read cadence.
// This schedules observed speed; it never writes a speed or grants movement.
func explorationCheckpointDelay(speed float64) time.Duration {
	delay := time.Duration(1.4 / speed * float64(time.Second))
	if delay < 150*time.Millisecond {
		return 150 * time.Millisecond
	}
	if delay > 200*time.Millisecond {
		return 200 * time.Millisecond
	}
	return delay
}
