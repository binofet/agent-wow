package buddyactions

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
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
	intermediateLeg                     bool
	legVisit                            uint64
	legOwner                            position
	expected                            position
	selfRevision                        uint64
	mapID                               uint32
	nextPlanAt                          time.Time
	lastError                           string
}

func (f *followNavigation) Reset() { *f = followNavigation{} }

func formationDestinationUnavailable(err error) bool {
	if err == nil || !strings.Contains(err.Error(), "find navigation path: destination:") {
		return false
	}
	return strings.Contains(err.Error(), "sample is outside ground polygon") || strings.Contains(err.Error(), "no nearby ground polygon") || strings.Contains(err.Error(), "polygon is not ground-only")
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
	if err == nil {
		if arrival, radius, ok := validate(path, target, false); ok {
			return path, arrival, radius, false, nil
		}
		return nil, position{}, 0, false, errors.New("formation route exceeds point limit or ends outside the slot")
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
	if len(ownerFallback) == 1 && (formationDestinationUnavailable(err) || partial) {
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
		if !finite(goal) {
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
	return p, index, finite(p)
}

func (s *Service) followFormationStepLocked(ctx context.Context, target position, stopDistance float64, ownerFallback ...position) bool {
	now := s.now()
	f := &s.followNav
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
	ownerArrivalValid := (!f.ownerArrival || len(ownerFallback) == 1 && distance(f.arrival, ownerFallback[0]) <= .25) &&
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
	validCached := len(f.path) != 0 && ownerArrivalValid && f.selfRevision == s.positionRevision && f.mapID == s.posMap && distance(f.expected, s.pos) <= 0.05
	needsPlan := !validCached || f.index >= len(f.path) || distance(f.target, target) > 1.5
	if needsPlan && !now.Before(f.nextPlanAt) {
		f.path = nil
		f.nextPlanAt = now.Add(750 * time.Millisecond)
		planCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		path, arrival, arrivalStop, ownerArrival, err := s.formationNavigationPath(planCtx, target, stopDistance, ownerFallback...)
		intermediate := false
		legVisit := uint64(0)
		if err != nil && len(ownerFallback) == 1 && followRouteLengthExceeded(err) &&
			finite(ownerFallback[0]) && distance(s.pos, ownerFallback[0]) <= 200 && s.planLegPath != nil && planCtx.Err() == nil &&
			s.lastWorld.GetVisitRevision() != 0 && safeFollowPlayers(s.lastWorld, s.selfGUID, true) == "" {
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
			if len(ownerFallback) == 1 && swimFallbackError(err) && s.swimFollowTickLocked(ctx, ownerFallback[0], now) {
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
		f.intermediateLeg = intermediate
		f.legVisit = legVisit
		if legVisit != 0 {
			f.legOwner = playerPosition(s.lastWorld.GetOwner())
		}
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
	if f.legVisit != 0 {
		// The existing movement writer refreshes World after its clock RPC.
		// Bind that final boundary too: planning is not follow authority and an
		// owner's changed pose must not continue an old winding-route leg.
		visit, revision, sid, guid := f.legVisit, s.followRevision, s.sessionID, s.selfGUID
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
				w.GetVisitRevision() == visit && safeFollowPlayers(w, guid, true) == "" && speed != 0 && observedRunSpeed(w.GetSelf()) == speed && !s.state.MotionUnconfirmed &&
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
	speed := observedRunSpeed(s.lastWorld.GetSelf())
	if speed == 0 {
		return fail("follow_speed_unknown", errors.New("current observed run speed is unavailable"))
	}
	p, index, ok := followPathStep(s.pos, f.path, f.index, speed*elapsed)
	if !ok || math.Abs(float64(p.z-s.pos.z)) > 2 || distance(s.pos, p) > speed*elapsed+0.01 {
		return fail("follow_navigation_invalid_step", errors.New("formation movement exceeded a verified ground segment"))
	}
	if !s.movementLocked(ctx, opcode.MSGMoveHeartbeat, p, true) {
		f.path = nil
		s.stopLocked(ctx)
		return false
	}
	f.index, f.expected = index, s.pos
	s.state.Reason = "following_formation"
	arrival = f.arrival
	if f.ownerArrival && len(ownerFallback) == 1 {
		arrival = ownerFallback[0]
	}
	if followAtTarget(s.pos, arrival, f.arrivalStop) {
		return arrived()
	}
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
