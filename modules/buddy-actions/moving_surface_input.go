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

type explicitSurfaceInputKey struct{}

type surfaceBodyInput struct {
	request       *actionsv1.BodyMovementRequest
	view          *actionsv1.BodyMovementState
	localBearing  float64
	ownerRevision uint64
}

func (s *Service) explicitSurfaceCurrentLocked(ctx context.Context, w *worldv1.SnapshotResponse, in *surfaceMotionSource) bool {
	body := in.body
	r := body.request
	if ctx.Value(explicitSurfaceInputKey{}) != body || !s.bodyMoving || s.state.BodyMovement != body.view || s.state.Traveling || s.recovery.state.Active ||
		!s.surfaceOwnMotionAllowed(w) || !s.matchesCurrentEncounterVisitLocked(w) || s.sessionID != r.SessionId || r.SelfGuid != w.Self.Guid ||
		r.VisitId != w.VisitRevision || w.Self.PositionRevision != in.rawRevision || in.hostEpoch != s.hostEncounterEpoch || !s.admitsPolicyEncounterLocked(ctx, w) ||
		s.movementOwner.owner != "" || s.movementOwner.revision != body.ownerRevision || w.Self.DynamicFlags&0x20 != 0 || w.Self.PlayerFlags&ghostFlag != 0 ||
		!w.Self.HealthKnown || w.Self.Health == 0 || w.Self.UnitFlags&movementflags.UnsafeUnitFlags != 0 || s.paidTaxiBodyHeld(w) {
		return false
	}
	if r.IdleToken != "" {
		return s.bodyIdleLeaseCurrentLocked(r, s.now())
	}
	return s.state.InteractionOwner == ""
}

func (s *Service) planExplicitSurface(ctx context.Context, w *worldv1.SnapshotResponse, r *actionsv1.BodyMovementRequest, mesh *buddynav.SurfaceMesh, path *buddynav.TransportPath, tr buddynav.SurfaceTransform, clock uint32, start buddynav.Point, bearing, speed float64) ([]buddynav.BodyMotionSample, bool, error) {
	foot, err := hullFoot(ctx, mesh.Collision(), tr.Local(start))
	if err != nil || !foot {
		return nil, false, errors.New("current passenger input lacks measured deck contact")
	}
	rows, err := buddynav.PlanSurfaceBodyContext(ctx, s.navDataDir, w.Self.MapId, start, bearing, speed, time.Duration(r.DurationMs)*time.Millisecond, r.Jump, r.Landing == "ground_or_water", buddynav.MovingSurface{Transform: tr, Model: mesh.Collision()})
	if err != nil {
		return nil, false, err
	}
	limit := 24
	if r.Landing != "" {
		limit = buddynav.BodyLandingSampleLimit(speed)
	}
	if len(rows) < 2 || len(rows) > limit || rows[0].Settling {
		return nil, false, errors.New("invalid finite passenger input")
	}
	last := rows[len(rows)-1]
	end := uint64(clock) + uint64((last.At+time.Millisecond-1)/time.Millisecond)
	if end > math.MaxUint32 || path.CrossesTransfer(uint64(clock), end) {
		return nil, false, errors.New("passenger input crosses a map transfer")
	}
	if !r.Jump && last.FallTime == 0 && !last.Swimming {
		for _, row := range rows {
			supported, e := hullFoot(ctx, mesh.Collision(), tr.Local(row.Point))
			if e != nil || !supported {
				return nil, false, errors.New("selected forward input leaves measured deck support")
			}
		}
		return rows, true, nil
	}
	if last.Swimming && (r.Landing != "ground_or_water" || w.Self.UnitFlags&movementflags.CannotSwim != 0) {
		return nil, false, errors.New("selected passenger cannot enter this water")
	}
	landedOnDeck, e := hullFoot(ctx, mesh.Collision(), tr.Local(last.Point))
	if e != nil {
		return nil, false, e
	}
	if landedOnDeck && !last.Swimming {
		// A short same-deck hop has the normal stationary physical model only
		// while this specific ship remains within the same measured dock dwell.
		if r.Landing != "" || !dockInput(path, clock, last.At) {
			return nil, false, errors.New("selected jump did not clear the moving deck")
		}
	} else if e := checkSurfaceDeparture(ctx, mesh.Collision(), path, tr, clock, rows); e != nil {
		return nil, false, e
	}
	return rows, false, nil
}

// Execute the caller's relative bearing on the current, positively observed
// passenger body. The plan is synchronous under the original Lua dispatch.
// Each actual frame and terminal STOP share the native surface writer.
func (s *Service) moveSurfaceBodyLocked(ctx context.Context, r *actionsv1.BodyMovementRequest, w *worldv1.SnapshotResponse, speed float64, view *actionsv1.BodyMovementState, extend func(time.Duration)) {
	sf := &s.surfaceFollow
	reject := func(phase, detail string) { view.Phase = phase; view.Detail = boundedPolicyDiagnostic(detail, 240) }
	if sf.planSource != nil || s.state.Moving || s.movementOwner.owner != "" || r.DurationMs > 500 || r.Landing == "follow_water" {
		reject("body_scope_not_dispatched", "current stopped passenger and finite independently selected input required")
		return
	}
	own, _, err := s.surfaceMotionSourceLocked(ctx, w)
	if err != nil || !own.TransportPresent || own.Flags != passengerFlag || own.ExtraFlags != 0 || own.JumpPresent || own.Transport.Seat != -1 || own.Transport.Time2Present {
		reject("body_scope_not_dispatched", "current independently received or exactly written stopped passenger required")
		return
	}
	h := movingHull(w, own.Transport.GUID)
	if h == nil || h.MovingTransport.ObjectRevision == 0 {
		reject("body_scope_not_dispatched", "current matching hull required")
		return
	}
	if prior := sf.body; prior == nil || prior.visit != w.VisitRevision || prior.rawPositionRevision != w.Self.PositionRevision {
		p, known := receivedPassenger(w.Self, w.VisitRevision)
		if !known || p.Revision < h.MovingTransport.ObjectRevision || p.Info.GetTransport().GetGuid() != own.Transport.GUID {
			reject("body_scope_not_dispatched", "current passenger must belong to this hull lifetime")
			return
		}
	}
	mesh, err := s.surfaceMeshLocked(ctx, h)
	if err != nil {
		reject("local_physics_refused", navigationDiagnostic(err, s.navDataDir))
		return
	}
	tr, clock, err := s.surfacePoseLocked(ctx, w, h)
	if err != nil {
		reject("local_physics_refused", err.Error())
		return
	}
	body := &surfaceBodyInput{request: r, view: view, localBearing: float64(own.Transport.Orientation) + float64(r.BearingRadians), ownerRevision: s.movementOwner.revision}
	in := &surfaceMotionSource{body: body, ship: h.Guid, rawRevision: w.Self.PositionRevision, objectRevision: h.MovingTransport.ObjectRevision, hostEpoch: s.hostEncounterEpoch, progress: clock, firstProgress: clock, lastTransform: tr}
	ctx = context.WithValue(ctx, explicitSurfaceInputKey{}, body)
	if !s.explicitSurfaceCurrentLocked(ctx, w, in) {
		reject("body_scope_not_dispatched", "selected passenger body or movement lease unavailable")
		return
	}
	path := sf.paths[h.Guid]
	bearing := float64(tr.Yaw) + body.localBearing
	rows, local, err := s.planExplicitSurface(ctx, w, r, mesh, path, tr, clock, tr.World(surfaceLocalPoint(own)), bearing, speed)
	if err != nil {
		reject("local_physics_refused", navigationDiagnostic(err, s.navDataDir))
		return
	}
	end := rows[len(rows)-1].At
	if deadline, bounded := ctx.Deadline(); bounded && time.Until(deadline) < end {
		reject("body_scope_not_dispatched", "remaining RPC deadline cannot cover the measured passenger input")
		return
	}
	extend(end)
	sf.plan, sf.planAt, sf.planIndex = rows, s.now(), 0
	sf.planTransform, sf.planWasAttached, sf.planDetached = tr, true, false
	sf.planBearing, sf.planSpeed, sf.planJump = bearing, speed, r.Jump
	sf.planSource, in.localRoute = in, local
	defer func() {
		if sf.planSource == in {
			sf.plan, sf.planSource = nil, nil
		}
	}()
	view.Phase = "executing_body_input"
	stopRefused := false
	for {
		if !s.explicitSurfaceCurrentLocked(ctx, w, in) || ctx.Err() != nil || sf.planSource != in {
			break
		}
		h = movingHull(w, in.ship)
		if h == nil || h.MovingTransport.ObjectRevision != in.objectRevision {
			break
		}
		tr, clock, err = s.surfacePoseLocked(ctx, w, h)
		if err != nil {
			break
		}
		own, _, err = s.surfaceMotionSourceLocked(ctx, w)
		if err != nil {
			break
		}
		if in.released && !in.airborne {
			if _, ok := s.stopSurfaceLocked(ctx); !ok {
				stopRefused = true
				break
			}
			reject("body_input_released", "selected forward input was released")
			return
		}
		priorBody := sf.body
		s.advanceSurfaceInputLocked(ctx, w, mesh, tr, clock, own, s.now())
		if sf.planSource == nil && !s.state.Moving && !s.state.MotionUnconfirmed && view.TransportWritten {
			view.Phase = "body_input_written"
			s.followNav.Reset()
			s.targetMovement.nav.Reset()
			return
		}
		if sf.planSource == in && sf.planIndex == len(sf.plan) {
			// The terminal sample was written, but its one scoped STOP did not settle.
			stopRefused = true
			break
		}
		if s.state.MotionUnconfirmed || sf.planSource != in || sf.body == priorBody {
			break
		}
		next := sf.planAt.Add(sf.plan[sf.planIndex].At).Sub(s.now())
		if next > 0 {
			s.mu.Unlock()
			err = s.waitBodyMotion(ctx, next)
			s.mu.Lock()
			if err != nil {
				break
			}
		}
		w, err = s.readWorldSnapshot(ctx)
		if err != nil {
			break
		}
	}
	if !view.TransportWritten && !s.state.MotionUnconfirmed {
		reject("body_scope_not_dispatched", sf.phase)
		return
	}
	if !stopRefused && view.TransportWritten && !s.state.MotionUnconfirmed && !in.airborne && s.explicitSurfaceCurrentLocked(ctx, w, in) {
		if _, ok := s.stopSurfaceLocked(ctx); ok {
			reject("body_input_interrupted", "selected input stopped at its last written passenger frame")
			return
		}
	}
	s.state.MotionUnconfirmed = true
	reject("body_scope_unconfirmed_no_retry", sf.phase)
}
