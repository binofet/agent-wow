package buddyactions

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestNearOwnerFollowDefaultLuaHoldsAtExistingBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		x, z   float32
		reason string
	}{
		{"already together", 0, 0, "within_follow_distance"},
		{"inside stop radius", 2.5, 0, "within_follow_distance"},
		{"exact stop radius", 3, 0, "within_follow_distance"},
		{"outside catch-up range", 30.01, 0, "follow_target_too_far_or_vertical"},
		{"vertical gap", 10, 1.51, "follow_target_too_far_or_vertical"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, advance := rig(t)
			f.world.Owner.X, f.world.Owner.Z = tc.x, tc.z
			s.planPath = func(context.Context, string, position, position) ([]position, error) {
				t.Fatal("held owner observation caused a route query")
				return nil, nil
			}
			toggle(t, s, true)
			for range 3 {
				s.Tick(context.Background())
				advance(200 * time.Millisecond)
			}
			if s.state.Moving || !s.state.Following || s.state.Reason != tc.reason || len(f.packets) != 0 {
				t.Fatal("default near-owner policy changed its hold boundary", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestNearOwnerFollowUsesVerifiedRouteAndNormalRunBudget(t *testing.T) {
	s, f, advance := rig(t)
	plans := 0
	s.planPath = func(ctx context.Context, root string, from, to position) ([]position, error) {
		plans++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 251*time.Millisecond || root != "" || to != playerPosition(f.world.Owner) {
			t.Fatal("near-owner executor changed its planner scope", root, to, deadline)
		}
		return []position{from, {x: from.x, y: from.y + 5, z: from.z}, to}, nil
	}
	toggle(t, s, true)
	s.Tick(context.Background())
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if plans != 2 || len(f.packets) != 2 || f.packets[0].Opcode != opcode.MSGMoveStartForward || f.packets[1].Opcode != opcode.MSGMoveHeartbeat ||
		math.Abs(float64(s.pos.y)-1.4) > .001 || s.pos.x != 0 || !s.state.NavAvailable || !s.state.Following || !s.state.Moving {
		t.Fatal("follow crossed the planned corner or changed the 7yd/s budget", plans, s.pos, s.stateLocked(), f.packets)
	}
}

func TestNearOwnerFollowNeverFallsBackFromIncompleteOrBlockedRoute(t *testing.T) {
	for _, mode := range []string{"blocked", "partial", "empty", "nonfinite", "overbudget"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := rig(t)
			s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
				switch mode {
				case "blocked":
					return nil, errors.New("ground corridor blocked")
				case "partial":
					to.x -= 3
					return []position{from, to}, nil
				case "empty":
					return nil, nil
				case "nonfinite":
					return []position{from, {x: float32(math.NaN())}, to}, nil
				default:
					return []position{from, {x: from.x, y: from.y + 150, z: from.z}, to}, nil
				}
			}
			toggle(t, s, true)
			for range 3 {
				s.Tick(context.Background())
				advance(200 * time.Millisecond)
			}
			if len(f.packets) != 0 || s.state.Moving || s.state.NavAvailable || !s.state.Following || s.state.Reason != "follow_navigation_unavailable" {
				t.Fatal("incomplete near-owner route produced movement", s.stateLocked(), f.packets)
			}
		})
	}
	s, f, advance := rig(t)
	toggle(t, s, true)
	s.Tick(context.Background())
	s.planPath = func(context.Context, string, position, position) ([]position, error) {
		return nil, errors.New("route became blocked")
	}
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if len(f.packets) != 2 || f.packets[1].Opcode != opcode.MSGMoveStop || s.state.Moving || s.state.NavAvailable || !s.state.Following {
		t.Fatal("lost route continued a previously started movement", s.stateLocked(), f.packets)
	}
}

func TestNearOwnerFollowAcceptsFiniteHostGoalAndExactRadiusWithoutReconnect(t *testing.T) {
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	activateEncounterWriter(t, s, f)
	toggle(t, s, true)
	ctx := encounterHostContext(testEncounterKey, 1)
	owner := f.world.Owner
	req := &actionsv1.FollowFormationRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1,
		FollowRevision: s.followRevision, OwnerGuid: owner.Guid, OwnerPositionRevision: owner.PositionRevision,
		X: owner.X, Y: owner.Y, Z: owner.Z, Orientation: owner.Orientation, StartDistance: 1, StopDistance: 1, Ready: true}
	for range 9 {
		if _, err := s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(1)); err != nil {
			t.Fatal(err)
		}
		before := len(f.packets)
		if _, err := s.SetFollowFormation(ctx, req); err != nil || len(f.packets) != before {
			t.Fatal("current host could not update a no-nav goal without a packet", err, f.packets)
		}
		s.Tick(context.Background())
		advance(200 * time.Millisecond)
	}
	if s.state.Moving || !s.state.Following || math.Abs(distance(s.pos, playerPosition(owner))-1) > .001 || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal("no-nav executor ignored the host's exact stop radius", s.pos, s.stateLocked(), f.packets)
	}
	// Change the personal destination in the same session, then hold it while
	// still farther than the requested restart radius. Neither needs a toggle.
	req.X, req.Y, req.Hold = owner.X, owner.Y+4, true
	before := len(f.packets)
	if _, err := s.SetFollowFormation(ctx, req); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	if len(f.packets) != before || s.state.Moving || !s.state.Following {
		t.Fatal("host hold was ignored by no-nav follow", s.stateLocked(), f.packets)
	}
	advance(200 * time.Millisecond)
	req.Hold = false
	if _, err := s.SetFollowFormation(ctx, req); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background())
	if !s.state.Moving || countPackets(f, opcode.MSGMoveStartForward) != 2 || s.sessionID != req.SessionId || s.followRevision != req.FollowRevision {
		t.Fatal("replacement goal needed a reconnect or changed follow intent", s.stateLocked(), f.packets)
	}
	oldGoal := s.followGoal
	if _, err := s.SetFollowFormation(encounterHostContext(testEncounterKey, 2), req); status.Code(err) != codes.FailedPrecondition || s.followGoal != oldGoal {
		t.Fatal("obsolete host visit changed the no-nav goal", err)
	}
}

func TestNearOwnerFollowPreservesWaitCustodyAndStandingHealing(t *testing.T) {
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	toggle(t, s, true)
	s.Tick(context.Background())
	lease := acquireMovementForTest(t, s, 1000)
	before := len(f.packets)
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if len(f.packets) != before || s.state.Moving || !s.state.Following || s.movementOwner.token != lease.Token {
		t.Fatal("shared near-owner policy displaced finite wait custody", s.stateLocked(), f.packets)
	}
	learn(s)
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if !s.state.CastPending || s.state.LastCastTargetGuid != ownerGUID || !s.state.Following || s.state.Moving || s.movementOwner.token != lease.Token || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("near-owner hold suppressed standing healing or resumed voluntary movement", s.stateLocked(), f.packets)
	}
}
