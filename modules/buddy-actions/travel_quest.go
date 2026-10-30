package buddyactions

import (
	"context"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) questTravelContextLocked(ctx context.Context, r *actionsv1.ApproachQuestRequest) (*worldv1.CreatureState, string) {
	if !s.ownsQuestApproach(r) || s.interactionUntil.Sub(s.now()) < time.Second {
		return nil, "quest_travel_lease_changed_or_expiring"
	}
	if s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed || s.autoTarget != 0 || s.state.AutoAttacking {
		return nil, "quest_travel_motion_or_spell_unconfirmed"
	}
	w, err := s.snapshot(ctx)
	if err != nil {
		return nil, "quest_travel_observations_unavailable"
	}
	if reason := safeQuestCharacter(w, s.selfGUID); reason != "" {
		return nil, "quest_travel_" + reason
	}
	if !s.refreshPositionLocked(w) {
		return nil, "quest_travel_authoritative_position_changed"
	}
	if !northshire(s.pos) || !w.Self.SpeedsKnown || math.Abs(float64(w.Self.RunSpeed)-7) > 0.01 || math.IsNaN(float64(w.Self.RunSpeed)) {
		return nil, "quest_travel_northshire_normal_ground_required"
	}
	if len(w.QuestGivers) <= 64 {
		for _, c := range w.QuestGivers {
			if c == nil || c.Guid != r.NpcGuid {
				continue
			}
			p := position{c.X, c.Y, c.Z, c.Orientation}
			if (c.Entry != 823 && c.Entry != 197 && c.Entry != 196) || !c.Present || !c.MapKnown || c.MapId != 0 || !c.PositionKnown || !c.AliveKnown || !c.Alive || !c.HealthKnown || c.Health == 0 || c.NpcFlags&2 == 0 || c.UnitFlags&0x80000 != 0 || c.MovementFlags&0x080000ff != 0 || c.Trajectory != nil && c.Trajectory.Known && (!c.Trajectory.Stopped || c.Trajectory.TransportRelative) || !northshire(p) {
				return nil, "quest_travel_observed_stationary_giver_required"
			}
			return c, ""
		}
	}
	return nil, "quest_travel_giver_not_observed"
}

// TravelQuest never accepts caller coordinates. It binds one observed allowed
// questgiver to a complete static-client navmesh route and revalidates it at
// every ordinary movement checkpoint. Its existing quests lease is retained.
func (s *Service) TravelQuest(ctx context.Context, r *actionsv1.ApproachQuestRequest) (result *actionsv1.State, err error) {
	if r == nil || r.Token == "" || r.NpcGuid>>48 != 0xf130 || r.NpcGuid&0xffffff == 0 {
		return nil, status.Error(codes.InvalidArgument, "quests token and observed creature NPC GUID are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	s.expireInteractionLocked()
	if !s.ownsQuestApproach(r) || s.questApproaching {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "exclusive quests lease is required")
	}
	s.questApproaching, s.state.Traveling, s.state.TravelNpcGuid = true, true, r.NpcGuid
	s.mu.Unlock()
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		defer cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.ownsQuestApproach(r) {
			s.stopLocked(stop)
		}
		s.questApproaching, s.state.Traveling = false, false
		result = s.stateLocked()
	}()
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.mu.Lock()
	npc, reason := s.questTravelContextLocked(call, r)
	if reason != "" {
		s.state.Reason = reason
		s.mu.Unlock()
		return nil, nil
	}
	from, to := s.pos, position{npc.X, npc.Y, npc.Z, npc.Orientation}
	revision, baseRevision, root := npc.PositionRevision, s.positionRevision, s.navDataDir
	if distance(from, to) <= 3 {
		s.state.Reason = "quest_travel_within_distance"
		s.mu.Unlock()
		return nil, nil
	}
	s.state.Reason = "quest_travel_planning_client_navmesh"
	s.mu.Unlock()
	path, planErr := s.navigationPath(call, root, from, to)
	s.mu.Lock()
	if planErr != nil {
		s.state.NavAvailable, s.state.NavReason, s.state.Reason = false, navigationDiagnostic(planErr, root), "quest_travel_navigation_unavailable"
		s.mu.Unlock()
		return nil, nil
	}
	s.state.NavAvailable, s.state.NavReason = true, "client_navmesh_route_verified"
	s.mu.Unlock()
	index := 0
	for step := 0; step < 150; step++ {
		s.mu.Lock()
		current, why := s.questTravelContextLocked(call, r)
		if why == "" && step == 0 && (s.positionRevision != baseRevision || distance(s.pos, from) > 0.05) {
			why = "quest_travel_position_changed_during_planning"
		}
		if why == "" && (current.PositionRevision != revision || current.X != to.x || current.Y != to.y || current.Z != to.z) {
			why = "quest_travel_giver_position_changed"
		}
		if why == "" && distance(s.pos, to) <= 3 && routeRemaining(s.pos, path, index) <= 3 {
			why = "quest_travel_within_distance"
		}
		if why == "" && index >= len(path) {
			why = "quest_travel_route_ended_outside_range"
		}
		if why != "" {
			s.state.Reason = why
			s.mu.Unlock()
			return nil, nil
		}
		if !s.navigationStepLocked(call, path, &index) {
			s.state.Reason = "quest_travel_step_or_transport_failed"
			s.mu.Unlock()
			return nil, nil
		}
		if distance(s.pos, to) <= 3 && routeRemaining(s.pos, path, index) <= 3 {
			s.state.Reason = "quest_travel_within_distance"
			s.mu.Unlock()
			return nil, nil
		}
		s.state.Reason = "quest_travel_following_client_navmesh_estimate"
		s.mu.Unlock()
		if err := s.approachDelay(call, 200*time.Millisecond); err != nil {
			return nil, status.FromContextError(err).Err()
		}
	}
	s.mu.Lock()
	s.state.Reason = "quest_travel_step_limit"
	s.mu.Unlock()
	return nil, nil
}
