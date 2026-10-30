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
	path         []position
	index        int
	target       position
	arrival      position
	arrivalStop  float64
	arrivalKnown bool
	ownerArrival bool
	expected     position
	selfRevision uint64
	nextPlanAt   time.Time
	lastError    string
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
	path, err := s.navigationPath(ctx, s.navDataDir, s.pos, target)
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
			// Formation's smallest restart threshold is1.8yd. Settle inside
			// that band even when the alternate itself is1.25yd off-centre.
			arrivalStop = math.Min(stop, 1.65-offset)
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
			if !northshire(candidate) {
				continue
			}
			path, candidateErr := s.navigationPath(ctx, s.navDataDir, s.pos, candidate)
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
		if northshire(owner) && followHorizontalDistance(owner, target) <= 6.5 && math.Abs(float64(owner.z-target.z)) <= 1.5 {
			path, ownerErr := s.navigationPath(ctx, s.navDataDir, s.pos, owner)
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

func (s *Service) followFormationStepLocked(ctx context.Context, target position, stopDistance float64, ownerFallback ...position) bool {
	now := s.now()
	f := &s.followNav
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
	if !s.posKnown || !northshire(s.pos) || !northshire(target) || math.IsNaN(stopDistance) || math.IsInf(stopDistance, 0) || stopDistance < 0.1 || stopDistance > 4 {
		return fail("follow_navigation_invalid_target", errors.New("formation position or stop distance is invalid"))
	}
	if s.state.MotionUnconfirmed {
		return fail("motion_unconfirmed_explicit_stop_required", errors.New("movement stop is unconfirmed"))
	}
	ownerArrivalValid := !f.ownerArrival || len(ownerFallback) == 1 && distance(f.arrival, ownerFallback[0]) <= .25
	arrival := f.arrival
	if f.ownerArrival && ownerArrivalValid {
		arrival = ownerFallback[0]
	}
	if f.arrivalKnown && ownerArrivalValid && f.selfRevision == s.positionRevision && distance(f.expected, s.pos) <= 0.05 && distance(f.target, target) <= 1.5 && followAtTarget(s.pos, arrival, f.arrivalStop) {
		ok := s.stopLocked(ctx)
		if ok {
			s.state.Reason = "within_follow_formation"
		}
		return ok
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
	validCached := len(f.path) != 0 && ownerArrivalValid && f.selfRevision == s.positionRevision && distance(f.expected, s.pos) <= 0.05
	needsPlan := !validCached || f.index >= len(f.path) || distance(f.target, target) > 1.5
	if needsPlan && !now.Before(f.nextPlanAt) {
		f.path = nil
		f.nextPlanAt = now.Add(750 * time.Millisecond)
		planCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		path, arrival, arrivalStop, ownerArrival, err := s.formationNavigationPath(planCtx, target, stopDistance, ownerFallback...)
		if err == nil {
			err = planCtx.Err()
		}
		cancel()
		if err != nil {
			return fail("follow_navigation_unavailable", err)
		}
		f.path = append([]position(nil), path...)
		f.index, f.target, f.expected, f.selfRevision = 0, target, s.pos, s.positionRevision
		f.arrival, f.arrivalStop, f.arrivalKnown = arrival, arrivalStop, true
		f.ownerArrival = ownerArrival
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
	arrival = f.arrival
	if f.ownerArrival && len(ownerFallback) == 1 {
		arrival = ownerFallback[0]
	}
	if followAtTarget(s.pos, arrival, f.arrivalStop) {
		if !s.stopLocked(ctx) {
			return false
		}
		f.path = nil
		s.state.Reason = "within_follow_formation"
	}
	return true
}
