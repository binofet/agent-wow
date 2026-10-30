package buddyactions

import (
	"context"
	"crypto/subtle"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) ownsVendorTravel(r *actionsv1.ApproachQuestRequest) bool {
	return !s.closed && s.state.InteractionOwner == "trade" && subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) == 1
}

// Only an actual observed vendor supplies a destination. There are no vendor
// landmarks or caller coordinates, and no direct-movement fallback on NAV failure.
func (s *Service) vendorTravelContextLocked(ctx context.Context, r *actionsv1.ApproachQuestRequest) (*questTravelTarget, string) {
	if !s.ownsVendorTravel(r) || s.interactionUntil.Sub(s.now()) < time.Second {
		return nil, "vendor_travel_lease_changed_or_expiring"
	}
	if ctx.Err() != nil {
		return nil, "vendor_travel_canceled_or_time_limit"
	}
	if s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed || s.autoTarget != 0 || s.state.AutoAttacking {
		return nil, "vendor_travel_motion_or_spell_unconfirmed"
	}
	w, err := s.snapshot(ctx, r.NpcGuid)
	if err != nil {
		return nil, "vendor_travel_observations_unavailable"
	}
	if reason := safeQuestCharacter(w, s.selfGUID); reason != "" {
		return nil, "vendor_travel_" + reason
	}
	if !s.refreshPositionLocked(w) {
		return nil, "vendor_travel_authoritative_position_changed"
	}
	if !w.Self.SpeedsKnown || math.Abs(float64(w.Self.RunSpeed)-7) > 0.01 || math.IsNaN(float64(w.Self.RunSpeed)) {
		return nil, "vendor_travel_supported_normal_ground_required"
	}
	if len(w.QuestGivers) > 64 || len(w.NearbyCreatures) > 32 || len(w.LootableCorpses) > 64 {
		return nil, "vendor_travel_observations_exceed_bounds"
	}
	entry := uint32(r.NpcGuid >> 24 & 0xffffff)
	views := [][]*worldv1.CreatureState{{w.RequestedCreature}, w.NearbyCreatures, w.QuestGivers, w.LootableCorpses, {w.OwnerTarget}}
	var target *questTravelTarget
	for _, view := range views {
		for _, c := range view {
			if c == nil || c.Guid != r.NpcGuid {
				continue
			}
			p := position{c.X, c.Y, c.Z, c.Orientation}
			if c.Entry != entry || !c.Present || !c.MapKnown || c.MapId != w.Self.MapId || !c.PositionKnown || !c.AliveKnown || !c.Alive || !c.HealthKnown || c.Health == 0 || c.NpcFlags&0x80 == 0 || c.UnitFlags&0x80000 != 0 || c.MovementFlags&0x080000ff != 0 || c.Trajectory != nil && c.Trajectory.Known && (!c.Trajectory.Stopped || c.Trajectory.TransportRelative) || !finite(p) {
				return nil, "vendor_travel_observed_stationary_vendor_required"
			}
			if target != nil && (target.position != p || target.revision != c.PositionRevision) {
				return nil, "vendor_travel_observed_vendor_views_disagree"
			}
			target = &questTravelTarget{position: p, revision: c.PositionRevision}
		}
	}
	if target == nil {
		return nil, "vendor_travel_actual_observed_vendor_required"
	}
	return target, ""
}

// TravelVendor pauses other controls only under an exclusive trade lease.
// Every step uses a verified route on the observed map and normal run physics;
// completion reports proximity, never a sale or purchase.
func (s *Service) TravelVendor(ctx context.Context, r *actionsv1.ApproachQuestRequest) (result *actionsv1.State, err error) {
	if r == nil || r.Token == "" || r.NpcGuid>>48 != 0xf130 || r.NpcGuid&0xffffff == 0 {
		return nil, status.Error(codes.InvalidArgument, "trade token and observed creature vendor GUID required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	r = &actionsv1.ApproachQuestRequest{Token: r.Token, NpcGuid: r.NpcGuid}
	s.mu.Lock()
	s.expireInteractionLocked()
	if !s.ownsVendorTravel(r) || s.questApproaching {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "exclusive owned trade lease is required")
	}
	s.questApproaching, s.state.Traveling, s.state.TravelNpcGuid = true, true, r.NpcGuid
	phase, moving := "vendor_travel_planning_client_navmesh", s.state.Moving
	s.state.Reason = phase
	s.mu.Unlock()
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		defer cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.ownsVendorTravel(r) {
			s.stopLocked(stop)
		}
		if s.ownsVendorTravel(r) || s.state.InteractionOwner == "" {
			s.state.Traveling = false
		}
		s.questApproaching = false
		result = s.stateLocked()
	}()
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.mu.Lock()
	if !s.ownsVendorTravel(r) {
		s.mu.Unlock()
		if call.Err() != nil {
			return nil, status.FromContextError(call.Err()).Err()
		}
		return nil, nil
	}
	var target *questTravelTarget
	reason := ""
	if s.state.Reason != phase || s.state.Moving != moving {
		reason = "vendor_travel_interrupted_by_controller_change"
	} else {
		target, reason = s.vendorTravelContextLocked(call, r)
	}
	if reason != "" {
		s.state.Reason = reason
		s.mu.Unlock()
		if call.Err() != nil {
			return nil, status.FromContextError(call.Err()).Err()
		}
		return nil, nil
	}
	from, to, revision, baseRevision, root, mapID := s.pos, target.position, target.revision, s.positionRevision, s.navDataDir, s.posMap
	if distance(from, to) <= 3 {
		s.state.Reason = "vendor_travel_within_distance"
		s.mu.Unlock()
		return nil, nil
	}
	s.state.Reason = "vendor_travel_planning_client_navmesh"
	s.mu.Unlock()
	path, planErr := s.interactionNavigationPath(call, root, mapID, from, to)
	s.mu.Lock()
	// A planner runs unlocked; a revoked lease or explicit stop must not
	// publish old diagnostics or restart a movement frame that is already idle.
	if !s.ownsVendorTravel(r) {
		s.mu.Unlock()
		if call.Err() != nil {
			return nil, status.FromContextError(call.Err()).Err()
		}
		return nil, nil
	}
	if call.Err() != nil {
		s.state.Reason = "vendor_travel_canceled_or_time_limit"
		s.mu.Unlock()
		return nil, status.FromContextError(call.Err()).Err()
	}
	if s.state.Reason != phase || s.state.Moving != moving {
		s.state.Reason = "vendor_travel_interrupted_by_controller_change"
		s.mu.Unlock()
		return nil, nil
	}
	if planErr != nil {
		s.state.NavAvailable, s.state.NavReason, s.state.Reason = false, navigationDiagnostic(planErr, root), "vendor_travel_navigation_unavailable"
		s.mu.Unlock()
		return nil, nil
	}
	s.state.NavAvailable, s.state.NavReason = true, "client_navmesh_route_verified"
	s.mu.Unlock()
	expected, index := from, 0
	for step := 0; step < 150; step++ {
		s.mu.Lock()
		if !s.ownsVendorTravel(r) {
			s.mu.Unlock()
			if call.Err() != nil {
				return nil, status.FromContextError(call.Err()).Err()
			}
			return nil, nil
		}
		var current *questTravelTarget
		why := ""
		if s.state.Reason != phase || s.state.Moving != moving {
			why = "vendor_travel_interrupted_by_controller_change"
		} else {
			current, why = s.vendorTravelContextLocked(call, r)
		}
		if why == "" && s.posMap != mapID {
			why = "vendor_travel_map_changed"
		}
		if why == "" && (s.positionRevision != baseRevision || distance(s.pos, expected) > 0.05) {
			why = "vendor_travel_position_changed_during_planning"
		}
		if why == "" && (current.revision != revision || current.position != to) {
			why = "vendor_travel_vendor_position_changed"
		}
		if why == "" && distance(s.pos, to) <= 3 && routeRemaining(s.pos, path, index) <= 3 {
			why = "vendor_travel_within_distance"
		}
		if why == "" && index >= len(path) {
			why = "vendor_travel_route_ended_outside_range"
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
			s.state.Reason = "vendor_travel_step_or_transport_failed"
			if call.Err() != nil {
				s.state.Reason = "vendor_travel_canceled_or_time_limit"
			}
			s.mu.Unlock()
			if call.Err() != nil {
				return nil, status.FromContextError(call.Err()).Err()
			}
			return nil, nil
		}
		expected = s.pos
		if distance(s.pos, to) <= 3 && routeRemaining(s.pos, path, index) <= 3 {
			s.state.Reason = "vendor_travel_within_distance"
			s.mu.Unlock()
			return nil, nil
		}
		s.state.Reason = "vendor_travel_following_client_navmesh_estimate"
		moving, phase = s.state.Moving, s.state.Reason
		s.mu.Unlock()
		if err := s.approachDelay(call, 200*time.Millisecond); err != nil {
			s.mu.Lock()
			if s.ownsVendorTravel(r) {
				s.state.Reason = "vendor_travel_canceled_or_time_limit"
			}
			s.mu.Unlock()
			return nil, status.FromContextError(err).Err()
		}
	}
	s.mu.Lock()
	if s.ownsVendorTravel(r) {
		s.state.Reason = "vendor_travel_step_limit"
	}
	s.mu.Unlock()
	return nil, nil
}
