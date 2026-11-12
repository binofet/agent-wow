package buddyactions

import (
	"context"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
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
	// The observed-owner catch-up bound leaves room for a verified detour
	// inside the planner's independent 240-yard route budget.
	if distance(s.pos, owner) > 200 {
		pause("formation_owner_outside_supported_range")
		return
	}
	if observedRunSpeed(world.Self) == 0 {
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

// Only the observed leader may be airborne: the follower still travels on
// verified ground segments. Keep the shared combat/interaction guard strict.
func safeFollowPlayers(world *worldv1.SnapshotResponse, selfGUID uint64, groundFollow bool) string {
	if !groundFollow || world == nil || world.Owner == nil {
		return safePlayers(world, selfGUID)
	}
	// FALLING and FALLING_FAR describe a jump/fall, not swimming or flight.
	// Require a fresh target even for a purely vertical jump.
	const falling uint32 = 0x1000 | 0x2000
	if world.Owner.MovementFlags&falling == 0 {
		return safePlayers(world, selfGUID)
	}
	copyWorld := proto.Clone(world).(*worldv1.SnapshotResponse)
	owner := copyWorld.Owner
	owner.MovementFlags &^= falling
	if reason := safePlayers(copyWorld, selfGUID); reason != "" {
		return reason
	}
	if owner.PositionAgeMs > 2000 {
		return "owner_moving_position_stale"
	}
	return ""
}
