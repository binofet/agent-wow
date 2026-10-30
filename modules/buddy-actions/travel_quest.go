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

type questTravelTarget struct {
	position position
	revision uint64
	landmark bool
}

// These three Northshire locations were observed from normal server CREATE
// packets. They are travel landmarks, never assertions of a live NPC's state.
func northshireQuestLandmark(guid uint64) (position, uint32, bool) {
	if guid>>48 != 0xf130 || guid&0xffffff == 0 {
		return position{}, 0, false
	}
	entry := uint32(guid >> 24 & 0xffffff)
	switch entry {
	case 196:
		return position{x: -8869.22, y: -163.237, z: 80.9719}, entry, true
	case 197:
		return position{x: -8902.59, y: -162.606, z: 82.0223}, entry, true
	case 823:
		return position{x: -8947.64, y: -132.319, z: 83.7199}, entry, true
	}
	return position{}, entry, false
}

func (s *Service) questTravelContextLocked(ctx context.Context, r *actionsv1.ApproachQuestRequest) (*questTravelTarget, string) {
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
	landmark, entry, hasLandmark := northshireQuestLandmark(r.NpcGuid)
	if entry == 0 || len(w.QuestGivers) > 64 || len(w.NearbyCreatures) > 32 || len(w.LootableCorpses) > 64 {
		return nil, "quest_travel_allowed_northshire_giver_required"
	}
	// Search other bounded views too: an observed NPC that lost its questgiver
	// flag, died or started moving must not silently become a landmark fallback.
	views := [][]*worldv1.CreatureState{w.QuestGivers, w.NearbyCreatures, w.LootableCorpses, {w.OwnerTarget}}
	var observed *questTravelTarget
	for _, view := range views {
		for _, c := range view {
			if c == nil || c.Guid != r.NpcGuid {
				continue
			}
			p := position{c.X, c.Y, c.Z, c.Orientation}
			if c.Entry != entry || !c.Present || !c.MapKnown || c.MapId != 0 || !c.PositionKnown || !c.AliveKnown || !c.Alive || !c.HealthKnown || c.Health == 0 || c.NpcFlags&2 == 0 || c.UnitFlags&0x80000 != 0 || c.MovementFlags&0x080000ff != 0 || c.Trajectory != nil && c.Trajectory.Known && (!c.Trajectory.Stopped || c.Trajectory.TransportRelative) || !northshire(p) {
				return nil, "quest_travel_observed_stationary_giver_required"
			}
			if observed != nil && (observed.position != p || observed.revision != c.PositionRevision) {
				return nil, "quest_travel_observed_giver_views_disagree"
			}
			observed = &questTravelTarget{position: p, revision: c.PositionRevision}
		}
	}
	if observed != nil {
		return observed, ""
	}
	if !hasLandmark {
		return nil, "quest_travel_current_observed_giver_required"
	}
	return &questTravelTarget{position: landmark, landmark: true}, ""
}

// TravelQuest never accepts caller coordinates. It binds one currently observed
// Northshire questgiver or one of three explicit remembered landmarks to a complete
// static-client navmesh route. Landmark travel stops when the real NPC returns
// to observation; interaction still requires a current actual NPC snapshot.
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
	from, to := s.pos, npc.position
	revision, landmark, baseRevision, root := npc.revision, npc.landmark, s.positionRevision, s.navDataDir
	withinReason, planningReason, followingReason := "quest_travel_within_distance", "quest_travel_planning_client_navmesh", "quest_travel_following_client_navmesh_estimate"
	if landmark {
		withinReason, planningReason, followingReason = "quest_travel_landmark_reached_requires_observation", "quest_travel_landmark_planning_client_navmesh", "quest_travel_landmark_following_client_navmesh_estimate"
	}
	if distance(from, to) <= 3 {
		s.state.Reason = withinReason
		s.mu.Unlock()
		return nil, nil
	}
	s.state.Reason = planningReason
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
		if why == "" && current.landmark != landmark {
			why = "quest_travel_giver_not_observed"
			if landmark {
				why = "quest_travel_landmark_giver_reobserved"
			}
		}
		if why == "" && (current.revision != revision || current.position.x != to.x || current.position.y != to.y || current.position.z != to.z) {
			why = "quest_travel_giver_position_changed"
		}
		if why == "" && distance(s.pos, to) <= 3 && routeRemaining(s.pos, path, index) <= 3 {
			why = withinReason
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
			s.state.Reason = withinReason
			s.mu.Unlock()
			return nil, nil
		}
		s.state.Reason = followingReason
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
