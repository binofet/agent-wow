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
	for step := 0; step < 10; step++ {
		s.mu.Lock()
		state, moving, stepErr := s.questApproachStepLocked(call, r, &targetRevision, &initialTarget)
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

func (s *Service) questApproachStepLocked(ctx context.Context, r *actionsv1.ApproachQuestRequest, revision *uint64, initial *position) (*actionsv1.State, bool, error) {
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
	state := s.groundApproachStepLocked(ctx, target, "quest")
	return state, state.Moving && !state.MotionUnconfirmed, nil
}
