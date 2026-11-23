package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	buddyworld "github.com/agent-wow/agent-wow/modules/buddy-world"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Native-unit tests use explicit World observations below. Setting world routes
// the same adapter to the real World parser/adoption service for composed tests.
type flightpathTransferSession struct {
	*flightpathSession
	world        *buddyworld.Service
	adoptCalls   int
	adoptRequest *worldv1.AdoptSplineTransferRequest
	afterAdopt   func()
	adoptError   error
	refuseAdopt  bool
}

func (f *flightpathTransferSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, options ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module != "buddy-world" {
		return f.flightpathSession.InvokeModule(ctx, r, options...)
	}
	if r.Method == "snapshot" && f.world != nil {
		view, err := f.world.Snapshot(ctx, &worldv1.SnapshotRequest{})
		if err != nil {
			return nil, err
		}
		f.fakeSession.world = view
	}
	if r.Method != "adopt_spline_transfer" {
		return f.flightpathSession.InvokeModule(ctx, r, options...)
	}
	f.adoptCalls++
	f.order = append(f.order, "adopt")
	req := &worldv1.AdoptSplineTransferRequest{}
	if r.Request == nil || r.Request.UnmarshalTo(req) != nil {
		return nil, errors.New("fixture malformed World adoption request")
	}
	f.adoptRequest = proto.Clone(req).(*worldv1.AdoptSplineTransferRequest)
	if f.refuseAdopt {
		return nil, f.adoptError
	}
	var view *worldv1.SplineCompletionObservation
	if f.world != nil {
		var err error
		view, err = f.world.AdoptSplineTransfer(ctx, req)
		if err != nil {
			return nil, err
		}
	} else {
		w := f.fakeSession.world
		if w.SplineCompletion == nil || req.ExpectedRevision != w.SplineCompletion.Revision ||
			req.VisitRevision != w.VisitRevision || req.TransferRevision != w.WorldTransfer.Revision ||
			!proto.Equal(req.TransportScope, w.SelfMotionAuthority.TransportScope) {
			return nil, status.Error(codes.FailedPrecondition, "fixture World CAS mismatch")
		}
		view = w.SplineCompletion
		view.Revision++
		view.Operation = nil
		view.TransferAdoption = &worldv1.SplineTransferAdoption{Revision: view.Revision, SelfGuid: req.SelfGuid,
			FromVisitRevision: w.WorldTransfer.FromVisitRevision, ToVisitRevision: req.VisitRevision,
			Transfer: proto.Clone(w.WorldTransfer).(*worldv1.WorldTransferObservation)}
		view.TransferAdoptionCurrent = true
	}
	if f.afterAdopt != nil {
		f.afterAdopt()
	}
	if f.adoptError != nil {
		return nil, f.adoptError
	}
	packed, err := anypb.New(view)
	return &modv1.InvokeModuleResponse{Result: packed}, err
}

func newFlightpathTransferRig(t *testing.T) (*flightpathRig, *flightpathTransferSession) {
	t.Helper()
	x := newFlightpathActivationRig(t)
	flightpathActivate(t, x, flightpathActivationRequest(t, x))
	acceptance := &modv1.TransportScope{ConnectionId: "adoption-test-stream", SelfGuid: 1, VisitBound: true,
		VisitRevision: 9, MapId: 0, MotionRevision: 10}
	flightpathSettlementPacket(t, x, opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0}, acceptance)
	old := cloneFlightpathScope(acceptance)
	old.MotionRevision, old.SplineSourceRevision, old.SplineRevision = 13, 11, 41
	old.SplineIdKnown, old.SplineActive, old.SplineSourceOpcode = true, true, opcode.SMSGMonsterMove
	predecessor := &worldv1.SplineCompletionOperation{Revision: 1, SelfGuid: 1, VisitRevision: 1, AuthorityRevision: 4,
		MovementRevision: 1, SplineRevision: 3, TransportScope: old, SplineIdKnown: true, Pending: true,
		TransportUnknown: true, Phase: "transport_unknown", Error: "original completion reply lost"}
	edge := &modv1.ObservedWorldTransfer{Revision: 3, Known: true, Phase: "ack_written", ConnectionId: old.ConnectionId, SelfGuid: 1,
		FromVisitRevision: 9, FromMapId: 0, FromMotionRevision: 13, FromSplineIdKnown: true, FromSplineRevision: 41,
		FromSplineSourceRevision: 11, FromSplineSourceOpcode: opcode.SMSGMonsterMove, FromSplineActive: true,
		PendingMotionRevision: 14, TargetMapKnown: true, TargetMapId: 1, ToVisitRevision: 10, ToMapId: 1,
		EntryMotionRevision: 15, WorldportAckMotionRevision: 16, WorldportAckWritten: true,
		CompletionReportWritten: true, CompletionReportMotionRevision: 13}
	current := &modv1.TransportScope{ConnectionId: old.ConnectionId, SelfGuid: 1, VisitBound: true,
		VisitRevision: 10, MapId: 1, MotionRevision: 19, SplineIdKnown: true, SplineActive: true,
		SplineRevision: 45, SplineSourceRevision: 18, SplineSourceOpcode: opcode.SMSGMonsterMove, WorldTransfer: edge}
	flightpathSettlementPacket(t, x, opcode.SMSGTransferPending, binary.LittleEndian.AppendUint32(nil, 1), nil)
	flightpathSettlementPacket(t, x, opcode.SMSGNewWorld, settlementPolicyFloats(binary.LittleEndian.AppendUint32(nil, 1), 1, 2, 3, .5), current)
	w := x.f.world
	w.VisitRevision, w.Self.MapId, w.Self.PositionRevision, w.Self.UnitFlags = 2, 1, 2, 0x100000
	w.Self.X, w.Self.Y, w.Self.Z, w.Self.Orientation = 1, 2, 3, .5
	info := movementinfo.Info{Time: 500, X: 1, Y: 2, Z: 3, Orientation: .5}
	wire, err := movementinfo.Encode(info)
	if err != nil {
		t.Fatal(err)
	}
	rawScope := cloneFlightpathScope(current)
	// Actual own CREATE precedes the new standalone header. Its original
	// scope is bound to this visit/edge but has no source identity yet.
	rawScope.MotionRevision, rawScope.SplineRevision = 17, 44
	rawScope.SplineIdKnown, rawScope.SplineActive = false, false
	rawScope.SplineSourceRevision, rawScope.SplineSourceOpcode = 0, 0
	w.SelfMovementFacts = &worldv1.OwnMovementFacts{Known: true, SelfGuid: 1, VisitRevision: 2, Revision: 2, SourceOpcode: opcode.MSGMoveHeartbeat,
		MapKnown: true, MapId: 1, WireImage: wire, TransportScope: rawScope,
		Info: &worldv1.MovementInfoFacts{Time: 500, X: 1, Y: 2, Z: 3, Orientation: .5}}
	w.SelfMotionAuthority = &worldv1.OwnMotionAuthority{Known: true, SelfGuid: 1, VisitRevision: 2, Revision: 8,
		MovementRevision: 2, SplineRevision: 4, TransportScope: current, UnitFlagsKnown: true, UnitFlags: 0x100000,
		UnitFlagsRevision: 3, UnitFlagsMotionRevision: 17}
	w.WorldTransfer = &worldv1.WorldTransferObservation{Revision: 5, Known: true, SelfGuid: 1, FromVisitRevision: 1, ToVisitRevision: 2,
		Core: proto.Clone(edge).(*modv1.ObservedWorldTransfer), PredecessorCompletion: proto.Clone(predecessor).(*worldv1.SplineCompletionOperation),
		PredecessorAuthority: &worldv1.OwnMotionAuthority{Known: true, SelfGuid: 1, VisitRevision: 1, Revision: 4, TransportScope: old},
		PredecessorQualified: true, ContinuationKnown: true, CurrentScope: cloneFlightpathScope(current),
		CurrentAuthorityRevision: 8, CurrentMovementRevision: 2, CurrentSplineRevision: 4, CurrentUnitFlagsRevision: 3,
		CurrentUnitFlagsKnown: true, CurrentUnitFlags: 0x100000, CurrentUnitFlagsMotionRevision: 17}
	w.SplineCompletion = &worldv1.SplineCompletionObservation{SelfGuid: 1, VisitRevision: 2, Revision: 1, Operation: predecessor}
	client := &flightpathTransferSession{flightpathSession: x.client}
	x.s.session = client
	x.f.packets = nil
	x.client.worldReads, x.client.sendCalls, x.client.order = 0, 0, nil
	return x, client
}

func flightpathTransferRequest(t *testing.T, x *flightpathRig) *actionsv1.AdoptFlightpathTransferRequest {
	t.Helper()
	w := x.f.world
	return &actionsv1.AdoptFlightpathTransferRequest{SessionId: x.s.sessionID, SelfGuid: 1, VisitId: 2,
		ActivationRevision: x.s.flightpathActivation.revision, WorldTransferRevision: w.WorldTransfer.Revision,
		ExpectedCompletionRevision: w.SplineCompletion.Revision, CoreTransferRevision: w.WorldTransfer.Core.Revision,
		AuthorityRevision: w.SelfMotionAuthority.Revision, MovementRevision: w.SelfMovementFacts.Revision,
		SplineRevision: w.SelfMotionAuthority.SplineRevision, UnitFlagsRevision: w.SelfMotionAuthority.UnitFlagsRevision,
		TransportScope: cloneFlightpathScope(w.SelfMotionAuthority.TransportScope)}
}

func assertFlightpathTransferOriginal(t *testing.T, x *flightpathRig, original *actionsv1.FlightpathActivationOperation, request *actionsv1.FlightpathActivationRequest, menu *actionsv1.FlightpathOperation) {
	t.Helper()
	got := proto.Clone(x.s.flightpathActivation.operation).(*actionsv1.FlightpathActivationOperation)
	got.CurrentSegment = nil
	if !proto.Equal(got, original) || !proto.Equal(x.s.flightpathActivation.request, request) || !proto.Equal(x.s.flightpathActivation.menu, menu) ||
		len(x.f.packets) != 0 || x.client.sendCalls != 0 || x.f.clock != 0 || len(x.f.movementObservations) != 0 {
		t.Fatal("transfer changed original booking/history or produced a game effect", got, original, x.f.packets)
	}
}

func TestFlightpathTransferAdoptsKnownAcceptanceWithoutRewritingOriginalOrCustody(t *testing.T) {
	x, client := newFlightpathTransferRig(t)
	r := flightpathTransferRequest(t, x)
	original := proto.Clone(x.s.flightpathActivation.operation).(*actionsv1.FlightpathActivationOperation)
	request := proto.Clone(x.s.flightpathActivation.request).(*actionsv1.FlightpathActivationRequest)
	menu := proto.Clone(x.s.flightpathActivation.menu).(*actionsv1.FlightpathOperation)
	token, until, binding := x.s.interactionToken, x.s.interactionUntil, x.s.idleInteraction
	view, err := x.s.AdoptFlightpathTransfer(context.Background(), r)
	segment := view.GetActivation().GetCurrentSegment()
	if err != nil || segment == nil || segment.VisitId != 2 || !segment.ParentLeaseRequired || segment.WorldAdoptionRevision != 2 ||
		segment.WorldTransferRevision != r.WorldTransferRevision || segment.CoreTransferRevision != r.CoreTransferRevision ||
		!proto.Equal(segment.SourceScope, r.TransportScope) || view.Activation.VisitId != 1 || view.VisitId != 2 ||
		client.adoptCalls != 1 || !reflect.DeepEqual(x.client.order, []string{"world", "adopt", "world"}) {
		t.Fatal("known accepted booking did not adopt one exact adjacent segment", view, err, x.client.order)
	}
	assertFlightpathTransferOriginal(t, x, original, request, menu)
	if x.s.interactionToken != token || x.s.interactionUntil != until || x.s.idleInteraction != binding {
		t.Fatal("state-only adoption changed parent custody")
	}
	reads := x.client.worldReads
	replay, err := x.s.AdoptFlightpathTransfer(context.Background(), proto.Clone(r).(*actionsv1.AdoptFlightpathTransferRequest))
	if err != nil || !proto.Equal(view, replay) || client.adoptCalls != 1 || x.client.worldReads != reads {
		t.Fatal("exact handoff replay performed a dependency/mutation", replay, err)
	}
	view.Activation.CurrentSegment.SourceScope.ConnectionId = "caller mutation"
	if x.s.flightpathActivation.operation.CurrentSegment.SourceScope.ConnectionId == "caller mutation" ||
		x.s.flightpathActivation.transferAdoption.Transfer.CurrentScope.ConnectionId == "caller mutation" {
		t.Fatal("adoption response aliases private history")
	}
	bad := proto.Clone(r).(*actionsv1.AdoptFlightpathTransferRequest)
	bad.CoreTransferRevision++
	if _, err := x.s.AdoptFlightpathTransfer(context.Background(), bad); status.Code(err) != codes.Aborted || client.adoptCalls != 1 || x.client.worldReads != reads {
		t.Fatal("conflicting second handoff changed retained receipt", err)
	}
}

func TestFlightpathTransferRejectsUnknownAcceptanceStaleWorldAndLateBookingChanges(t *testing.T) {
	for _, change := range []string{"unknown acceptance", "nonzero reply", "session", "World visit", "Core connection", "old source before acceptance", "missing report", "unsupported continuation", "raw before entry", "raw edge", "authority", "late booking", "late Actions visit"} {
		t.Run(change, func(t *testing.T) {
			x, client := newFlightpathTransferRig(t)
			r := flightpathTransferRequest(t, x)
			switch change {
			case "unknown acceptance":
				x.s.flightpathActivation.operation.ReplyKnown = false
			case "nonzero reply":
				x.s.flightpathActivation.operation.ReplyCode = 13
			case "session":
				r.SessionId = "replacement"
			case "World visit":
				x.f.world.VisitRevision++
			case "Core connection":
				x.f.world.SelfMotionAuthority.TransportScope.ConnectionId = "replacement"
			case "old source before acceptance":
				x.s.flightpathActivation.operation.AcceptanceScope.MotionRevision = 12
			case "missing report":
				x.f.world.WorldTransfer.Core.CompletionReportWritten = false
			case "unsupported continuation":
				x.f.world.WorldTransfer.ContinuationKnown = false
			case "raw before entry":
				x.f.world.SelfMovementFacts.TransportScope.MotionRevision = x.f.world.WorldTransfer.Core.WorldportAckMotionRevision
			case "raw edge":
				x.f.world.SelfMovementFacts.TransportScope.WorldTransfer.Revision++
			case "authority":
				r.AuthorityRevision++
			case "late booking":
				x.client.beforeWorld = func() { x.s.flightpathActivation.operation.DestinationNode++ }
			case "late Actions visit":
				x.client.beforeWorld = func() { x.s.encounterVisit.revision++ }
			}
			view, err := x.s.AdoptFlightpathTransfer(context.Background(), r)
			knownRefusal := view.GetTransferAttempt().GetPhase() == "not_invoked" && !view.GetTransferAttempt().GetWorldInvoked()
			if err == nil && !knownRefusal || client.adoptCalls != 0 ||
				x.s.flightpathActivation.operation.CurrentSegment != nil || len(x.f.packets) != 0 {
				t.Fatal("stale/mismatched acceptance or World facts reached adoption", change, err, client.adoptCalls)
			}
		})
	}
}

func TestFlightpathTransferInitialWorldFailureRetainsExactNoCallReceipt(t *testing.T) {
	x, client := newFlightpathTransferRig(t)
	r := flightpathTransferRequest(t, x)
	original := proto.Clone(x.s.flightpathActivation.operation).(*actionsv1.FlightpathActivationOperation)
	request := proto.Clone(x.s.flightpathActivation.request).(*actionsv1.FlightpathActivationRequest)
	menu := proto.Clone(x.s.flightpathActivation.menu).(*actionsv1.FlightpathOperation)
	x.f.failSnapshot = true
	view, err := x.s.AdoptFlightpathTransfer(context.Background(), r)
	attempt := view.GetTransferAttempt()
	if err != nil || attempt == nil || attempt.Phase != "not_invoked" || attempt.WorldInvoked ||
		!proto.Equal(attempt.Request, r) || attempt.Error == "" || len(attempt.Error) > 240 || client.adoptCalls != 0 ||
		x.s.flightpathActivation.transferInvoked || view.Activation.CurrentSegment != nil {
		t.Fatal("initial failed dependency lost the exact known-zero-call receipt", view, err)
	}
	assertFlightpathTransferOriginal(t, x, original, request, menu)
	// Treat the returned host reply as lost: a getter must still expose the
	// native no-call fact, without another dependency read or World mutation.
	reads := x.client.worldReads
	x.f.failSnapshot = false
	observed, err := x.s.FlightpathObservation(context.Background(), &emptypb.Empty{})
	if err != nil || !proto.Equal(observed.TransferAttempt, attempt) || x.client.worldReads != reads || client.adoptCalls != 0 {
		t.Fatal("no-call receipt could not reconcile a lost host reply", observed, err)
	}
	replay, err := x.s.AdoptFlightpathTransfer(context.Background(), proto.Clone(r).(*actionsv1.AdoptFlightpathTransferRequest))
	if err != nil || !proto.Equal(view, replay) || x.client.worldReads != reads || client.adoptCalls != 0 {
		t.Fatal("same no-call request was automatically retried", replay, err)
	}
	view.TransferAttempt.Request.SessionId = "caller mutation"
	if x.s.flightpathActivation.transferAttempt.Request.SessionId != r.SessionId {
		t.Fatal("no-call response aliases retained request")
	}
	bad := proto.Clone(r).(*actionsv1.AdoptFlightpathTransferRequest)
	bad.SessionId = "wrong session"
	if _, err := x.s.AdoptFlightpathTransfer(context.Background(), bad); err == nil || x.client.worldReads != reads || client.adoptCalls != 0 {
		t.Fatal("new invalid body request bypassed the original admission guard", err)
	}
	// A new explicit request may use coherently advanced actual facts. It does
	// not replay the refused request or change any accepted booking fields.
	w := x.f.world
	w.SelfMotionAuthority.Revision++
	w.SelfMotionAuthority.TransportScope.MotionRevision++
	w.WorldTransfer.CurrentAuthorityRevision = w.SelfMotionAuthority.Revision
	w.WorldTransfer.CurrentScope = cloneFlightpathScope(w.SelfMotionAuthority.TransportScope)
	fresh := flightpathTransferRequest(t, x)
	adopted, err := x.s.AdoptFlightpathTransfer(context.Background(), fresh)
	if err != nil || adopted.GetActivation().GetCurrentSegment() == nil || adopted.GetTransferAttempt().GetPhase() != "adopted" ||
		!adopted.TransferAttempt.WorldInvoked || client.adoptCalls != 1 {
		t.Fatal("new coherent explicit request could not replace known-no-call metadata", adopted, err)
	}
	assertFlightpathTransferOriginal(t, x, original, request, menu)
}

func TestFlightpathTransferObservationReconcilesLostWorldReplyAndContextOnlyByReading(t *testing.T) {
	for _, failure := range []string{"lost reply and snapshot", "context canceled after commit"} {
		t.Run(failure, func(t *testing.T) {
			x, client := newFlightpathTransferRig(t)
			r := flightpathTransferRequest(t, x)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client.adoptError = errors.New("World unary reply lost after state commit")
			client.afterAdopt = func() {
				if failure == "lost reply and snapshot" {
					x.f.failSnapshot = true
				} else {
					cancel()
				}
			}
			if _, err := x.s.AdoptFlightpathTransfer(ctx, r); err == nil || client.adoptCalls != 1 ||
				x.s.flightpathActivation.operation.CurrentSegment != nil || x.f.world.SplineCompletion.TransferAdoption == nil {
				t.Fatal("fixture did not retain partial World success", err)
			}
			x.f.failSnapshot = false
			// A harmless current counter advance must not invalidate the exact
			// immutable grant witness recorded by the original request.
			x.f.world.SelfMotionAuthority.Revision++
			x.f.world.SelfMotionAuthority.TransportScope.MotionRevision++
			x.f.world.WorldTransfer.CurrentAuthorityRevision = x.f.world.SelfMotionAuthority.Revision
			x.f.world.WorldTransfer.CurrentScope = cloneFlightpathScope(x.f.world.SelfMotionAuthority.TransportScope)
			view, err := x.s.FlightpathObservation(context.Background(), &emptypb.Empty{})
			if err != nil || view.GetActivation().GetCurrentSegment() == nil || client.adoptCalls != 1 || len(x.f.packets) != 0 {
				t.Fatal("read-only observation failed to reconcile known native receipt", view, err, client.adoptCalls)
			}
			reads := x.client.worldReads
			x.s.FlightpathObservation(context.Background(), &emptypb.Empty{})
			if x.client.worldReads != reads || client.adoptCalls != 1 {
				t.Fatal("settled read continued handoff callbacks")
			}
		})
	}
	x, client := newFlightpathTransferRig(t)
	if view, err := x.s.FlightpathObservation(context.Background(), &emptypb.Empty{}); err != nil || view.Activation.CurrentSegment != nil ||
		x.client.worldReads != 0 || client.adoptCalls != 0 {
		t.Fatal("an observation created unrequested transfer intent", view, err)
	}
}

func TestFlightpathTransferUnconfirmedOrSupersededGrantNeverMutatesAgain(t *testing.T) {
	for _, mode := range []string{"no receipt", "source changed", "body changed", "World grant no longer current"} {
		t.Run(mode, func(t *testing.T) {
			x, client := newFlightpathTransferRig(t)
			r := flightpathTransferRequest(t, x)
			client.adoptError = errors.New("unknown World callback result")
			client.refuseAdopt = mode == "no receipt"
			client.afterAdopt = func() {
				switch mode {
				case "source changed":
					x.f.world.SelfMotionAuthority.TransportScope.SplineRevision++
				case "body changed":
					x.f.world.Self.Guid++
				case "World grant no longer current":
					x.f.world.SplineCompletion.TransferAdoptionCurrent = false
				}
			}
			if _, err := x.s.AdoptFlightpathTransfer(context.Background(), r); err == nil {
				t.Fatal("unsupported partial handoff committed")
			}
			for range 2 {
				x.s.FlightpathObservation(context.Background(), &emptypb.Empty{})
				x.s.AdoptFlightpathTransfer(context.Background(), r)
			}
			if client.adoptCalls != 1 || x.s.flightpathActivation.operation.CurrentSegment != nil ||
				!x.s.flightpathActivation.operation.Pending || len(x.f.packets) != 0 {
				t.Fatal("uncertain/stale grant retried or cleared booking", mode, client.adoptCalls)
			}
		})
	}
}

// These are exact synthetic retired observations for the adopted source; World
// parser/retirement behavior is qualified separately by its own native tests.
func flightpathTransferGround(t *testing.T, x *flightpathRig) {
	t.Helper()
	w, source := x.f.world, x.s.flightpathActivation.operation.CurrentSegment.SourceScope
	w.Self.UnitFlags, w.Self.MovementFlags, w.Self.ExtraMovementFlags, w.Self.MovementTime = 0, 0, 0, 600
	w.SelfMotionAuthority.Known, w.SelfMotionAuthority.Revision, w.SelfMotionAuthority.MovementRevision = false, 10, 3
	w.SelfMotionAuthority.UnitFlags, w.SelfMotionAuthority.UnitFlagsRevision, w.SelfMotionAuthority.UnitFlagsMotionRevision = 0, 4, 22
	w.SelfMotionAuthority.TransportScope = cloneFlightpathScope(source)
	w.SelfMotionAuthority.TransportScope.MotionRevision = 22
	w.SelfMovementFacts.Revision, w.SelfMovementFacts.Info.Time = 3, 600
	wire, err := movementinfo.Encode(movementinfo.Info{Time: 600, X: w.Self.X, Y: w.Self.Y, Z: w.Self.Z, Orientation: w.Self.Orientation})
	if err != nil {
		t.Fatal(err)
	}
	w.SelfMovementFacts.WireImage = wire
	w.SelfMovementFacts.TransportScope = cloneFlightpathScope(source)
	w.SelfMovementFacts.TransportScope.MotionRevision = 21
	w.SplineCompletion.Revision, w.SplineCompletion.TransferAdoptionCurrent, w.SplineCompletion.RetirementCurrent = 4, false, true
	completionScope := cloneFlightpathScope(source)
	completionScope.MotionRevision = 20
	w.SplineCompletion.Operation = &worldv1.SplineCompletionOperation{Revision: 3, SelfGuid: 1, VisitRevision: 2,
		AuthorityRevision: 8, MovementRevision: 2, SplineRevision: 4, TransportScope: completionScope,
		SplineIdKnown: true, TransportWritten: true, Phase: "transport_written", Retired: true, RetirementRevision: 4,
		RetiredMovementRevision: 3, RetiredUnitFlagsRevision: 4, RetirementScope: cloneFlightpathScope(w.SelfMotionAuthority.TransportScope)}
	for _, npc := range w.NearbyCreatures {
		npc.MapId, npc.X, npc.Y, npc.Z, npc.PositionRevision = 1, w.Self.X+1, w.Self.Y, w.Self.Z, 2
	}
}

func TestFlightpathTransferRequiresFreshIdleParentCustodyBeforeArrivalAndCanSettle(t *testing.T) {
	x, client := newFlightpathTransferRig(t)
	if _, err := x.s.AdoptFlightpathTransfer(context.Background(), flightpathTransferRequest(t, x)); err != nil {
		t.Fatal(err)
	}
	flightpathTransferGround(t, x)
	originalToken := x.token
	query := &actionsv1.FlightpathQueryRequest{SessionId: x.s.sessionID, SelfGuid: 1, VisitId: 2, Token: originalToken,
		NpcGuid: x.npc, Mode: "arrival", ExpectedRevision: x.s.flightpath.revision,
		ActivationRevision: 1, CompletionRevision: 4}
	view, err := x.s.QueryFlightpath(context.Background(), query)
	if err != nil || view.Operation.Phase != "not_dispatched" || len(x.f.packets) != 0 {
		t.Fatal("legacy old-visit parent token silently carried across transfer", view, err)
	}
	// The test models the parent explicitly releasing its unchanged old token.
	// Adoption itself must not perform this cleanup or acquire replacement custody.
	if _, err := x.s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "exploration", Token: originalToken,
		SessionId: x.s.sessionID, SelfGuid: 1, VisitId: 2}); err != nil {
		t.Fatal(err)
	}
	lease, err := x.s.AcquireIdleInteraction(context.Background(), &actionsv1.IdleInteractionRequest{SessionId: x.s.sessionID,
		SelfGuid: 1, VisitId: 2, Owner: "exploration", LeaseMs: 15000})
	if err != nil || lease.Token == "" || lease.Token == originalToken || len(x.f.packets) != 0 {
		t.Fatal("fresh parent idle custody could not bind actual retired ground proof", lease, err)
	}
	query.Token, query.ExpectedRevision = lease.Token, x.s.flightpath.revision
	view, err = x.s.QueryFlightpath(context.Background(), query)
	if err != nil || view.Operation.Phase != "requested" || len(x.f.packets) != 1 || x.f.packets[0].Opcode != opcode.CMSGTaxiqueryavailablenodes {
		t.Fatal("new current-visit idle parent lease did not authorize exact arrival query", view, err)
	}
	flightpathSettlementPacket(t, x, opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, 2, 2, 12), x.f.world.SelfMotionAuthority.TransportScope)
	w := x.f.world
	settle := &actionsv1.SettleFlightpathRequest{SessionId: x.s.sessionID, SelfGuid: 1, VisitId: 2, ActivationRevision: 1,
		MenuRevision: x.s.flightpath.revision, CompletionRevision: 4, AuthorityRevision: w.SelfMotionAuthority.Revision,
		MovementRevision: w.SelfMovementFacts.Revision, UnitFlagsRevision: w.SelfMotionAuthority.UnitFlagsRevision,
		TransportScope: cloneFlightpathScope(w.SelfMotionAuthority.TransportScope)}
	x.advance(20 * time.Second)
	view, err = x.s.SettleFlightpath(context.Background(), settle)
	if err != nil || view.Activation.Pending || !view.Activation.ArrivalKnown || view.Activation.VisitId != 1 || view.Activation.CurrentSegment.VisitId != 2 ||
		client.adoptCalls != 1 || len(x.f.packets) != 1 || !view.Activation.CurrentSegment.ParentLeaseRequired {
		t.Fatal("adopted-source settlement compared old visit counters or required stale lease", view, err)
	}
}

func TestFlightpathTransferIdleExceptionRequiresAdoptedRetiredGroundAndCurrentBinding(t *testing.T) {
	for _, change := range []string{"not adopted", "still flying", "not retired", "wrong source", "wrong owner", "old idle token"} {
		t.Run(change, func(t *testing.T) {
			x, _ := newFlightpathTransferRig(t)
			if change != "not adopted" {
				if _, err := x.s.AdoptFlightpathTransfer(context.Background(), flightpathTransferRequest(t, x)); err != nil {
					t.Fatal(err)
				}
				flightpathTransferGround(t, x)
			}
			x.s.state.InteractionOwner, x.s.interactionToken = "", ""
			r := &actionsv1.IdleInteractionRequest{SessionId: x.s.sessionID, SelfGuid: 1, VisitId: 2, Owner: "exploration", LeaseMs: 5000}
			switch change {
			case "still flying":
				x.f.world.Self.UnitFlags = 0x100000
			case "not retired":
				x.f.world.SplineCompletion.RetirementCurrent = false
			case "wrong source":
				x.f.world.SelfMotionAuthority.TransportScope.SplineRevision++
			case "wrong owner":
				r.Owner = "trade"
			case "old idle token":
				x.s.state.InteractionOwner, x.s.interactionToken = "exploration", "old-token"
				x.s.idleInteraction = idleInteractionBinding{sessionID: x.s.sessionID, selfGUID: 1, visit: 1, token: "old-token"}
				r.Token = "old-token"
			}
			before := idleRawSnapshot(t, x.s)
			if _, err := x.s.AcquireIdleInteraction(context.Background(), r); err == nil {
				t.Fatal("unsupported pending flight obtained fresh custody", change)
			}
			idleNoEffects(t, x.s, x.f, before)
		})
	}
}
