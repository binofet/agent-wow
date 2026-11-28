package buddyactions

import (
	"context"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// A late cleanup is a movement write too. Keep the former position out of
// ordinary Follow and BeforeLogout even when the current World read failed.
func (s *Service) paidTaxiBodyHeld(w *worldv1.SnapshotResponse) bool {
	a := s.flightpathActivation.operation
	if a == nil || a == s.taxiBodyReleased || !a.TransportWritten && !a.TransportUnknown ||
		a.ReplyKnown && a.ReplyCode > 0 && a.ReplyCode <= 12 {
		return false
	}
	if a.SessionId != s.sessionID || a.SelfGuid != s.selfGUID || !s.matchesCurrentEncounterVisitLocked(w) {
		return true
	}
	p, authority := w.Self, w.SelfMotionAuthority
	origin, scope := authority.GetTaxiBodyOriginMotionRevision(), authority.GetTransportScope()
	if origin == 0 || !p.PositionKnown || !p.AliveKnown || !p.Alive || !p.MapKnown || !p.MovementFlagsKnown || !p.UnitFlagsKnown ||
		p.UnitFlags&movementflags.UnsafeUnitFlags != 0 || !finite(playerPosition(p)) || !flightpathScopeBound(scope, a.SelfGuid, p.MapId) || origin > scope.MotionRevision {
		return true
	}
	// A newly bound visit with its own stationary raw image restores only
	// body control. The original activation keeps its actual disposition.
	if w.VisitRevision > a.VisitId && a.VisitId != 0 && !p.PositionInferred && !scope.SplineActive {
		raw := w.GetSelfMovementFacts()
		return !raw.GetKnown() || raw.GetVisitRevision() != w.VisitRevision || raw.GetSelfGuid() != s.selfGUID ||
			!sameFlightpathVisit(scope, raw.GetTransportScope()) || raw.TransportScope.MotionRevision != origin ||
			a.AcceptanceScope != nil && (a.AcceptanceScope.ConnectionId != scope.ConnectionId || scope.VisitRevision <= a.AcceptanceScope.VisitRevision)
	}
	if !a.ActivationAccepted || !scope.SplineIdKnown || scope.SplineSourceOpcode != opcode.SMSGMonsterMove && scope.SplineSourceOpcode != opcode.SMSGMonsterMoveTransport {
		return true
	}
	from := a.AcceptanceScope
	if seg := a.CurrentSegment; seg != nil {
		if !s.flightpathSegmentCurrentLocked() {
			return true
		}
		from = seg.SourceScope
	}
	return !sameFlightpathVisit(from, scope) || origin <= from.MotionRevision || scope.SplineSourceRevision <= a.AcceptanceScope.GetMotionRevision()
}

func (s *Service) releasePaidTaxiBody(w *worldv1.SnapshotResponse) {
	if a := s.flightpathActivation.operation; a != nil && !s.paidTaxiBodyHeld(w) {
		s.taxiBodyReleased = a
	}
}

// An owner can resume Follow before the first ordinary ground step. At an
// elevated, completed taxi endpoint, retain the one movement writer through
// a measured vertical landing before asking ground navigation for a route.
func (s *Service) settleFollowTaxiBody(ctx context.Context, w *worldv1.SnapshotResponse, formation bool) bool {
	a, p := s.flightpathActivation.operation, w.GetSelf()
	if a == nil || !a.ActivationAccepted || a == s.taxiBodyReleased {
		return false
	}
	if s.paidTaxiBodyHeld(w) {
		s.state.Reason = "waiting_for_post_taxi_body_position"
		return true
	}
	if !p.GetPositionInferred() {
		return false
	}
	if s.navDataDir == "" || s.state.Moving || s.state.MotionUnconfirmed || s.state.CastPending || s.state.Traveling ||
		s.bodyMoving || s.state.InteractionOwner != "" || s.clientControlLost || s.clientControlRestoring || !s.posKnown ||
		s.pos != playerPosition(p) || observedRunSpeed(p) == 0 || !movementflags.GroundCompatible(p.MovementFlags, p.ExtraMovementFlags) || p.MovementFlags != 0 {
		s.state.Reason = "waiting_for_post_taxi_ground_control"
		return true
	}
	if parent, ok := ctx.Value(followRunParentKey{}).(context.Context); ok {
		ctx = parent
	}
	from, revision, visit, follow := s.pos, p.PositionRevision, w.VisitRevision, s.followRevision
	bearing, speed := float64(from.o), observedRunSpeed(p)
	path, err := buddynav.PlanBodyMotionContext(ctx, s.navDataDir, p.MapId, swimPoint(from), bearing, speed, 100*time.Millisecond, false)
	if err != nil || len(path) < 2 {
		s.state.Reason = "post_taxi_ground_support_unavailable"
		return true
	}
	if !path[0].Settling {
		return false
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < path[len(path)-1].At {
		s.state.Reason = "post_taxi_landing_deadline_too_short"
		return true
	}
	s.bodyMoving = true
	defer func() { s.bodyMoving = false; s.lastStep = s.now() }()
	view := new(actionsv1.BodyMovementState)
	guard := func(_, _, _ bool, _ *movingSurfaceBody) (*worldv1.SnapshotResponse, bool) {
		fresh, e := s.snapshot(ctx)
		ready := e == nil && ctx.Err() == nil && s.flightpathActivation.operation == a && s.matchesCurrentEncounterVisitLocked(fresh) && fresh.VisitRevision == visit &&
			fresh.Self.PositionRevision == revision && playerPosition(fresh.Self) == from && safeSelfCombat(fresh, s.selfGUID) == "" &&
			!s.closed && !s.clientControlLost && !s.clientControlRestoring && !s.state.MotionUnconfirmed && s.bodyMoving
		if !view.TransportWritten {
			ready = ready && !s.paidTaxiBodyHeld(fresh) && s.followRevision == follow && (s.state.Following || formation && s.currentFollowFormationGoalLocked(s.followGoal, fresh))
		}
		return fresh, ready
	}
	if !s.executeBodyTrajectoryLocked(ctx, from, path, bearing, speed, false, false, 0, view, nil, guard) {
		s.state.Reason = "post_taxi_grounding_interrupted"
		if view.TransportWritten {
			s.state.MotionUnconfirmed = true
		}
		return true
	}
	s.state.Reason = "post_taxi_ground_contact_written"
	return true
}
