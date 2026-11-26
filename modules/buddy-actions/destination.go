package buddyactions

import (
	"context"
	"errors"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
)

// UNIT_FLAG_CANNOT_SWIM is a unit capability, independent of the current
// swimming movement flag. A walking body may also have this restriction.
func waterMovementAllowed(w *worldv1.SnapshotResponse) bool {
	return w.GetSelf().GetUnitFlagsKnown() && w.Self.UnitFlags&movementflags.CannotSwim == 0 && w.Self.UnitFlags_2&movementflags.ForcedOrFeign == 0 && w.Self.DynamicFlags&0x20 == 0 && w.Self.PlayerFlags&ghostFlag == 0
}

type destinationFlight struct {
	owner             destinationOwner
	airborne, release bool
}

type destinationOwnerKey struct{}
type followRunParentKey struct{}

func destinationPlanningContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if parent, ok := ctx.Value(followRunParentKey{}).(context.Context); ok {
		return context.WithTimeout(parent, 10*time.Second)
	}
	return context.WithCancel(ctx)
}

type destinationOwner struct {
	lastSeen, formation  bool
	gravity              destinationGravity
	token                string
	guid, followRevision uint64
	target               position
}

func (d destinationOwner) localCurrent(s *Service, now time.Time) bool {
	if s.closed || s.state.CastPending || s.state.MotionUnconfirmed || s.clientControlLost || s.clientControlRestoring || s.recovery.state.Active || s.movementOwner.owner != "" {
		return false
	}
	if flight := s.destinationFlight; flight != nil && flight.owner == d && (flight.airborne || flight.release) && s.bodyMoving {
		// A changed destination cannot change the physical trajectory once
		// airborne. An explicit release switches it to gravity-only inputs.
		return s.state.InteractionOwner == "" || d.token != "" && s.ownsExploration(d.token)
	}
	return d.voluntaryCurrent(s, now)
}

func (d destinationOwner) voluntaryCurrent(s *Service, now time.Time) bool {
	if d.token != "" {
		return s.ownsExploration(d.token) && s.state.Traveling && !s.state.InteractionHarvest && now.Before(s.interactionUntil)
	}
	formation := d.formation && s.followGoal.target.Valid && !s.followGoal.target.Hold && s.followGoal.owner == d.guid && s.followGoal.followRevision == d.followRevision && s.hostEncounterActiveLocked() && now.Before(s.followGoal.until)
	return d.guid != 0 && (s.state.Following && now.Before(s.followUntil) || formation) && s.followRevision == d.followRevision && s.state.InteractionOwner == "" && !s.state.Traveling
}
func (d destinationOwner) surfaceCurrent(ctx context.Context, s *Service, w *worldv1.SnapshotResponse) bool {
	if !d.localCurrent(s, s.now()) {
		return false
	}
	if d.lastSeen {
		return s.lastSeenCurrentLocked(ctx, d, w)
	}
	if d.formation && !s.currentFollowFormationGoalLocked(s.followGoal, w) {
		return false
	}
	if d.token != "" {
		return s.explorationIdleBindingCurrentLocked(d.token, w) && safeQuestCharacterMode(w, s.selfGUID, func(f, x uint32) bool { return destinationSelfMode(w, f, x) || d.gravity.current(w) }) == ""
	}
	return s.safeRouteFollowLocked(w) == "" && w.GetOwner().GetGuid() == d.guid
}

// An incoming airborne pose supplies its own fall clock and vertical impulse.
// A stationary observed body may settle from rest only if measured unsupported.
type destinationGravity struct {
	known, falling                  bool
	self, visit, movement, position uint64
	mapID                           uint32
	raw                             position
	wire                            string
	elapsed                         time.Duration
	initial                         float64
	bearing                         float64
}

func destinationGravitySource(w *worldv1.SnapshotResponse, p position) destinationGravity {
	me := w.GetSelf()
	g := destinationGravity{bearing: float64(p.o)}
	if !me.GetPresent() || !me.GetAliveKnown() || !me.GetAlive() || !me.GetPositionKnown() || me.GetPositionInferred() || !me.GetMovementFlagsKnown() || me.GetExtraMovementFlags() != 0 {
		return g
	}
	if me.MovementFlags == 0 {
		g.known = true
		return g
	}
	if me.MovementFlags != 0x1000 && me.MovementFlags != 0x3000 {
		return g
	}
	f := w.GetSelfMovementFacts()
	info := f.GetInfo()
	jump := info.GetJump()
	if len(f.GetWireImage()) == 0 || !f.GetKnown() || f.GetSelfGuid() != me.Guid || f.GetVisitRevision() != w.GetVisitRevision() || !f.GetMapKnown() || f.GetMapId() != me.MapId || f.GetAgeMs() > 500 || info.GetFlags() != me.MovementFlags || info.GetExtraFlags() != 0 || jump == nil || jump.XySpeed != 0 || info.X != p.x || info.Y != p.y || info.Z != p.z {
		return g
	}
	g.self, g.visit, g.movement, g.position, g.mapID, g.raw, g.wire = me.Guid, w.VisitRevision, f.Revision, me.PositionRevision, me.MapId, playerPosition(me), string(f.WireImage)
	g.known, g.falling, g.elapsed, g.initial = true, true, time.Duration(info.FallTime)*time.Millisecond, float64(jump.Velocity)
	return g
}

// Source freshness is checked on selection. Planning may take longer than
// the initial age band; only the same complete received frame may continue.
func (g destinationGravity) current(w *worldv1.SnapshotResponse) bool {
	p, f := w.GetSelf(), w.GetSelfMovementFacts()
	return g.falling && f.GetKnown() && p.GetPresent() && !p.GetPositionInferred() && p.GetPositionKnown() && p.GetGuid() == g.self && w.GetVisitRevision() == g.visit && p.GetMapKnown() && p.GetMapId() == g.mapID && p.GetPositionRevision() == g.position && playerPosition(p) == g.raw && f.GetSelfGuid() == g.self && f.GetRevision() == g.movement && f.GetVisitRevision() == g.visit && f.GetMapKnown() && f.GetMapId() == g.mapID && f.GetInfo().GetFlags() == p.GetMovementFlags() && p.GetExtraMovementFlags() == 0 && g.wire == string(f.GetWireImage())
}

// The last checked socket receipt pins the estimate after a stationary fall.
// A correction, foreign writer, different visit or mismatched pose revokes it.
func (s *Service) routeProvesPriorFall(w *worldv1.SnapshotResponse, b *idleMotionScope) bool {
	return b != nil && !b.broken && b.landing && b.writes > 0 && b.lastAttempt != "" && b.destination != nil &&
		s.matchesCurrentEncounterVisitLocked(w) && w.Self.PositionRevision == b.rawRevision && playerPosition(w.Self) == b.rawPose &&
		s.posRevision == b.clientRevision && s.pos == b.pose && s.posMap == b.mapID && s.lastTransportError == nil
}
func (s *Service) safeRouteFollowLocked(w *worldv1.SnapshotResponse) string {
	return safePlayerMovementModes(w, s.selfGUID, func(f, x uint32) bool {
		return destinationSelfMode(w, f, x) || x == 0 && (f == 0x1000 || f == 0x3000) && (s.swimFollow.owner.gravity.current(w) || s.routeProvesPriorFall(w, s.swimFollow.motion))
	}, swimFollowCompatible)
}

func destinationSelfMode(w *worldv1.SnapshotResponse, f, x uint32) bool {
	return swimFollowCompatible(f, x) || x == 0 && f&0x1000 != 0 && w.GetSelf() != nil && destinationGravitySource(w, playerPosition(w.Self)).falling
}

func (s *Service) planDestinationEntry(ctx context.Context, root string, mapID uint32, from, to position, speed float64, g destinationGravity) (buddynav.DestinationRoute, error) {
	plan := s.planWaterEntry
	if plan == nil {
		plan = buddynav.PlanWaterEntryContext
	}
	start := swimPoint(from)
	if g.known {
		frames, err := buddynav.PlanBodyGravityContext(ctx, root, mapID, start, g.bearing, g.elapsed, g.initial)
		if err == nil && len(frames) >= 2 {
			land := frames[len(frames)-1]
			route := buddynav.DestinationRoute{Approach: []buddynav.Point{start}, Fall: frames, Bearing: g.bearing, RunSpeed: speed}
			surface, err := buddynav.PlanSwimContext(ctx, root, mapID, land.Point, swimPoint(to))
			if err == nil && len(surface) >= 2 && surface[0].Swimming == land.Swimming && distance(navPosition(surface[0].Point), navPosition(land.Point)) <= .025 {
				route.Onward = surface
				return route, nil
			}
			if !land.Swimming {
				onward, e := plan(ctx, root, mapID, land.Point, swimPoint(to), speed)
				if e == nil {
					route.Next = &onward
					return route, nil
				}
			}
			return buddynav.DestinationRoute{}, errors.New("measured first contact does not connect to destination")
		}
		if g.falling {
			return buddynav.DestinationRoute{}, errors.New("observed fall has no verified physical continuation")
		}
	}
	return plan(ctx, root, mapID, start, swimPoint(to), speed)
}
func navPosition(p buddynav.Point) position { return position{x: p.X, y: p.Y, z: p.Z} }
func (route *swimFollowRoute) setEntry(entry buddynav.DestinationRoute) {
	route.drop = &entry
	route.path = make([]position, len(entry.Approach))
	route.wet = make([]bool, len(entry.Approach))
	for i, p := range entry.Approach {
		route.path[i] = navPosition(p)
	}
	route.index = 0
}
func (route *swimFollowRoute) setSurface(samples []buddynav.SwimPoint, from, to position, allowDry bool) bool {
	path := make([]position, len(samples))
	wet := make([]bool, len(samples))
	hasWater := false
	for i, s := range samples {
		path[i], wet[i] = navPosition(s.Point), s.Swimming
		hasWater = hasWater || s.Swimming
	}
	if !hasWater && !allowDry || !validMapNavigationPath(path, from, to) {
		return false
	}
	route.path, route.wet, route.index = path, wet, 0
	return true
}

// Only the leg selected by the current ordinary owner reaches this writer.
// The periodic follow tick has a short dependency-read budget; its parent
// owns cancellation of a measured flight that necessarily spans several ticks.
func (s *Service) destinationFallLocked(ctx context.Context, route *swimFollowRoute) bool {
	entry := route.drop
	if entry == nil || len(entry.Fall) < 2 {
		return false
	}
	owner := route.owner
	mapID, revision, destination := route.mapID, route.revision, route.target
	snapshot, err := s.snapshot(ctx)
	if err != nil || owner.gravity.falling && !owner.gravity.current(snapshot) || !owner.surfaceCurrent(ctx, s, snapshot) || !waterMovementAllowed(snapshot) || !s.matchesCurrentEncounterVisitLocked(snapshot) || !s.posKnown || s.positionRevision != route.revision || s.posMap != route.mapID || distance(s.pos, navPosition(entry.Fall[0].Point)) > .01 || s.swimming || s.bodyMoving || snapshot.Self.MovementFlags&movementflags.PassiveModifiers != 0 || !entry.Fall[0].Settling && snapshot.Self.UnitFlags_2&movementflags.CannotTurn != 0 && math.Abs(math.Remainder(float64(s.pos.o)-entry.Bearing, 2*math.Pi)) > .00001 || observedRunSpeed(snapshot.Self) != entry.RunSpeed || s.state.Moving && !s.stopLocked(ctx) {
		s.state.Reason = "destination_fall_source_changed"
		return false
	}
	if owner.token == "" && !owner.lastSeen && distance(playerPosition(snapshot.Owner), owner.target) > 1.5 {
		s.state.Reason = "destination_changed_before_fall"
		return false
	}
	if parent, ok := ctx.Value(followRunParentKey{}).(context.Context); ok {
		ctx = parent
	}
	if points := s.currentMovementPointsLocked(); len(points) > 0 {
		path := make([]position, len(entry.Fall))
		for i, p := range entry.Fall {
			path[i] = navPosition(p.Point)
		}
		if !pointPathClear(path, points) {
			s.state.Reason = "movement_points_next_leg_blocked"
			return false
		}
	}
	flight := entry.Fall[len(entry.Fall)-1].At
	if until, ok := ctx.Deadline(); ok && time.Until(until) < flight || owner.token != "" && s.interactionUntil.Sub(s.now()) < flight+time.Second {
		s.state.Reason = "destination_fall_deadline_too_short"
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, flight+2*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, destinationOwnerKey{}, owner)
	motion, _ := ctx.Value(idleMotionScopeKey{}).(*idleMotionScope)
	if route.motion != nil {
		motion = route.motion
	}
	if motion == nil || !motion.landing {
		motion, err = s.newBodyMotionScopeLocked(ctx, owner.token, snapshot, false, "destination")
		if err != nil {
			s.state.Reason = "destination_fall_source_unavailable"
			s.state.NavReason = navigationDiagnostic(err, s.navDataDir)
			return false
		}
	}
	ctx = context.WithValue(ctx, idleMotionScopeKey{}, motion)
	flightState := &destinationFlight{owner: owner}
	s.bodyMoving, s.destinationFlight = true, flightState
	s.state.Reason = "following_destination_fall"
	defer func() { s.bodyMoving, s.destinationFlight = false, nil; s.lastStep = s.now() }()
	check := func(started, falling, wet bool) (*worldv1.SnapshotResponse, bool) {
		w, e := s.snapshot(ctx)
		valid := e == nil && waterMovementAllowed(w) && owner.localCurrent(s, s.now()) && s.matchesCurrentEncounterVisitLocked(w) && w.Self.MapId == mapID && w.Self.PositionRevision == revision && observedRunSpeed(w.Self) == entry.RunSpeed && w.Self.MovementFlags&movementflags.PassiveModifiers == 0
		compatible := func(f, x uint32) bool {
			return movementflags.GroundCompatible(f, x) || s.routeProvesPriorFall(w, route.motion) && x == 0 && (f == 0x1000 || f == 0x3000) || entry.Fall[0].Settling && (destinationSelfMode(w, f, x) || owner.gravity.current(w)) || x == 0 && (falling && (f == 0x1000 || f == 0x1001) || wet && (f == swimMovementFlag || f == swimMovementFlag|1))
		}
		valid = valid && safeSelfMovement(w, s.selfGUID, compatible) == ""
		if valid && owner.token != "" && !flightState.release && !flightState.airborne {
			valid = s.explorationIdleBindingCurrentLocked(owner.token, w) && safeQuestCharacterMode(w, s.selfGUID, compatible) == ""
		}
		if valid && owner.lastSeen && !flightState.airborne && !flightState.release {
			valid = s.lastSeenCurrentLocked(ctx, owner, w)
		}
		if valid && !owner.lastSeen && owner.token == "" && !flightState.release && !flightState.airborne {
			p := w.GetOwner()
			valid = (p.GetPresent() && p.GetGuid() == owner.guid && p.GetAliveKnown() && p.GetAlive() && p.GetMapKnown() && p.GetMapId() == mapID && p.GetPositionKnown() && !p.GetPositionInferred() && finite(playerPosition(p)) && p.GetMovementFlagsKnown() && swimFollowCompatible(p.GetMovementFlags(), p.GetExtraMovementFlags()) && distance(playerPosition(p), owner.target) <= 1.5)
		}
		if !valid && motion.writes > 0 && !flightState.airborne && !flightState.release && e == nil && s.matchesCurrentEncounterVisitLocked(w) && w.Self.MapId == mapID && w.Self.PositionRevision == revision && owner.token == "" && safeSelfMovement(w, s.selfGUID, compatible) == "" {
			flightState.release = true
			return w, true // the common writer may now issue only its ground STOP
		}
		if !valid && motion.writes > 0 {
			s.state.MotionUnconfirmed = true
		}
		return w, valid
	}
	view := new(actionsv1.BodyMovementState)
	if !s.executeBodyTrajectoryLocked(ctx, s.pos, entry.Fall, entry.Bearing, entry.RunSpeed, false, true, 0, view, motion, check) {
		s.state.Reason = "destination_fall_interrupted"
		s.state.NavReason = boundedPolicyDiagnostic(view.Phase+" "+view.Detail, 240)
		return false
	}
	if flightState.release || owner.token == "" && (!owner.voluntaryCurrent(s, s.now()) || !owner.lastSeen && (!s.lastWorld.GetOwner().GetPresent() || distance(playerPosition(s.lastWorld.Owner), owner.target) > 1.5)) {
		*route = swimFollowRoute{}
		s.state.Reason = "destination_recheck_after_contact"
		return true
	}
	if entry.Fall[0].Settling || route.motion != nil {
		route.motion = motion
	}
	if entry.Next != nil {
		route.setEntry(*entry.Next)
		route.expected = s.pos
		s.state.Reason = "destination_reached_measured_footing"
		return true
	}
	if !route.setSurface(entry.Onward, s.pos, destination, true) {
		s.state.Reason = "destination_continuation_changed"
		return false
	}
	route.drop, route.expected = nil, s.pos
	s.state.Reason = "following_mixed_swim_route"
	return true
}

type swimPlanner = func(context.Context, string, uint32, buddynav.Point, buddynav.Point) ([]buddynav.SwimPoint, error)
type observedSwimJoin func(context.Context, buddynav.Point, swimPlanner) []buddynav.SwimPoint

func (s *Service) planMixedDestination(ctx context.Context, root string, mapID uint32, from, to position, speed float64, swimming bool, gravity destinationGravity, join observedSwimJoin) (swimFollowRoute, error) {
	var route swimFollowRoute
	plan := s.planSwimPath
	if plan == nil {
		plan = buddynav.PlanSwimContext
	}
	points, err := plan(ctx, root, mapID, swimPoint(from), swimPoint(to))
	if join != nil && disconnectedSwimRoute(err) {
		if via := join(ctx, swimPoint(to), plan); len(via) > 0 {
			points, err = via, nil
		}
	}
	if err == nil {
		if !route.setSurface(points, from, to, swimming) {
			return route, errors.New("destination water corridor is invalid")
		}
	} else {
		if swimming || to.z >= from.z {
			return route, err
		}
		entry, err := s.planDestinationEntry(ctx, root, mapID, from, to, speed, gravity)
		if err != nil {
			return route, err
		}
		route.setEntry(entry)
	}
	route.target, route.mapID, route.expected, route.owner.gravity = to, mapID, from, gravity
	return route, nil
}
