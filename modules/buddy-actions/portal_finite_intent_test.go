package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type finitePortalIntentFixture struct {
	s       *Service
	f       *fakeSession
	ctx     context.Context
	advance func(time.Duration)
	portal  dungeonPortal
}

func newFinitePortalIntentFixture(t *testing.T, far bool) *finitePortalIntentFixture {
	t.Helper()
	s, f, advance := rig(t)
	s.sessionID, s.navDataDir = f.party.SessionId, "/client/finite-portal-fixture"
	f.world.Owner.Health = f.world.Owner.MaxHealth
	portal := dungeonEntrances[0]
	start := portal.center
	start.y -= 20
	if far {
		start.y = portal.center.y - 80
	}
	putAtPortal(f.world.Self, portal.source, start)
	putAtPortal(f.world.Owner, portal.source, portal.center)
	// Native custody assertions use the actual embedded Lua with a healthy
	// fixture budget. The production five-millisecond watchdog has its own test.
	runtime, err := behavior.NewWithOptions(behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Reload(behaviors.FollowFormationSource, ""); err != nil {
		t.Fatal(err)
	}
	s.formation.runtime = runtime
	activateEncounterWriter(t, s, f)
	x := &finitePortalIntentFixture{s: s, f: f, ctx: encounterHostContext(testEncounterKey, 1), advance: advance, portal: portal}
	x.admitFormation(t)
	s.Tick(context.Background())
	if s.followPortal.witness == 0 || s.followPortal.portal.trigger != portal.trigger || s.followPortal.catchingUp != far ||
		s.followPortal.sourceVisit != 1 || s.followPortal.followRevision != 0 || len(f.packets) != 0 ||
		s.state.Following || !s.followUntil.IsZero() {
		t.Fatal("finite formation goal did not capture the actual entrance without legacy Follow", far, s.followPortal, s.stateLocked(), f.packets)
	}
	x.disappear()
	return x
}

func (x *finitePortalIntentFixture) admitFormation(t *testing.T) {
	t.Helper()
	if _, err := x.s.RenewPolicyEncounterLease(x.ctx, wrapperspb.UInt64(1)); err != nil {
		t.Fatal(err)
	}
	owner := x.f.world.Owner
	_, err := x.s.SetFollowFormation(x.ctx, &actionsv1.FollowFormationRequest{
		SessionId: x.s.sessionID, SelfGuid: x.s.selfGUID, VisitId: 1, OwnerGuid: owner.Guid,
		OwnerPositionRevision: owner.PositionRevision, FollowRevision: x.s.followRevision,
		X: owner.X, Y: owner.Y, Z: owner.Z, Orientation: owner.Orientation,
		StartDistance: 1.9, StopDistance: .85, Ready: true, Hold: true, Phase: "within_personal_follow_space",
	})
	if err != nil {
		t.Fatal("current finite formation goal was refused", err)
	}
}

func (x *finitePortalIntentFixture) disappear() {
	x.f.world.Owner.Present, x.f.world.Owner.MapKnown, x.f.world.Owner.PositionKnown = false, false, false
	x.s.observeDungeonPortalLocked(x.f.world, x.s.now())
	x.advance(500 * time.Millisecond)
}

func (x *finitePortalIntentFixture) request(t *testing.T) *actionsv1.PortalCatchupRequest {
	t.Helper()
	view := x.s.portalObservationLocked(x.f.world)
	if view.GetWitnessRevision() == 0 || view.GetStart().GetRevision() == 0 || view.GetGround() == nil {
		t.Fatal("actual portal observation lost its witness or current controller start", view)
	}
	return &actionsv1.PortalCatchupRequest{SessionId: x.s.sessionID, SelfGuid: x.s.selfGUID, VisitId: 1,
		OwnerGuid: ownerGUID, FollowRevision: x.s.followRevision, WitnessRevision: view.WitnessRevision,
		Start: view.Start, Candidates: []*actionsv1.PortalPoint{view.Ground}}
}

func (x *finitePortalIntentFixture) renew(t *testing.T) *actionsv1.PortalCatchupRequest {
	t.Helper()
	if _, err := x.s.RenewPolicyEncounterLease(x.ctx, wrapperspb.UInt64(1)); err != nil {
		t.Fatal(err)
	}
	r := x.request(t)
	before, clock := len(x.f.packets), x.f.clock
	if _, err := x.s.SetPortalCatchup(x.ctx, r); err != nil {
		t.Fatal("current finite portal goal was refused", err, x.s.followPortal)
	}
	if len(x.f.packets) != before || x.f.clock != clock || x.s.state.Following || !x.s.followUntil.IsZero() {
		t.Fatal("portal goal admission prepared transport or enabled legacy intent", x.s.stateLocked(), x.f.packets)
	}
	return r
}

func (x *finitePortalIntentFixture) arrive(t *testing.T) {
	t.Helper()
	// World and Actions consume separate queues. A World-only destination
	// observation must retain the one-shot receipt until Actions sees the same
	// real transfer boundary, including its incremented visit and actual pose.
	x.f.world.Self.MapId = x.portal.destination
	x.f.world.Self.PositionRevision++
	x.f.world.VisitRevision++
	x.s.Tick(context.Background())
	if !x.s.followPortal.notified || countPackets(x.f, opcode.CMSGAreatrigger) != 1 {
		t.Fatal("World alone settled or replayed an unacknowledged Actions visit", x.s.followPortal, x.f.packets)
	}
	entry := binary.LittleEndian.AppendUint32(nil, x.portal.destination)
	for _, value := range []float32{x.f.world.Self.X, x.f.world.Self.Y, x.f.world.Self.Z, x.f.world.Self.Orientation} {
		entry = binary.LittleEndian.AppendUint32(entry, math.Float32bits(value))
	}
	for _, packet := range []*modv1.WorldPacket{
		{Opcode: opcode.SMSGTransferPending, Payload: binary.LittleEndian.AppendUint32(nil, x.portal.destination)},
		{Opcode: opcode.SMSGNewWorld, Payload: entry},
	} {
		if _, err := x.s.OnPacket(context.Background(), packet); err != nil {
			t.Fatal("actual Actions transfer packet failed", err)
		}
	}
	if !x.s.encounterVisit.bound || x.s.encounterVisit.mapID != x.portal.destination || x.s.encounterVisit.revision != x.f.world.VisitRevision {
		t.Fatal("Actions and World did not reach the same actual destination visit", x.s.encounterVisit, x.f.world.VisitRevision)
	}
	x.s.Tick(context.Background())
}

func TestFinitePortalIntentNearAndFarUseVerifiedRoutesAndOneActualTrigger(t *testing.T) {
	for _, far := range []bool{false, true} {
		t.Run(fmt.Sprintf("far=%t", far), func(t *testing.T) {
			x := newFinitePortalIntentFixture(t, far)
			plans := 0
			x.s.planMapPath = func(ctx context.Context, _ string, mapID uint32, from, to position) ([]position, error) {
				plans++
				if _, finite := ctx.Deadline(); !finite || mapID != x.portal.source {
					t.Fatal("portal travel did not use a bounded actual-map route", mapID)
				}
				return []position{from, to}, nil
			}
			for step := 0; step < 100 && countPackets(x.f, opcode.CMSGAreatrigger) == 0; step++ {
				x.renew(t)
				x.s.Tick(context.Background())
				x.advance(250 * time.Millisecond)
			}
			if plans == 0 || countPackets(x.f, opcode.MSGMoveStartForward) == 0 || countPackets(x.f, opcode.MSGMoveHeartbeat) == 0 ||
				countPackets(x.f, opcode.CMSGAreatrigger) != 1 || !x.s.followPortal.notified ||
				distance(x.s.pos, x.portal.center) >= x.portal.radius || x.s.state.Following || !x.s.followUntil.IsZero() || x.s.followRevision != 0 {
				t.Fatal("finite portal journey did not reach one observed trigger", plans, x.s.followPortal, x.s.stateLocked(), x.f.packets)
			}
			for _, packet := range x.f.packets {
				if packet.Opcode == opcode.CMSGAreatrigger && !bytes.Equal(packet.Payload, binary.LittleEndian.AppendUint32(nil, x.portal.trigger)) {
					t.Fatal("finite portal journey sent the wrong trigger", packet)
				}
			}
			x.arrive(t)
			if x.s.followPortal.portal.trigger != 0 || !x.s.portalGoal.until.IsZero() ||
				x.s.state.Reason != "follow_instance_transfer_observed" || countPackets(x.f, opcode.CMSGAreatrigger) != 1 || x.s.state.Following {
				t.Fatal("observed destination did not clean up the finite crossing", x.s.followPortal, x.s.stateLocked(), x.f.packets)
			}
		})
	}
}

func TestFinitePortalIntentRequiresCurrentGoalAndHostEvenWithWitness(t *testing.T) {
	for _, far := range []bool{false, true} {
		for _, change := range []string{"missing goal", "goal expiry", "host expiry", "host epoch", "follow revision"} {
			t.Run(fmt.Sprintf("far=%t/%s", far, change), func(t *testing.T) {
				x := newFinitePortalIntentFixture(t, far)
				if change != "missing goal" {
					x.renew(t)
				}
				switch change {
				case "goal expiry":
					x.s.portalGoal.until = x.s.now()
				case "host expiry":
					x.s.hostEncounterUntil = x.s.now()
				case "host epoch":
					x.s.hostEncounterEpoch++
				case "follow revision":
					x.s.followRevision++
				}
				if x.s.followPortal.witness == 0 {
					t.Fatal("fixture lost its retained entrance witness before revocation")
				}
				x.s.Tick(context.Background())
				if len(x.f.packets) != 0 || x.s.state.Moving || x.s.state.Following || !x.s.followUntil.IsZero() {
					t.Fatal("a witness alone replaced expired finite authority", x.s.followPortal, x.s.stateLocked(), x.f.packets)
				}
			})
		}
	}
}

func TestFinitePortalIntentWitnessNeedsCurrentFiniteFormationAuthority(t *testing.T) {
	for _, change := range []string{"missing formation", "expired formation", "expired host"} {
		t.Run(change, func(t *testing.T) {
			x := newFinitePortalIntentFixture(t, false)
			x.f.world.Owner.Present, x.f.world.Owner.MapKnown, x.f.world.Owner.PositionKnown = true, true, true
			x.s.followPortal, x.s.portalGoal = dungeonPortalFollow{}, portalCatchupGoal{}
			switch change {
			case "missing formation":
				x.s.followGoal = followFormationGoal{}
			case "expired formation":
				x.s.followGoal.until = x.s.now()
			case "expired host":
				x.s.hostEncounterUntil = x.s.now()
			}
			x.s.Tick(context.Background())
			if x.s.followPortal.witness != 0 || len(x.f.packets) != 0 || x.s.state.Moving || x.s.state.Following {
				t.Fatal("entrance proximity alone created a finite-follow witness", x.s.followPortal, x.s.stateLocked(), x.f.packets)
			}
		})
	}
}

func TestFinitePortalIntentZeroCounterDoesNotRelaxOtherAdmissionBindings(t *testing.T) {
	for _, name := range []string{"public", "self zero", "visit zero", "owner zero", "witness zero", "start zero", "follow changed", "witness changed", "independent"} {
		t.Run(name, func(t *testing.T) {
			x := newFinitePortalIntentFixture(t, false)
			r := x.request(t)
			ctx, want := x.ctx, codes.InvalidArgument
			switch name {
			case "public":
				ctx, want = context.Background(), codes.PermissionDenied
			case "self zero":
				r.SelfGuid = 0
			case "visit zero":
				r.VisitId = 0
			case "owner zero":
				r.OwnerGuid = 0
			case "witness zero":
				r.WitnessRevision = 0
			case "start zero":
				r.Start.Revision = 0
			case "follow changed":
				r.FollowRevision, want = 1, codes.FailedPrecondition
			case "witness changed":
				r.WitnessRevision, want = r.WitnessRevision+1, codes.FailedPrecondition
			case "independent":
				x.s.independent, want = true, codes.FailedPrecondition
			}
			before := x.s.portalGoal
			if _, err := x.s.SetPortalCatchup(ctx, r); status.Code(err) != want ||
				!reflect.DeepEqual(before, x.s.portalGoal) || len(x.f.packets) != 0 {
				t.Fatal("zero counter weakened an identity, witness or host fence", err, want, x.s.portalGoal, x.f.packets)
			}
		})
	}
}

type finitePortalIntentWorldHook struct {
	*fakeSession
	beforeWorld func()
}

func (s *finitePortalIntentWorldHook) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, options ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "buddy-world" && r.Method == "snapshot" && s.beforeWorld != nil {
		s.beforeWorld()
	}
	return s.fakeSession.InvokeModule(ctx, r, options...)
}

func TestFinitePortalIntentRechecksExactAuthorityAfterPlannerClockAndWorld(t *testing.T) {
	for _, far := range []bool{false, true} {
		for _, seam := range []string{"planner", "clock", "World"} {
			for _, change := range []string{"goal expiry", "host expiry", "host epoch", "session", "body", "controller body", "visit", "Actions visit", "follow", "witness", "goal replacement"} {
				t.Run(fmt.Sprintf("far=%t/%s/%s", far, seam, change), func(t *testing.T) {
					x := newFinitePortalIntentFixture(t, far)
					x.renew(t)
					injected, clockSeen := false, false
					var replacement portalCatchupGoal
					mutate := func() {
						if injected {
							return
						}
						injected = true
						switch change {
						case "goal expiry":
							x.s.portalGoal.until = x.s.now()
						case "host expiry":
							x.s.hostEncounterUntil = x.s.now()
						case "host epoch":
							x.s.hostEncounterEpoch++
						case "session":
							x.s.sessionID = "replacement-session"
						case "body":
							x.f.world.Self.Guid++
						case "controller body":
							x.s.selfGUID++
						case "visit":
							x.f.world.VisitRevision++
						case "Actions visit":
							x.s.encounterVisit.revision++
						case "follow":
							x.s.followRevision++
						case "witness":
							x.s.followPortal.witness++
						case "goal replacement":
							x.s.portalGoalRevision++
							x.s.portalGoal.revision = x.s.portalGoalRevision
							x.s.portalGoal.until = x.s.portalGoal.until.Add(time.Millisecond)
							replacement = x.s.portalGoal
						}
					}
					x.s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
						if seam == "planner" {
							mutate()
						}
						return []position{from, to}, nil
					}
					x.f.onClock = func() {
						clockSeen = true
						if seam == "clock" {
							mutate()
						}
					}
					x.s.session = &finitePortalIntentWorldHook{fakeSession: x.f, beforeWorld: func() {
						if seam == "World" && clockSeen {
							mutate()
						}
					}}
					x.s.Tick(context.Background())
					if !injected || len(x.f.packets) != 0 || x.s.state.Moving || x.s.state.Following || !x.s.followUntil.IsZero() {
						t.Fatal("late dependency facts permitted stale finite portal transport", injected, x.s.followPortal, x.s.stateLocked(), x.f.packets)
					}
					if change == "goal replacement" && !reflect.DeepEqual(replacement, x.s.portalGoal) {
						t.Fatal("old portal executor erased a replacement goal", replacement, x.s.portalGoal)
					}
				})
			}
		}
	}
}

func TestFinitePortalIntentFinalWorldReadCannotAuthorizeAStaleTrigger(t *testing.T) {
	for _, change := range []string{"goal expiry", "host expiry", "host epoch", "session", "body", "visit", "follow", "witness", "goal replacement"} {
		t.Run(change, func(t *testing.T) {
			x := newFinitePortalIntentFixture(t, false)
			putAtPortal(x.f.world.Self, x.portal.source, x.portal.center)
			x.f.world.Self.PositionRevision++
			if !x.s.refreshPositionLocked(x.f.world) {
				t.Fatal("could not observe the physical trigger pose")
			}
			x.renew(t)
			runtime := x.s.formation.runtime
			if runtime == nil {
				t.Fatal("fixture lost its actual embedded Lua evaluator")
			}
			reads, readyReads := 0, 0
			armed, injected := false, false
			var replacement portalCatchupGoal
			x.s.formation.remote = func(input map[string]any) (bool, string, map[string]any, error) {
				revision := runtime.Status().Revision
				output, err := runtime.Evaluate(input)
				if err != nil {
					t.Fatal("healthy embedded Lua evaluation failed before the final-read seam", err)
				}
				if input["mode"] == "portal_follow" && output["ready"] == true {
					readyReads++
					if readyReads == 2 {
						armed = true
					}
				}
				return true, revision, output, err
			}
			x.s.session = &finitePortalIntentWorldHook{fakeSession: x.f, beforeWorld: func() {
				reads++
				// The second ready portal decision follows STOP and the fresh
				// source observation. Its next World read is the final physical
				// entry check, independent of unrelated earlier maintenance reads.
				if !armed || injected {
					return
				}
				armed = false
				injected = true
				switch change {
				case "goal expiry":
					x.s.portalGoal.until = x.s.now()
				case "host expiry":
					x.s.hostEncounterUntil = x.s.now()
				case "host epoch":
					x.s.hostEncounterEpoch++
				case "session":
					x.s.sessionID = "replacement-session"
				case "body":
					x.f.world.Self.Guid++
				case "visit":
					x.f.world.VisitRevision++
				case "follow":
					x.s.followRevision++
				case "witness":
					x.s.followPortal.witness++
				case "goal replacement":
					x.s.portalGoalRevision++
					x.s.portalGoal.revision = x.s.portalGoalRevision
					replacement = x.s.portalGoal
				}
			}}
			x.s.Tick(context.Background())
			// A known safe STOP precedes this seam; the later revocation must
			// prevent the ID-only trigger without inventing unknown movement.
			if readyReads != 2 || !injected || armed || len(x.f.packets) != 1 || countPackets(x.f, opcode.MSGMoveStop) != 1 || x.s.followPortal.notified || x.s.state.Moving || x.s.state.Following {
				t.Fatal("late final-read authority change reached the trigger", injected, readyReads, reads, x.s.followPortal, x.s.stateLocked(), x.f.packets)
			}
			if change == "goal replacement" && !reflect.DeepEqual(replacement, x.s.portalGoal) {
				t.Fatal("old entry attempt rewrote a replacement portal goal", replacement, x.s.portalGoal)
			}
		})
	}
}

func TestFinitePortalIntentExplicitStopNeedsCurrentRevisionAndNewWitness(t *testing.T) {
	x := newFinitePortalIntentFixture(t, false)
	old := x.renew(t)
	oldWitness := old.WitnessRevision
	x.s.Tick(context.Background())
	if !x.s.state.Moving || countPackets(x.f, opcode.MSGMoveStartForward) != 1 {
		t.Fatal("fixture did not begin its finite portal approach", x.s.stateLocked(), x.f.packets)
	}
	toggle(t, x.s, false)
	before := len(x.f.packets)
	if _, err := x.s.SetPortalCatchup(x.ctx, old); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("stale revision zero portal goal survived explicit stop", err)
	}
	old.FollowRevision = x.s.followRevision
	if _, err := x.s.SetPortalCatchup(x.ctx, old); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("a new counter reused the stopped entrance witness", err)
	}
	if len(x.f.packets) != before || x.s.state.Moving || countPackets(x.f, opcode.MSGMoveStop) != 1 ||
		x.s.followRevision != 1 || x.s.state.Following || x.s.followPortal.witness != 0 {
		t.Fatal("rejected old goal changed stopped custody", x.s.followPortal, x.s.stateLocked(), x.f.packets)
	}
	x.f.world.Owner.Present, x.f.world.Owner.MapKnown, x.f.world.Owner.PositionKnown = true, true, true
	x.f.world.Owner.PositionRevision++
	x.admitFormation(t)
	x.s.Tick(context.Background())
	if x.s.followPortal.witness <= oldWitness || x.s.followPortal.followRevision != 1 {
		t.Fatal("fresh current formation did not establish a new entrance witness", x.s.followPortal)
	}
	x.disappear()
	x.renew(t)
	x.s.Tick(context.Background())
	if countPackets(x.f, opcode.MSGMoveStartForward) != 2 || countPackets(x.f, opcode.MSGMoveStop) != 1 ||
		!x.s.state.Moving || x.s.state.Following || x.s.followRevision != 1 {
		t.Fatal("fresh current counter and new witness could not resume finite travel", x.s.followPortal, x.s.stateLocked(), x.f.packets)
	}
}

func TestFinitePortalIntentNotifiedTriggerNeverReplaysAfterExpiryAndCleansUpAtDestination(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("uncertain=%t", uncertain), func(t *testing.T) {
			x := newFinitePortalIntentFixture(t, false)
			putAtPortal(x.f.world.Self, x.portal.source, x.portal.center)
			x.f.world.Self.PositionRevision++
			if !x.s.refreshPositionLocked(x.f.world) {
				t.Fatal("could not apply the observed entrance pose")
			}
			old := x.renew(t)
			x.f.failSendOpcode = opcode.CMSGAreatrigger
			x.f.failSend = uncertain
			x.s.Tick(context.Background())
			if countPackets(x.f, opcode.CMSGAreatrigger) != 1 || !x.s.followPortal.notified {
				t.Fatal("trigger attempt was not retained before transport", x.s.followPortal, x.f.packets)
			}
			if uncertain && (x.s.state.LastTransportFailure == nil || x.s.state.LastTransportFailure.Opcode != opcode.CMSGAreatrigger ||
				!x.s.state.LastTransportFailure.OutcomeUnknown) {
				t.Fatal("ambiguous trigger transport lost its unknown receipt", x.s.state.LastTransportFailure)
			}
			x.f.failSend = false
			x.advance(2 * encounterLeaseDuration)
			for range 3 {
				x.s.Tick(context.Background())
				x.advance(250 * time.Millisecond)
			}
			if _, err := x.s.RenewPolicyEncounterLease(x.ctx, wrapperspb.UInt64(1)); err != nil {
				t.Fatal(err)
			}
			if _, err := x.s.SetPortalCatchup(x.ctx, old); status.Code(err) != codes.FailedPrecondition ||
				countPackets(x.f, opcode.CMSGAreatrigger) != 1 || !x.s.followPortal.notified {
				t.Fatal("goal or host renewal replayed a notified crossing", err, x.s.followPortal, x.f.packets)
			}
			x.arrive(t)
			if x.s.followPortal.portal.trigger != 0 || !x.s.portalGoal.until.IsZero() ||
				x.s.state.Reason != "follow_instance_transfer_observed" || countPackets(x.f, opcode.CMSGAreatrigger) != 1 || x.s.state.Following {
				t.Fatal("terminal crossing lost its destination cleanup after expiry", x.s.followPortal, x.s.stateLocked(), x.f.packets)
			}
		})
	}
}
