package buddyactions

import (
	"context"
	"errors"
	"math"
	"math/bits"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type surfaceMotionSource struct {
	goal                                 *actionsv1.FollowFormationRequest
	body                                 *surfaceBodyInput
	ship                                 uint64
	rawRevision, objectRevision          uint64
	progress, firstProgress, clientClock uint32
	hostEpoch                            uint64
	lastTransform                        buddynav.SurfaceTransform
	ownerPoint                           buddynav.Point
	departureOffset                      float64
	localRoute, airborne, released       bool
}

func (in *surfaceMotionSource) shipGUID() uint64 {
	if in.ship != 0 {
		return in.ship
	}
	return in.goal.GetTransportGuid()
}

func surfaceInfoPoint(i movementinfo.Info) buddynav.Point {
	return buddynav.Point{X: i.X, Y: i.Y, Z: i.Z}
}
func surfaceLocalPoint(i movementinfo.Info) buddynav.Point {
	return buddynav.Point{X: i.Transport.X, Y: i.Transport.Y, Z: i.Transport.Z}
}
func surfaceDistance(a, b buddynav.Point) float64 {
	return distance(surfacePosition(a, 0), surfacePosition(b, 0))
}
func surfaceBearing(a, b buddynav.Point) float64 {
	return math.Atan2(float64(b.Y-a.Y), float64(b.X-a.X))
}

func surfaceOwnerPoint(w *worldv1.SnapshotResponse) buddynav.Point {
	i := w.GetOwner().GetPassengerMovement().GetInfo()
	if t := i.GetTransport(); t != nil {
		return buddynav.Point{X: t.X, Y: t.Y, Z: t.Z}
	}
	return buddynav.Point{X: i.GetX(), Y: i.GetY(), Z: i.GetZ()}
}

func (s *Service) surfaceGoalCurrentLocked(w *worldv1.SnapshotResponse, g *actionsv1.FollowFormationRequest) bool {
	sf := &s.surfaceFollow
	o := w.GetOwner()
	passenger, ok := receivedPassenger(o, w.GetVisitRevision())
	if g == nil || !ok || !s.hostEncounterActiveLocked() || sf.hostEpoch != s.hostEncounterEpoch || !s.now().Before(sf.until) ||
		s.closed || s.independent || s.sessionID != g.SessionId || s.selfGUID != g.SelfGuid || s.followRevision != g.FollowRevision ||
		!s.matchesCurrentEncounterVisitLocked(w) || g.VisitId != w.VisitRevision || g.OwnerGuid != o.GetGuid() || o.GetMapId() != w.GetSelf().GetMapId() ||
		!o.GetMapKnown() || !o.GetPositionKnown() {
		return false
	}
	ownerShip := passenger.Info.GetTransport().GetGuid()
	// The setter admits an exact received frame. A later heartbeat refreshes
	// that same owner's pose; it does not revoke the finite follow intent.
	return g.SurfaceMode == "leave" && ownerShip == 0 || (g.SurfaceMode == "board" || g.SurfaceMode == "ride") && ownerShip == g.TransportGuid
}

func (s *Service) continuesDetachedSurfaceLocked(w *worldv1.SnapshotResponse, in *surfaceMotionSource) bool {
	return in.goal != nil && s.surfaceFollow.goal == nil && s.surfaceFollow.planDetached && in.hostEpoch == s.hostEncounterEpoch &&
		s.followRevision == in.goal.FollowRevision && s.currentFollowFormationGoalLocked(s.followGoal, w) && s.followGoal.owner == in.goal.OwnerGuid
}

func (s *Service) surfaceOwnMotionAllowed(w *worldv1.SnapshotResponse) bool {
	p := w.GetSelf()
	if p == nil || p.Guid != s.selfGUID || !p.Present || !p.MapKnown || !p.PositionKnown || !p.AliveKnown || !p.Alive || !p.MovementFlagsKnown ||
		!p.UnitFlagsKnown || p.UnitFlags_2&(movementflags.ForcedOrFeign|movementflags.CannotTurn) != 0 || p.GetChannelSpellId() != 0 ||
		s.closed || s.clientControlLost || s.clientControlRestoring || s.state.CastPending || s.state.MotionUnconfirmed {
		return false
	}
	flags := p.UnitFlags
	if flags&movementflags.Mounted != 0 {
		if id, _ := s.groundMountAuraLocked(p.Guid); id == 0 {
			return false
		}
		flags &^= movementflags.Mounted
	}
	return flags&movementflags.UnsafeUnitFlags == 0
}

// The first frame uses an actual complete own packet or the physical STOP
// establishing the current client-estimated pose. Every continuation uses
// the exact per-frame callback receipt.
func (s *Service) surfaceMotionSourceLocked(ctx context.Context, w *worldv1.SnapshotResponse) (movementinfo.Info, *modv1.TransportScope, error) {
	var zero movementinfo.Info
	scope := portalObservedWorldScope(w, s.selfGUID)
	if scope == nil || s.session == nil || !s.matchesCurrentEncounterVisitLocked(w) {
		return zero, nil, errors.New("current body and physical visit required")
	}
	b := s.surfaceFollow.body
	if b != nil && b.visit == w.VisitRevision && b.rawPositionRevision == w.Self.PositionRevision {
		r, err := s.currentSurfaceReportLocked(ctx, w)
		if err != nil {
			return zero, nil, err
		}
		return b.info, r.TransportScope, nil
	}
	if w.Self.PositionInferred || s.posEstimated && s.posMap == w.Self.MapId && s.positionRevision == w.Self.PositionRevision {
		r, err := modcb.ReadSettledBodySource(ctx, s.session, s.sessionID, s.selfGUID)
		if err != nil {
			return zero, nil, err
		}
		i, ok := movementinfo.OwnFrame(r.Payload, s.selfGUID)
		if !ok || r.Opcode != opcode.MSGMoveStop || !s.posKnown || s.positionRevision != w.Self.PositionRevision || s.posMap != w.Self.MapId ||
			!samePortalCoreVisit(scope, r.TransportScope) || (position{i.X, i.Y, i.Z, i.Orientation}) != s.pos || i.Flags&^passengerFlag != 0 {
			return zero, nil, errors.New("current own stopped source required")
		}
		if scope.MotionRevision < r.TransportScope.MotionRevision {
			scope = r.TransportScope
		}
		return i, scope, nil
	}
	i, ok := s.receivedSurfaceSource(w, scope)
	if !ok {
		return zero, nil, errors.New("current received own MovementInfo required")
	}
	return i, scope, nil
}

func (s *Service) receivedSurfaceSource(w *worldv1.SnapshotResponse, scope *modv1.TransportScope) (movementinfo.Info, bool) {
	raw := w.GetSelfMovementFacts()
	i, n, err := movementinfo.DecodePrefix(raw.GetWireImage())
	if err != nil || n != len(raw.GetWireImage()) || !i.AllFinite() || !raw.GetKnown() || raw.GetSelfGuid() != s.selfGUID ||
		w.GetSelf() == nil || w.Self.PositionInferred || raw.GetVisitRevision() != w.VisitRevision || !raw.GetMapKnown() || raw.GetMapId() != w.Self.MapId || !samePortalCoreVisit(scope, raw.GetTransportScope()) ||
		!w.Self.MovementFlagsKnown || raw.Info == nil || i.Flags != w.Self.MovementFlags || uint32(i.ExtraFlags) != w.Self.ExtraMovementFlags ||
		(position{i.X, i.Y, i.Z, i.Orientation}) != playerPosition(w.Self) {
		return movementinfo.Info{}, false
	}
	t := raw.Info.GetTransport()
	if i.TransportPresent != (t != nil) || t != nil && (t.Guid != i.Transport.GUID ||
		t.X != i.Transport.X || t.Y != i.Transport.Y || t.Z != i.Transport.Z || t.Orientation != i.Transport.Orientation ||
		t.Time != i.Transport.Time || t.Seat != int32(i.Transport.Seat) || t.Time2Known != i.Transport.Time2Present ||
		t.Time2Known && t.Time2 != i.Transport.Time2) {
		return movementinfo.Info{}, false
	}
	return i, true
}

// A map boundary ends the old steering request, including committed samples
// in the old coordinate system. Keep the original packet, attempt, and
// delivery status. A new map's movement needs its own policy lease and goal.
func (s *Service) retireSurfaceGoalLocked() {
	sf := &s.surfaceFollow
	if sf.goal == nil && sf.planSource == nil && sf.body == nil {
		return
	}
	sf.goal, sf.plan, sf.planSource = nil, nil, nil
	sf.exitSearch = buddynav.SurfaceDepartureSearch{}
	sf.phase = "surface_waiting_for_receiving_body"
}

func (s *Service) priorPassengerMovingLocked() bool {
	b := s.surfaceFollow.body
	return b != nil && b.info.TransportPresent && (s.state.Moving || s.state.MotionUnconfirmed) &&
		(!s.encounterVisit.bound || b.visit != s.encounterVisit.revision)
}

// Only an independently received stopped body resolves physical custody.
// The earlier uncertain send and its original attempt remain historical.
func (s *Service) reconcileSurfaceMotionLocked(w *worldv1.SnapshotResponse) bool {
	sf := &s.surfaceFollow
	b := sf.body
	if b == nil || b.packet == nil ||
		!s.matchesCurrentEncounterVisitLocked(w) || w.GetSelf().GetGuid() != s.selfGUID ||
		!w.Self.GetPositionKnown() || !w.Self.GetAliveKnown() || !w.Self.GetAlive() {
		return false
	}
	uncertain := b.uncertain && s.state.MotionUnconfirmed
	previousVisit := !b.uncertain && !s.state.MotionUnconfirmed && s.priorPassengerMovingLocked()
	if !uncertain && !previousVisit {
		return false
	}
	scope := portalObservedWorldScope(w, s.selfGUID)
	i, ok := s.receivedSurfaceSource(w, scope)
	if !ok || i.Flags != 0 && i.Flags != passengerFlag && i.Flags != swimMovementFlag || i.ExtraFlags != 0 ||
		i.JumpPresent || i.TransportPresent && (!buddynav.IsMovingTransportGUID(i.Transport.GUID) || i.Transport.Seat != -1 || i.Transport.Time2Present) {
		return false
	}
	old, current := b.packet.TransportScope, w.SelfMovementFacts.TransportScope
	if b.visit == w.VisitRevision {
		if w.Self.PositionRevision <= b.rawPositionRevision || !samePortalCoreVisit(old, current) || current.MotionRevision <= old.MotionRevision {
			return false
		}
	} else if w.VisitRevision <= b.visit || old.GetConnectionId() == "" || old.ConnectionId != current.GetConnectionId() ||
		old.SelfGuid != s.selfGUID || !old.VisitBound || current.GetVisitRevision() <= old.VisitRevision {
		return false
	}
	if b.visit != w.VisitRevision && b.info.TransportPresent && i.TransportPresent {
		frame, known := receivedPassenger(w.Self, w.VisitRevision)
		hull := movingHull(w, i.Transport.GUID)
		if !known || i.Transport.GUID != b.info.Transport.GUID || frame.Info.Time != i.Time ||
			frame.Info.Flags != i.Flags || (position{frame.Info.X, frame.Info.Y, frame.Info.Z, frame.Info.Orientation}) != playerPosition(w.Self) ||
			!proto.Equal(frame.Info, w.SelfMovementFacts.Info) || hull == nil || hull.MovingTransport.ObjectRevision == 0 ||
			frame.Revision < hull.MovingTransport.ObjectRevision {
			return false
		}
	}
	s.pos, s.posKnown, s.positionRevision = playerPosition(w.Self), true, w.Self.PositionRevision
	s.observePositionLocked(w.Self)
	s.state.Moving, s.state.MotionUnconfirmed = false, false
	sf.plan, sf.planSource = nil, nil
	return true
}

func (s *Service) surfaceHold(reason string, err error) bool {
	s.surfaceFollow.phase = reason
	s.state.Reason = reason
	if err != nil {
		s.state.NavReason = boundedPolicyDiagnostic(err.Error(), 240)
	} else {
		s.state.NavReason = ""
	}
	return true
}

func dockInput(path *buddynav.TransportPath, progress uint32, duration time.Duration) bool {
	if path == nil || duration < 0 {
		return false
	}
	start := uint64(progress)
	end := start + uint64((duration+time.Millisecond-1)/time.Millisecond)
	a, ea := path.Sample(start)
	b, eb := path.Sample(end)
	return end <= math.MaxUint32 && ea == nil && eb == nil && a.Stopped && b.Stopped && a.MapID == b.MapID && a.Position == b.Position && !a.Transfer && !path.CrossesTransfer(start, end)
}

func hullFoot(ctx context.Context, model *buddynav.TransportSurface, q buddynav.Point) (bool, error) {
	z, hit, err := model.Height(ctx, q, .035, .035)
	return hit && math.Abs(float64(z-q.Z)) <= .035, err
}

// The local mesh supplies every real turn on deck, including its measured Z.
// Limit the retained input by actual ground speed. The next decision can
// continue from its exact terminal passenger frame.
func deckInput(route []buddynav.Point, t buddynav.SurfaceTransform, speed, stop float64) []buddynav.BodyMotionSample {
	if len(route) < 2 || speed <= 0 {
		return nil
	}
	path := make([]position, len(route))
	for i, q := range route {
		path[i] = surfacePosition(q, 0)
	}
	p := path[0]
	idx := 1
	points := []buddynav.BodyMotionSample{{Point: t.World(route[0])}}
	for at := time.Duration(0); idx < len(path) && at < 500*time.Millisecond; {
		remaining := distance(p, path[len(path)-1]) - stop
		if remaining <= .01 {
			break
		}
		budget := math.Min(remaining, math.Min(.1, (500*time.Millisecond-at).Seconds())*speed)
		next, n, walked, ok := followStride(p, path, idx, budget, 2)
		if !ok || walked <= .001 {
			break
		}
		at += time.Duration(walked / speed * float64(time.Second))
		p, idx = next, n
		points = append(points, buddynav.BodyMotionSample{At: at, Point: t.World(surfacePoint(next))})
	}
	return points
}

// A ship moves between physical frames. Sweep in each endpoint's actual
// local frame; both the body radius and height participate in the check.
func surfaceHullSweep(ctx context.Context, m *buddynav.TransportSurface, a, b buddynav.Point) error {
	bearing := surfaceBearing(a, b)
	for _, side := range []float64{0, -.25, .25} {
		dx, dy := float32(-math.Sin(bearing)*side), float32(math.Cos(bearing)*side)
		for _, height := range []float32{.1, .9, 1.7} {
			from, to := a, b
			from.X += dx
			from.Y += dy
			from.Z += height
			to.X += dx
			to.Y += dy
			to.Z += height
			clear, err := m.Clear(ctx, from, to)
			if err != nil {
				return err
			}
			if !clear {
				return errors.New("moving hull intersects measured body flight")
			}
		}
	}
	return nil
}

// An attempted exit must clear the translating ship for the entire measured
// input before takeoff. Each emitted frame repeats its segment check against
// the latest hull lifetime and observed client clock.
func checkSurfaceDeparture(ctx context.Context, m *buddynav.TransportSurface, path *buddynav.TransportPath, first buddynav.SurfaceTransform, progress uint32, plan []buddynav.BodyMotionSample) error {
	last := first.Local(plan[0].Point)
	for _, row := range plan[1:] {
		at := uint64(progress) + uint64(row.At/time.Millisecond)
		sample, err := path.Sample(at)
		if err != nil || at > math.MaxUint32 || sample.Transfer || path.CrossesTransfer(uint64(progress), at) {
			return errors.New("departure crosses an unobserved hull transfer")
		}
		tr := first
		if !dockInput(path, progress, row.At) {
			tr = buddynav.SurfaceTransform{Origin: sample.Position, Yaw: sample.Yaw, Scale: first.Scale}
		}
		local := tr.Local(row.Point)
		if err := surfaceHullSweep(ctx, m, last, local); err != nil {
			return err
		}
		if row.FallTime > 0 && local.Z < last.Z {
			if _, hit, err := surfaceRecontact(ctx, m, last, local); err != nil {
				return err
			} else if hit {
				return errors.New("the moving deck intercepts the exit before its exterior landing")
			}
		}
		last = local
	}
	return nil
}

func (s *Service) surfaceFollowTickLocked(ctx context.Context, w *worldv1.SnapshotResponse, now time.Time) bool {
	sf := &s.surfaceFollow
	if s.priorPassengerMovingLocked() && !s.reconcileSurfaceMotionLocked(w) {
		return s.surfaceHold("surface_waiting_for_receiving_body", nil)
	}
	g := sf.goal
	in := sf.planSource
	if in != nil && in.body != nil {
		return true // Its synchronous MoveBody call owns every frame and the STOP.
	}
	if g == nil && in == nil {
		return false
	}
	active := in != nil && len(sf.plan) > 0
	committed := active && in.airborne
	if active && (w.GetVisitRevision() != in.goal.VisitId || w.GetSelf().GetPositionRevision() != in.rawRevision) {
		sf.plan, sf.planSource = nil, nil
		return s.surfaceHold("surface_body_source_changed", nil)
	}
	current := s.surfaceGoalCurrentLocked(w, g)
	sameInput := current && (!active || in.goal != nil && g.SurfaceMode == in.goal.SurfaceMode && g.TransportGuid == in.goal.TransportGuid && g.OwnerGuid == in.goal.OwnerGuid && in.hostEpoch == s.hostEncounterEpoch)
	if sameInput && active && surfaceDistance(in.ownerPoint, surfaceOwnerPoint(w)) > g.StopDistance {
		sameInput = false
	}
	if !sameInput {
		if committed {
			// Ordinary dry/wet formation for the same owner may replace the
			// surface directive as the body clears the deck. An explicit stop,
			// new writer, different owner or expired instruction releases input.
			if !s.continuesDetachedSurfaceLocked(w, in) {
				in.released = true
			}
		} else {
			if _, stopped := s.stopSurfaceLocked(ctx); !stopped {
				return true
			}
			sf.plan, sf.planSource = nil, nil
			if !current {
				sf.goal = nil
			}
			return s.surfaceHold("surface_goal_changed", nil)
		}
	}
	if !s.surfaceOwnMotionAllowed(w) {
		return s.surfaceHold("surface_body_unavailable", nil)
	}
	if committed {
		g = in.goal
	}
	hull := movingHull(w, g.TransportGuid)
	if hull == nil {
		return s.surfaceHold("surface_ship_unavailable", nil)
	}
	if hull.MovingTransport.ObjectRevision == 0 || active && hull.MovingTransport.ObjectRevision != in.objectRevision {
		return s.surfaceHold("surface_ship_creation_changed", nil)
	}
	mesh, err := s.surfaceMeshLocked(ctx, hull)
	if err != nil {
		return s.surfaceHold("surface_model_unavailable", err)
	}
	transform, progress, err := s.surfacePoseLocked(ctx, w, hull)
	if err != nil {
		return s.surfaceHold("surface_clock_unavailable", err)
	}
	own, _, err := s.surfaceMotionSourceLocked(ctx, w)
	if err != nil {
		return s.surfaceHold("surface_own_source_unavailable", err)
	}
	if active {
		return s.advanceSurfaceInputLocked(ctx, w, mesh, transform, progress, own, now)
	}
	if own.TransportPresent && (own.Transport.GUID != g.TransportGuid || own.Transport.Seat != -1 || own.Transport.Time2Present) {
		return s.surfaceHold("surface_attachment_changed", nil)
	}
	if own.Flags&^(passengerFlag|1) != 0 || own.ExtraFlags != 0 || own.JumpPresent || own.PitchPresent {
		return s.surfaceHold("surface_body_mode_unavailable", nil)
	}
	if g.SurfaceMode == "board" && own.TransportPresent || g.SurfaceMode == "leave" && !own.TransportPresent {
		return s.surfaceHold("surface_input_written_waiting_for_follow", nil)
	}
	if g.SurfaceMode == "ride" && !own.TransportPresent {
		return s.surfaceHold("surface_attachment_unavailable", nil)
	}
	speed := observedRunSpeed(w.Self)
	if speed == 0 {
		return s.surfaceHold("surface_run_speed_unavailable", nil)
	}
	start := surfaceInfoPoint(own)
	if own.TransportPresent {
		start = transform.World(surfaceLocalPoint(own))
	}
	ownerPoint := surfaceOwnerPoint(w)
	target := ownerPoint
	if g.SurfaceMode != "leave" {
		target = transform.World(ownerPoint)
	}
	path := sf.paths[hull.Guid]
	localStart, localEnd := transform.Local(start), transform.Local(target)
	floor, err := hullFoot(ctx, mesh.Collision(), localStart)
	if err != nil {
		return s.surfaceHold("surface_support_unavailable", err)
	}
	dockWalk := surfaceDockWalk(g)
	if dockWalk && !dockInput(path, progress, time.Millisecond) {
		return s.surfaceHold("surface_waiting_for_dock", nil)
	}
	if g.SurfaceMode == "board" && floor {
		if corridor, e := mesh.Route(ctx, localStart, localEnd); e == nil && len(corridor) > 0 {
			info := movementinfo.Info{X: start.X, Y: start.Y, Z: start.Z, Orientation: own.Orientation}
			setPassengerFrame(&info, g.TransportGuid, transform, progress)
			if err := s.writeSurfaceInfoLocked(ctx, w, info, opcode.MSGMoveSetFacing); err != nil {
				return s.surfaceWriteFailure(err)
			}
			return s.surfaceHold("surface_contact_written", nil)
		}
	}
	gap := surfaceDistance(start, target)
	if g.SurfaceMode == "ride" && (gap <= g.StopDistance || !s.state.Moving && gap < g.StartDistance) {
		if _, ok := s.stopSurfaceLocked(ctx); !ok {
			return true
		}
		return s.surfaceHold("surface_riding", nil)
	}
	if own.TransportPresent && !floor {
		return s.surfaceHold("surface_deck_support_unknown", nil)
	}
	localRoute := g.SurfaceMode == "ride"
	var points []buddynav.BodyMotionSample
	var departureOffset float64
	bearing := surfaceBearing(start, target)
	if dockWalk {
		points, bearing, localRoute, err = s.planSurfaceDockWalk(ctx, w, g, mesh, transform, progress, start, target, speed)
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return s.surfaceHold("surface_searching_for_dock_walk", err)
		}
		if err != nil {
			return s.surfaceHold("surface_dock_walk_unavailable", err)
		}
	} else if g.SurfaceMode == "leave" {
		// A checked jump from the current deck position can clear the side
		// before the closest projected deck point, especially as the ferry
		// turns. The full moving-hull sweep must reach exterior contact.
		points, _ = s.measureSurfaceCrossing(ctx, w, g, mesh, transform, progress, start, target, speed, true)
		if len(points) == 0 && w.Owner.GetPassengerMovement().GetInfo().GetFlags()&swimMovementFlag != 0 {
			var outward buddynav.Point
			// Leave time for a fresh World read and the scoped socket writer.
			search, cancel := context.WithTimeout(ctx, actionsTickBudget/2)
			route, e := mesh.DepartureRoute(search, localStart, localEnd, func(deck, normal buddynav.Point, length float64) bool {
				approach := time.Duration(length / speed * float64(time.Second))
				at := uint64(progress) + uint64(approach/time.Millisecond)
				if at > math.MaxUint32 || path.CrossesTransfer(uint64(progress), at) {
					return false
				}
				pose, e := path.Sample(at)
				if e != nil || pose.MapID != w.Self.MapId {
					return false
				}
				launch := transform
				if !dockInput(path, progress, approach) {
					launch = buddynav.SurfaceTransform{Origin: pose.Position, Yaw: pose.Yaw, Scale: transform.Scale}
				}
				rows, _ := s.checkSurfaceExit(search, w, g, mesh, launch, uint32(at), launch.World(deck), target, normal, speed)
				if len(rows) == 0 {
					return false
				}
				outward = normal
				return true
			}, &sf.exitSearch)
			cancel()
			if errors.Is(e, context.DeadlineExceeded) && ctx.Err() == nil {
				return s.surfaceHold("surface_searching_for_exit", nil)
			}
			if e != nil {
				return s.surfaceHold("surface_exit_route_unavailable", e)
			}
			if surfaceDistance(localStart, route[len(route)-1]) <= .45 {
				points, departureOffset = s.checkSurfaceExit(ctx, w, g, mesh, transform, progress, start, target, outward, speed)
			}
			if len(points) == 0 {
				points = deckInput(route, transform, speed, 0)
				localRoute = true
			}
		}
		if len(points) == 0 {
			entry, e := mesh.BoardingEntry(ctx, localEnd, localStart)
			if e != nil {
				return s.surfaceHold("surface_exit_route_unavailable", e)
			}
			if surfaceDistance(entry, localStart) > .45 {
				localEnd, localRoute = entry, true
			}
		}
	}
	bearing += departureOffset
	jump := g.Jump && !localRoute && !dockWalk
	if localRoute && len(points) == 0 {
		route, e := mesh.Route(ctx, localStart, localEnd)
		if e != nil {
			return s.surfaceHold("surface_deck_route_unavailable", e)
		}
		stop := 0.0
		if g.SurfaceMode == "ride" {
			stop = g.StopDistance
		}
		points = deckInput(route, transform, speed, stop)
	} else if len(points) == 0 {
		points, err = s.measureSurfaceCrossing(ctx, w, g, mesh, transform, progress, start, target, speed, jump)
		if err != nil {
			return s.surfaceHold("surface_body_path_unavailable", err)
		}
	}
	if len(points) < 2 {
		return s.surfaceHold("surface_route_exhausted", nil)
	}
	if dockWalk && !dockInput(path, progress, points[len(points)-1].At) {
		return s.surfaceHold("surface_waiting_for_dock", nil)
	}
	sf.plan, sf.planAt, sf.planIndex = points, s.now(), 0
	sf.planTransform, sf.planWasAttached, sf.planDetached = transform, own.TransportPresent, false
	sf.planBearing, sf.planSpeed, sf.planJump = bearing, speed, jump
	sf.planSource = &surfaceMotionSource{goal: g, rawRevision: w.Self.PositionRevision, objectRevision: hull.MovingTransport.ObjectRevision,
		progress: progress, firstProgress: progress, hostEpoch: s.hostEncounterEpoch, lastTransform: transform, localRoute: localRoute, ownerPoint: ownerPoint, departureOffset: departureOffset}
	return s.advanceSurfaceInputLocked(ctx, w, mesh, transform, progress, own, s.now())
}

// An observed swimmer may be at an angle to the opening. Try the bearing
// toward that swimmer, through the opening, or between them. An angled jump
// must reach actual water closer to that same current owner.
func (s *Service) checkSurfaceExit(ctx context.Context, w *worldv1.SnapshotResponse, g *actionsv1.FollowFormationRequest, mesh *buddynav.SurfaceMesh, tr buddynav.SurfaceTransform, progress uint32, start, owner, normal buddynav.Point, speed float64) ([]buddynav.BodyMotionSample, float64) {
	toward := surfaceBearing(start, owner)
	angle := math.Remainder(math.Atan2(float64(normal.Y), float64(normal.X))+float64(tr.Yaw)-toward, 2*math.Pi)
	for _, offset := range [...]float64{0, angle / 2, angle} {
		if offset != 0 && (w.Owner.GetPassengerMovement().GetInfo().GetFlags()&swimMovementFlag == 0 || math.Abs(offset) >= math.Pi/2) {
			continue
		}
		rows, err := s.measureSurfaceExitHeading(ctx, w, g, mesh, tr, progress, start, owner, offset, speed, true)
		if err == nil {
			return rows, offset
		}
	}
	return nil, 0
}

func (s *Service) measureSurfaceExitHeading(ctx context.Context, w *worldv1.SnapshotResponse, g *actionsv1.FollowFormationRequest, mesh *buddynav.SurfaceMesh, tr buddynav.SurfaceTransform, progress uint32, start, owner buddynav.Point, offset, speed float64, hop bool) ([]buddynav.BodyMotionSample, error) {
	target := owner
	if offset != 0 {
		bearing := surfaceBearing(start, owner) + offset
		rangeXY := math.Hypot(float64(owner.X-start.X), float64(owner.Y-start.Y))
		target.X = start.X + float32(rangeXY*math.Cos(bearing))
		target.Y = start.Y + float32(rangeXY*math.Sin(bearing))
	}
	rows, err := s.measureSurfaceCrossing(ctx, w, g, mesh, tr, progress, start, target, speed, hop)
	if err == nil && offset != 0 {
		last := rows[len(rows)-1]
		if w.Owner.GetPassengerMovement().GetInfo().GetFlags()&swimMovementFlag == 0 || !last.Swimming || surfaceDistance(last.Point, owner) >= surfaceDistance(start, owner) {
			return nil, errors.New("angled exit has no closer water contact toward this swimmer")
		}
	}
	return rows, err
}

// A single ordinary forward input is at most half a second. Once gravity
// starts, the same measurement continues to its first genuine physical contact.
func (s *Service) measureSurfaceCrossing(ctx context.Context, w *worldv1.SnapshotResponse, g *actionsv1.FollowFormationRequest,
	mesh *buddynav.SurfaceMesh, transform buddynav.SurfaceTransform, progress uint32, start, target buddynav.Point, speed float64, hop bool) ([]buddynav.BodyMotionSample, error) {
	bearing := surfaceBearing(start, target)
	duration := time.Duration(math.Min(.5, math.Hypot(float64(target.X-start.X), float64(target.Y-start.Y))/speed) * float64(time.Second))
	surface := buddynav.MovingSurface{Transform: transform, Model: mesh.Collision()}
	rows, err := buddynav.PlanSurfaceBodyContext(ctx, s.navDataDir, w.Self.MapId, start, bearing, speed, max(time.Millisecond, duration), hop, true, surface)
	if err != nil {
		return nil, err
	}
	if len(rows) < 2 || rows[0].Settling {
		return nil, errors.New("surface input requires actual starting support and a measured route")
	}
	last := rows[len(rows)-1]
	onDeck, err := hullFoot(ctx, mesh.Collision(), transform.Local(last.Point))
	if err != nil {
		return nil, err
	}
	if g.SurfaceMode == "board" && (last.Swimming || (hop || last.FallTime > 0) && !onDeck) {
		return nil, errors.New("boarding input misses the actual deck")
	}
	if g.SurfaceMode == "leave" {
		if last.Swimming && w.Self.UnitFlags&movementflags.CannotSwim != 0 || hop && onDeck {
			return nil, errors.New("exit misses usable exterior contact")
		}
		if hop || last.FallTime > 0 {
			if err := checkSurfaceDeparture(ctx, mesh.Collision(), s.surfaceFollow.paths[g.TransportGuid], transform, progress, rows); err != nil {
				return nil, err
			}
		}
	}
	return rows, nil
}

func setPassengerFrame(i *movementinfo.Info, ship uint64, tr buddynav.SurfaceTransform, progress uint32) {
	q := tr.Local(surfaceInfoPoint(*i))
	i.Flags |= passengerFlag
	i.TransportPresent = true
	i.Transport = movementinfo.Transport{GUID: ship, X: q.X, Y: q.Y, Z: q.Z, Orientation: float32(math.Remainder(float64(i.Orientation-tr.Yaw), 2*math.Pi)), Time: progress, Seat: -1}
}

func surfaceAirInfo(i *movementinfo.Info, fall time.Duration, bearing, speed, vertical float64) {
	i.Flags |= 0x1000
	i.JumpPresent = true
	i.FallTime = uint32(fall / time.Millisecond)
	i.Jump = movementinfo.Jump{Velocity: float32(vertical), SinAngle: float32(math.Sin(bearing)), CosAngle: float32(math.Cos(bearing)), XYSpeed: float32(speed)}
	if speed > 0 {
		i.Flags |= 1
	}
}

// Follow the feet through this time-varying hull in local space. A terminal
// MAP/liquid result cannot take precedence over an earlier real deck contact.
func surfaceRecontact(ctx context.Context, m *buddynav.TransportSurface, a, b buddynav.Point) (buddynav.Point, bool, error) {
	if b.Z >= a.Z-.0001 {
		return b, false, nil
	}
	steps := max(1, int(math.Ceil(surfaceDistance(a, b)/.15)))
	prev := a
	for n := 1; n <= steps; n++ {
		f := float32(n) / float32(steps)
		q := buddynav.Point{X: a.X + f*(b.X-a.X), Y: a.Y + f*(b.Y-a.Y), Z: a.Z + f*(b.Z-a.Z)}
		z, hit, err := m.Height(ctx, q, prev.Z-q.Z+.002, .002)
		if err != nil {
			return b, false, err
		}
		if hit && z <= prev.Z+.002 && z >= q.Z-.002 {
			q.Z = z
			return q, true, nil
		}
		prev = q
	}
	return b, false, nil
}

func (s *Service) advanceSurfaceInputLocked(ctx context.Context, w *worldv1.SnapshotResponse, mesh *buddynav.SurfaceMesh, tr buddynav.SurfaceTransform, progress uint32, own movementinfo.Info, now time.Time) bool {
	sf := &s.surfaceFollow
	in := sf.planSource
	p := sf.paths[in.shipGUID()]
	// Real received progress is a uint32. A counter reset or jump cannot join
	// a prior movement frame even if the same model exists in the new map.
	if progress < in.progress || p.CrossesTransfer(uint64(in.progress), uint64(progress)) {
		return s.surfaceHold("surface_transfer_needs_new_body", nil)
	}
	if sf.planIndex > 0 && now.Sub(sf.planAt.Add(sf.plan[sf.planIndex-1].At)) > encounterLeaseDuration {
		if !in.airborne {
			_, _ = s.stopSurfaceLocked(ctx)
		}
		// The original instruction's entire physical-input window elapsed
		// since our last actual frame. A later own observation can rebase it.
		return s.surfaceHold("surface_waiting_for_current_body", nil)
	}
	if surfaceDockWalk(in.goal) && !sf.dockWalkCurrent(p, tr, progress, now) {
		if in.airborne {
			in.released = true
		} else {
			if _, stopped := s.stopSurfaceLocked(ctx); stopped {
				sf.plan, sf.planSource = nil, nil
			}
			return s.surfaceHold("surface_dock_dwell_changed", nil)
		}
	}
	if in.released && in.airborne && !sf.plan[0].Settling {
		start := surfaceInfoPoint(own)
		if own.TransportPresent {
			start = tr.World(surfaceLocalPoint(own))
		}
		age := time.Duration(own.FallTime) * time.Millisecond
		vertical := float64(own.Jump.Velocity)
		if math.Abs(vertical-buddynav.BodyJumpVelocity) < .0001 {
			vertical = buddynav.BodyJumpVelocity
		}
		rows, err := buddynav.PlanSurfaceGravityContext(ctx, s.navDataDir, w.Self.MapId, start, sf.planBearing, age, vertical, buddynav.MovingSurface{Transform: tr, Model: mesh.Collision()})
		if err != nil || len(rows) < 2 {
			return s.surfaceHold("surface_gravity_unavailable", err)
		}
		sf.plan, sf.planAt, sf.planIndex = rows, s.now(), 0
		sf.planSpeed, sf.planJump = 0, vertical != 0
		in.lastTransform, in.firstProgress = tr, progress
		sf.planTransform = tr
	}
	if sf.planIndex == len(sf.plan) {
		if _, ok := s.stopSurfaceLocked(ctx); !ok {
			return true
		}
		sf.plan, sf.planSource = nil, nil
		return s.surfaceHold("surface_input_written", nil)
	}
	row := sf.plan[sf.planIndex]
	if now.Before(sf.planAt.Add(row.At)) {
		return true
	}
	// Packets captured after a delayed native tick still describe movement
	// at the measured trajectory times. Preserve every physical corner,
	// attachment edge and true contact; never stamp a backlog of steps one
	// millisecond apart with fresh clocks. Check the ship at that same time.
	if sf.planIndex > 0 {
		at := in.firstProgress + uint32(row.At/time.Millisecond)
		if at > progress || at < in.progress {
			return s.surfaceHold("surface_waiting_for_frame_clock", nil)
		}
		if at != progress {
			sample, err := p.Sample(uint64(at))
			if err != nil || sample.Transfer || sample.MapID != w.Self.MapId || p.CrossesTransfer(uint64(in.progress), uint64(at)) {
				return s.surfaceHold("surface_sample_clock_unavailable", err)
			}
			if !dockInput(p, at, time.Duration(progress-at)*time.Millisecond) {
				tr = buddynav.SurfaceTransform{Origin: sample.Position, Yaw: sample.Yaw, Scale: 1}
			}
			progress = at
		}
	}
	last := sf.planIndex == len(sf.plan)-1
	point := row.Point
	bearing := sf.planBearing
	if in.localRoute {
		point = tr.World(sf.planTransform.Local(point))
		idx := sf.planIndex
		if idx == 0 {
			idx++
		}
		a, b := sf.planTransform.Local(sf.plan[idx-1].Point), sf.planTransform.Local(sf.plan[idx].Point)
		bearing = surfaceBearing(a, b) + float64(tr.Yaw)
	}
	info := movementinfo.Info{X: point.X, Y: point.Y, Z: point.Z, Orientation: float32(math.Remainder(bearing, 2*math.Pi))}
	code := uint32(opcode.MSGMoveHeartbeat)
	attach := sf.planWasAttached && !sf.planDetached
	local := tr.Local(point)
	if sf.planIndex == 0 {
		if !in.localRoute && surfaceDistance(surfaceInfoPoint(own), point) > .04 && !own.TransportPresent {
			return s.surfaceHold("surface_takeoff_source_changed", nil)
		}
		if sf.plan[0].Settling {
			vertical := 0.0
			if sf.planJump {
				vertical = buddynav.BodyJumpVelocity
			}
			surfaceAirInfo(&info, row.FallTime, bearing, 0, vertical)
		} else if sf.planJump {
			code = opcode.MSGMoveJump
			surfaceAirInfo(&info, 0, bearing, sf.planSpeed, buddynav.BodyJumpVelocity)
		} else {
			code = opcode.MSGMoveStartForward
			info.Flags = 1
		}
	} else {
		prev := in.lastTransform.Local(surfaceInfoPoint(own))
		if own.TransportPresent {
			prev = surfaceLocalPoint(own)
		}
		falling := in.airborne || row.FallTime > 0
		landedOnHull := false
		if falling {
			contact, hit, err := surfaceRecontact(ctx, mesh.Collision(), prev, local)
			if err != nil {
				return s.surfaceHold("surface_contact_unavailable", err)
			}
			if hit {
				local, landedOnHull = contact, true
				point = tr.World(contact)
				info.X, info.Y, info.Z = point.X, point.Y, point.Z
				row.Swimming = false
				last = true
			}
		}
		if err := surfaceHullSweep(ctx, mesh.Collision(), prev, local); err != nil {
			if !in.airborne {
				_, _ = s.stopSurfaceLocked(ctx)
			}
			return s.surfaceHold("surface_body_sweep_blocked", err)
		}
		foot, err := hullFoot(ctx, mesh.Collision(), local)
		if err != nil {
			return s.surfaceHold("surface_support_unavailable", err)
		}
		switch {
		case last && falling:
			if row.Swimming {
				code = opcode.MSGMoveStartSwim
				info.Flags = swimMovementFlag
				info.PitchPresent = true
				if sf.planSpeed > 0 {
					info.Flags |= 1
				}
			} else {
				code = opcode.MSGMoveFallLand
				if sf.planSpeed > 0 {
					info.Flags = 1
				}
			}
			attach = !row.Swimming && (foot || landedOnHull)
			if !attach && !row.Swimming {
				// A planned contact that belonged solely to the old hull cannot
				// turn into static ground when the hull has moved away.
				wasHull, e := hullFoot(ctx, mesh.Collision(), sf.planTransform.Local(row.Point))
				if e != nil || wasHull {
					in.released = true
					return s.surfaceHold("surface_landing_support_changed", e)
				}
			}
		case falling:
			vertical := 0.0
			if sf.planJump {
				vertical = buddynav.BodyJumpVelocity
			}
			surfaceAirInfo(&info, row.FallTime, bearing, sf.planSpeed, vertical)
			if attach {
				_, under, e := mesh.Collision().Height(ctx, local, .025, float32(buddynav.MaxRouteLength))
				if e != nil {
					return s.surfaceHold("surface_attachment_unknown", e)
				}
				attach = under
			}
		default:
			info.Flags = 1
			if in.localRoute && !foot {
				return s.surfaceHold("surface_deck_support_changed", nil)
			}
			attach = foot && (sf.planWasAttached || in.goal.GetSurfaceMode() == "board")
		}
	}
	if attach {
		setPassengerFrame(&info, in.shipGUID(), tr, progress)
	}
	if err := s.writeSurfaceInfoLocked(ctx, w, info, code); err != nil {
		return s.surfaceWriteFailure(err)
	}
	if sf.planIndex == 0 {
		sf.planAt = s.now()
		in.clientClock = sf.body.info.Time
	} else {
		in.lastTransform, in.progress = tr, progress
	}
	in.airborne = info.JumpPresent
	sf.planDetached = sf.planWasAttached && !attach
	sf.planIndex++
	if last {
		sf.planIndex = len(sf.plan)
		if _, ok := s.stopSurfaceLocked(ctx); !ok {
			return true
		}
		sf.plan, sf.planSource = nil, nil
		if info.PitchPresent {
			return s.surfaceHold("surface_water_contact_written", nil)
		}
		if info.TransportPresent {
			return s.surfaceHold("surface_contact_written", nil)
		}
		return s.surfaceHold("surface_ground_contact_written", nil)
	}
	return s.surfaceHold("surface_input_in_progress", nil)
}

func (s *Service) surfaceWriteFailure(err error) bool {
	if s.state.MotionUnconfirmed {
		return s.surfaceHold("surface_transport_unconfirmed_no_retry", err)
	}
	return s.surfaceHold("surface_frame_not_dispatched", err)
}

func (s *Service) writeSurfaceInfoLocked(ctx context.Context, w *worldv1.SnapshotResponse, info movementinfo.Info, op uint32) error {
	if s.session == nil || s.state.MotionUnconfirmed || ctx.Err() != nil {
		return modcb.PacketNotDispatchedError("surface writer unavailable")
	}
	stamp, err := s.session.GetClock(ctx, &emptypb.Empty{})
	if err != nil || stamp == nil {
		return modcb.PacketNotDispatchedError("current body clock unavailable")
	}
	latest, err := s.readWorldSnapshot(ctx)
	if err != nil || !s.matchesCurrentEncounterVisitLocked(latest) || latest.VisitRevision != w.GetVisitRevision() ||
		latest.Self.PositionRevision != w.GetSelf().GetPositionRevision() || !samePortalCoreVisit(portalObservedWorldScope(w, s.selfGUID), portalObservedWorldScope(latest, s.selfGUID)) {
		return modcb.PacketNotDispatchedError("surface body changed before write")
	}
	g := s.surfaceFollow.goal
	in := s.surfaceFollow.planSource
	committed := in != nil && in.airborne
	if in != nil {
		g = in.goal
	}
	if in != nil && in.body != nil {
		if !s.explicitSurfaceCurrentLocked(ctx, latest, in) {
			return modcb.PacketNotDispatchedError("selected passenger body or lease changed before write")
		}
		if in.released && info.Flags&1 != 0 && op != opcode.MSGMoveStop {
			return modcb.PacketNotDispatchedError("voluntary passenger input was released before write")
		}
	} else if op != opcode.MSGMoveStop {
		if !s.surfaceOwnMotionAllowed(latest) || s.state.InteractionOwner != "" || s.movementOwner.owner != "" ||
			!committed && !s.surfaceGoalCurrentLocked(latest, g) {
			return modcb.PacketNotDispatchedError("surface follow authority changed before write")
		}
		if !committed && in != nil && surfaceDistance(in.ownerPoint, surfaceOwnerPoint(latest)) > g.StopDistance {
			return modcb.PacketNotDispatchedError("surface owner moved beyond the current input before write")
		}
		if committed && info.Flags&1 != 0 && !s.surfaceGoalCurrentLocked(latest, s.surfaceFollow.goal) && !s.continuesDetachedSurfaceLocked(latest, in) {
			in.released = true
			return modcb.PacketNotDispatchedError("horizontal surface input was superseded before write")
		}
	}
	ship := info.Transport.GUID
	if in != nil {
		// The sampled exit still relies on its particular hull through first
		// contact and the final STOP, even after clearing ONTRANSPORT.
		ship = in.shipGUID()
	} else if op != opcode.MSGMoveStop {
		ship = g.TransportGuid
	}
	if !buddynav.IsMovingTransportGUID(ship) || info.TransportPresent && info.Transport.GUID != ship {
		return modcb.PacketNotDispatchedError("surface frame requires its selected moving ship")
	}
	a, b := movingHull(w, ship), movingHull(latest, ship)
	if a == nil || b == nil || a.MovingTransport.ObjectRevision == 0 || a.MovingTransport.ObjectRevision != b.MovingTransport.ObjectRevision ||
		in != nil && in.objectRevision != b.MovingTransport.ObjectRevision ||
		a.MovingTransport.PositionRevision != b.MovingTransport.PositionRevision || a.MovingTransport.MovementRevision != b.MovingTransport.MovementRevision ||
		a.MovingTransport.PathFractionRevision != b.MovingTransport.PathFractionRevision {
		return modcb.PacketNotDispatchedError("observed hull changed before write")
	}
	hullRevision := b.MovingTransport.ObjectRevision
	nowTransform, nowProgress, e := s.surfacePoseLocked(ctx, latest, b)
	if e != nil {
		return modcb.PacketNotDispatchedError("current hull transform unavailable")
	}
	path := s.surfaceFollow.paths[ship]
	if op != opcode.MSGMoveStop && !committed && surfaceDockWalk(g) && !s.surfaceFollow.dockWalkCurrent(path, nowTransform, nowProgress, s.now()) {
		return modcb.PacketNotDispatchedError("dock walk no longer fits its measured dwell")
	}
	if info.TransportPresent && (info.Transport.Time > nowProgress || path.CrossesTransfer(uint64(info.Transport.Time), uint64(nowProgress))) {
		return modcb.PacketNotDispatchedError("passenger clock crosses an unobserved hull transfer")
	}
	if info.TransportPresent && (op == opcode.MSGMoveStop || op == opcode.MSGMoveSetFacing) {
		local := surfaceLocalPoint(info)
		q := nowTransform.World(local)
		info.X, info.Y, info.Z = q.X, q.Y, q.Z
		info.Orientation = float32(math.Remainder(float64(info.Transport.Orientation+nowTransform.Yaw), 2*math.Pi))
		info.Transport.Time = nowProgress
	}
	if in != nil && s.surfaceFollow.planIndex == 0 && info.TransportPresent && op != opcode.MSGMoveStop && info.Transport.Time != nowProgress {
		// Deck routing is local and survives a hull translation. A jump
		// is a world trajectory: a ship can move during the first route
		// search, so remeasure that complete departure at the fresh pose.
		sf := &s.surfaceFollow
		start := nowTransform.World(surfaceLocalPoint(info))
		if in.body != nil && !in.localRoute && !sf.plan[0].Settling {
			mesh, e := s.surfaceMeshLocked(ctx, b)
			if e != nil {
				return modcb.PacketNotDispatchedError("current selected passenger model unavailable")
			}
			bearing := float64(nowTransform.Yaw) + in.body.localBearing
			flight, local, e := s.planExplicitSurface(ctx, latest, in.body.request, mesh, path, nowTransform, nowProgress, start, bearing, sf.planSpeed)
			if e != nil || local {
				return modcb.PacketNotDispatchedError("current selected passenger trajectory changed")
			}
			sf.plan, sf.planTransform, sf.planBearing = flight, nowTransform, bearing
			info.Orientation = float32(math.Remainder(bearing, 2*math.Pi))
			if info.JumpPresent {
				info.Jump.SinAngle, info.Jump.CosAngle = float32(math.Sin(bearing)), float32(math.Cos(bearing))
			}
		} else if in.goal.GetSurfaceMode() == "leave" && !surfaceDockWalk(in.goal) && !in.localRoute && !sf.plan[0].Settling {
			bearing := surfaceBearing(start, surfaceOwnerPoint(latest)) + in.departureOffset
			mesh, e := s.surfaceMeshLocked(ctx, b)
			if e != nil {
				return modcb.PacketNotDispatchedError("current departure hull unavailable")
			}
			flight, e := s.measureSurfaceExitHeading(ctx, latest, g, mesh, nowTransform, nowProgress, start, surfaceOwnerPoint(latest), in.departureOffset, sf.planSpeed, sf.planJump)
			if e != nil {
				return modcb.PacketNotDispatchedError("current departure: " + e.Error())
			}
			sf.plan, sf.planTransform, sf.planBearing = flight, nowTransform, bearing
			info.Orientation = float32(math.Remainder(bearing, 2*math.Pi))
			if info.JumpPresent {
				info.Jump.SinAngle, info.Jump.CosAngle = float32(math.Sin(bearing)), float32(math.Cos(bearing))
			}
		} else {
			info.Orientation = float32(math.Remainder(float64(info.Transport.Orientation+nowTransform.Yaw), 2*math.Pi))
		}
		info.X, info.Y, info.Z = start.X, start.Y, start.Z
		setPassengerFrame(&info, ship, nowTransform, nowProgress)
		in.lastTransform, in.progress, in.firstProgress = nowTransform, nowProgress, nowProgress
	}
	prior, scope, err := s.surfaceMotionSourceLocked(ctx, latest)
	if err != nil {
		return modcb.PacketNotDispatchedError("surface origin no longer current: " + err.Error())
	}
	if op == opcode.MSGMoveStop && info.TransportPresent && prior.TransportPresent && prior.Transport.GUID == info.Transport.GUID &&
		prior.Transport.Time == info.Transport.Time && surfaceLocalPoint(prior) == surfaceLocalPoint(info) && prior.Transport.Orientation == info.Transport.Orientation {
		info.X, info.Y, info.Z, info.Orientation = prior.X, prior.Y, prior.Z, prior.Orientation
	}
	if pts := s.currentMovementPointsLocked(); op != opcode.MSGMoveStop && len(pts) > 0 && !pointPathClear([]position{
		surfacePosition(surfaceInfoPoint(prior), prior.Orientation), surfacePosition(surfaceInfoPoint(info), info.Orientation)}, pts) {
		return modcb.PacketNotDispatchedError("current movement avoidance excludes this surface leg")
	}
	info.Time = stamp.ClientTimeMs
	if in != nil && s.surfaceFollow.planIndex > 0 && s.surfaceFollow.planIndex < len(s.surfaceFollow.plan) && op != opcode.MSGMoveStop {
		offset := s.surfaceFollow.plan[s.surfaceFollow.planIndex].At
		planned := in.clientClock + uint32(offset/time.Millisecond)
		if int32(stamp.ClientTimeMs-planned) < 0 || int32(planned-prior.Time) < 0 {
			return modcb.PacketNotDispatchedError("physical source clock has not reached the scheduled sample")
		}
		info.Time = planned
	}
	payload, err := movementinfo.MoverFrame(s.selfGUID, info)
	if err != nil || !info.AllFinite() || !buddynav.RouteBodyFrame(op, payload, s.selfGUID) {
		return modcb.PacketNotDispatchedError("invalid measured surface movement frame")
	}
	attempt, err := newMovementAttemptID()
	if err != nil {
		return modcb.PacketNotDispatchedError("surface movement attempt identity unavailable")
	}
	worldPackets, carry := bits.Add64(latest.ObservedPackets, latest.IgnoredPackets, 0)
	worldPackets, carryMalformed := bits.Add64(worldPackets, latest.MalformedPackets, 0)
	if carry != 0 || carryMalformed != 0 || worldPackets == 0 || worldPackets == math.MaxUint64 {
		return modcb.PacketNotDispatchedError("surface World packet sequence unavailable")
	}
	wctx := context.WithValue(ctx, surfaceOwnPacketKey{}, true)
	if in != nil && in.body != nil {
		r := in.body.request
		wctx = modcb.WithSurfaceBodySelection(modcb.ForwardPolicySpell(wctx), r.VisitId, r.SequenceId)
	}
	wctx = modcb.WithMovementAttempt(modcb.WithRouteBodyInput(wctx, false), attempt)
	wctx = modcb.WithRouteHull(wctx, ship, hullRevision, worldPackets)
	err = s.sendPacketLocked(wctx, op, payload, scope)
	if err != nil && modcb.PacketWasNotDispatched(err) {
		return err
	}
	if err == nil {
		if in != nil && in.body != nil {
			in.body.view.TransportWritten = true
		}
		err = s.recordSurfaceFrameLocked(ctx, latest, op, payload, attempt)
	}
	if err != nil {
		s.surfaceFollow.lastAttempt = attempt
		var hullRevision uint64
		if info.TransportPresent {
			hullRevision = movingHull(latest, info.Transport.GUID).GetMovingTransport().GetObjectRevision()
		}
		s.surfaceFollow.body = &movingSurfaceBody{visit: latest.VisitRevision, rawPositionRevision: latest.Self.PositionRevision, hullObjectRevision: hullRevision, info: info,
			packet: &modv1.WorldPacket{Opcode: op, Payload: append([]byte(nil), payload...), TransportScope: clonePortalScope(scope)}, uncertain: true}
		s.state.MotionUnconfirmed = true
		return err
	}
	s.pos = position{info.X, info.Y, info.Z, info.Orientation}
	s.positionRevision, s.posMap = latest.Self.PositionRevision, latest.Self.MapId
	s.posKnown, s.posEstimated, s.posAt = true, true, s.now()
	s.posRevision++
	s.swimming = info.Flags&swimMovementFlag != 0
	s.state.Moving = info.Flags&1 != 0 || info.JumpPresent
	s.publishClientMovementLocked(ctx, latest, s.pos, true)
	if in != nil && in.body != nil {
		view := in.body.view
		view.PositionEstimated = true
		view.X, view.Y, view.Z = info.X, info.Y, info.Z
	}
	return nil
}

func (s *Service) stopSurfaceLocked(ctx context.Context) (bool, bool) {
	sf := &s.surfaceFollow
	b := sf.body
	if in := sf.planSource; in != nil && in.body != nil && ctx.Value(explicitSurfaceInputKey{}) != in.body {
		in.released = true
		return true, false
	}
	if s.priorPassengerMovingLocked() {
		if !s.encounterVisit.bound {
			return true, false
		}
		w, err := s.readWorldSnapshot(ctx)
		return true, err == nil && s.reconcileSurfaceMotionLocked(w)
	}
	if b != nil && b.uncertain {
		if s.state.MotionUnconfirmed {
			return true, false
		}
		// A newer received stationary body may have already settled custody.
		w, e := s.readWorldSnapshot(ctx)
		if e != nil || b.visit == w.GetVisitRevision() && b.rawPositionRevision == w.GetSelf().GetPositionRevision() {
			s.state.MotionUnconfirmed = true
			return true, false
		}
		return false, true
	}
	if sf.planSource == nil && (b == nil || !b.info.TransportPresent) {
		return false, true
	}
	if in := sf.planSource; in != nil && in.airborne {
		in.released = true
		return true, false
	}
	terminal := sf.planSource != nil && sf.planIndex == len(sf.plan) && b != nil && b.packet != nil &&
		(b.packet.Opcode == opcode.MSGMoveStartSwim || b.packet.Opcode == opcode.MSGMoveFallLand)
	if !s.state.Moving && !terminal {
		return true, true
	}
	w, err := s.readWorldSnapshot(ctx)
	if err != nil {
		return true, false
	}
	own, _, err := s.surfaceMotionSourceLocked(ctx, w)
	if err != nil {
		return true, false
	}
	if own.JumpPresent {
		return true, false
	}
	info := own
	info.Flags &^= 1
	if info.TransportPresent {
		hull := movingHull(w, info.Transport.GUID)
		if hull == nil {
			return true, false
		}
		tr, progress, e := s.surfacePoseLocked(ctx, w, hull)
		if e != nil {
			return true, false
		}
		q := tr.World(surfaceLocalPoint(own))
		info.X, info.Y, info.Z = q.X, q.Y, q.Z
		info.Orientation = float32(math.Remainder(float64(own.Transport.Orientation+tr.Yaw), 2*math.Pi))
		info.Transport.Time = progress
	}
	if err := s.writeSurfaceInfoLocked(ctx, w, info, opcode.MSGMoveStop); err != nil {
		s.surfaceWriteFailure(err)
		return true, false
	}
	sf.plan, sf.planSource = nil, nil
	return true, true
}
