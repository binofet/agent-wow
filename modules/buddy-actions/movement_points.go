package buddyactions

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"math"
	"sort"
	"time"
)

// This is observation data owned by the accepted Lua, not an action, lease or
// autonomous task. Absolute expiry is never extended by reads or repeated input.
type movementPointInput struct {
	session     string
	self, visit uint64
	mapID       uint32
	revision    uint64
	points      []*actionsv1.MovementPoint
	reason      string
}

func (s *Service) SetMovementPoints(ctx context.Context, r *actionsv1.MovementPointsRequest) (*actionsv1.State, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetVisitId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "current body and visit required")
	}
	// No spell-specific lifetime: Lua owns the effect duration. Milliseconds must
	// fit the signed time domain; already expired entries are harmlessly omitted.
	r = proto.Clone(r).(*actionsv1.MovementPointsRequest)
	points := make([]*actionsv1.MovementPoint, 0, len(r.Points))
	for _, p := range r.Points {
		if p == nil || !finite(position{x: p.X, y: p.Y, z: p.Z}) || math.IsNaN(float64(p.RadiusYards)) || math.IsInf(float64(p.RadiusYards), 0) || p.RadiusYards <= 0 || p.ExpiresAtUnixMs == 0 || p.ExpiresAtUnixMs > math.MaxInt64 {
			return nil, status.Error(codes.InvalidArgument, "finite point, positive radius and absolute Unix-ms expiry required")
		}
		points = append(points, proto.Clone(p).(*actionsv1.MovementPoint))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.trustedEncounterHostLocked(ctx) {
		return nil, status.Error(codes.PermissionDenied, "current attached encounter host required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	// Clearing data grants no movement; source reload may occur after its lease expires.
	if s.closed || len(points) != 0 && !s.hasPolicyEncounterAccessLocked(ctx) || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID || r.VisitId != s.encounterVisit.revision || r.MapId != s.encounterVisit.mapID {
		return nil, status.Error(codes.FailedPrecondition, "current authenticated body, map and visit required")
	}
	w, err := s.readWorldSnapshot(ctx)
	if err != nil || !s.matchesCurrentEncounterVisitLocked(w) || w.GetSelf().GetMapId() != r.MapId || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID || r.VisitId != s.encounterVisit.revision || len(points) != 0 && !s.hasPolicyEncounterAccessLocked(ctx) {
		return nil, status.Error(codes.FailedPrecondition, "current World body and visit required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	// Retain the exact freshly validated public observation used for this data update.
	s.lastWorld = w
	// Input order and repeated polling do not create new geometry. Keep a
	// stable revision for an admitted prefix while the same absolute points
	// are republished; expired rows cannot rearm or disturb that input.
	now := s.now().UnixMilli()
	active := points[:0]
	for _, point := range points {
		if int64(point.ExpiresAtUnixMs) > now {
			active = append(active, point)
		}
	}
	points = active
	sort.Slice(points, func(i, j int) bool {
		a, b := points[i], points[j]
		if a.X != b.X {
			return a.X < b.X
		}
		if a.Y != b.Y {
			return a.Y < b.Y
		}
		if a.Z != b.Z {
			return a.Z < b.Z
		}
		if a.RadiusYards != b.RadiusYards {
			return a.RadiusYards < b.RadiusYards
		}
		if a.GroundEffectGuid != b.GroundEffectGuid {
			return a.GroundEffectGuid < b.GroundEffectGuid
		}
		return a.ExpiresAtUnixMs < b.ExpiresAtUnixMs
	})
	previous := s.rawMovementPointsLocked()
	changed := s.movementPoints.revision == 0 || len(previous) != len(points)
	if !changed {
		for i := range points {
			if !proto.Equal(previous[i], points[i]) {
				changed = true
				break
			}
		}
	}
	revision := s.movementPoints.revision
	if changed {
		revision++
	}
	s.movementPoints = movementPointInput{session: r.SessionId, self: r.SelfGuid, visit: r.VisitId, mapID: r.MapId, revision: revision, points: points, reason: "observed_input_only"}
	return s.stateLocked(), nil
}

func (s *Service) rawMovementPointsLocked() []*actionsv1.MovementPoint {
	p := &s.movementPoints
	if p.session != s.sessionID || p.self != s.selfGUID || p.visit != s.encounterVisit.revision || !s.encounterVisit.bound || p.mapID != s.encounterVisit.mapID {
		return nil
	}
	now := s.now().UnixMilli()
	out := make([]*actionsv1.MovementPoint, 0, len(p.points))
	for _, v := range p.points {
		if int64(v.ExpiresAtUnixMs) > now {
			out = append(out, v)
		}
	}
	return out
}

// Match only this body's current public World observation. Ordinary authored
// points never acquire unit-size or sphere semantics implicitly.
func (s *Service) observedPointRadiusLocked(p *actionsv1.MovementPoint) (float32, bool) {
	w := s.lastWorld
	if !s.matchesCurrentEncounterVisitLocked(w) || !w.GroundEffectsSupported || w.GroundEffectsOverflow {
		return 0, false
	}
	own := w.GetSelf()
	if own.GetGuid() != s.selfGUID || !own.GetPresent() || !own.GetMapKnown() || own.GetMapId() != s.encounterVisit.mapID || !own.GetCombatReachKnown() || math.IsNaN(float64(own.CombatReach)) || math.IsInf(float64(own.CombatReach), 0) || own.CombatReach < 0 {
		return 0, false
	}
	for _, effect := range w.GroundEffects {
		if effect != nil && effect.Guid == p.GroundEffectGuid && effect.X == p.X && effect.Y == p.Y && effect.Z == p.Z && effect.RadiusYards == p.RadiusYards {
			radius := p.RadiusYards + own.CombatReach
			return radius, !math.IsInf(float64(radius), 0) && radius > 0
		}
	}
	return 0, false
}
func (s *Service) movementPointObservedUnknownLocked() bool {
	for _, p := range s.rawMovementPointsLocked() {
		if p.GroundEffectGuid != 0 {
			if _, ok := s.observedPointRadiusLocked(p); !ok {
				return true
			}
		}
	}
	return false
}
func (s *Service) currentMovementPointsLocked() []*actionsv1.MovementPoint {
	out := make([]*actionsv1.MovementPoint, 0)
	for _, p := range s.rawMovementPointsLocked() {
		if p.GroundEffectGuid != 0 {
			radius, ok := s.observedPointRadiusLocked(p)
			// Unknown object/reach is not an exclusion with invented dimensions, nor
			// a veto of unrelated follow or care. Explicit escape refuses below.
			if !ok {
				continue
			}
			p = proto.Clone(p).(*actionsv1.MovementPoint)
			p.RadiusYards = radius
		}
		out = append(out, p)
	}
	return out
}
func (s *Service) movementPointsStateLocked() *actionsv1.MovementPointsState {
	if s.movementPoints.revision == 0 {
		return nil
	}
	p := s.currentMovementPointsLocked()
	reason := s.movementPoints.reason
	if reason == "observed_input_only" && s.posKnown {
		reason = "clear"
		if pointClearance(s.pos, p) < 0 {
			reason = "inside"
		}
	}
	if s.movementPointObservedUnknownLocked() {
		reason = "observed_effect_clearance_unknown"
	} else if len(p) == 0 {
		reason = "no_current_unexpired_points"
	} else if s.state.CastPending {
		reason = "protected_cast_pending"
	} else if s.state.MotionUnconfirmed {
		reason = "motion_unknown"
	}
	return &actionsv1.MovementPointsState{Revision: s.movementPoints.revision, PointCount: uint32(len(p)), Active: len(p) > 0, Reason: reason}
}

func pointClearance(p position, points []*actionsv1.MovementPoint) float64 {
	best := math.Inf(1)
	for _, v := range points {
		if v.GroundEffectGuid != 0 {
			best = math.Min(best, math.Sqrt(math.Pow(float64(p.x-v.X), 2)+math.Pow(float64(p.y-v.Y), 2)+math.Pow(float64(p.z-v.Z), 2))-float64(v.RadiusYards))
		} else if math.Abs(float64(p.z-v.Z)) <= 2 {
			best = math.Min(best, math.Hypot(float64(p.x-v.X), float64(p.y-v.Y))-float64(v.RadiusYards))
		}
	}
	return best
}

// For an outside start every segment stays outside. From inside overlapping
// disks a verified route may pass closer to a redundant center on its way out.
// Its endpoint must improve union clearance; actual input prefixes need the
// same route and unchanged-geometry proof below, not per-circle monotonicity.
func pointPathClear(path []position, points []*actionsv1.MovementPoint) bool {
	if len(path) < 2 {
		return false
	}
	initial := pointClearance(path[0], points)
	if initial < 0 {
		return pointClearance(path[len(path)-1], points) > initial+.01
	}
	floor := float64(0)
	for i := 1; i < len(path); i++ {
		a, b := path[i-1], path[i]
		for _, v := range points {
			if v.GroundEffectGuid != 0 {
				dx, dy, dz := float64(b.x-a.x), float64(b.y-a.y), float64(b.z-a.z)
				den := dx*dx + dy*dy + dz*dz
				t := 0.0
				if den > 0 {
					t = math.Max(0, math.Min(1, (float64(v.X-a.x)*dx+float64(v.Y-a.y)*dy+float64(v.Z-a.z)*dz)/den))
				}
				clearance := math.Sqrt(math.Pow(float64(a.x-v.X)+t*dx, 2)+math.Pow(float64(a.y-v.Y)+t*dy, 2)+math.Pow(float64(a.z-v.Z)+t*dz, 2)) - float64(v.RadiusYards)
				if clearance < 0 {
					return false
				}
				continue
			}
			if math.Min(float64(a.z), float64(b.z)) > float64(v.Z)+2 || math.Max(float64(a.z), float64(b.z)) < float64(v.Z)-2 {
				continue
			}
			dx, dy := float64(b.x-a.x), float64(b.y-a.y)
			den := dx*dx + dy*dy
			t := 0.0
			if den > 0 {
				t = math.Max(0, math.Min(1, (float64(v.X-a.x)*dx+float64(v.Y-a.y)*dy)/den))
			}
			clearance := math.Hypot(float64(a.x-v.X)+t*dx, float64(a.y-v.Y)+t*dy) - float64(v.RadiusYards)
			if clearance < floor-0.001 {
				return false
			}
		}
	}
	return initial >= 0 || pointClearance(path[len(path)-1], points) > initial+0.01
}

// Reuse the ordinary navmesh planner and existing movement writer. A local
// detour is not a new task: the original goal remains with its existing caller.
func (s *Service) movementPointRouteLocked(ctx context.Context, path []position, constraints ...func([]position) bool) ([]position, bool) {
	points := s.currentMovementPointsLocked()
	if s.recoveryOwnsLocked() || s.lastWorld.GetSelf().GetAliveKnown() && !s.lastWorld.GetSelf().GetAlive() {
		return path, true
	}
	if len(points) == 0 {
		return path, true
	}
	lookahead := path
	if len(path) > 1 {
		budget := observedRunSpeed(s.lastWorld.GetSelf()) * .6
		if budget > 0 {
			end, index, ok := followPathStep(path[0], path, 1, budget)
			if ok {
				lookahead = append(append([]position(nil), path[:index]...), end)
			}
		}
	}
	acceptable := func(route []position) bool { return len(constraints) == 0 || constraints[0](route) }
	initial := pointClearance(s.pos, points)
	immediate := func(route []position) bool {
		if initial >= 0 {
			return true
		}
		end, _, ok := followPathStep(s.pos, route, 1, observedRunSpeed(s.lastWorld.GetSelf())*.2)
		return ok && pointClearance(end, points) >= initial-.001
	}
	var escapeFallback []position
	if pointPathClear(lookahead, points) && acceptable(lookahead) && !immediate(path) {
		escapeFallback = path
	}
	if pointPathClear(lookahead, points) && acceptable(lookahead) && immediate(path) {
		s.movementPoints.reason = "already_clear"
		if pointClearance(path[0], points) < 0 {
			s.movementPoints.reason = "escaping_existing_route"
		}
		return path, true
	}
	start := s.pos
	originalGoal := path[len(path)-1]
	// Stop only when the upcoming leg is blocked and its actual destination
	// is inside an active disk; a distant blocked goal does not freeze travel.
	if initial >= 0 && pointClearance(originalGoal, points) < 0 {
		s.movementPoints.reason = "goal_inside_active_point"
		return nil, false
	}
	var bestOutside []position
	bestCost := math.Inf(1)
	// Candidates are physical waypoints only; every complete route is checked by
	// the same ground planner, then against the supplied exclusions.
	planCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	for _, d := range [][2]float64{{1, 0}, {0, 1}, {-1, 0}, {0, -1}, {.70710678, .70710678}, {-.70710678, .70710678}, {-.70710678, -.70710678}, {.70710678, -.70710678}} {
		candidateGoal := start
		candidateGoal.x += float32(d[0] * 6)
		candidateGoal.y += float32(d[1] * 6)
		var candidate []position
		var err error
		if retained, ok := ctx.Value(followCorridorMovementKey{}).(followCorridor); ok {
			candidate, err = s.followAvoidancePath(planCtx, retained, start, candidateGoal)
		} else {
			candidate, err = s.mapNavigationPath(planCtx, s.navDataDir, s.posMap, start, candidateGoal)
		}
		if err == nil {
			// The ordinary planner permits a nearby snapped start. Include
			// the actual body-to-snap join in circle and first-prefix proofs.
			if distance(start, candidate[0]) > .005 {
				if len(candidate) >= 256 {
					continue
				}
				candidate = append([]position{start}, candidate...)
			} else {
				candidate = append([]position(nil), candidate...)
				candidate[0] = start
			}
		}
		if err == nil && pointPathClear(candidate, points) && acceptable(candidate) {
			if initial >= 0 {
				cost := distance(candidate[len(candidate)-1], originalGoal)
				for i := 1; i < len(candidate); i++ {
					cost += distance(candidate[i-1], candidate[i])
				}
				if cost < bestCost {
					bestOutside, bestCost = candidate, cost
				}
				continue
			}
			if immediate(candidate) {
				s.movementPoints.reason = "avoiding"
				return candidate, true
			}
			if escapeFallback == nil {
				escapeFallback = candidate
			}
		}
		if planCtx.Err() != nil {
			break
		}
	}
	if bestOutside != nil {
		s.movementPoints.reason = "avoiding"
		return bestOutside, true
	}
	// Inside overlapping disks the only walkable exit can briefly approach
	// another center. Use its proven route prefix only when no immediately
	// improving navigable alternative was found; do not declare the exit absent.
	if escapeFallback != nil {
		s.movementPoints.reason = "escape_prefix_selected"
		return escapeFallback, true
	}
	s.movementPoints.reason = "no_navigable_escape"
	return nil, false
}

type movementPointsBodyGuardKey struct{}
type movementPointsEscapePathKey struct{}
type movementPointsEscapePath struct {
	revision uint64
	path     []position
}

// Only a prefix of this same complete, verified escape route can use the
// inside-union exception. This is route evidence, never movement authority.
func movementPointFirstStraight(path []position) (position, position, bool) {
	if len(path) < 2 {
		return position{}, position{}, false
	}
	start := path[0]
	i := 1
	for i < len(path) && distance(start, path[i]) <= .005 {
		i++
	}
	if i == len(path) {
		return position{}, position{}, false
	}
	end := path[i]
	for i++; i < len(path); i++ {
		if !followCollinear(start, end, path[i]) {
			break
		}
		end = path[i]
	}
	return start, end, true
}

func (s *Service) movementPointEscapePrefix(proof *movementPointsEscapePath, p position) bool {
	if proof == nil || proof.revision != s.movementPoints.revision {
		return false
	}
	a, b, ok := movementPointFirstStraight(proof.path)
	if !ok {
		return false
	}
	points := s.currentMovementPointsLocked()
	if pointClearance(a, points) >= 0 || !pointPathClear(proof.path, points) {
		return false
	}
	dx, dy, dz := float64(b.x-a.x), float64(b.y-a.y), float64(b.z-a.z)
	den := dx*dx + dy*dy + dz*dz
	t := (float64(p.x-a.x)*dx + float64(p.y-a.y)*dy + float64(p.z-a.z)*dz) / den
	if t < -.000001 || t > 1.000001 {
		return false
	}
	t = math.Max(0, math.Min(1, t))
	nearest := position{x: a.x + float32(t*dx), y: a.y + float32(t*dy), z: a.z + float32(t*dz)}
	return distance(nearest, p) <= .005
}

// StepMovementPoints is one Lua-requested normal bounded body input. It shares
// MoveBody's operation sequence, physics samples, final identity checks and
// UNKNOWN ledger; no autonomous continuation or cast cancellation is added.
func (s *Service) StepMovementPoints(ctx context.Context, r *actionsv1.MovementPointsStepRequest) (*actionsv1.State, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetVisitId() == 0 || r.GetSequenceId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "current body, visit and exact operation sequence required")
	}
	r = proto.Clone(r).(*actionsv1.MovementPointsStepRequest)
	band := r.TargetConstraints
	if band != nil && (band.SessionId != r.SessionId || band.SelfGuid != r.SelfGuid || band.VisitId != r.VisitId || band.TargetGuid == 0 || band.Stop || band.Behind || band.RequireLos || math.IsNaN(float64(band.RangeYards)) || math.IsInf(float64(band.RangeYards), 0) || band.RangeYards <= 0 || !validTargetBandRequest(band)) {
		return nil, status.Error(codes.InvalidArgument, "matching exact body and supported target-space constraints required")
	}
	s.mu.Lock()
	s.expireMovementOwnerLocked()
	ownerCurrent := func() bool {
		return (s.movementOwner.owner == "" && r.MovementOwner == "" && r.MovementToken == "") || (r.MovementOwner != "" && r.MovementToken != "" && s.movementOwner.owner == r.MovementOwner && s.movementOwner.token == r.MovementToken && s.now().Before(s.movementOwner.until))
	}
	if !s.hasPolicyEncounterAccessLocked(ctx) || r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID || r.VisitId != s.encounterVisit.revision || r.MapId != s.encounterVisit.mapID || !ownerCurrent() {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "current Lua body, visit and exact movement custody required")
	}
	w, err := s.snapshot(ctx)
	if err != nil || !s.matchesCurrentEncounterVisitLocked(w) || !s.refreshPositionLocked(w) {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "current own ground pose required")
	}
	if s.state.CastPending || s.state.MotionUnconfirmed || s.bodyMoving || s.state.Traveling || s.state.InteractionOwner != "" || s.recovery.state.Active {
		s.movementPoints.reason = "protected_operation_pending"
		out := s.stateLocked()
		s.mu.Unlock()
		return out, nil
	}
	if s.movementPointObservedUnknownLocked() {
		s.movementPoints.reason = "observed_effect_clearance_unknown"
		out := s.stateLocked()
		s.mu.Unlock()
		return out, nil
	}
	points := s.currentMovementPointsLocked()
	start := s.pos
	if len(points) == 0 || pointClearance(start, points) >= 0 {
		s.movementPoints.reason = "already_clear"
		out := s.stateLocked()
		s.mu.Unlock()
		return out, nil
	}
	// Find a physical outward route, then use only its first straight segment.
	// MoveBody separately proves the actual bounded input's complete physics.
	bandCurrent := func(path []position) bool {
		if band == nil {
			return true
		}
		fresh, err := s.snapshot(ctx, band.TargetGuid)
		target, known := targetMovementPosition(fresh, band.TargetGuid, r.MapId)
		hazards, complete := targetClearancePositions(fresh, band.ClearanceTargetGuids, r.MapId)
		return err == nil && known && complete && s.matchesCurrentEncounterVisitLocked(fresh) && targetBandPathValid(path, start, target, float64(band.MinRangeYards), float64(band.RangeYards), hazards, float64(band.ClearanceYards))
	}
	route, ok := s.movementPointRouteLocked(ctx, []position{start, start}, bandCurrent)
	if !ok {
		if band != nil {
			s.movementPoints.reason = "hard_constraints_or_navigation_blocked"
		}
		out := s.stateLocked()
		s.mu.Unlock()
		return out, nil
	}
	_, firstEnd, hasPrefix := movementPointFirstStraight(route)
	speed := observedRunSpeed(s.lastWorld.GetSelf())
	if !hasPrefix || speed <= 0 {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "known speed and a verified outward ground prefix required")
	}
	// Never run a fixed input through the route's first turn. The same
	// ordinary input duration may be shortened, never extended or teleported.
	duration := math.Floor(math.Min(200, followHorizontalDistance(start, firstEnd)/speed*1000))
	if duration < 1 {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "verified escape prefix is shorter than one normal input millisecond")
	}
	bearing := math.Remainder(math.Atan2(float64(firstEnd.y-start.y), float64(firstEnd.x-start.x))-float64(start.o), 2*math.Pi)
	proof := &movementPointsEscapePath{revision: s.movementPoints.revision, path: route}
	ctx = context.WithValue(ctx, movementPointsBodyGuardKey{}, func(p position, moving bool) bool {
		// STOP is cleanup of this exact input, not use of a renewed or
		// foreign lease. MoveBody independently rechecks body/visit/epoch.
		if !moving {
			return r.VisitId == s.encounterVisit.revision && r.MapId == s.posMap
		}
		if !ownerCurrent() || r.VisitId != s.encounterVisit.revision || r.MapId != s.posMap || observedRunSpeed(s.lastWorld.GetSelf()) != speed || s.movementPointObservedUnknownLocked() {
			return false
		}
		if distance(start, p) < .001 {
			return true
		}
		return (pointPathClear([]position{start, p}, s.currentMovementPointsLocked()) || s.movementPointEscapePrefix(proof, p)) && bandCurrent([]position{start, p})
	})
	s.mu.Unlock()
	return s.MoveBody(ctx, &actionsv1.BodyMovementRequest{SessionId: r.SessionId, SelfGuid: r.SelfGuid, VisitId: r.VisitId, SequenceId: r.SequenceId, BearingRadians: float32(bearing), DurationMs: uint32(duration)})
}
