package buddyactions

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
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
	ownerVisit, ownerFollowRevision     uint64
	groundAccess                        bool
	intermediateLeg                     bool
	legVisit                            uint64
	legOwner                            position
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

func missingFormationEndpoint(err error) bool {
	if err == nil {
		return false
	}
	for _, endpoint := range []string{"start", "destination"} {
		prefix := "find navigation path: " + endpoint + ": "
		if strings.HasSuffix(err.Error(), prefix+"sample is outside ground polygon") ||
			strings.HasSuffix(err.Error(), prefix+"no nearby ground polygon") {
			return true
		}
	}
	return false
}

// A blocked personal slot can use a nearby pocket only after that pocket's
// complete ground route succeeds. All candidate queries share one deadline.
// An invalid start never authorizes a candidate, snap or unverified recovery.
func (s *Service) formationNavigationPath(ctx context.Context, target position, stop float64, ownerFallback ...position) ([]position, position, float64, bool, error) {
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
	// PlanMap can reject the normalized destination before returning its
	// samples. A start or corridor failure cannot authorize an owner fallback.
	slotFloorMismatch := err != nil && strings.HasSuffix(err.Error(), "navigation normalized endpoint is outside destination tolerance")
	if err == nil {
		if arrival, radius, ok := validate(path, target, false); ok {
			return path, arrival, radius, false, nil
		}
		// An offset shares its leader's reported altitude. On a slope the
		// verified floor may miss that height at exactly the requested XY.
		// Only the ordinary observed-owner fallback can try its own floor.
		arrival := path[len(path)-1]
		slotFloorMismatch = followHorizontalDistance(arrival, target) <= stop && math.Abs(float64(arrival.z-target.z)) > 1.5
		err = errors.New("formation route exceeds point limit or ends outside the slot")
	}
	if formationDestinationUnavailable(err) {
		for _, offset := range [][2]float32{{1.25, 0}, {-1.25, 0}, {0, 1.25}, {0, -1.25}} {
			if ctx.Err() != nil {
				return nil, position{}, 0, false, ctx.Err()
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
				return path, arrival, radius, false, nil
			}
		}
	}
	// A formation offset can land outside an upstairs floor or on another
	// disconnected layer. Only the follow controller supplies this fallback;
	// melee and other users never change their target to the owner. A complete
	// separate route still has to validate the actual observed owner's floor.
	partial := strings.Contains(err.Error(), "navigation polygon path is partial, exhausted, or unavailable") || strings.Contains(err.Error(), "navigation straight path is partial or unavailable")
	if len(ownerFallback) == 1 && (slotFloorMismatch || formationDestinationUnavailable(err) || partial) {
		owner := ownerFallback[0]
		if ctx.Err() != nil {
			return nil, position{}, 0, false, ctx.Err()
		}
		if finite(owner) && followHorizontalDistance(owner, target) <= 6.5 && math.Abs(float64(owner.z-target.z)) <= 1.5 {
			path, ownerErr := s.mapNavigationPath(ctx, s.navDataDir, s.posMap, s.pos, owner)
			if ownerErr == nil && len(path) <= 256 && followAtTarget(path[len(path)-1], owner, 2) {
				// Cache the actual owner separately from the wanted personal slot.
				// Its Z check prevents stopping directly below the upstairs owner.
				return path, owner, 2, true, nil
			}
		}
	}
	return nil, position{}, 0, false, err
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

// An already-verified route to a previous sighting can carry a moving body
// until the normal replan. Each such packet must still make physical progress
// toward the currently observed owner. Reaching the old endpoint is not arrival.
func (s *Service) advancingOwnerFloorPrefix(w *worldv1.SnapshotResponse, next position) bool {
	f, owner := &s.followNav, w.GetOwner()
	if !f.ownerArrival || f.ownerVisit == 0 || w.GetVisitRevision() != f.ownerVisit ||
		s.followRevision != f.ownerFollowRevision || (!s.state.Following && s.followGoalExecuting == nil) ||
		owner.GetPositionInferred() || owner.GetMovementFlags()&0x3f == 0 || s.safeFollowPlayers(w, s.selfGUID, true) != "" {
		return false
	}
	current := playerPosition(owner)
	return distance(next, current) < distance(s.pos, current)
}

func (s *Service) followFormationStepLocked(ctx context.Context, target position, stopDistance float64, ownerFallback ...position) bool {
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
		f.intermediateLeg = false
		f.legVisit = 0
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
		if f.intermediateLeg {
			// A planned waypoint is not the owner's formation slot. Re-enter
			// ordinary following with fresh owner/intent facts for the next leg.
			f.path, f.arrivalKnown, f.intermediateLeg = nil, false, false
			f.legVisit = 0
			s.state.Reason = "follow_route_leg_complete"
		} else {
			s.state.Reason = "within_follow_formation"
		}
		return true
	}
	if !s.posKnown || !finite(s.pos) || !finite(target) || math.IsNaN(stopDistance) || math.IsInf(stopDistance, 0) || stopDistance < 0.1 || stopDistance > 4 {
		return fail("follow_navigation_invalid_target", errors.New("formation position or stop distance is invalid"))
	}
	s.navigationGoal, s.navigationGoalKnown = target, true
	if s.state.MotionUnconfirmed {
		return fail("motion_unconfirmed_explicit_stop_required", errors.New("movement stop is unconfirmed"))
	}
	if f.legVisit != 0 && (distance(f.target, target) > 1.5 || len(ownerFallback) != 1 || distance(f.legOwner, ownerFallback[0]) > 1.5) {
		// A leg belongs to its planned owner/slot, not whichever owner pose
		// happens to be visible on a later tick inside the replan throttle.
		f.path, f.arrivalKnown = nil, false
	}
	ownerArrivalValid := (!f.ownerArrival || len(ownerFallback) == 1 && distance(f.arrival, ownerFallback[0]) <= .25 &&
		(f.ownerVisit == 0 || f.ownerVisit == s.lastWorld.GetVisitRevision() && f.ownerFollowRevision == s.followRevision)) &&
		(f.legVisit == 0 || f.legVisit == s.lastWorld.GetVisitRevision())
	arrival := f.arrival
	if f.ownerArrival && ownerArrivalValid {
		arrival = ownerFallback[0]
	}
	if f.arrivalKnown && ownerArrivalValid && f.selfRevision == s.positionRevision && f.mapID == s.posMap && distance(f.expected, s.pos) <= 0.05 && distance(f.target, target) <= 1.5 && followAtTarget(s.pos, arrival, f.arrivalStop) {
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
	continuePrefix := false
	if f.ownerArrival && !ownerArrivalValid && len(ownerFallback) == 1 && s.state.Moving && f.expected == s.pos &&
		s.lastWorld.GetOwner() != nil && playerPosition(s.lastWorld.Owner) == ownerFallback[0] {
		// Re-use only the unconsumed physical prefix. On exhausting it, a
		// moving owner needs a new observed destination immediately.
		if f.index < len(f.path) {
			continuePrefix = s.advancingOwnerFloorPrefix(s.lastWorld, f.path[f.index])
		} else if s.advancingOwnerFloorPrefix(s.lastWorld, ownerFallback[0]) {
			f.nextPlanAt = time.Time{}
		}
	}
	validCached := len(f.path) != 0 && (ownerArrivalValid || continuePrefix) && f.selfRevision == s.positionRevision && f.mapID == s.posMap && distance(f.expected, s.pos) <= 0.05
	needsPlan := !validCached || !ownerArrivalValid || f.index >= len(f.path) || distance(f.target, target) > 1.5
	if needsPlan && !now.Before(f.nextPlanAt) {
		f.path = nil
		f.nextPlanAt = now.Add(750 * time.Millisecond)
		planCtx, cancel := context.WithTimeout(ctx, s.followPlanBudget(s.followPortal.movementInProgress))
		path, arrival, arrivalStop, ownerArrival, err := s.formationNavigationPath(planCtx, target, stopDistance, ownerFallback...)
		if err != nil && len(ownerFallback) == 1 && ownerFallback[0].z < s.pos.z && followRouteLengthExceeded(err) && waterMovementAllowed(s.lastWorld) {
			// Try a complete lower route before following a prefix of a long
			// ground detour that may lead away from the actual destination.
			if s.planSwimFollowLocked(ctx, ownerFallback[0], now) {
				cancel()
				f.Reset()
				return s.swimFollowTickLocked(ctx, ownerFallback[0], now)
			}
		}
		intermediate := false
		legVisit := uint64(0)
		groundAccess := false
		if len(ownerFallback) == 1 && missingFormationEndpoint(err) && s.lastWorld.GetVisitRevision() != 0 &&
			s.safeFollowPlayers(s.lastWorld, s.selfGUID, true) == "" && !s.lastWorld.Owner.PositionInferred &&
			playerPosition(s.lastWorld.Owner) == ownerFallback[0] {
			// Geometry only: the observed owner, receiving visit, next body revision
			// and current movement writer are checked again on the ordinary leg below.
			points, accessErr := buddynav.PlanMapWithGroundAccessContext(planCtx, s.navDataDir, s.posMap, surfacePoint(s.pos), surfacePoint(target))
			if accessErr != nil {
				err = errors.Join(err, accessErr)
			} else if !followAtTarget(surfacePosition(points[len(points)-1], 0), target, stopDistance) {
				err = errors.Join(err, errors.New("physical route ends outside the requested formation slot"))
			} else {
				path = make([]position, len(points))
				for i, p := range points {
					path[i] = surfacePosition(p, 0)
				}
				arrival, arrivalStop, ownerArrival = path[len(path)-1], stopDistance, false
				legVisit, groundAccess, err = s.lastWorld.GetVisitRevision(), true, nil
			}
		}
		if err != nil && len(ownerFallback) == 1 && followRouteLengthExceeded(err) &&
			finite(ownerFallback[0]) && distance(s.pos, ownerFallback[0]) <= 200 && s.planLegPath != nil && planCtx.Err() == nil &&
			s.lastWorld.GetVisitRevision() != 0 && s.safeFollowPlayers(s.lastWorld, s.selfGUID, true) == "" {
			// Only ordinary observed-owner following supplies this argument.
			// Reuse the complete-corridor planner, never a straight-line prefix
			// or a partial result from the failed normal route.
			path, err = s.planLegPath(planCtx, s.navDataDir, s.posMap, s.pos, target)
			if err == nil {
				if _, valid := validRouteLeg(path, s.pos, target); !valid {
					err = errors.New("follow planner returned invalid physical leg")
				} else {
					arrival, arrivalStop, ownerArrival = path[len(path)-1], .25, false
					intermediate = !followAtTarget(arrival, target, stopDistance)
					legVisit = s.lastWorld.GetVisitRevision()
				}
			}
		}
		if err == nil {
			err = planCtx.Err()
		}
		cancel()
		if err != nil {
			if len(ownerFallback) == 1 && (swimFallbackError(err) || target.z < s.pos.z && followRouteLengthExceeded(err)) && s.swimFollowTickLocked(ctx, ownerFallback[0], now) {
				f.Reset()
				return true
			}
			return fail("follow_navigation_unavailable", err)
		}
		f.path = append([]position(nil), path...)
		f.pointDetour, f.pointEscape = false, false
		f.index, f.target, f.expected, f.selfRevision, f.mapID = 0, target, s.pos, s.positionRevision, s.posMap
		f.arrival, f.arrivalStop, f.arrivalKnown = arrival, arrivalStop, true
		f.ownerArrival = ownerArrival
		f.groundAccess = groundAccess
		f.ownerVisit, f.ownerFollowRevision = 0, 0
		if ownerArrival && s.lastWorld.GetOwner() != nil && playerPosition(s.lastWorld.Owner) == arrival {
			f.ownerVisit, f.ownerFollowRevision = s.lastWorld.GetVisitRevision(), s.followRevision
		}
		continuePrefix = false
		f.intermediateLeg = intermediate
		f.legVisit = legVisit
		if legVisit != 0 {
			f.legOwner = playerPosition(s.lastWorld.GetOwner())
		}
		validCached = true
		s.state.NavAvailable = true
		s.state.NavReason = "client_ground_navmesh"
		if groundAccess {
			s.state.NavReason = "client_ground_navmesh_with_endpoint_access"
		}
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
	if f.legVisit != 0 {
		// The existing movement writer refreshes World after its clock RPC.
		// Bind that final boundary too: planning is not follow authority and an
		// owner's changed pose must not continue an old winding-route leg.
		visit, revision, sid, guid, groundAccess := f.legVisit, s.followRevision, s.sessionID, s.selfGUID, f.groundAccess
		owner, speed := f.legOwner, observedRunSpeed(s.lastWorld.GetSelf())
		previous, _ := ctx.Value(movementPrewriteGuardKey{}).(movementPrewriteGuard)
		ctx = context.WithValue(ctx, movementPrewriteGuardKey{}, movementPrewriteGuard(func(call context.Context, w *worldv1.SnapshotResponse) (*worldv1.SnapshotResponse, bool) {
			if previous != nil {
				var allowed bool
				w, allowed = previous(call, w)
				if !allowed {
					return w, false
				}
			}
			allowed := !s.closed && !s.independent && s.sessionID == sid && s.selfGUID == guid && s.followRevision == revision &&
				(s.state.Following || s.followGoalExecuting != nil && s.currentFollowFormationGoalLocked(*s.followGoalExecuting, w)) &&
				(!groundAccess || !w.GetOwner().GetPositionInferred()) &&
				w.GetVisitRevision() == visit && s.safeFollowPlayers(w, guid, true) == "" && speed != 0 && observedRunSpeed(w.GetSelf()) == speed && !s.state.MotionUnconfirmed &&
				distance(playerPosition(w.GetOwner()), owner) <= 1.5 && !s.state.CastPending && s.state.InteractionOwner == "" && !s.recovery.state.Active
			if !allowed {
				s.state.Reason = "follow_route_leg_context_changed"
			}
			return w, allowed
		}))
	}
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
		if len(ownerFallback) == 1 && speed > 0 {
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
	if continuePrefix {
		previous, _ := ctx.Value(movementPrewriteGuardKey{}).(movementPrewriteGuard)
		ctx = withMovementPrewriteGuard(ctx, func(call context.Context, w *worldv1.SnapshotResponse) (*worldv1.SnapshotResponse, bool) {
			if previous != nil {
				var allowed bool
				w, allowed = previous(call, w)
				if !allowed {
					return w, false
				}
			}
			if !s.advancingOwnerFloorPrefix(w, p) {
				s.state.Reason = "follow_owner_floor_route_changed"
				return w, false
			}
			return w, true
		})
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
	arrival = f.arrival
	if f.ownerArrival && len(ownerFallback) == 1 {
		arrival = ownerFallback[0]
	}
	if !continuePrefix && followAtTarget(s.pos, arrival, f.arrivalStop) {
		return arrived()
	}
	schedule()
	return true
}

// These are the normal planner's explicit physical-length failures. Missing
// ground, partial corridors, arbitrary errors and cancellation do not qualify.
func followRouteLengthExceeded(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "navigation route exceeds 240 yards") ||
		strings.Contains(message, "navigation normalized route exceeds 240 yards") ||
		strings.Contains(message, "navigation endpoints exceed 240 yard route budget")
}
