package buddyactions

import (
	"context"
	"fmt"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func startupFollowFixture(t *testing.T, navigation bool) (*Service, *fakeSession, *actionsv1.FollowFormationRequest, context.Context, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	f.world.Owner.Health = f.world.Owner.MaxHealth
	if navigation {
		s.SetNavigationDataDir("/client/startup-follow-fixture")
	}
	activateEncounterWriter(t, s, f)
	if !s.refreshPositionLocked(f.world) {
		t.Fatal("startup fixture could not bind its actual pose")
	}
	s.lastStep = s.now()
	owner := f.world.Owner
	r := &actionsv1.FollowFormationRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, FollowRevision: 0,
		OwnerGuid: owner.Guid, OwnerPositionRevision: owner.PositionRevision,
		X: owner.X - 2, Y: owner.Y, Z: owner.Z, Orientation: owner.Orientation,
		StartDistance: 1.9, StopDistance: .85, Ready: true, Phase: "following_formation",
	}
	if s.state.Following || !s.followUntil.IsZero() || s.followRevision != 0 || len(f.packets) != 0 {
		t.Fatal("fixture implicitly enabled legacy following", s.stateLocked(), f.packets)
	}
	return s, f, r, encounterHostContext(testEncounterKey, 1), advance
}

func admitStartupFollow(t *testing.T, s *Service, f *fakeSession, r *actionsv1.FollowFormationRequest, ctx context.Context) {
	t.Helper()
	if _, err := s.SetFollowFormation(ctx, r); err != nil {
		t.Fatal("trusted startup revision-zero goal was refused", err)
	}
	if s.state.Following || !s.followUntil.IsZero() || s.followRevision != 0 || len(f.packets) != 0 || f.clock != 0 {
		t.Fatal("goal admission changed legacy intent or prepared a packet", s.stateLocked(), f.packets)
	}
}

func TestStartupFollowExactGoalMovesWithoutEnablingLegacyIntent(t *testing.T) {
	for _, navigation := range []bool{false, true} {
		t.Run(fmt.Sprintf("navigation=%t", navigation), func(t *testing.T) {
			s, f, request, ctx, advance := startupFollowFixture(t, navigation)
			admitStartupFollow(t, s, f, request, ctx)
			s.formation.remote = func(map[string]any) (bool, string, map[string]any, error) {
				t.Fatal("an admitted exact goal consulted a fallback chooser")
				return false, "", nil, nil
			}
			plans := 0
			s.planMapPath = func(_ context.Context, _ string, mapID uint32, from, to position) ([]position, error) {
				plans++
				if mapID != f.world.Self.MapId || to != (position{request.X, request.Y, request.Z, request.Orientation}) {
					t.Fatal("startup movement replaced its exact qualified destination", mapID, to)
				}
				return []position{from, to}, nil
			}
			start := s.pos
			s.Tick(context.Background())
			advance(200 * time.Millisecond)
			s.Tick(context.Background())
			if plans == 0 || !s.state.Moving || countPackets(f, opcode.MSGMoveStartForward) != 1 ||
				countPackets(f, opcode.MSGMoveHeartbeat) != 1 || distance(start, s.pos) <= 0 ||
				s.state.Following || !s.followUntil.IsZero() || s.followRevision != 0 || s.followGoalExecuting != nil {
				t.Fatal("finite goal failed to execute or leaked into standing legacy intent", plans, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestStartupFollowMissingGoalOrUncertainMotionCannotUseFallback(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("uncertain=%t", uncertain), func(t *testing.T) {
			s, f, request, ctx, _ := startupFollowFixture(t, true)
			if uncertain {
				admitStartupFollow(t, s, f, request, ctx)
				s.state.MotionUnconfirmed = true
				f.world.Self.PositionKnown = false
			}
			s.formation.remote = func(map[string]any) (bool, string, map[string]any, error) {
				t.Fatal("missing finite authority invoked a fallback chooser")
				return false, "", nil, nil
			}
			s.Tick(context.Background())
			if len(f.packets) != 0 || s.state.Moving || s.state.Following || !s.followUntil.IsZero() ||
				uncertain && !s.state.MotionUnconfirmed {
				t.Fatal("startup goal invented movement or cleared unresolved transport", s.stateLocked(), f.packets)
			}
		})
	}
}

func TestStartupFollowRevokedAuthorityStopsExistingSafeMovement(t *testing.T) {
	for _, change := range []string{"goal expiry", "host expiry", "missing goal", "host epoch", "owner absent", "World transfer", "World body"} {
		t.Run(change, func(t *testing.T) {
			s, f, request, ctx, advance := startupFollowFixture(t, true)
			if change == "host expiry" {
				advance(200 * time.Millisecond) // The goal will outlive this older host lease.
			}
			admitStartupFollow(t, s, f, request, ctx)
			s.Tick(context.Background())
			if !s.state.Moving || countPackets(f, opcode.MSGMoveStartForward) != 1 {
				t.Fatal("fixture did not start the finite route", s.stateLocked(), f.packets)
			}
			switch change {
			case "goal expiry":
				advance(encounterLeaseDuration)
				if _, err := s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(1)); err != nil {
					t.Fatal("could not keep only the host lease current", err)
				}
			case "host expiry":
				advance(800 * time.Millisecond)
			case "missing goal":
				s.followGoal = followFormationGoal{}
			case "host epoch":
				s.hostEncounterEpoch++
			case "owner absent":
				f.world.Owner.Present = false
			case "World transfer":
				f.world.VisitRevision++
				f.world.Self.MapId++
				f.world.Self.PositionRevision++
			case "World body":
				f.world.Self.Guid++
			}
			s.formation.remote = func(map[string]any) (bool, string, map[string]any, error) {
				t.Fatal("revoked startup authority fell through to builtin following")
				return false, "", nil, nil
			}
			s.Tick(context.Background())
			if countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 ||
				s.state.Following || !s.followUntil.IsZero() {
				t.Fatal("revoked authority wrote more travel or enabled legacy intent", s.stateLocked(), f.packets)
			}
			if change != "World transfer" && change != "World body" && (s.state.Moving || countPackets(f, opcode.MSGMoveStop) != 1) {
				t.Fatal("revoked goal did not stop its still-observed own movement", s.stateLocked(), f.packets)
			}
		})
	}
}

type startupFollowWorldHook struct {
	*fakeSession
	beforeWorld func()
}

func (s *startupFollowWorldHook) InvokeModule(ctx context.Context, request *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if request.Module == "buddy-world" && request.Method == "snapshot" && s.beforeWorld != nil {
		s.beforeWorld()
	}
	return s.fakeSession.InvokeModule(ctx, request, opts...)
}

func TestStartupFollowRechecksCapturedGoalAfterPlannerClockAndWorld(t *testing.T) {
	cases := []struct{ seam, change string }{
		{"planner", "goal replacement"}, {"planner", "host epoch"},
		{"clock", "goal expiry"}, {"clock", "host expiry"}, {"clock", "follow revision"}, {"clock", "session"},
		{"World", "World visit"}, {"World", "owner absent"}, {"World", "World body"}, {"World", "goal replacement"},
	}
	for _, navigation := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("navigation=%t/%s/%s", navigation, tc.seam, tc.change), func(t *testing.T) {
				s, f, request, ctx, advance := startupFollowFixture(t, navigation)
				if tc.change == "host expiry" {
					advance(200 * time.Millisecond)
				}
				admitStartupFollow(t, s, f, request, ctx)
				if tc.change == "goal expiry" {
					advance(200 * time.Millisecond)
					if _, err := s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(1)); err != nil {
						t.Fatal("could not separate goal expiry from host expiry", err)
					}
				}
				injected, clockSeen := false, false
				var replacement followFormationGoal
				change := func() {
					if injected {
						return
					}
					injected = true
					switch tc.change {
					case "goal replacement":
						s.followGoal.target.Position.x += .5
						replacement = s.followGoal
					case "host epoch":
						s.hostEncounterEpoch++
					case "goal expiry", "host expiry":
						advance(800 * time.Millisecond)
					case "follow revision":
						s.followRevision++
					case "session":
						s.sessionID = "replacement-session"
					case "World visit":
						f.world.VisitRevision++
					case "owner absent":
						f.world.Owner.Present = false
					case "World body":
						f.world.Self.Guid++
					}
				}
				s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
					if tc.seam == "planner" {
						change()
					}
					return []position{from, to}, nil
				}
				f.onClock = func() {
					clockSeen = true
					if tc.seam == "clock" {
						change()
					}
				}
				s.session = &startupFollowWorldHook{fakeSession: f, beforeWorld: func() {
					if tc.seam == "World" && clockSeen {
						change()
					}
				}}
				s.Tick(context.Background())
				if !injected || len(f.packets) != 0 || s.state.Moving || s.state.Following || !s.followUntil.IsZero() || s.followGoalExecuting != nil {
					t.Fatal("stale captured goal reached physical transport", injected, s.stateLocked(), f.packets)
				}
				if tc.change == "goal replacement" && s.followGoal != replacement {
					t.Fatal("old executor cleared or rewrote the replacement goal", replacement, s.followGoal)
				}
			})
		}
	}
}

func TestStartupFollowExplicitStopRejectsOldGoalAndAllowsFreshCurrentDecision(t *testing.T) {
	s, f, request, ctx, _ := startupFollowFixture(t, true)
	admitStartupFollow(t, s, f, request, ctx)
	s.Tick(context.Background())
	toggle(t, s, false)
	before := len(f.packets)
	if s.followRevision != 1 || !s.followGoal.until.IsZero() || s.state.Following || !s.followUntil.IsZero() {
		t.Fatal("explicit disable left the startup goal authoritative", s.stateLocked(), s.followGoal)
	}
	if _, err := s.SetFollowFormation(ctx, request); status.Code(err) != codes.FailedPrecondition || len(f.packets) != before {
		t.Fatal("old revision zero rearmed the explicit disable", err, f.packets)
	}
	s.Tick(context.Background())
	if s.state.Moving || len(f.packets) != before || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal("stopped startup goal resumed without a fresh Lua decision", s.stateLocked(), f.packets)
	}
	request.FollowRevision = s.followRevision
	if _, err := s.SetFollowFormation(ctx, request); err != nil || len(f.packets) != before {
		t.Fatal("fresh current-revision decision could not acquire a new finite goal", err, f.packets)
	}
	s.Tick(context.Background())
	if !s.state.Moving || countPackets(f, opcode.MSGMoveStartForward) != 2 || countPackets(f, opcode.MSGMoveStop) != 1 ||
		s.state.Following || !s.followUntil.IsZero() || s.followRevision != 1 {
		t.Fatal("fresh current Lua decision failed to resume independently of legacy Follow", s.stateLocked(), f.packets)
	}
}

func TestStartupFollowIndependentModeCannotAcquireOwnerGoal(t *testing.T) {
	base, f, request, _, _ := startupFollowFixture(t, true)
	s := NewIndependentService(f, base.selfGUID, base.sessionID)
	s.now = base.now
	activateEncounterWriter(t, s, f)
	if _, err := s.SetFollowFormation(encounterHostContext(testEncounterKey, 1), request); status.Code(err) != codes.FailedPrecondition ||
		!s.followGoal.until.IsZero() || len(f.packets) != 0 {
		t.Fatal("independent body acquired owner-follow authority", err, s.followGoal, f.packets)
	}
}

func TestStartupFollowWaitCustodyPreservesStandingHealing(t *testing.T) {
	s, f, request, ctx, advance := startupFollowFixture(t, true)
	admitStartupFollow(t, s, f, request, ctx)
	tickLuaHealing(t, s)
	if !s.state.Moving {
		t.Fatal("fixture did not begin its exact startup route")
	}
	lease := acquireMovementForTest(t, s, 3000)
	before := len(f.packets)
	advance(200 * time.Millisecond)
	if _, err := s.SetFollowFormation(ctx, request); err != nil {
		t.Fatal(err)
	}
	tickLuaHealing(t, s)
	if len(f.packets) != before || s.state.Moving || s.state.Following || !s.followUntil.IsZero() || s.movementOwner.token != lease.Token {
		t.Fatal("startup goal escaped the finite task's movement custody", s.stateLocked(), f.packets)
	}
	learn(s)
	f.world.Owner.Health = 25
	advance(200 * time.Millisecond)
	tickLuaHealing(t, s)
	if countPackets(f, opcode.CMSGCastSpell) != 1 || s.state.LastCastTargetGuid != f.world.Owner.Guid || !s.state.CastPending ||
		s.state.Moving || s.state.Following || !s.followUntil.IsZero() || s.followRevision != 0 || s.movementOwner.token != lease.Token {
		t.Fatal("finite startup follow or wait suppressed standing healing", s.stateLocked(), f.packets)
	}
}
