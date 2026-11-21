package buddyactions

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func followGoalFixture(t *testing.T) (*Service, *fakeSession, *actionsv1.FollowFormationRequest, context.Context, func(time.Duration)) {
	t.Helper()
	s, f, advance := formationRig(t, 1, 5)
	s.sessionID = f.party.SessionId
	f.world.Owner.Health = f.world.Owner.MaxHealth
	f.world.Owner.X += 30
	activateEncounterWriter(t, s, f)
	toggle(t, s, true)
	owner := f.world.Owner
	req := &actionsv1.FollowFormationRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, FollowRevision: s.followRevision,
		OwnerGuid: owner.Guid, OwnerPositionRevision: owner.PositionRevision,
		X: owner.X - 4, Y: owner.Y + 2, Z: owner.Z, Orientation: owner.Orientation,
		StartDistance: 1.9, StopDistance: .85, Ready: true,
	}
	return s, f, req, encounterHostContext(testEncounterKey, 1), advance
}

func TestFollowFormationGoalRequiresCurrentHostBodyVisitIntentAndOwner(t *testing.T) {
	for _, scenario := range []string{
		"valid", "missing host", "wrong host", "stale metadata visit", "session", "body", "visit", "follow revision", "follow disabled", "follow toggled",
		"expired host", "Actions transfer", "World transfer", "World unavailable", "World body", "owner", "owner revision", "owner absent", "owner dead", "owner map", "owner position",
		"nonfinite target", "nonfinite radius", "radius ordering", "destination envelope", "cancelled",
	} {
		t.Run(scenario, func(t *testing.T) {
			s, f, req, ctx, advance := followGoalFixture(t)
			if _, err := s.SetFollowFormation(ctx, req); err != nil {
				t.Fatal("valid goal was not admitted", err)
			}
			want := codes.FailedPrecondition
			switch scenario {
			case "valid":
				want = codes.OK
			case "missing host":
				ctx, want = context.Background(), codes.PermissionDenied
			case "wrong host":
				ctx, want = encounterHostContext(strings.Repeat("a", 64), 1), codes.PermissionDenied
			case "stale metadata visit":
				ctx = encounterHostContext(testEncounterKey, 2)
			case "session":
				req.SessionId = "previous-body-session"
			case "body":
				req.SelfGuid++
			case "visit":
				req.VisitId++
			case "follow revision":
				req.FollowRevision++
			case "follow disabled":
				toggle(t, s, false)
			case "follow toggled":
				toggle(t, s, false)
				toggle(t, s, true)
			case "expired host":
				advance(encounterLeaseDuration)
			case "Actions transfer":
				s.encounterVisit.bound = false
			case "World transfer":
				f.world.VisitRevision++
			case "World unavailable":
				f.failSnapshot = true
			case "World body":
				f.world.Self.Guid++
			case "owner":
				req.OwnerGuid++
			case "owner revision":
				f.world.Owner.PositionRevision++
			case "owner absent":
				f.world.Owner.Present = false
			case "owner dead":
				f.world.Owner.Alive = false
			case "owner map":
				f.world.Owner.MapId++
			case "owner position":
				f.world.Owner.PositionKnown = false
			case "nonfinite target":
				req.X, want = float32(math.NaN()), codes.InvalidArgument
			case "nonfinite radius":
				req.StartDistance, want = math.Inf(1), codes.InvalidArgument
			case "radius ordering":
				req.StartDistance, want = req.StopDistance-.1, codes.InvalidArgument
			case "destination envelope":
				req.X = f.world.Owner.X + 17
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = codes.Canceled
			}
			before := s.followGoal
			out, err := s.SetFollowFormation(ctx, req)
			if status.Code(err) != want || want == codes.OK && out == nil {
				t.Fatalf("goal admission returned %v, want %v", err, want)
			}
			if want != codes.OK && s.followGoal != before {
				t.Fatal("rejected goal replaced admitted state", before, s.followGoal)
			}
			if len(f.packets) != 0 || len(f.movementObservations) != 0 || f.clock != 0 {
				t.Fatal("goal setter emitted gameplay or prepared movement", f.packets, f.movementObservations, f.clock)
			}
		})
	}
}

func TestFollowFormationGoalRefreshKeepsContinuousVerifiedRoute(t *testing.T) {
	s, f, req, ctx, advance := followGoalFixture(t)
	plans := 0
	s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
		plans++
		return []position{from, to}, nil
	}
	if _, err := s.SetFollowFormation(ctx, req); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	if !s.state.Moving || countPackets(f, opcode.MSGMoveStartForward) != 1 || plans != 1 {
		t.Fatal("admitted goal did not start its verified route", s.stateLocked(), f.packets, plans)
	}
	for range 5 {
		advance(200 * time.Millisecond)
		if _, err := s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(1)); err != nil {
			t.Fatal(err)
		}
		before, writes := s.followNav, len(f.packets)
		if _, err := s.SetFollowFormation(ctx, req); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, s.followNav) || len(f.packets) != writes {
			t.Fatal("refresh discarded the route or wrote a packet", before, s.followNav, f.packets)
		}
		s.Tick(context.Background())
	}
	if plans != 1 || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 5 || !s.state.Moving || !s.state.Following {
		t.Fatal("unchanged goal caused replans or start-stop motion", plans, s.stateLocked(), f.packets)
	}
}

func TestFollowFormationGoalExpiryReturnsToSharedLuaPlan(t *testing.T) {
	s, f, req, ctx, advance := followGoalFixture(t)
	if _, err := s.SetFollowFormation(ctx, req); err != nil {
		t.Fatal(err)
	}
	owner := playerPosition(f.world.Owner)
	if got := s.followFormationPlanLocked(s.now(), f.world, 5, owner, false); got.Position != (position{req.X, req.Y, req.Z, req.Orientation}) || got.StopDistance != req.StopDistance || s.formation.memory != nil {
		t.Fatal("current finite goal did not retain its exact Lua selection", got)
	}
	advance(800 * time.Millisecond)
	if _, err := s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(1)); err != nil {
		t.Fatal(err)
	}
	advance(200 * time.Millisecond) // Exact goal expiry while the host lease is current.
	var fallback followFormation
	want := fallback.Plan(s.now(), s.selfGUID, 5, owner, false, s.pos, s.state.Moving, s.followNav.ownerArrival)
	got := s.followFormationPlanLocked(s.now(), f.world, 5, owner, false)
	if got != want || !got.Valid || !got.Hold || !s.followGoal.until.IsZero() || s.formation.memory == nil || !s.state.Following || !s.hostEncounterActiveLocked() {
		t.Fatal("expired goal did not resume the shared Lua plan", got, want, s.followGoal, s.stateLocked())
	}
	advance(300 * time.Millisecond)
	want = fallback.Plan(s.now(), s.selfGUID, 5, owner, false, s.pos, s.state.Moving, s.followNav.ownerArrival)
	if got = s.followFormationPlanLocked(s.now(), f.world, 5, owner, false); got != want || !got.Ready || len(f.packets) != 0 {
		t.Fatal("fallback reaction did not progress without a host refresh", got, want, f.packets)
	}
}

func TestFollowFormationGoalYieldsToMovementWaitAndStandingHealing(t *testing.T) {
	s, f, req, ctx, advance := followGoalFixture(t)
	if _, err := s.SetFollowFormation(ctx, req); err != nil {
		t.Fatal(err)
	}
	tickLuaHealing(t, s)
	if !s.state.Moving {
		t.Fatal("fixture did not begin following", s.stateLocked())
	}
	lease := acquireMovementForTest(t, s, 3000)
	before, revision := len(f.packets), s.followRevision
	advance(200 * time.Millisecond)
	if _, err := s.SetFollowFormation(ctx, req); err != nil {
		t.Fatal(err)
	}
	tickLuaHealing(t, s)
	if len(f.packets) != before || s.state.Moving || !s.state.Following || s.followRevision != revision || s.state.Reason != "finite_movement_owner_active" || s.movementOwner.token != lease.Token {
		t.Fatal("formation goal escaped task custody or replaced follow intent", s.stateLocked(), f.packets)
	}
	learn(s)
	f.world.Owner.Health = 25
	advance(200 * time.Millisecond)
	tickLuaHealing(t, s)
	if countPackets(f, opcode.CMSGCastSpell) != 1 || s.state.LastCastTargetGuid != f.world.Owner.Guid || !s.state.CastPending || s.state.Moving || !s.state.Following || s.followRevision != revision || s.movementOwner.token != lease.Token {
		t.Fatal("finite formation or movement hold suppressed standing healing", s.stateLocked(), f.packets)
	}
}
