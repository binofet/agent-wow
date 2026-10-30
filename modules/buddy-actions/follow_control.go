package buddyactions

import (
	"context"
	"math"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

// Called only after the normal identity, observation, combat and action gates.
func (s *Service) followFormationLocked(ctx context.Context, world *worldv1.SnapshotResponse, now time.Time) {
	pause := func(reason string) {
		s.stopLocked(ctx)
		s.followNav.Reset()
		s.lastStep = now
		s.state.Reason = reason
	}
	owner := playerPosition(world.Owner)
	// A complete verified ground route can include stairs between floors.
	// The three-dimensional catch-up bound still limits the observed owner.
	if distance(s.pos, owner) > 120 {
		pause("formation_owner_outside_supported_range")
		return
	}
	if !world.Self.SpeedsKnown || math.IsNaN(float64(world.Self.RunSpeed)) || math.Abs(float64(world.Self.RunSpeed)-7) > 0.01 {
		pause("normal_run_speed_unknown")
		return
	}
	elapsed := now.Sub(s.lastStep)
	s.lastStep = now
	if elapsed < 0 || elapsed > 600*time.Millisecond {
		pause("follow_tick_late")
		return
	}
	// Turning alone does not change the travel heading or trigger a restart.
	moving := world.Owner.MovementFlags&0x3f != 0
	classID := uint32(0)
	if world.Self.ClassKnown {
		classID = world.Self.ClassId
	}
	target := s.formation.Target(now, s.selfGUID, classID, owner, moving)
	if !finite(target.Position) {
		pause("formation_position_invalid")
		return
	}
	// Hysteresis lets them stay put through tiny player adjustments instead
	// of constantly shuffling, while a different floor still requires a route.
	if !s.followNav.ownerArrival && (followAtTarget(s.pos, target.Position, target.StopDistance) || !s.state.Moving && followAtTarget(s.pos, target.Position, target.StartDistance)) {
		pause("within_personal_follow_space")
		return
	}
	if !s.state.Moving && !target.Ready {
		pause("formation_reaction_delay")
		return
	}
	s.followFormationStepLocked(ctx, target.Position, target.StopDistance, owner)
}
