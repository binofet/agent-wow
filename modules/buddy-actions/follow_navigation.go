package buddyactions

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Called under Service.mu. expected binds a route to the last position actually
// written by this controller, rather than allowing another actuator's movement
// to join an old route across an unverified chord.
type followNavigation struct {
	pointDetour bool
	pointEscape bool

	slotStartDistance, slotStopDistance float64
	path                                []position
	index                               int
	target                              position
	arrival                             position
	arrivalStop                         float64
	arrivalKnown                        bool
	ownerArrival                        bool
	expected                            position
	selfRevision                        uint64
	mapID                               uint32
	nextPlanAt, checkpointAt            time.Time
	lastError                           string
}

func (f *followNavigation) Reset() { *f = followNavigation{} }

func formationDestinationUnavailable(err error) bool {
	if err == nil || !strings.Contains(err.Error(), "find navigation path: destination:") {
		return false
	}
	return strings.Contains(err.Error(), "sample is outside ground polygon") || strings.Contains(err.Error(), "no nearby ground polygon") || strings.Contains(err.Error(), "polygon is not ground-only")
}

// A local target can use a nearby pocket only after its complete ground route
// succeeds. The target and its candidate queries share one deadline.
// An invalid start never authorizes a candidate, snap or unverified recovery.
func (s *Service) formationNavigationPath(ctx context.Context, target position, stop float64) ([]position, position, float64, error) {
	path, err := s.mapNavigationPath(ctx, s.navDataDir, s.posMap, s.pos, target)
	validate := func(path []position, candidate position, alternate bool) (position, float64, bool) {
		if len(path) > 256 || !followAtTarget(path[len(path)-1], candidate, stop) {
			return position{}, 0, false
		}
		arrival := path[len(path)-1]
		arrivalStop := stop
		if alternate {
			offset := followHorizontalDistance(arrival, target)
			if offset > 1.5 || math.Abs(float64(arrival.z-target.z)) > 1.5 {
				return position{}, 0, false
			}
			// The policy's restart radius bounds an alternate endpoint's
			// total displacement from its requested personal slot.
			restart := s.followNav.slotStartDistance
			if restart == 0 {
				restart = 1.8 // Generic route callers provide only an arrival radius.
			}
			arrivalStop = math.Min(stop, restart-.15-offset)
			if arrivalStop < 0.1 {
				return position{}, 0, false
			}
			if s.lastWorld != nil && s.lastWorld.Owner != nil && s.lastWorld.Owner.Present && s.lastWorld.Owner.PositionKnown && followHorizontalDistance(arrival, playerPosition(s.lastWorld.Owner)) < 2 {
				return position{}, 0, false
			}
		}
		return arrival, arrivalStop, true
	}
	if err == nil {
		if arrival, radius, ok := validate(path, target, false); ok {
			return path, arrival, radius, nil
		}
		err = errors.New("formation route exceeds point limit or ends outside the slot")
	}
	if formationDestinationUnavailable(err) {
		for _, offset := range [][2]float32{{1.25, 0}, {-1.25, 0}, {0, 1.25}, {0, -1.25}} {
			if ctx.Err() != nil {
				return nil, position{}, 0, ctx.Err()
			}
			candidate := target
			candidate.x += offset[0]
			candidate.y += offset[1]
			if !finite(candidate) {
				continue
			}
			path, candidateErr := s.mapNavigationPath(ctx, s.navDataDir, s.posMap, s.pos, candidate)
			if candidateErr != nil {
				continue
			}
			if arrival, radius, ok := validate(path, candidate, true); ok {
				return path, arrival, radius, nil
			}
		}
	}
	return nil, position{}, 0, err
}

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
	p, next, _, ok := followStride(from, path, index, budget, math.Inf(1))
	return p, next, ok
}

// Follow the measured polyline up to the first turn, the travel budget or
// the per-packet vertical bound. Return the distance travelled on the path
// so the normal controller can wake when the body actually reaches a turn.
func followStride(from position, path []position, index int, budget, maxDZ float64) (position, int, float64, bool) {
	if !finite(from) || index < 0 || index > len(path) || math.IsNaN(budget) || math.IsInf(budget, 0) || budget <= 0 {
		return from, index, 0, false
	}
	p := from
	for index < len(path) && distance(p, path[index]) <= 0.005 {
		index++
	}
	if index == len(path) {
		return p, index, 0, true
	}
	first, available := index, budget
	for index < len(path) && budget > 0.0001 {
		goal := path[index]
		if !finite(goal) {
			return from, index, 0, false
		}
		d := distance(p, goal)
		if d <= 0.005 {
			index++
			continue
		}
		step := math.Min(budget, d)
		if dz := float64(goal.z) - float64(from.z); math.Abs(dz) > maxDZ && math.Abs(float64(goal.z)-float64(p.z)) <= maxDZ {
			// Clip a climb accumulated across verified samples. A single
			// unsupported height jump must still fail the packet bound.
			boundary := float64(from.z) + math.Copysign(maxDZ, dz)
			toBoundary := (boundary - float64(p.z)) / (float64(goal.z) - float64(p.z))
			step = math.Min(step, math.Max(0, toBoundary*d))
		}
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
	return p, index, available - budget, finite(p)
}

// A periodic tick still handles all the ordinary follow and safety work. A
// nearer verified corner or height boundary can request one earlier tick,
// without retaining authority or moving the body from a timer callback.
func (f *followNavigation) groundCheckpoint(from position, at time.Time, speed float64) time.Time {
	const horizon = 0.6
	budget := speed * horizon
	point, _, used, ok := followStride(from, f.path, f.index, budget, 2)
	if !ok || math.Abs(float64(point.z-from.z)) > 2 || used <= 0 || used >= budget-0.0001 {
		return time.Time{}
	}
	return at.Add(time.Duration(math.Ceil(used / speed * float64(time.Second))))
}

func (s *Service) followFormationStepLocked(ctx context.Context, target position, stopDistance float64, observedOwner ...position) bool {
	now := s.now()
	f := &s.followNav
	f.checkpointAt = time.Time{}
	if f.pointDetour && (len(s.currentMovementPointsLocked()) == 0 || f.pointEscape && pointClearance(s.pos, s.currentMovementPointsLocked()) >= 0) {
		f.Reset()
	}
	fail := func(reason string, err error) bool {
		f.path = nil
		f.arrivalKnown = false
		f.ownerArrival = false
		s.state.NavAvailable = false
		s.state.NavReason = navigationDiagnostic(err, s.navDataDir)
		f.lastError = s.state.NavReason
		s.stopLocked(ctx)
		s.state.Reason = reason
		return false
	}
	arrived := func() bool {
		if !s.stopLocked(ctx) {
			return false
		}
		s.state.Reason = "within_follow_formation"
		return true
	}
	if !s.posKnown || !finite(s.pos) || !finite(target) || math.IsNaN(stopDistance) || math.IsInf(stopDistance, 0) || stopDistance < 0.1 || stopDistance > 4 {
		return fail("follow_navigation_invalid_target", errors.New("formation position or stop distance is invalid"))
	}
	s.navigationGoal, s.navigationGoalKnown = target, true
	if s.state.MotionUnconfirmed {
		return fail("motion_unconfirmed_explicit_stop_required", errors.New("movement stop is unconfirmed"))
	}
	if len(observedOwner) == 1 && s.planFollowCorridor != nil {
		var last *destinationOwner
		if bound, ok := ctx.Value(destinationOwnerKey{}).(destinationOwner); ok && bound.lastSeen {
			last = &bound
		}
		return s.stepOnFollowCorridorLocked(ctx, now, target, stopDistance, observedOwner[0], last, fail, arrived)
	}
	if f.arrivalKnown && !f.ownerArrival && f.selfRevision == s.positionRevision && f.mapID == s.posMap && distance(f.expected, s.pos) <= 0.05 && distance(f.target, target) <= 1.5 && followAtTarget(s.pos, f.arrival, f.arrivalStop) {
		return arrived()
	}
	if !f.ownerArrival && followAtTarget(s.pos, target, stopDistance) {
		f.path = nil
		f.arrivalKnown = false
		ok := s.stopLocked(ctx)
		if ok {
			s.state.Reason = "within_follow_formation"
		}
		return ok
	}
	validCached := len(f.path) != 0 && !f.ownerArrival && f.selfRevision == s.positionRevision && f.mapID == s.posMap && distance(f.expected, s.pos) <= 0.05
	needsPlan := !validCached || f.index >= len(f.path) || distance(f.target, target) > 1.5
	if needsPlan && !now.Before(f.nextPlanAt) {
		f.path = nil
		f.nextPlanAt = now.Add(750 * time.Millisecond)
		planCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		path, arrival, arrivalStop, err := s.formationNavigationPath(planCtx, target, stopDistance)
		if err == nil {
			err = planCtx.Err()
		}
		cancel()
		if err != nil {
			return fail("follow_navigation_unavailable", err)
		}
		f.path = append([]position(nil), path...)
		f.pointDetour, f.pointEscape = false, false
		f.index, f.target, f.expected, f.selfRevision, f.mapID = 0, target, s.pos, s.positionRevision, s.posMap
		f.arrival, f.arrivalStop, f.arrivalKnown = arrival, arrivalStop, true
		f.ownerArrival = false
		validCached = true
		s.state.NavAvailable = true
		s.state.NavReason = "client_ground_navmesh"
		f.lastError = ""
	}
	if !validCached || f.index >= len(f.path) {
		if !validCached {
			s.state.NavAvailable = false
			if f.lastError != "" {
				s.state.NavReason = f.lastError
			} else {
				s.state.NavReason = "follow_navigation_replan_wait"
			}
		}
		ok := s.stopLocked(ctx)
		s.state.Reason = "follow_navigation_replan_wait"
		return ok
	}
	return s.advanceFormationGroundLocked(ctx, now, len(observedOwner) == 1, fail, arrived)
}

func (s *Service) advanceFormationGroundLocked(ctx context.Context, now time.Time, followingOwner bool, fail func(string, error) bool, arrived func() bool) bool {
	f := &s.followNav
	for f.index < len(f.path) && distance(s.pos, f.path[f.index]) <= 0.005 {
		f.index++
	}
	if f.index == len(f.path) {
		ok := s.stopLocked(ctx)
		s.state.Reason = "follow_navigation_replan_wait"
		return ok
	}
	if len(s.currentMovementPointsLocked()) != 0 {
		// Apply current Lua exclusions to this already-admitted ordinary route.
		// The native movement writer, speed limit and final body guards stay shared.
		remaining := append([]position{s.pos}, f.path[f.index:]...)
		adjusted, allowed := s.movementPointRouteLocked(ctx, remaining)
		if !allowed {
			s.stopLocked(ctx)
			s.state.Reason = "movement_points_" + s.movementPoints.reason
			return false
		}
		if pointClearance(s.pos, s.currentMovementPointsLocked()) < 0 {
			ctx = context.WithValue(ctx, movementPointsEscapePathKey{}, &movementPointsEscapePath{revision: s.movementPoints.revision, path: adjusted})
		}
		changedRoute := len(adjusted) != len(remaining)
		if !changedRoute {
			for i := range adjusted {
				if adjusted[i] != remaining[i] {
					changedRoute = true
					break
				}
			}
		}
		if changedRoute {
			if _, retained := ctx.Value(followCorridorMovementKey{}).(followCorridor); retained {
				s.state.NavReason = "client_ground_hazard_detour"
			}
			f.path, f.index, f.pointDetour = adjusted, 0, true
			f.pointEscape = pointClearance(s.pos, s.currentMovementPointsLocked()) < 0
			for f.index < len(f.path) && distance(s.pos, f.path[f.index]) <= .005 {
				f.index++
			}
			if f.index == len(f.path) {
				return false
			}
		}
	}
	speed := observedRunSpeed(s.lastWorld.GetSelf())
	// Only an ordinary follow with a currently observed owner schedules the
	// periodic controller. Other users keep their own cadence and custody.
	schedule := func() {
		if followingOwner && speed > 0 {
			f.checkpointAt = f.groundCheckpoint(s.pos, s.posAt, speed)
		}
	}
	if !s.state.Moving {
		p := s.pos
		p.o = followFacing(p, f.path[f.index])
		if !s.movementLocked(ctx, opcode.MSGMoveStartForward, p, true) {
			f.path = nil
			return false
		}
		f.expected = s.pos
		schedule()
		s.state.Reason = "following_formation"
		return true
	}
	elapsed := now.Sub(s.posAt).Seconds()
	if elapsed < 0 || elapsed > 0.6 {
		return fail("follow_tick_late", errors.New("formation movement checkpoint is late"))
	}
	if elapsed < 0.15 {
		schedule()
		if f.checkpointAt.IsZero() || now.Before(f.checkpointAt) {
			return true
		}
	}
	if speed == 0 {
		return fail("follow_speed_unknown", errors.New("current observed run speed is unavailable"))
	}
	p, index, _, ok := followStride(s.pos, f.path, f.index, speed*elapsed, 2)
	if !ok || math.Abs(float64(p.z-s.pos.z)) > 2 || distance(s.pos, p) > speed*elapsed+0.01 {
		return fail("follow_navigation_invalid_step", errors.New("formation movement exceeded a verified ground segment"))
	}
	if index < len(f.path) && followHorizontalDistance(p, f.path[index]) > .005 {
		// At a turn the body remains in forward motion. Face into the
		// next verified segment as soon as its corner is reached.
		p.o = followFacing(p, f.path[index])
	}
	proof := &navigationSpeedProof{from: s.pos, speed: speed, elapsed: elapsed}
	ctx = context.WithValue(ctx, navigationSpeedProofKey{}, proof)
	if !s.movementLocked(ctx, opcode.MSGMoveHeartbeat, p, true) {
		f.path = nil
		s.stopLocked(ctx)
		if proof.refused && !s.state.MotionUnconfirmed {
			s.state.Reason = "follow_observed_run_speed_changed"
		}
		return false
	}
	f.index, f.expected = index, s.pos
	s.state.Reason = "following_formation"
	if followAtTarget(s.pos, f.arrival, f.arrivalStop) {
		return arrived()
	}
	schedule()
	return true
}
