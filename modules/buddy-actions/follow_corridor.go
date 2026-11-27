package buddyactions

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
)

type followCorridor interface {
	ContainsGroundSegment(context.Context, buddynav.Point, buddynav.Point) bool
	FormationDestination(context.Context, buddynav.Point, buddynav.Point) (buddynav.Point, error)
	PathAhead(context.Context, buddynav.Point, buddynav.Point) ([]buddynav.Point, error)
	Destination() buddynav.Point
}

type followCorridorMovementKey struct{}

type followCorridorPlanner func(context.Context, string, uint32, buddynav.Point, buddynav.Point) (followCorridor, error)

func openFollowCorridor(ctx context.Context, root string, mapID uint32, start, goal buddynav.Point) (followCorridor, error) {
	corridor, err := buddynav.PlanGroundCorridorContext(ctx, root, mapID, start, goal)
	if err != nil {
		return nil, err
	}
	return &corridor, nil
}

type followGroundBinding struct {
	assets, session    string
	body, owner, visit uint64
	intent, policy     uint64
	mapID              uint32
	formation          bool
}

type groundedFollow struct {
	binding  followGroundBinding
	corridor followCorridor
	owner    buddynav.Point
	slot     buddynav.Point
	lastSeen bool
}

func (s *Service) followGroundBinding(w *worldv1.SnapshotResponse, owner uint64) followGroundBinding {
	return followGroundBinding{
		assets: s.navDataDir, session: s.sessionID, body: s.selfGUID, owner: owner, visit: w.GetVisitRevision(),
		intent: s.followRevision, policy: s.hostEncounterEpoch, mapID: w.GetSelf().GetMapId(), formation: s.followGoalExecuting != nil,
	}
}

// The surface handle owns the connected polygon strip. The sampled prefix
// belongs to one steering result and can be replaced on a fresh sighting or
// body correction without discarding the strip or its loaded mesh.
func (s *Service) stepOnFollowCorridorLocked(ctx context.Context, now time.Time, slot position, stop float64, owner position,
	last *destinationOwner, fail func(string, error) bool, arrived func() bool) bool {
	w := s.lastWorld
	guid := w.GetOwner().GetGuid()
	if last != nil {
		guid = last.guid
	}
	binding := s.followGroundBinding(w, guid)
	if last == nil && (w.GetOwner().GetPositionInferred() || s.safeFollowPlayers(w, s.selfGUID, true) != "") {
		return fail("follow_corridor_source_unavailable", errors.New("visible, controllable ground follow required"))
	}
	if binding.visit == 0 || binding.body != w.GetSelf().GetGuid() || binding.owner != ownerGUID || binding.mapID != s.posMap {
		return fail("follow_corridor_source_unavailable", errors.New("current ground follow body, owner, map and visit required"))
	}
	g, f := &s.followGround, &s.followNav
	if g.binding != binding {
		*g = groundedFollow{binding: binding}
		f.Reset()
	}
	source, leader, requested := swimPoint(s.pos), swimPoint(owner), swimPoint(slot)
	rebuild := g.corridor == nil || g.slot != requested || g.owner != leader || g.lastSeen != (last != nil) ||
		!f.arrivalKnown || f.selfRevision != s.positionRevision || distance(f.expected, s.pos) > .005 ||
		f.mapID != s.posMap || f.pointDetour || f.index >= len(f.path)
	if rebuild {
		if g.corridor == nil {
			corridor, err := s.planFollowCorridor(ctx, binding.assets, binding.mapID, source, leader)
			if err != nil {
				return s.followCorridorFailureLocked(ctx, now, owner, last, err, fail)
			}
			g.corridor = corridor
		}
		goal, realOwner := leader, false
		if last == nil {
			projected, err := g.corridor.FormationDestination(ctx, leader, requested)
			switch {
			case err == nil:
				goal = projected
			case errors.Is(err, buddynav.ErrGroundDestination):
				realOwner = true
			default:
				return fail("follow_corridor_projection_unavailable", err)
			}
		}
		points, err := g.corridor.PathAhead(ctx, source, goal)
		if err != nil && last == nil && !realOwner && errors.Is(err, buddynav.ErrGroundDestination) && ctx.Err() == nil {
			realOwner = true
			points, err = g.corridor.PathAhead(ctx, source, leader)
		}
		if err != nil {
			return s.followCorridorFailureLocked(ctx, now, owner, last, err, fail)
		}
		destination := navPosition(g.corridor.Destination())
		if realOwner {
			stop = 2
		}
		if !finite(destination) || len(points) > buddynav.MaxPoints || len(points) == 0 ||
			!followGroundPrefix(points, source) {
			return fail("follow_corridor_invalid_profile", errors.New("ground corridor returned an invalid physical prefix"))
		}
		g.slot, g.owner, g.lastSeen = requested, leader, last != nil
		f.path = f.path[:0]
		for _, point := range points {
			f.path = append(f.path, navPosition(point))
		}
		f.target, f.arrival, f.arrivalStop, f.arrivalKnown = slot, destination, stop, true
		f.expected, f.selfRevision, f.mapID, f.index = s.pos, s.positionRevision, s.posMap, 0
		f.ownerArrival, f.intermediateLeg, f.pointEscape, f.pointDetour, f.legVisit = realOwner, false, false, false, 0
	}
	s.state.NavAvailable = true
	switch {
	case last != nil:
		s.state.NavReason = "client_ground_retained_corridor_last_seen"
	case f.ownerArrival:
		s.state.NavReason = "client_ground_retained_corridor_owner"
	default:
		s.state.NavReason = "client_ground_retained_corridor"
	}
	s.navigationGoal, s.navigationGoalKnown = f.arrival, true
	if followAtTarget(s.pos, f.arrival, f.arrivalStop) {
		return arrived()
	}
	if len(f.path) < 2 {
		return fail("follow_corridor_no_progress", errors.New("ground corridor prefix ended before its destination"))
	}
	speed := observedRunSpeed(w.GetSelf())
	previous, _ := ctx.Value(movementPrewriteGuardKey{}).(movementPrewriteGuard)
	ctx = withMovementPrewriteGuard(ctx, func(call context.Context, fresh *worldv1.SnapshotResponse) (*worldv1.SnapshotResponse, bool) {
		if previous != nil {
			var allowed bool
			fresh, allowed = previous(call, fresh)
			if !allowed {
				return fresh, false
			}
		}
		allowed := s.followGroundBinding(fresh, guid) == binding && !s.closed && !s.independent && !s.state.MotionUnconfirmed &&
			!s.state.CastPending && s.state.InteractionOwner == "" && !s.recovery.state.Active &&
			speed > 0 && observedRunSpeed(fresh.GetSelf()) == speed &&
			(s.state.Following || s.followGoalExecuting != nil && s.currentFollowFormationGoalLocked(*s.followGoalExecuting, fresh))
		if allowed && last != nil {
			allowed = s.lastSeenCurrentLocked(call, *last, fresh)
		} else if allowed {
			nowOwner := fresh.GetOwner()
			allowed = nowOwner.GetGuid() == guid && !nowOwner.GetPositionInferred() && s.safeFollowPlayers(fresh, s.selfGUID, true) == "" &&
				distance(playerPosition(nowOwner), owner) <= movementinfo.FollowDestinationRadius
		}
		if !allowed {
			s.state.Reason = "follow_corridor_context_changed"
		}
		return fresh, allowed
	})
	ctx = context.WithValue(ctx, followCorridorMovementKey{}, g.corridor)
	return s.advanceFormationGroundLocked(ctx, now, []position{f.arrival}, false, fail, arrived)
}

func followGroundPrefix(path []buddynav.Point, from buddynav.Point) bool {
	if distance(navPosition(path[0]), navPosition(from)) > buddynav.EndpointTolerance {
		return false
	}
	var length float64
	for i, point := range path {
		if !finite(navPosition(point)) {
			return false
		}
		if i != 0 {
			d := distance(navPosition(path[i-1]), navPosition(point))
			if d > 2.001 || math.Abs(float64(path[i-1].Z)-float64(point.Z)) > 1.001 {
				return false
			}
			length += d
		}
	}
	return length <= buddynav.LegMaxLength+.01
}

func (s *Service) followCorridorFailureLocked(ctx context.Context, now time.Time, owner position, last *destinationOwner, err error, fail func(string, error) bool) bool {
	w := s.lastWorld
	if ctx.Err() == nil && (w.GetSelf().GetUnitFlags()|w.GetOwner().GetUnitFlags())&movementflags.Mounted == 0 && waterMovementAllowed(w) &&
		(swimFallbackError(err) || errors.Is(err, buddynav.ErrGroundDestination) || strings.Contains(err.Error(), "no complete ground corridor")) {
		if last != nil {
			return s.followLastSeenSurfaceLocked(ctx, w, *last, now, true)
		}
		if s.swimFollowTickLocked(ctx, owner, now) {
			s.followNav.Reset()
			return true
		}
	}
	return fail("follow_corridor_unavailable", err)
}

// An explicit hazard owns its short avoidance path. Reuse independently
// verified planner samples when they stay inside the follow strip. If the
// avoidance exits that strip, plot its own strip to the selected waypoint.
func (s *Service) followAvoidancePath(ctx context.Context, retained followCorridor, start, target position) ([]position, error) {
	from := swimPoint(start)
	goal, err := retained.FormationDestination(ctx, from, swimPoint(target))
	if err != nil {
		return nil, err
	}
	profile, err := s.mapNavigationPath(ctx, s.navDataDir, s.posMap, start, navPosition(goal))
	if err == nil {
		inside := true
		prev := from
		for _, p := range profile {
			next := swimPoint(p)
			if prev != next && !retained.ContainsGroundSegment(ctx, prev, next) {
				inside = false
				break
			}
			prev = next
		}
		if inside {
			return profile, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	other, err := s.planFollowCorridor(ctx, s.navDataDir, s.posMap, from, goal)
	if err != nil {
		return nil, err
	}
	points, err := other.PathAhead(ctx, from, goal)
	if err != nil {
		return nil, err
	}
	if len(points) < 2 || len(points) > buddynav.MaxPoints || !followGroundPrefix(points, from) ||
		!followAtTarget(navPosition(points[len(points)-1]), navPosition(goal), .25) {
		return nil, errors.New("hazard corridor did not reach the selected ground waypoint")
	}
	out := make([]position, len(points))
	for i, point := range points {
		out[i] = navPosition(point)
	}
	return out, nil
}
