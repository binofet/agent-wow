package buddyactions

import (
	"context"
	"crypto/subtle"
	"errors"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type questApproachNavigation struct {
	path         []position
	index        int
	expected     position
	selfRevision uint64
}

func waitApproach(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) ownsQuestApproach(r *actionsv1.ApproachQuestRequest) bool {
	return !s.closed && s.state.InteractionOwner == "quests" && subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) == 1
}

// Local NPC interactions depend on this character's own observations. Joel's
// visibility and activity remain requirements of the separate social controllers.
func safeQuestCharacter(w *worldv1.SnapshotResponse, selfGUID uint64) string {
	if w == nil || w.Self == nil || w.Self.Guid != selfGUID || !w.Self.Present {
		return "character_missing_or_identity_mismatch"
	}
	p := w.Self
	if !p.MapKnown || p.MapId != 0 || !p.PositionKnown || !finite(playerPosition(p)) {
		return "character_outdoor_position_unknown"
	}
	if !p.AliveKnown || !p.Alive {
		return "character_dead_or_unknown"
	}
	if !p.MovementFlagsKnown || p.MovementFlags&^uint32(0x1ff) != 0 || p.ExtraMovementFlags != 0 {
		return "character_ordinary_ground_movement_unknown"
	}
	const unsafe = uint32(0x1 | 0x4 | 0x00040000 | 0x00080000 | 0x00100000 | 0x00400000 | 0x00800000 | 0x01000000 | 0x02000000 | 0x08000000)
	if !p.UnitFlagsKnown || p.UnitFlags&unsafe != 0 {
		return "character_unsafe_or_in_combat"
	}
	return ""
}

// ApproachQuest performs a finite nearby ground approach under the existing
// quest lease. It selects no coordinates, never follows a moving NPC, and stops
// before returning. The resulting position remains a client estimate; the
// quest module must refresh its gates before sending any interaction packet.
func (s *Service) ApproachQuest(ctx context.Context, r *actionsv1.ApproachQuestRequest) (result *actionsv1.State, err error) {
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
		return nil, status.Error(codes.FailedPrecondition, "exclusive owned quests interaction is required")
	}
	s.questApproaching = true
	s.mu.Unlock()
	// A canceled RPC still owes its own movement a bounded STOP. A replacement
	// lease can never be stopped by this old operation's cleanup.
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		defer cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.ownsQuestApproach(r) {
			s.stopLocked(stop)
		}
		s.questApproaching = false
		result = s.stateLocked()
	}()
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var targetRevision uint64
	var initialTarget position
	var navigation questApproachNavigation
	for step := 0; step < 10; step++ {
		s.mu.Lock()
		state, moving, stepErr := s.questApproachStepLocked(call, r, &targetRevision, &initialTarget, &navigation)
		s.mu.Unlock()
		if stepErr != nil || !moving {
			return state, stepErr
		}
		if err := s.approachDelay(call, 200*time.Millisecond); err != nil {
			s.mu.Lock()
			if s.ownsQuestApproach(r) {
				s.state.Reason = "quest_approach_canceled_or_time_limit"
			}
			s.mu.Unlock()
			return nil, status.FromContextError(err).Err()
		}
	}
	s.mu.Lock()
	if s.ownsQuestApproach(r) {
		s.state.Reason = "quest_approach_step_limit"
	}
	s.mu.Unlock()
	return nil, nil
}

func (s *Service) questApproachStepLocked(ctx context.Context, r *actionsv1.ApproachQuestRequest, revision *uint64, initial *position, navigation *questApproachNavigation) (*actionsv1.State, bool, error) {
	s.expireInteractionLocked()
	if !s.ownsQuestApproach(r) {
		return nil, false, status.Error(codes.FailedPrecondition, "quests interaction lease changed")
	}
	pause := func(reason string) (*actionsv1.State, bool, error) {
		s.state.Reason = "quest_approach_" + reason
		return s.stateLocked(), false, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, false, status.FromContextError(err).Err()
	}
	if s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed || s.autoTarget != 0 || s.state.AutoAttacking || s.interactionUntil.Sub(s.now()) < time.Second {
		return pause("state_or_lease_unavailable")
	}
	w, err := s.snapshot(ctx)
	if err != nil {
		return pause("observations_unavailable")
	}
	if reason := safeQuestCharacter(w, s.selfGUID); reason != "" {
		return pause(reason)
	}
	if !s.refreshPositionLocked(w) {
		return pause("authoritative_position_changed")
	}
	if !w.Self.SpeedsKnown || math.Abs(float64(w.Self.RunSpeed)-7) > 0.01 || math.IsNaN(float64(w.Self.RunSpeed)) {
		return pause("normal_run_speed_unknown")
	}
	var npc *worldv1.CreatureState
	if len(w.QuestGivers) <= 64 {
		for _, c := range w.QuestGivers {
			if c != nil && c.Guid == r.NpcGuid {
				npc = c
				break
			}
		}
	}
	if npc == nil || !npc.Present || !npc.MapKnown || npc.MapId != w.Self.MapId || !npc.PositionKnown || !npc.HealthKnown || !npc.AliveKnown || !npc.Alive || npc.Health == 0 || npc.NpcFlags&2 == 0 || npc.UnitFlags&0x80000 != 0 {
		return pause("npc_not_eligible")
	}
	target := position{npc.X, npc.Y, npc.Z, npc.Orientation}
	if !finite(target) || npc.MovementFlags&0x080000ff != 0 || npc.Trajectory != nil && npc.Trajectory.Known && (!npc.Trajectory.Stopped || npc.Trajectory.TransportRelative) {
		return pause("npc_not_stationary")
	}
	if distance(s.pos, target) > 8 || math.Abs(float64(target.z-s.pos.z)) > 1.5 {
		return pause("npc_outside_bounded_approach")
	}
	if *revision == 0 {
		*revision, *initial = npc.PositionRevision+1, target
	} else if *revision != npc.PositionRevision+1 || initial.x != target.x || initial.y != target.y || initial.z != target.z {
		return pause("npc_position_changed")
	}
	if s.navDataDir != "" && northshire(s.pos) && northshire(target) {
		return s.questApproachNavigationStepLocked(ctx, r, target, revision, initial, navigation)
	}
	state := s.groundApproachStepLocked(ctx, target, "quest")
	return state, state.Moving && !state.MotionUnconfirmed, nil
}

func (s *Service) questApproachNavigationStepLocked(ctx context.Context, r *actionsv1.ApproachQuestRequest, target position, revision *uint64, initial *position, route *questApproachNavigation) (*actionsv1.State, bool, error) {
	pause := func(reason string) (*actionsv1.State, bool, error) {
		s.state.Reason = "quest_approach_" + reason
		return s.stateLocked(), false, nil
	}
	if len(route.path) == 0 && distance(s.pos, target) <= 3 {
		return pause("within_distance")
	}
	if len(route.path) == 0 {
		planCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		path, err := s.navigationPath(planCtx, s.navDataDir, s.pos, target)
		if err == nil {
			err = planCtx.Err()
		}
		cancel()
		if err == nil && (len(path) > 256 || distance(s.pos, path[0])+routeRemaining(path[0], path, 1) > 8) {
			err = errors.New("nearby quest route exceeds8yd or256points")
		}
		if err != nil {
			s.state.NavAvailable, s.state.NavReason = false, navigationDiagnostic(err, s.navDataDir)
			return pause("navigation_unavailable")
		}
		route.path, route.expected, route.selfRevision = append([]position(nil), path...), s.pos, s.positionRevision
		s.state.NavAvailable, s.state.NavReason = true, "client_navmesh_route_verified"
		// Planning can consume time. Fetch and check the NPC, lease and self
		// once more before the first write; the nonempty route bounds this
		// recursion to a single revalidation.
		return s.questApproachStepLocked(ctx, r, revision, initial, route)
	}
	if route.selfRevision != s.positionRevision || distance(route.expected, s.pos) > 0.05 {
		return pause("position_changed_after_planning")
	}
	if distance(s.pos, target) <= 3.01 && routeRemaining(s.pos, route.path, route.index) <= 3.01 {
		return pause("within_distance")
	}
	for route.index < len(route.path) && distance(s.pos, route.path[route.index]) <= 0.005 {
		route.index++
	}
	if route.index == len(route.path) {
		return pause("route_ended_outside_range")
	}
	p := s.pos
	op := uint32(opcode.MSGMoveStartForward)
	index := route.index
	if s.state.Moving {
		elapsed := s.now().Sub(s.posAt).Seconds()
		if elapsed < 0 || elapsed > 0.6 {
			return pause("tick_late")
		}
		if elapsed < 0.15 {
			return s.stateLocked(), true, nil
		}
		var ok bool
		p, index, ok = followPathStep(s.pos, route.path, route.index, 7*elapsed)
		if !ok || math.Abs(float64(p.z-s.pos.z)) > 2 || distance(s.pos, p) > 7*elapsed+0.01 {
			return pause("invalid_navigation_step")
		}
		op = opcode.MSGMoveHeartbeat
	} else {
		p.o = followFacing(p, route.path[route.index])
	}
	if !s.movementLocked(ctx, op, p, true) {
		return s.stateLocked(), false, nil
	}
	route.index, route.expected = index, s.pos
	if distance(s.pos, target) <= 3.01 && routeRemaining(s.pos, route.path, route.index) <= 3.01 {
		return pause("within_distance")
	}
	s.state.Reason = "quest_approaching_client_navmesh_estimate"
	return s.stateLocked(), true, nil
}
