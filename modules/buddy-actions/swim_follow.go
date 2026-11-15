package buddyactions

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// The unmodified Wrath forward swim speed is 4.722222 yards per second.
// Use less than that speed until speed changes are observed by buddy-world.
const followSwimSpeed = 4.5

type swimMovement struct {
	wet   bool
	pitch float32
	flags uint32
}

type swimFollowRoute struct {
	path     []position
	wet      []bool
	index    int
	target   position
	expected position
	mapID    uint32
	revision uint64
	nextPlan time.Time
}

func swimFollowCompatible(flags, extra uint32) bool {
	if flags&swimMovementFlag != 0 {
		flags &^= swimMovementFlag | swimAscendingFlag | swimDescendingFlag
	}
	return movementflags.GroundCompatible(flags, extra)
}

func swimFallbackError(err error) bool {
	for _, diagnostic := range []string{
		"water depth exceeds shallow wading limit",
		"no physical floor",
		"navigation polygon path is partial",
		"navigation straight path is partial",
		"no nearby ground polygon",
		"sample is outside ground polygon",
		"polygon is not ground or shallow-wading candidate",
	} {
		if strings.Contains(err.Error(), diagnostic) {
			return true
		}
	}
	return false
}

// A mixed route stays tied to the movement actuator and authoritative position
// revision that proved its starting point. A cast or combat stop may pause it.
func (s *Service) swimFollowTickLocked(ctx context.Context, target position, now time.Time) bool {
	route := &s.swimFollow
	pause := func(reason string) bool {
		s.stopLocked(ctx)
		s.state.Reason = reason
		return false
	}
	if !s.posKnown || !finite(target) || distance(s.pos, target) > 200 {
		return pause("swim_follow_owner_position_unavailable")
	}
	elapsed := now.Sub(s.posAt).Seconds()
	s.lastStep = now
	valid := len(route.path) != 0 && route.mapID == s.posMap &&
		route.revision == s.positionRevision && distance(route.expected, s.pos) <= .05
	if !valid || distance(route.target, target) > 1.5 {
		if now.Before(route.nextPlan) {
			if !valid {
				return pause("swim_follow_replan_wait")
			}
		} else if !s.planSwimFollowLocked(ctx, target, now) {
			return pause("swim_follow_route_unavailable")
		}
	}
	if len(route.path) == 0 {
		return false
	}

	for route.index < len(route.path) && route.wet[route.index] == s.swimming && distance(s.pos, route.path[route.index]) <= .005 {
		route.index++
	}
	if route.index == len(route.path) {
		if !s.swimming {
			*route = swimFollowRoute{}
		}
		s.stopLocked(ctx)
		s.state.Reason = "within_swim_follow_distance"
		return true
	}
	speed := observedRunSpeed(s.lastWorld.GetSelf())
	if s.swimming || route.wet[route.index] {
		speed = followSwimSpeed
	}
	if speed == 0 {
		return pause("swim_follow_run_speed_unknown")
	}
	if s.state.Moving && (elapsed < 0 || elapsed > .6) {
		return pause("swim_follow_tick_late")
	}
	if s.state.Moving && elapsed < .15 {
		return true
	}

	p, index := s.pos, route.index
	if s.state.Moving {
		limit := index + 1
		// Keep the next mode transition as an explicit waypoint even where
		// the path is straight. Dry patches inside a pool can be very short.
		if route.wet[index] == s.swimming {
			for limit < len(route.path) && route.wet[limit] == route.wet[index] {
				limit++
			}
		}
		next, nextIndex, ok := followPathStep(p, route.path[:limit], index, speed*elapsed)
		if !ok {
			*route = swimFollowRoute{}
			return pause("swim_follow_invalid_step")
		}
		p, index = next, nextIndex
	}

	// Enter as we start across a wet chord. Leave only when the following
	// dry sample, which the planner checked against the floor, is reached.
	wet := route.wet[route.index] || s.swimming && index == route.index
	direction := p
	if distance(s.pos, p) <= .005 {
		direction = route.path[route.index]
	}
	horizontal := followHorizontalDistance(s.pos, direction)
	pitch := float32(math.Atan2(float64(direction.z-s.pos.z), horizontal))
	if horizontal > .005 {
		p.o = followFacing(s.pos, direction)
	}
	move := swimMovement{wet: wet, pitch: pitch, flags: 1}
	op := uint32(opcode.MSGMoveHeartbeat)
	switch {
	case wet && !s.swimming:
		op = opcode.MSGMoveStartSwim
	case !wet && s.swimming:
		op = opcode.MSGMoveStopSwim
	case !s.state.Moving:
		op = opcode.MSGMoveStartForward
	}
	if !s.writeMovementLocked(ctx, op, p, true, &move) {
		*route = swimFollowRoute{}
		s.stopLocked(ctx)
		return false
	}
	route.index, route.expected = index, s.pos
	s.state.Reason = "following_mixed_swim_route"
	return true
}

func (s *Service) planSwimFollowLocked(ctx context.Context, target position, now time.Time) bool {
	route := &s.swimFollow
	*route = swimFollowRoute{nextPlan: now.Add(750 * time.Millisecond)}
	plan := s.planSwimPath
	if plan == nil {
		plan = buddynav.PlanSwimContext
	}
	point := func(p position) buddynav.Point { return buddynav.Point{X: p.x, Y: p.y, Z: p.z} }
	samples, err := plan(ctx, s.navDataDir, s.posMap, point(s.pos), point(target))
	if err != nil {
		s.state.NavAvailable = false
		s.state.NavReason = navigationDiagnostic(err, s.navDataDir)
		return false
	}
	var hasWater bool
	path, wet := make([]position, len(samples)), make([]bool, len(samples))
	for i, sample := range samples {
		path[i], wet[i] = position{x: sample.X, y: sample.Y, z: sample.Z}, sample.Swimming
		hasWater = hasWater || sample.Swimming
	}
	// A dry result is useful only for exiting an already-observed swim. Let
	// the original ground planner keep ownership of ordinary dry movement.
	if (!hasWater && !s.swimming) || !validMapNavigationPath(path, s.pos, target) {
		return false
	}
	route.path, route.wet, route.target = path, wet, target
	route.mapID, route.revision, route.expected = s.posMap, s.positionRevision, s.pos
	s.state.NavAvailable = true
	s.state.NavReason = "client_mixed_ground_swim_navmesh"
	return true
}
