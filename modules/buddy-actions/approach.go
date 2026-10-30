package buddyactions

import (
	"context"
	"crypto/subtle"
	"math"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ApproachLoot advances at most one ordinary ground-movement step toward an
// observed eligible corpse. Only the current loot interaction token permits it;
// callers cannot select coordinates or an arbitrary living target.
func (s *Service) ApproachLoot(ctx context.Context, r *actionsv1.ApproachLootRequest) (*actionsv1.State, error) {
	if r == nil || r.Token == "" || r.CorpseGuid == 0 {
		return nil, status.Error(codes.InvalidArgument, "loot token and corpse GUID are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireInteractionLocked()
	if s.closed || s.state.InteractionOwner != "loot" || subtle.ConstantTimeCompare([]byte(r.Token), []byte(s.interactionToken)) != 1 {
		return nil, status.Error(codes.FailedPrecondition, "owned loot interaction is required")
	}
	pause := func(reason string) (*actionsv1.State, error) {
		if s.stopLocked(ctx) {
			s.state.Reason = reason
		}
		return s.stateLocked(), nil
	}
	if s.state.CastPending || s.state.MotionUnconfirmed || s.interactionUntil.Sub(s.now()).Milliseconds() < 1000 {
		return pause("loot_approach_state_or_lease_unavailable")
	}
	w, err := s.snapshot(ctx)
	if err != nil {
		return pause("loot_approach_observations_unavailable")
	}
	if reason := safeQuestCharacter(w, s.selfGUID); reason != "" {
		return pause("loot_approach_" + reason)
	}
	if !s.refreshPositionLocked(w) {
		return pause("loot_approach_authoritative_position_changed")
	}
	if !w.Self.SpeedsKnown || math.Abs(float64(w.Self.RunSpeed)-7) > 0.01 || math.IsNaN(float64(w.Self.RunSpeed)) {
		return pause("loot_approach_normal_run_speed_unknown")
	}
	var corpse *worldv1.CreatureState
	if len(w.LootableCorpses) <= 64 {
		for _, c := range w.LootableCorpses {
			if c != nil && c.Guid == r.CorpseGuid {
				corpse = c
				break
			}
		}
	}
	if corpse == nil || !corpse.Present || !corpse.MapKnown || corpse.MapId != w.Self.MapId || !corpse.HealthKnown || corpse.Health != 0 || !corpse.AliveKnown || corpse.Alive || corpse.DynamicFlags&1 == 0 || corpse.Guid>>48 != 0xf130 && corpse.Guid>>48 != 0xf150 {
		return pause("loot_approach_corpse_not_eligible")
	}
	quest33, _ := ownWolfQuest(w)
	if !quest33 || corpse.Entry != 299 {
		if reason := safePlayers(w, s.selfGUID); reason != "" {
			return pause("loot_approach_" + reason)
		}
		if w.Owner.UnitFlags&0x80000 != 0 || distance(s.pos, playerPosition(w.Owner)) > 30 {
			return pause("loot_approach_requires_nearby_owner_outside_combat")
		}
	}
	target, known, _ := creaturePosition(corpse)
	d := distance(s.pos, target)
	if !known || d > 12 || math.Abs(float64(target.z-s.pos.z)) > 1.5 {
		return pause("loot_approach_target_unknown_far_or_vertical")
	}
	return s.groundApproachStepLocked(ctx, target, "loot"), nil
}

// Both interaction approaches use the same monotonic, speed-bounded movement
// writer. Eligibility and lease ownership are checked by each caller first.
func (s *Service) groundApproachStepLocked(ctx context.Context, target position, kind string) *actionsv1.State {
	pause := func(reason string) *actionsv1.State {
		if s.stopLocked(ctx) {
			s.state.Reason = kind + "_approach_" + reason
		}
		return s.stateLocked()
	}
	d := distance(s.pos, target)
	if d <= 3 {
		return pause("within_distance")
	}
	p := s.pos
	p.o = float32(math.Atan2(float64(target.y-p.y), float64(target.x-p.x)))
	if p.o < 0 {
		p.o += 2 * math.Pi
	}
	op := uint32(opcode.MSGMoveStartForward)
	if s.state.Moving {
		elapsed := s.now().Sub(s.posAt).Seconds()
		if elapsed < 0 || elapsed > 0.6 {
			return pause("tick_late")
		}
		// Duplicate calls cannot move farther without elapsed monotonic time.
		if elapsed < 0.15 {
			s.state.Reason = kind + "_approach_waiting_for_next_step"
			return s.stateLocked()
		}
		step := math.Min(7*elapsed, d-3)
		fraction := float32(step / d)
		p.x += (target.x - p.x) * fraction
		p.y += (target.y - p.y) * fraction
		p.z += (target.z - p.z) * fraction
		op = opcode.MSGMoveHeartbeat
	}
	if !s.movementLocked(ctx, op, p, true) {
		return s.stateLocked()
	}
	if distance(s.pos, target) <= 3.01 {
		return pause("within_distance")
	}
	s.state.Reason = kind + "_approaching_direct_ground_estimate"
	return s.stateLocked()
}
