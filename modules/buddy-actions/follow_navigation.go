package buddyactions

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Called under Service.mu. expected binds a route to the last position actually
// written by this controller, rather than allowing another actuator's movement
// to join an old route across an unverified chord.
type followNavigation struct {
	path         []position
	index        int
	target       position
	expected     position
	selfRevision uint64
	nextPlanAt   time.Time
}

func (f *followNavigation) Reset() { *f = followNavigation{} }

func followHorizontalDistance(a, b position) float64 {
	return math.Hypot(float64(a.x-b.x), float64(a.y-b.y))
}

func followAtTarget(from, to position, stop float64) bool {
	// The owner's reported Z and a ground-normalized slot can differ slightly.
	// Never chase that vertical difference after reaching the horizontal slot.
	return followHorizontalDistance(from, to) <= stop && math.Abs(float64(from.z-to.z)) <= 1.5
}

func followFacing(from, to position) float32 {
	a := math.Atan2(float64(to.y-from.y), float64(to.x-from.x))
	if a < 0 {
		a += 2 * math.Pi
	}
	return float32(a)
}

// Samples are dense ground-height checks, not necessarily corners. Permit one
// packet to cross collinear samples, but retain an actual turn as a checkpoint.
// The maximum omitted deviation is one centimetre in full three dimensions.
func followCollinear(a, b, c position) bool {
	ux, uy, uz := float64(b.x-a.x), float64(b.y-a.y), float64(b.z-a.z)
	vx, vy, vz := float64(c.x-a.x), float64(c.y-a.y), float64(c.z-a.z)
	uu := ux*ux + uy*uy + uz*uz
	vv := vx*vx + vy*vy + vz*vz
	if uu < 1e-10 {
		return true
	}
	if vv < 1e-10 {
		return false
	}
	dot := ux*vx + uy*vy + uz*vz
	if dot <= 0 || dot < uu-1e-6 {
		return false
	}
	t := dot / vv
	dx, dy, dz := ux-t*vx, uy-t*vy, uz-t*vz
	return dx*dx+dy*dy+dz*dz <= 0.0001
}

func followPathStep(from position, path []position, index int, budget float64) (position, int, bool) {
	if !finite(from) || index < 0 || index > len(path) || math.IsNaN(budget) || math.IsInf(budget, 0) || budget <= 0 {
		return from, index, false
	}
	p := from
	for index < len(path) && distance(p, path[index]) <= 0.005 {
		index++
	}
	if index == len(path) {
		return p, index, true
	}
	first := index
	for index < len(path) && budget > 0.0001 {
		goal := path[index]
		if !northshire(goal) {
			return from, index, false
		}
		d := distance(p, goal)
		if d <= 0.005 {
			index++
			continue
		}
		step := math.Min(budget, d)
		p.o = followFacing(p, goal)
		f := float32(step / d)
		p.x += (goal.x - p.x) * f
		p.y += (goal.y - p.y) * f
		p.z += (goal.z - p.z) * f
		budget -= step
		if step < d {
			break
		}
		index++
		if index < len(path) {
			straight := true
			// Check every crossed sample against the proposed chord. Testing
			// only adjacent segments could gradually smooth a curved wall.
			for crossed := first; crossed < index; crossed++ {
				if !followCollinear(from, path[crossed], path[index]) {
					straight = false
					break
				}
			}
			if !straight {
				break
			}
		}
	}
	return p, index, northshire(p)
}

func (s *Service) followFormationStepLocked(ctx context.Context, target position, stopDistance float64) bool {
	now := s.now()
	f := &s.followNav
	fail := func(reason string, err error) bool {
		f.path = nil
		s.state.NavAvailable = false
		s.state.NavReason = navigationDiagnostic(err, s.navDataDir)
		s.stopLocked(ctx)
		s.state.Reason = reason
		return false
	}
	if !s.posKnown || !northshire(s.pos) || !northshire(target) || math.IsNaN(stopDistance) || math.IsInf(stopDistance, 0) || stopDistance < 0.1 || stopDistance > 4 {
		return fail("follow_navigation_invalid_target", errors.New("formation position or stop distance is invalid"))
	}
	if s.state.MotionUnconfirmed {
		return fail("motion_unconfirmed_explicit_stop_required", errors.New("movement stop is unconfirmed"))
	}
	if followAtTarget(s.pos, target, stopDistance) {
		f.path = nil
		ok := s.stopLocked(ctx)
		if ok {
			s.state.Reason = "within_follow_formation"
		}
		return ok
	}
	validCached := len(f.path) != 0 && f.selfRevision == s.positionRevision && distance(f.expected, s.pos) <= 0.05
	needsPlan := !validCached || f.index >= len(f.path) || distance(f.target, target) > 1.5
	if needsPlan && !now.Before(f.nextPlanAt) {
		f.path = nil
		f.nextPlanAt = now.Add(750 * time.Millisecond)
		planCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		path, err := s.navigationPath(planCtx, s.navDataDir, s.pos, target)
		if err == nil {
			err = planCtx.Err()
		}
		cancel()
		if err != nil {
			return fail("follow_navigation_unavailable", err)
		}
		if len(path) > 256 || !followAtTarget(path[len(path)-1], target, stopDistance) {
			return fail("follow_navigation_unavailable", errors.New("formation route exceeds point limit or ends outside the slot"))
		}
		f.path = append([]position(nil), path...)
		f.index, f.target, f.expected, f.selfRevision = 0, target, s.pos, s.positionRevision
		validCached = true
		s.state.NavAvailable = true
		s.state.NavReason = "client_ground_navmesh"
	}
	if !validCached || f.index >= len(f.path) {
		if !validCached {
			s.state.NavAvailable = false
			s.state.NavReason = "follow_navigation_replan_wait"
		}
		ok := s.stopLocked(ctx)
		s.state.Reason = "follow_navigation_replan_wait"
		return ok
	}
	for f.index < len(f.path) && distance(s.pos, f.path[f.index]) <= 0.005 {
		f.index++
	}
	if f.index == len(f.path) {
		ok := s.stopLocked(ctx)
		s.state.Reason = "follow_navigation_replan_wait"
		return ok
	}
	if !s.state.Moving {
		p := s.pos
		p.o = followFacing(p, f.path[f.index])
		if !s.movementLocked(ctx, opcode.MSGMoveStartForward, p, true) {
			f.path = nil
			return false
		}
		f.expected = s.pos
		s.state.Reason = "following_formation"
		return true
	}
	elapsed := now.Sub(s.posAt).Seconds()
	if elapsed < 0 || elapsed > 0.6 {
		return fail("follow_tick_late", errors.New("formation movement checkpoint is late"))
	}
	if elapsed < 0.15 {
		return true
	}
	p, index, ok := followPathStep(s.pos, f.path, f.index, 7*elapsed)
	if !ok || math.Abs(float64(p.z-s.pos.z)) > 2 || distance(s.pos, p) > 7*elapsed+0.01 {
		return fail("follow_navigation_invalid_step", errors.New("formation movement exceeded a verified ground segment"))
	}
	if !s.movementLocked(ctx, opcode.MSGMoveHeartbeat, p, true) {
		f.path = nil
		s.stopLocked(ctx)
		return false
	}
	f.index, f.expected = index, s.pos
	s.state.Reason = "following_formation"
	if followAtTarget(s.pos, target, stopDistance) {
		if !s.stopLocked(ctx) {
			return false
		}
		f.path = nil
		s.state.Reason = "within_follow_formation"
	}
	return true
}
