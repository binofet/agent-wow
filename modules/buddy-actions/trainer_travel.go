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

func (s *Service) ownsTrainerTravel(r *actionsv1.ApproachQuestRequest) bool {
	return !s.closed && s.state.InteractionOwner == "training" && subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) == 1
}

// Only an actual observed trainer supplies a destination. There are no trainer
// landmarks or caller coordinates, and no direct-movement fallback on NAV failure.
func (s *Service) trainerTravelContextLocked(ctx context.Context, r *actionsv1.ApproachQuestRequest) (*questTravelTarget, string) {
	if !s.ownsTrainerTravel(r) || s.interactionUntil.Sub(s.now()) < time.Second {
		return nil, "trainer_travel_lease_changed_or_expiring"
	}
	if s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed || s.autoTarget != 0 || s.state.AutoAttacking {
		return nil, "trainer_travel_motion_or_spell_unconfirmed"
	}
	w, err := s.snapshot(ctx, r.NpcGuid)
	if err != nil {
		return nil, "trainer_travel_observations_unavailable"
	}
	if reason := safeQuestCharacter(w, s.selfGUID); reason != "" {
		return nil, "trainer_travel_" + reason
	}
	if !s.refreshPositionLocked(w) {
		return nil, "trainer_travel_authoritative_position_changed"
	}
	if !w.Self.SpeedsKnown || math.Abs(float64(w.Self.RunSpeed)-7) > 0.01 || math.IsNaN(float64(w.Self.RunSpeed)) {
		return nil, "trainer_travel_supported_normal_ground_required"
	}
	if len(w.QuestGivers) > 64 || len(w.NearbyCreatures) > 32 || len(w.LootableCorpses) > 64 {
		return nil, "trainer_travel_observations_exceed_bounds"
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
			if c.Entry != entry || !c.Present || !c.MapKnown || c.MapId != w.Self.MapId || !c.PositionKnown || !c.AliveKnown || !c.Alive || !c.HealthKnown || c.Health == 0 || c.NpcFlags&0x50 != 0x50 || c.UnitFlags&0x80000 != 0 || c.MovementFlags&0x080000ff != 0 || c.Trajectory != nil && c.Trajectory.Known && (!c.Trajectory.Stopped || c.Trajectory.TransportRelative) || !finite(p) {
				return nil, "trainer_travel_observed_stationary_profession_trainer_required"
			}
			if target != nil && (target.position != p || target.revision != c.PositionRevision) {
				return nil, "trainer_travel_observed_trainer_views_disagree"
			}
			target = &questTravelTarget{position: p, revision: c.PositionRevision}
		}
	}
	if target == nil {
		return nil, "trainer_travel_actual_observed_trainer_required"
	}
	return target, ""
}

// TravelTrainer pauses other controls only under an exclusive training lease.
// Every step uses a verified route on the observed map and normal run physics;
// completion reports proximity, never a trainer purchase or learned profession.
func (s *Service) TravelTrainer(ctx context.Context, r *actionsv1.ApproachQuestRequest) (result *actionsv1.State, err error) {
	if r == nil || r.Token == "" || r.NpcGuid>>48 != 0xf130 || r.NpcGuid&0xffffff == 0 {
		return nil, status.Error(codes.InvalidArgument, "training token and observed creature trainer GUID required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	s.expireInteractionLocked()
	if !s.ownsTrainerTravel(r) || s.questApproaching {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "exclusive owned training lease is required")
	}
	s.questApproaching, s.state.Traveling, s.state.TravelNpcGuid = true, true, r.NpcGuid
	s.mu.Unlock()
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		defer cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.ownsTrainerTravel(r) {
			s.stopLocked(stop)
		}
		s.questApproaching, s.state.Traveling = false, false
		result = s.stateLocked()
	}()
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.mu.Lock()
	target, reason := s.trainerTravelContextLocked(call, r)
	if reason != "" {
		s.state.Reason = reason
		s.mu.Unlock()
		return nil, nil
	}
	from, to, revision, baseRevision, root, mapID := s.pos, target.position, target.revision, s.positionRevision, s.navDataDir, s.posMap
	if distance(from, to) <= 3 {
		s.state.Reason = "trainer_travel_within_distance"
		s.mu.Unlock()
		return nil, nil
	}
	s.state.Reason = "trainer_travel_planning_client_navmesh"
	s.mu.Unlock()
	path, planErr := s.mapNavigationPath(call, root, mapID, from, to)
	s.mu.Lock()
	if planErr != nil {
		s.state.NavAvailable, s.state.NavReason, s.state.Reason = false, navigationDiagnostic(planErr, root), "trainer_travel_navigation_unavailable"
		s.mu.Unlock()
		return nil, nil
	}
	s.state.NavAvailable, s.state.NavReason = true, "client_navmesh_route_verified"
	s.mu.Unlock()
	index := 0
	for step := 0; step < 150; step++ {
		s.mu.Lock()
		current, why := s.trainerTravelContextLocked(call, r)
		if why == "" && s.posMap != mapID {
			why = "trainer_travel_map_changed"
		}
		if why == "" && step == 0 && (s.positionRevision != baseRevision || distance(s.pos, from) > 0.05) {
			why = "trainer_travel_position_changed_during_planning"
		}
		if why == "" && (current.revision != revision || current.position != to) {
			why = "trainer_travel_trainer_position_changed"
		}
		if why == "" && distance(s.pos, to) <= 3 && routeRemaining(s.pos, path, index) <= 3 {
			why = "trainer_travel_within_distance"
		}
		if why == "" && index >= len(path) {
			why = "trainer_travel_route_ended_outside_range"
		}
		if why != "" {
			s.state.Reason = why
			s.mu.Unlock()
			return nil, nil
		}
		if !s.mapNavigationStepLocked(call, path, &index) {
			s.state.Reason = "trainer_travel_step_or_transport_failed"
			s.mu.Unlock()
			return nil, nil
		}
		if distance(s.pos, to) <= 3 && routeRemaining(s.pos, path, index) <= 3 {
			s.state.Reason = "trainer_travel_within_distance"
			s.mu.Unlock()
			return nil, nil
		}
		s.state.Reason = "trainer_travel_following_client_navmesh_estimate"
		s.mu.Unlock()
		if err := s.approachDelay(call, 200*time.Millisecond); err != nil {
			return nil, status.FromContextError(err).Err()
		}
	}
	s.mu.Lock()
	s.state.Reason = "trainer_travel_step_limit"
	s.mu.Unlock()
	return nil, nil
}
