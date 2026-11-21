package buddyactions

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func portalGoalFixture(t *testing.T) (*Service, *fakeSession, *actionsv1.PortalCatchupRequest, context.Context, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	// Witness/goal semantics need the actual shared evaluator to be healthy;
	// the independent production watchdog test retains its default 5ms budget.
	runtime, err := behavior.NewWithOptions(behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Reload(behaviors.FollowFormationSource, ""); err != nil {
		t.Fatal(err)
	}
	s.formation.runtime = runtime
	s.sessionID = f.party.SessionId
	s.navDataDir = "/client/portal-fixture"
	portal := dungeonEntrances[0]
	start := portal.center
	start.y -= 80
	putAtPortal(f.world.Self, portal.source, start)
	putAtPortal(f.world.Owner, portal.source, portal.center)
	activateEncounterWriter(t, s, f)
	toggle(t, s, true)
	if !s.refreshPositionLocked(f.world) {
		t.Fatal("fixture could not observe its current pose")
	}
	s.lastWorld = f.world
	s.observeDungeonPortalLocked(f.world, s.now())
	if !s.followPortal.catchingUp || s.followPortal.witness == 0 {
		t.Fatal("fixture did not establish an actual entrance sighting", s.followPortal, s.formation.lastFailure, runtime.Status())
	}
	f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = false, false, false
	s.followPortal.lostAt = s.now().Add(-time.Second)
	bend := start
	bend.x, bend.y = bend.x+6, bend.y+8
	req := &actionsv1.PortalCatchupRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, OwnerGuid: ownerGUID,
		FollowRevision: s.followRevision, WitnessRevision: s.followPortal.witness,
		Start: s.portalStartLocked(f.world), Candidates: []*actionsv1.PortalPoint{
			{X: bend.x, Y: bend.y, Z: bend.z}, {X: portal.center.x, Y: portal.center.y, Z: portal.center.z},
		},
	}
	return s, f, req, encounterHostContext(testEncounterKey, 1), advance
}

func TestPortalCatchupGoalAdmissionBindsHostWitnessAndControllerPose(t *testing.T) {
	for _, name := range []string{
		"valid", "public caller", "foreign key", "metadata visit", "session", "body", "visit", "follow revision", "witness", "follow disabled",
		"Actions visit", "World visit", "World body", "expired host", "owner present", "raw pose revision", "client pose revision", "client XYZ", "client yaw",
		"start known", "start map", "start revision", "start XYZ", "start yaw", "missing candidates", "too many candidates", "nil candidate", "nonfinite candidate", "far candidate",
	} {
		t.Run(name, func(t *testing.T) {
			s, f, req, ctx, advance := portalGoalFixture(t)
			if _, err := s.SetPortalCatchup(ctx, req); err != nil {
				t.Fatal("valid fixture goal rejected", err)
			}
			want := codes.FailedPrecondition
			switch name {
			case "valid":
				want = codes.OK
			case "public caller":
				ctx, want = context.Background(), codes.PermissionDenied
			case "foreign key":
				ctx, want = encounterHostContext(strings.Repeat("a", 64), 1), codes.PermissionDenied
			case "metadata visit":
				ctx = encounterHostContext(testEncounterKey, 2)
			case "session":
				req.SessionId = "old-session"
			case "body":
				req.SelfGuid++
			case "visit":
				req.VisitId++
			case "follow revision":
				req.FollowRevision++
			case "witness":
				req.WitnessRevision++
			case "follow disabled":
				toggle(t, s, false)
			case "Actions visit":
				s.encounterVisit.revision++
			case "World visit":
				f.world.VisitRevision++
			case "World body":
				f.world.Self.Guid++
			case "expired host":
				advance(encounterLeaseDuration)
			case "owner present":
				f.world.Owner.Present = true
			case "raw pose revision":
				f.world.Self.PositionRevision++
			case "client pose revision":
				s.posRevision++
			case "client XYZ":
				s.pos.x += .01
			case "client yaw":
				s.pos.o += .01
			case "start known":
				req.Start.Known = false
			case "start map":
				req.Start.MapId++
			case "start revision":
				req.Start.Revision++
			case "start XYZ":
				req.Start.X += .01
			case "start yaw":
				req.Start.Orientation += .01
			case "missing candidates":
				req.Candidates, want = nil, codes.InvalidArgument
			case "too many candidates":
				req.Candidates, want = make([]*actionsv1.PortalPoint, 8), codes.InvalidArgument
			case "nil candidate":
				req.Candidates, want = []*actionsv1.PortalPoint{nil}, codes.InvalidArgument
			case "nonfinite candidate":
				req.Candidates[0].X, want = float32(math.NaN()), codes.InvalidArgument
			case "far candidate":
				req.Candidates[0].X, want = s.pos.x+25, codes.InvalidArgument
			}
			before := s.portalGoal
			out, err := s.SetPortalCatchup(ctx, req)
			if status.Code(err) != want || want == codes.OK && out == nil {
				t.Fatalf("admission error %v, want %v", err, want)
			}
			if want != codes.OK && !reflect.DeepEqual(before, s.portalGoal) {
				t.Fatal("rejected refresh changed an admitted goal", before, s.portalGoal)
			}
			if len(f.packets) != 0 || len(f.movementObservations) != 0 || f.clock != 0 {
				t.Fatal("goal setter emitted or prepared a gameplay packet", f.packets, f.clock)
			}
		})
	}
}

func TestPortalCatchupCandidateOrderIncludesExplicitDirectChoice(t *testing.T) {
	for _, directFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "prefer bend", true: "prefer direct"}[directFirst], func(t *testing.T) {
			s, f, req, ctx, _ := portalGoalFixture(t)
			if directFirst {
				req.Candidates[0], req.Candidates[1] = req.Candidates[1], req.Candidates[0]
			}
			chosen := position{x: req.Candidates[0].X, y: req.Candidates[0].Y, z: req.Candidates[0].Z}
			var queries [][2]position
			s.planMapPath = func(call context.Context, _ string, mapID uint32, from, to position) ([]position, error) {
				queries = append(queries, [2]position{from, to})
				deadline, ok := call.Deadline()
				if !ok || time.Until(deadline) > 251*time.Millisecond || mapID != f.world.Self.MapId {
					t.Fatal("unbounded or wrong-map route qualification", deadline, mapID)
				}
				return []position{from, to}, nil
			}
			if _, err := s.SetPortalCatchup(ctx, req); err != nil {
				t.Fatal(err)
			}
			s.Tick(context.Background())
			if len(queries) < 2 || queries[0][1] != chosen || !s.followPortal.catchUpTargetKnown || s.followPortal.catchUpTarget != chosen || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.CMSGAreatrigger) != 0 {
				t.Fatal("native ordering replaced Lua's first qualified candidate", directFirst, chosen, queries, s.followPortal, f.packets)
			}
			if !directFirst && (queries[1][0] != chosen || queries[1][1] != s.followPortal.ground) {
				t.Fatal("bend did not require its independent complete second leg", queries)
			}
		})
	}
}

func TestPortalCatchupRequiresCompleteBothRouteLegs(t *testing.T) {
	for _, name := range []string{"partial approach", "blocked approach", "partial continuation", "blocked continuation"} {
		t.Run(name, func(t *testing.T) {
			s, f, req, ctx, _ := portalGoalFixture(t)
			req.Candidates = req.Candidates[:1]
			calls := 0
			s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
				calls++
				bad := calls == 1 && strings.HasSuffix(name, "approach") || calls == 2 && strings.HasSuffix(name, "continuation")
				if bad {
					if strings.HasPrefix(name, "blocked") {
						return nil, errors.New("blocked ground corridor")
					}
					to.x -= 3 // Ends outside the maintained controller endpoint tolerance.
				}
				return []position{from, to}, nil
			}
			if _, err := s.SetPortalCatchup(ctx, req); err != nil {
				t.Fatal(err)
			}
			s.Tick(context.Background())
			if calls == 0 || s.followPortal.catchUpTargetKnown || s.state.Moving || len(f.packets) != 0 || !s.state.Following || s.state.Reason != "follow_instance_catch_up_route_unavailable" {
				t.Fatal("incomplete candidate caused movement or direct fallback", calls, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestPortalCatchupStaleGeometryCannotStartMovement(t *testing.T) {
	for _, name := range []string{"World visit", "Actions visit", "witness", "follow revision", "client revision", "client XYZ", "owner reappears", "combat", "goal expired", "host expired", "late executor witness", "late owner reappears"} {
		t.Run(name, func(t *testing.T) {
			s, f, req, ctx, _ := portalGoalFixture(t)
			req.Candidates = req.Candidates[:1]
			calls := 0
			s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
				calls++
				injectAt := 2
				if strings.HasPrefix(name, "late ") {
					injectAt = 3
				}
				if calls == injectAt {
					switch name {
					case "World visit":
						f.world.VisitRevision++
					case "Actions visit":
						s.encounterVisit.revision++
					case "witness", "late executor witness":
						s.followPortal.witness++
					case "follow revision":
						s.followRevision++
					case "client revision":
						s.posRevision++
					case "client XYZ":
						s.pos.x += .01
					case "owner reappears", "late owner reappears":
						f.world.Owner.Present = true
					case "combat":
						f.world.Self.UnitFlags |= 0x80000
					case "goal expired":
						s.portalGoal.until = s.now()
					case "host expired":
						s.hostEncounterUntil = s.now()
					}
				}
				return []position{from, to}, nil
			}
			if _, err := s.SetPortalCatchup(ctx, req); err != nil {
				t.Fatal(err)
			}
			s.Tick(context.Background())
			if calls < 2 || s.state.Moving || len(f.packets) != 0 {
				t.Fatal("changed custody or observations after geometry reached transport", name, calls, s.stateLocked(), f.packets)
			}
		})
	}
}

func TestPortalCatchupWitnessStaysStableThroughUnchangedTicks(t *testing.T) {
	s, f, _, ctx, advance := portalGoalFixture(t)
	f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = true, true, true
	s.followPortal.lostAt = time.Time{}
	witness := s.followPortal.witness
	for range 3 {
		advance(50 * time.Millisecond)
		s.Tick(context.Background())
		if s.followPortal.witness != witness || s.followPortal.sourceVisit != 1 || s.followPortal.followRevision != s.followRevision {
			t.Fatal("unchanged owner sighting continually invalidated the witness", witness, s.followPortal)
		}
	}
	// A genuinely different observed owner pose creates a new bounded witness.
	f.world.Owner.X += .1
	f.world.Owner.PositionRevision++
	advance(50 * time.Millisecond)
	s.Tick(context.Background())
	if s.followPortal.witness <= witness {
		t.Fatal("actual owner pose replacement kept the previous witness", s.followPortal)
	}
	witness = s.followPortal.witness
	f.world.Owner.Present, f.world.Owner.MapKnown, f.world.Owner.PositionKnown = false, false, false
	advance(50 * time.Millisecond)
	s.Tick(context.Background())
	view := s.portalObservationLocked(f.world)
	req := &actionsv1.PortalCatchupRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, OwnerGuid: ownerGUID,
		FollowRevision: s.followRevision, WitnessRevision: witness, Start: view.GetStart(), Candidates: []*actionsv1.PortalPoint{view.GetGround()}}
	if _, err := s.SetPortalCatchup(ctx, req); err != nil {
		t.Fatal("stable witness could not survive the host/native tick cadence", err, view)
	}
}

func TestPortalCatchupActiveHostWaitsForGoalBeforeBuiltinFallback(t *testing.T) {
	s, f, _, _, advance := portalGoalFixture(t)
	plans := 0
	s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
		plans++
		return []position{from, to}, nil
	}
	s.Tick(context.Background())
	if plans != 0 || len(f.packets) != 0 || s.state.Reason != "follow_instance_waiting_for_policy" || !s.state.Following {
		t.Fatal("builtin choice ran ahead of the active attached host", plans, s.stateLocked(), f.packets)
	}
	advance(encounterLeaseDuration)
	s.Tick(context.Background())
	if plans == 0 || countPackets(f, opcode.MSGMoveStartForward) != 1 || !s.state.Moving || !s.state.Following {
		t.Fatal("expired host did not permit bounded shared-source fallback", plans, s.stateLocked(), f.packets)
	}
}

func TestPortalCatchupUnknownTriggerCannotReplayAfterGoalExpiry(t *testing.T) {
	s, f, req, ctx, advance := portalGoalFixture(t)
	putAtPortal(f.world.Self, s.followPortal.portal.source, s.followPortal.portal.center)
	f.world.Self.PositionRevision++
	if !s.refreshPositionLocked(f.world) {
		t.Fatal("could not apply the actual source correction")
	}
	req.Start = s.portalStartLocked(f.world)
	req.Candidates = []*actionsv1.PortalPoint{{X: s.pos.x, Y: s.pos.y, Z: s.pos.z}}
	if _, err := s.SetPortalCatchup(ctx, req); err != nil {
		t.Fatal(err)
	}
	f.failSend = true
	s.Tick(context.Background())
	if countPackets(f, opcode.CMSGAreatrigger) != 1 || !s.followPortal.notified {
		t.Fatal("failed trigger was not recorded before transport", s.followPortal, f.packets)
	}
	advance(2 * encounterLeaseDuration)
	f.failSend = false
	toggle(t, s, true)
	for range 5 {
		s.Tick(context.Background())
		advance(250 * time.Millisecond)
	}
	if countPackets(f, opcode.CMSGAreatrigger) != 1 {
		t.Fatal("expired goal or restored following replayed the ambiguous crossing", f.packets)
	}
	old := proto.Clone(req).(*actionsv1.PortalCatchupRequest)
	if _, err := s.SetPortalCatchup(ctx, old); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("old crossing witness could rearm a goal", err)
	}
	if _, err := s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPortalCatchup(ctx, old); status.Code(err) != codes.FailedPrecondition || countPackets(f, opcode.CMSGAreatrigger) != 1 {
		t.Fatal("renewed host reused the obsolete crossing witness", err, f.packets)
	}
}
