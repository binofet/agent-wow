package buddyactions

import (
	"context"
	"errors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"math"
	"testing"
	"time"
)

func TestMovementPointsScopeExpiryAndNoPacket(t *testing.T) {
	s, f, _, ctx, advance := targetMovementFixture(t)
	r := &actionsv1.MovementPointsRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.encounterVisit.mapID, VisitId: 1, Points: []*actionsv1.MovementPoint{{X: 2, RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}}}
	before := len(f.packets)
	if _, err := s.SetMovementPoints(context.Background(), r); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	out, err := s.SetMovementPoints(ctx, r)
	if err != nil || !out.MovementPoints.Active || len(f.packets) != before {
		t.Fatal(out, err)
	}
	r.Points[0].X = 99
	if s.currentMovementPointsLocked()[0].X != 2 {
		t.Fatal("caller alias retained")
	}
	r.VisitId = 2
	if _, err = s.SetMovementPoints(ctx, r); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	advance(20 * time.Second)
	if s.movementPointsStateLocked().Active {
		t.Fatal("expired input rearmed")
	}
}

func TestMovementPointUnionGeometry(t *testing.T) {
	ps := []*actionsv1.MovementPoint{{X: 0, RadiusYards: 5}, {X: 1, RadiusYards: 5}}
	if !pointPathClear([]position{{x: 0}, {x: -6}}, ps) {
		t.Fatal("overlap prevented outward escape")
	}
	if pointPathClear([]position{{x: -6}, {x: 6}}, ps) {
		t.Fatal("crossing disk accepted")
	}
	if !pointPathClear([]position{{x: -6, y: 6}, {x: 6, y: 6}}, ps) {
		t.Fatal("clear route blocked")
	}
	if pointPathClear([]position{{x: -2}, {x: 2}}, ps) {
		t.Fatal("deeper penetration accepted")
	}
}

func TestMovementPointStepPreservesCastThenUsesSameBodyWriter(t *testing.T) {
	s, f, _, ctx, advance := targetMovementFixture(t)
	_, err := s.SetMovementPoints(ctx, &actionsv1.MovementPointsRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, Points: []*actionsv1.MovementPoint{{X: 2, RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}}})
	if err != nil {
		t.Fatal(err)
	}
	r := &actionsv1.MovementPointsStepRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, SequenceId: 61}
	s.state.CastPending = true
	before := len(f.packets)
	out, err := s.StepMovementPoints(ctx, r)
	if err != nil || !out.CastPending || len(f.packets) != before {
		t.Fatal("cast disturbed", out, err)
	}
	s.state.CastPending = false
	s.bodyMotionPlan = func(_ context.Context, _ string, _ uint32, from buddynav.Point, bearing, speed float64, d time.Duration, _ bool) ([]buddynav.BodyMotionSample, error) {
		to := from
		to.X += float32(math.Cos(bearing) * speed * d.Seconds())
		to.Y += float32(math.Sin(bearing) * speed * d.Seconds())
		return []buddynav.BodyMotionSample{{Point: from}, {At: d, Point: to}}, nil
	}
	s.bodyMotionWait = func(_ context.Context, d time.Duration) error { advance(d); return nil }
	out, err = s.StepMovementPoints(ctx, r)
	if err != nil || out.GetBodyMovement().GetPhase() != "body_input_written" || pointClearance(s.pos, s.currentMovementPointsLocked()) <= -3 {
		t.Fatal("no outward shared body input", out, err, s.pos)
	}
	before = len(f.packets)
	out, err = s.StepMovementPoints(ctx, r)
	if err != nil || len(f.packets) != before {
		t.Fatal("same sequence replayed", out, err)
	}
}

func TestMovementPointsAdmittedApproachDetoursAndExpiryRestoresGoal(t *testing.T) {
	s, f, r, ctx, advance := targetMovementFixture(t)
	_, err := s.SetMovementPoints(ctx, &actionsv1.MovementPointsRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, Points: []*actionsv1.MovementPoint{{X: 2, RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(time.Second).UnixMilli())}}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.ApproachTarget(ctx, r)
	if err != nil || !out.Moving || !s.targetMovement.nav.pointDetour {
		t.Fatal("admitted path did not use ground detour", out, err)
	}
	advance(200 * time.Millisecond)
	out, err = s.ApproachTarget(ctx, r)
	if err != nil || pointClearance(s.pos, s.currentMovementPointsLocked()) <= -3 {
		t.Fatal("admitted route failed outward progress", out, err, s.pos)
	}
	advance(time.Second)
	if _, err = s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(1)); err != nil {
		t.Fatal(err)
	}
	out, err = s.ApproachTarget(ctx, r)
	if err != nil || s.targetMovement.nav.pointDetour || s.targetMovement.nav.target.x <= 0 {
		t.Fatal("expiry did not restore caller goal", out, err)
	}
	_ = f
}

func TestMovementPointStepRejectsForeignCustodyAndUnknown(t *testing.T) {
	s, f, _, ctx, _ := targetMovementFixture(t)
	s.movementOwner = movementOwner{owner: "ranged:other", token: "private-token", until: s.now().Add(time.Second)}
	r := &actionsv1.MovementPointsStepRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, SequenceId: 77}
	before := len(f.packets)
	if _, err := s.StepMovementPoints(ctx, r); status.Code(err) != codes.FailedPrecondition || len(f.packets) != before {
		t.Fatal("foreign custody bypass", err)
	}
	r.MovementOwner = s.movementOwner.owner
	r.MovementToken = s.movementOwner.token
	s.state.MotionUnconfirmed = true
	out, err := s.StepMovementPoints(ctx, r)
	if err != nil || !out.MotionUnconfirmed || len(f.packets) != before {
		t.Fatal("unknown movement changed", out, err)
	}
}

func TestMovementPointFarDiskDoesNotDetourLocalLeg(t *testing.T) {
	s, _, _, ctx, _ := targetMovementFixture(t)
	if _, err := s.snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	s.movementPoints = movementPointInput{session: s.sessionID, self: s.selfGUID, visit: 1, mapID: s.posMap, revision: 1, points: []*actionsv1.MovementPoint{{X: 100, RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}}}
	path := []position{s.pos, {x: 200}}
	got, ok := s.movementPointRouteLocked(ctx, path)
	if !ok || len(got) != 2 || got[1] != path[1] {
		t.Fatal("distant disk changed local route", got, ok)
	}
}

func TestMovementPointStepKeepsEnteredUnknownAndHardGeometry(t *testing.T) {
	for _, mode := range []string{"unknown", "hard_geometry"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, ctx, advance := targetMovementFixture(t)
			if _, err := s.SetMovementPoints(ctx, &actionsv1.MovementPointsRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, Points: []*actionsv1.MovementPoint{{X: 2, RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}}}); err != nil {
				t.Fatal(err)
			}
			r := &actionsv1.MovementPointsStepRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, SequenceId: 91}
			if mode == "hard_geometry" {
				f.world.RequestedCreature.X = 0
				r.TargetConstraints = &actionsv1.ApproachTargetRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, TargetGuid: enemyGUID, MinRangeYards: 10, RangeYards: 15}
				out, err := s.StepMovementPoints(ctx, r)
				if err != nil || len(f.packets) != 0 || out.GetMovementPoints().GetReason() != "hard_constraints_or_navigation_blocked" {
					t.Fatal(out, err)
				}
				return
			}
			s.bodyMotionPlan = func(_ context.Context, _ string, _ uint32, from buddynav.Point, bearing, speed float64, d time.Duration, _ bool) ([]buddynav.BodyMotionSample, error) {
				to := from
				to.X += float32(math.Cos(bearing) * speed * d.Seconds())
				to.Y += float32(math.Sin(bearing) * speed * d.Seconds())
				return []buddynav.BodyMotionSample{{Point: from}, {At: d, Point: to}}, nil
			}
			s.bodyMotionWait = func(_ context.Context, d time.Duration) error { advance(d); return nil }
			f.failSend = true
			out, err := s.StepMovementPoints(ctx, r)
			if err != nil || !out.MotionUnconfirmed {
				t.Fatal("entered UNKNOWN lost", out, err)
			}
			before := len(f.packets)
			r.SequenceId++
			out, err = s.StepMovementPoints(ctx, r)
			if err != nil || !out.MotionUnconfirmed || len(f.packets) != before {
				t.Fatal("UNKNOWN replayed", out, err)
			}
		})
	}
}

func TestMovementPointsOrdinaryFollowTickAfterCastBoundary(t *testing.T) {
	s, f, _, ctx, advance := targetMovementFixture(t)
	f.world.Owner.Health = f.world.Owner.MaxHealth
	f.world.Owner.X = 20
	s.state.Following = true
	s.followUntil = s.now().Add(time.Minute)
	s.lastStep = s.now()
	if _, err := s.SetMovementPoints(ctx, &actionsv1.MovementPointsRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, Points: []*actionsv1.MovementPoint{{X: 2, RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}}}); err != nil {
		t.Fatal(err)
	}
	s.state.CastPending = true
	s.castUntil = s.now().Add(5 * time.Second)
	s.pendingSpell, s.pendingTarget = 2050, 3
	s.reserveExplicitCastLocked(testEncounterKey, 2050, 3, "heal", true)
	before := len(f.packets)
	s.Tick(context.Background())
	if !s.state.CastPending {
		t.Fatal("follow point cleared cast", s.stateLocked())
	}
	for _, p := range f.packets[before:] {
		if p.Opcode == opcode.MSGMoveStartForward || p.Opcode == opcode.MSGMoveHeartbeat || p.Opcode == opcode.CMSGCancelCast {
			t.Fatal("follow point interrupted cast", p)
		}
	}
	s.endExplicitCastLocked("effect_observed")
	progressed := false
	for i := 0; i < 8; i++ {
		advance(200 * time.Millisecond)
		if _, err := s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(1)); err != nil {
			t.Fatal(err)
		}
		s.Tick(context.Background())
		if pointClearance(s.pos, s.currentMovementPointsLocked()) > -3.0 {
			progressed = true
			break
		}
	}
	if !progressed || !s.state.Following {
		t.Fatal("ordinary follow did not progress away after safe yield", s.stateLocked(), s.pos)
	}
}

func TestMovementPointLateRejectionOwesOnlySameBodyStop(t *testing.T) {
	for _, mode := range []string{"lease_expiry", "point_update", "different_body"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, ctx, advance := targetMovementFixture(t)
			if _, err := s.SetMovementPoints(ctx, &actionsv1.MovementPointsRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, Points: []*actionsv1.MovementPoint{{X: 2, RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}}}); err != nil {
				t.Fatal(err)
			}
			s.movementOwner = movementOwner{owner: "ranged:exact", token: "exact-token", until: s.now().Add(time.Second)}
			r := &actionsv1.MovementPointsStepRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, SequenceId: 117, MovementOwner: s.movementOwner.owner, MovementToken: s.movementOwner.token}
			var plannedTo buddynav.Point
			s.bodyMotionPlan = func(_ context.Context, _ string, _ uint32, from buddynav.Point, bearing, speed float64, d time.Duration, _ bool) ([]buddynav.BodyMotionSample, error) {
				to := from
				to.X += float32(math.Cos(bearing) * speed * d.Seconds())
				to.Y += float32(math.Sin(bearing) * speed * d.Seconds())
				plannedTo = to
				return []buddynav.BodyMotionSample{{Point: from}, {At: d, Point: to}}, nil
			}
			s.bodyMotionWait = func(_ context.Context, d time.Duration) error {
				advance(d)
				switch mode {
				case "lease_expiry":
					s.movementOwner.until = s.now().Add(-time.Millisecond)
				case "point_update":
					_, err := s.SetMovementPoints(ctx, &actionsv1.MovementPointsRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, Points: []*actionsv1.MovementPoint{{X: plannedTo.X, Y: plannedTo.Y, Z: plannedTo.Z, RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}}})
					if err != nil {
						t.Fatal(err)
					}
				case "different_body":
					f.world.Self.Guid++
				}
				return nil
			}
			out, err := s.StepMovementPoints(ctx, r)
			if err != nil || !out.MotionUnconfirmed {
				t.Fatal("late custody lost", out, err)
			}
			starts, stops, heartbeats := 0, 0, 0
			for _, p := range f.packets {
				switch p.Opcode {
				case opcode.MSGMoveStartForward:
					starts++
				case opcode.MSGMoveStop:
					stops++
				case opcode.MSGMoveHeartbeat:
					heartbeats++
				}
			}
			if starts != 1 || heartbeats != 0 {
				t.Fatal("later travel entered", starts, heartbeats)
			}
			if mode == "different_body" {
				if stops != 0 {
					t.Fatal("stale body STOP written")
				}
			} else if stops != 1 || out.Moving {
				t.Fatal("same input did not settle its own movement", out, stops)
			}
		})
	}
}

func TestMovementPointExpiredLeaseAllowsOnlyDeclarativeClear(t *testing.T) {
	s, f, _, ctx, advance := targetMovementFixture(t)
	r := &actionsv1.MovementPointsRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, Points: []*actionsv1.MovementPoint{{X: 2, RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}}}
	if _, err := s.SetMovementPoints(ctx, r); err != nil {
		t.Fatal(err)
	}
	advance(2 * time.Second)
	s.state.MotionUnconfirmed = true
	before := len(f.packets)
	if _, err := s.SetMovementPoints(ctx, r); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("expired writer refreshed geometry", err)
	}
	r.Points = nil
	r.SelfGuid++
	if _, err := s.SetMovementPoints(ctx, r); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("foreign body cleared geometry", err)
	}
	r.SelfGuid = s.selfGUID
	if _, err := s.SetMovementPoints(context.Background(), r); status.Code(err) != codes.PermissionDenied {
		t.Fatal("unauthenticated clear", err)
	}
	out, err := s.SetMovementPoints(ctx, r)
	if err != nil || out.GetMovementPoints().GetActive() || !out.MotionUnconfirmed || len(f.packets) != before {
		t.Fatal("safe clear required movement authority", out, err)
	}
}

func TestMovementPointDataDoesNotRerouteKnownDeadBody(t *testing.T) {
	s, f, _, ctx, _ := targetMovementFixture(t)
	if _, err := s.SetMovementPoints(ctx, &actionsv1.MovementPointsRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, Points: []*actionsv1.MovementPoint{{X: 2, RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}}}); err != nil {
		t.Fatal(err)
	}
	f.world.Self.AliveKnown, f.world.Self.Alive = true, false
	if _, err := s.snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	path := []position{s.pos, {x: 10}}
	got, ok := s.movementPointRouteLocked(ctx, path)
	if !ok || len(got) != len(path) || got[1] != path[1] {
		t.Fatal("living effect data changed dead-body route", got, ok)
	}
}

func TestMovementPointOverlappingDisksUseOnlyWalkableExit(t *testing.T) {
	s, f, _, ctx, advance := targetMovementFixture(t)
	f.world.Self.X, f.world.Self.Y = 1, .5
	f.world.Self.PositionRevision++
	w, err := s.snapshot(ctx)
	if err != nil || !s.refreshPositionLocked(w) {
		t.Fatal(err)
	}
	s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
		if to.x >= from.x-.1 || math.Abs(float64(to.y-.5)) > .001 {
			return nil, errors.New("synthetic walls leave only the west ground route")
		}
		return []position{from, to}, nil
	}
	request := &actionsv1.MovementPointsRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, Points: []*actionsv1.MovementPoint{{RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}, {X: 2, RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}}}
	if _, err = s.SetMovementPoints(ctx, request); err != nil {
		t.Fatal(err)
	}
	s.bodyMotionPlan = func(_ context.Context, _ string, _ uint32, from buddynav.Point, bearing, speed float64, d time.Duration, _ bool) ([]buddynav.BodyMotionSample, error) {
		to := from
		to.X += float32(math.Cos(bearing) * speed * d.Seconds())
		to.Y += float32(math.Sin(bearing) * speed * d.Seconds())
		return []buddynav.BodyMotionSample{{Point: from}, {At: d, Point: to}}, nil
	}
	s.bodyMotionWait = func(_ context.Context, d time.Duration) error {
		advance(d)
		request.Points[0], request.Points[1] = request.Points[1], request.Points[0]
		_, err := s.SetMovementPoints(ctx, request)
		return err
	}
	for i := uint64(0); i < 8 && pointClearance(s.pos, s.currentMovementPointsLocked()) < 0; i++ {
		out, err := s.StepMovementPoints(ctx, &actionsv1.MovementPointsStepRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, SequenceId: 300 + i})
		if err != nil || out.GetBodyMovement().GetPhase() != "body_input_written" || out.MotionUnconfirmed {
			t.Fatal("overlap prevented verified escape prefix", out, err)
		}
	}
	if pointClearance(s.pos, s.currentMovementPointsLocked()) < 0 || s.pos.x >= 0 {
		t.Fatal("failed to leave overlapping disks along the only ground route", s.pos, s.stateLocked())
	}
	if s.movementPoints.revision != 1 {
		t.Fatal("unchanged reordered input looked like new geometry", s.movementPoints.revision)
	}
}

func TestMovementPointEscapeCannotShortcutFirstTurn(t *testing.T) {
	s, f, _, ctx, advance := targetMovementFixture(t)
	f.world.Self.X, f.world.Self.Y = 1, .5
	f.world.Self.PositionRevision++
	w, err := s.snapshot(ctx)
	if err != nil || !s.refreshPositionLocked(w) {
		t.Fatal(err)
	}
	request := &actionsv1.MovementPointsRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, Points: []*actionsv1.MovementPoint{{RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}, {X: 2, RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}}}
	if _, err = s.SetMovementPoints(ctx, request); err != nil {
		t.Fatal(err)
	}
	folded := []position{{x: 1, y: .5}, {x: 1.1, y: .5}, {x: 1.1, y: 1}, {x: 2.4, y: .5}, {x: -5, y: .5}}
	proof := &movementPointsEscapePath{revision: s.movementPoints.revision, path: folded}
	if s.movementPointEscapePrefix(proof, position{x: 2.4, y: .5}) {
		t.Fatal("a later segment authorized a first-leg shortcut")
	}
	if !s.movementPointEscapePrefix(proof, position{x: 1.05, y: .5}) {
		t.Fatal("actual initial prefix refused")
	}
	s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
		if to.x >= from.x-.1 || math.Abs(float64(to.y-.5)) > .001 {
			return nil, errors.New("synthetic west exit requires an early turn")
		}
		return []position{from, {x: 1.1, y: .5}, {x: 1.1, y: 1}, {x: 2.4, y: .5}, to}, nil
	}
	var planned time.Duration
	s.bodyMotionPlan = func(_ context.Context, _ string, _ uint32, from buddynav.Point, bearing, speed float64, d time.Duration, _ bool) ([]buddynav.BodyMotionSample, error) {
		planned = d
		to := from
		to.X += float32(math.Cos(bearing) * speed * d.Seconds())
		to.Y += float32(math.Sin(bearing) * speed * d.Seconds())
		return []buddynav.BodyMotionSample{{Point: from}, {At: d, Point: to}}, nil
	}
	s.bodyMotionWait = func(_ context.Context, d time.Duration) error { advance(d); return nil }
	out, err := s.StepMovementPoints(ctx, &actionsv1.MovementPointsStepRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, SequenceId: 401})
	if err != nil || planned < time.Millisecond || planned > 14*time.Millisecond || out.GetBodyMovement().GetPhase() != "body_input_written" || s.pos.x > 1.1 || math.Abs(float64(s.pos.y-.5)) > .001 {
		t.Fatal("input ran beyond its first verified segment", out, err, planned, s.pos)
	}
}

func TestMovementPointOutsideBlockedGoalHoldsThenExpiryResumes(t *testing.T) {
	s, f, r, ctx, advance := targetMovementFixture(t)
	f.world.Self.X = 5.6
	f.world.Self.PositionRevision++
	f.world.RequestedCreature.X = 0
	r.RangeYards = .5
	w, err := s.snapshot(ctx)
	if err != nil || !s.refreshPositionLocked(w) {
		t.Fatal(err)
	}
	if _, err = s.SetMovementPoints(ctx, &actionsv1.MovementPointsRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, Points: []*actionsv1.MovementPoint{{RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err = s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(1)); err != nil {
			t.Fatal(err)
		}
		out, err := s.ApproachTarget(ctx, r)
		if err != nil || out.Moving || out.GetTargetMovement().GetReady() || s.pos.x != 5.6 || out.GetMovementPoints().GetReason() != "goal_inside_active_point" {
			t.Fatal("blocked destination caused outward wandering", out, err, s.pos)
		}
		advance(200 * time.Millisecond)
	}
	if countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
		t.Fatal("blocked goal wrote travel")
	}
	advance(20 * time.Second)
	if _, err = s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(1)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApproachTarget(ctx, r); err != nil {
		t.Fatal(err)
	}
	advance(200 * time.Millisecond)
	out, err := s.ApproachTarget(ctx, r)
	if err != nil || s.pos.x >= 5.6 || !out.Moving {
		t.Fatal("expiry did not resume original movement", out, err, s.pos)
	}
}

func TestMovementPointOutsideDetourRejoinsOriginalGoal(t *testing.T) {
	s, f, r, ctx, advance := targetMovementFixture(t)
	f.world.Self.X = 8
	f.world.Self.PositionRevision++
	f.world.RequestedCreature.X = -8
	r.RangeYards = .5
	w, err := s.snapshot(ctx)
	if err != nil || !s.refreshPositionLocked(w) {
		t.Fatal(err)
	}
	if _, err = s.SetMovementPoints(ctx, &actionsv1.MovementPointsRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, Points: []*actionsv1.MovementPoint{{RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}}}); err != nil {
		t.Fatal(err)
	}
	ready := false
	for i := 0; i < 80; i++ {
		if _, err = s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(1)); err != nil {
			t.Fatal(err)
		}
		out, err := s.ApproachTarget(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		if pointClearance(s.pos, s.currentMovementPointsLocked()) < -.001 {
			t.Fatal("outside detour entered excluded area", s.pos)
		}
		if out.GetTargetMovement().GetReady() {
			ready = true
			break
		}
		advance(200 * time.Millisecond)
	}
	if !ready || distance(s.pos, position{x: -8}) > .5 {
		t.Fatal("detour oscillated or failed to rejoin original goal", s.pos, s.stateLocked())
	}
}

func TestMovementPointDistantBlockedGoalDoesNotFreezeLocalTravel(t *testing.T) {
	s, f, _, ctx, _ := targetMovementFixture(t)
	f.world.Self.X = 100
	f.world.Self.PositionRevision++
	w, err := s.snapshot(ctx)
	if err != nil || !s.refreshPositionLocked(w) {
		t.Fatal(err)
	}
	if _, err = s.SetMovementPoints(ctx, &actionsv1.MovementPointsRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, Points: []*actionsv1.MovementPoint{{RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}}}); err != nil {
		t.Fatal(err)
	}
	path := []position{s.pos, {x: 0}}
	got, ok := s.movementPointRouteLocked(ctx, path)
	if !ok || len(got) != 2 || got[1] != path[1] {
		t.Fatal("far circle froze an unrelated next leg", got, ok)
	}
}

func TestMovementPointDetourRanksAgainstOriginalGoal(t *testing.T) {
	s, f, _, ctx, _ := targetMovementFixture(t)
	f.world.Self.X = 6
	f.world.Self.PositionRevision++
	w, err := s.snapshot(ctx)
	if err != nil || !s.refreshPositionLocked(w) {
		t.Fatal(err)
	}
	if _, err = s.SetMovementPoints(ctx, &actionsv1.MovementPointsRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, Points: []*actionsv1.MovementPoint{{RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}}}); err != nil {
		t.Fatal(err)
	}
	originalGoal := position{x: -20}
	path, ok := s.movementPointRouteLocked(ctx, []position{s.pos, originalGoal})
	if !ok || len(path) < 2 {
		t.Fatal(path, ok)
	}
	endpoint := path[len(path)-1]
	if math.Abs(float64(endpoint.x-6)) > .01 || math.Abs(math.Abs(float64(endpoint.y))-6) > .01 {
		t.Fatal("candidate was ranked against itself rather than original goal", endpoint)
	}
}

func TestMovementPointOutsideOriginCannotUseSnappedInsideException(t *testing.T) {
	s, f, _, ctx, _ := targetMovementFixture(t)
	f.world.Self.X = 5.1
	f.world.Self.PositionRevision++
	w, err := s.snapshot(ctx)
	if err != nil || !s.refreshPositionLocked(w) {
		t.Fatal(err)
	}
	if _, err = s.SetMovementPoints(ctx, &actionsv1.MovementPointsRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, MapId: s.posMap, VisitId: 1, Points: []*actionsv1.MovementPoint{{RadiusYards: 5, ExpiresAtUnixMs: uint64(s.now().Add(20 * time.Second).UnixMilli())}}}); err != nil {
		t.Fatal(err)
	}
	s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
		if to.x < from.x+5.9 || math.Abs(float64(to.y)) > .001 {
			return nil, errors.New("only snapped east route supplied")
		}
		return []position{{x: 4}, to}, nil
	}
	if path, ok := s.movementPointRouteLocked(ctx, []position{s.pos, {x: -8}}); ok {
		t.Fatal("outside body joined an inside-only exception", path)
	}
}
