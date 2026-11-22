package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/taxireference"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func flightpathSettlementPacket(t *testing.T, x *flightpathRig, op uint32, payload []byte, scope *modv1.TransportScope) {
	t.Helper()
	if _, err := x.s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: payload, TransportScope: scope}); err != nil {
		t.Fatal(err)
	}
}

// This fixture performs real native query/menu/activation/reply transitions.
// The independently qualified World layer is represented by explicit coherent
// synthetic observations here; these tests do not claim another parser test.
func newFlightpathSettlementRig(t *testing.T) *flightpathRig {
	t.Helper()
	x := newFlightpathActivationRig(t)
	flightpathActivate(t, x, flightpathActivationRequest(t, x))
	acceptance := &modv1.TransportScope{ConnectionId: "settlement-test-connection", SelfGuid: 1,
		VisitBound: true, VisitRevision: 9, MapId: 0, MotionRevision: 10}
	flightpathSettlementPacket(t, x, opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0}, acceptance)
	catalog, err := taxireference.Default()
	if err != nil {
		t.Fatal(err)
	}
	node, ok := catalog.Node(2)
	if !ok || node.MapID != 0 {
		t.Fatal("maintained destination reference fixture missing", node)
	}
	w := x.f.world
	w.Self.X, w.Self.Y, w.Self.Z = float32(node.X), float32(node.Y), float32(node.Z)
	w.Self.PositionRevision, w.Self.PositionInferred = 2, false
	w.Self.MovementFlags, w.Self.ExtraMovementFlags, w.Self.UnitFlags = 0, 0, 0
	w.Self.MovementTime = 500
	source := cloneFlightpathScope(acceptance)
	source.MotionRevision, source.SplineSourceRevision, source.SplineRevision = 13, 11, 41
	source.SplineIdKnown, source.SplineId, source.SplineActive, source.SplineSourceOpcode = true, 0, true, opcode.SMSGMonsterMove
	current := cloneFlightpathScope(source)
	current.MotionRevision = 16
	rawScope := cloneFlightpathScope(source)
	rawScope.MotionRevision = 15
	info := movementinfo.Info{Time: 500, X: w.Self.X, Y: w.Self.Y, Z: w.Self.Z, Orientation: w.Self.Orientation}
	wire, err := movementinfo.Encode(info)
	if err != nil {
		t.Fatal(err)
	}
	w.SelfMovementFacts = &worldv1.OwnMovementFacts{Known: true, SelfGuid: 1, VisitRevision: 1, Revision: 2,
		SourceOpcode: opcode.MSGMoveHeartbeat, MapKnown: true, MapId: 0, WireImage: wire, TransportScope: rawScope,
		Info: &worldv1.MovementInfoFacts{Time: 500, X: info.X, Y: info.Y, Z: info.Z, Orientation: info.Orientation}}
	w.SelfSplineFacts = &worldv1.OwnSplineFacts{Known: true, SelfGuid: 1, VisitRevision: 1, Revision: 3,
		SourceOpcode: opcode.SMSGMonsterMove, IdKnown: true, SplineId: 0, Flags: 0x2000, DurationMs: 1000,
		DestinationKnown: true, DestinationX: info.X, DestinationY: info.Y, DestinationZ: info.Z, MapKnown: true, MapId: 0}
	w.SelfMotionAuthority = &worldv1.OwnMotionAuthority{SelfGuid: 1, VisitRevision: 1, Revision: 8,
		MovementRevision: 2, SplineRevision: 3, TransportScope: current,
		Known: false, Reason: "ground observations superseded ACK authority", UnitFlagsKnown: true, UnitFlags: 0,
		UnitFlagsRevision: 3, UnitFlagsSourceOpcode: opcode.SMSGUpdateObject, UnitFlagsMotionRevision: 16}
	w.SplineCompletion = &worldv1.SplineCompletionObservation{SelfGuid: 1, VisitRevision: 1, Revision: 2, RetirementCurrent: true,
		Operation: &worldv1.SplineCompletionOperation{Revision: 1, SelfGuid: 1, VisitRevision: 1, AuthorityRevision: 4,
			MovementRevision: 1, SplineRevision: 3, TransportScope: source, TransportWritten: true, Phase: "transport_written",
			MovementImageInferred: true, MovementImageSource: "received_movement_info_with_advertised_spline_endpoint", SplineIdKnown: true,
			Retired: true, RetirementRevision: 2, RetiredMovementRevision: 2, RetiredUnitFlagsRevision: 3, RetirementScope: cloneFlightpathScope(current)}}
	// An independently observed destination flightmaster, not the departure NPC.
	npc := proto.Clone(w.NearbyCreatures[0]).(*worldv1.CreatureState)
	x.npc, npc.Guid, npc.Entry = 0xf13000012400002b, 0xf13000012400002b, 0x124
	npc.X, npc.Y, npc.Z, npc.PositionRevision = info.X+1, info.Y, info.Z, 2
	w.NearbyCreatures = []*worldv1.CreatureState{npc}
	x.client.worldReads, x.client.sendCalls, x.client.order = 0, 0, nil
	return x
}

func flightpathSettlementArrivalRequest(t *testing.T, x *flightpathRig) *actionsv1.FlightpathQueryRequest {
	t.Helper()
	view := x.observation(t)
	return &actionsv1.FlightpathQueryRequest{SessionId: x.s.sessionID, SelfGuid: x.s.selfGUID, VisitId: 1,
		Token: x.token, NpcGuid: x.npc, Mode: "arrival", ExpectedRevision: view.Revision,
		ActivationRevision: view.ActivationRevision, CompletionRevision: x.f.world.SplineCompletion.Revision}
}

func flightpathSettlementObserveMenu(t *testing.T, x *flightpathRig, source uint32) {
	t.Helper()
	flightpathSettlementPacket(t, x, opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, source, 2, 12, 448),
		cloneFlightpathScope(x.f.world.SelfMotionAuthority.TransportScope))
}

func flightpathSettlementRequest(t *testing.T, x *flightpathRig) *actionsv1.SettleFlightpathRequest {
	t.Helper()
	view, w := x.observation(t), x.f.world
	return &actionsv1.SettleFlightpathRequest{SessionId: x.s.sessionID, SelfGuid: x.s.selfGUID, VisitId: 1,
		ActivationRevision: view.ActivationRevision, MenuRevision: view.Revision, CompletionRevision: w.SplineCompletion.Revision,
		AuthorityRevision: w.SelfMotionAuthority.Revision, MovementRevision: w.SelfMovementFacts.Revision,
		UnitFlagsRevision: w.SelfMotionAuthority.UnitFlagsRevision, TransportScope: cloneFlightpathScope(w.SelfMotionAuthority.TransportScope)}
}

func flightpathSettlementReady(t *testing.T, x *flightpathRig) *actionsv1.FlightpathQueryRequest {
	t.Helper()
	r := flightpathSettlementArrivalRequest(t, x)
	view, err := x.s.QueryFlightpath(context.Background(), r)
	if err != nil || !view.GetOperation().GetPending() || view.Operation.Phase != "requested" {
		t.Fatal("coherent retired flight could not query its actual destination menu", view, err)
	}
	flightpathSettlementObserveMenu(t, x, 2)
	if op := x.observation(t).Operation; op.Pending || !op.MenuKnown || op.Mode != "arrival" || op.SourceNode != 2 {
		t.Fatal("actual scoped destination menu did not settle its query", op)
	}
	return r
}

func TestFlightpathSettlementObservedDestinationMenuRetiresBookingAndCertifiesNextHop(t *testing.T) {
	x := newFlightpathSettlementRig(t)
	departure := x.observation(t).ActivationMenu
	if departure == nil || departure.SourceNode != 12 || departure.NpcGuid == x.npc {
		t.Fatal("fixture did not retain the original departure menu independently", departure)
	}
	query := flightpathSettlementReady(t, x)
	arrival := x.observation(t)
	if len(x.f.packets) != 2 || x.client.sendCalls != 1 || x.f.packets[1].Opcode != opcode.CMSGTaxiqueryavailablenodes ||
		!bytes.Equal(x.f.packets[1].Payload, binary.LittleEndian.AppendUint64(nil, x.npc)) ||
		arrival.Operation.ActivationRevision != query.ActivationRevision || arrival.Operation.CompletionRevision != query.CompletionRevision ||
		!proto.Equal(arrival.ActivationMenu, departure) || !arrival.Activation.Pending || arrival.Activation.ArrivalKnown {
		t.Fatal("arrival query changed booking history, spent again, or invented arrival", arrival, x.f.packets)
	}
	request := flightpathSettlementRequest(t, x)
	view, err := x.s.SettleFlightpath(context.Background(), request)
	if err != nil || view.Activation.Pending || view.Activation.Phase != "observed_arrival" || !view.Activation.ArrivalKnown ||
		!view.Activation.ReplyKnown || view.Activation.ReplyCode != 0 || !view.Activation.ActivationAccepted ||
		view.Activation.LiveFareKnown || view.Activation.LiveRouteKnown || view.Activation.FlightStartedKnown ||
		!proto.Equal(view.ActivationMenu, departure) || x.client.sendCalls != 1 || len(x.f.packets) != 2 ||
		x.f.world.SplineCompletion.Operation.ArrivalKnown {
		t.Fatal("settlement lacked actual bindings or changed unrelated delivery/fare facts", view, err)
	}
	want := proto.Clone(view)
	reads := x.client.worldReads
	view.ActivationMenu.KnownNodes[0] = 99
	view.Activation.AcceptanceScope.ConnectionId = "caller-mutation"
	view.Activation.Phase = "caller-mutation"
	again, err := x.s.SettleFlightpath(context.Background(), request)
	if err != nil || !proto.Equal(again, want) || x.client.worldReads != reads || x.client.sendCalls != 1 {
		t.Fatal("exact settlement replay lost its detached cached receipt or read/sent again", again, err)
	}
	// The actual, certified arrival menu can supply the next explicitly chosen
	// hop even after its activation reservation becomes pending.
	next := flightpathActivationRequest(t, x)
	next.SourceNode, next.DestinationNode = 2, 12
	next.NpcGuid, next.MenuRevision = x.npc, arrival.Revision
	nextView := flightpathActivate(t, x, next)
	if nextView.ActivationRevision != 2 || nextView.Activation.SourceNode != 2 || nextView.Activation.DestinationNode != 12 ||
		nextView.ActivationMenu.Mode != "arrival" || nextView.ActivationMenu.SourceNode != 2 || x.client.sendCalls != 2 {
		t.Fatal("certified actual arrival menu could not authorize the next explicit selection", nextView)
	}
	reads = x.client.worldReads
	if _, err := x.s.SettleFlightpath(context.Background(), request); status.Code(err) != codes.FailedPrecondition || x.client.worldReads != reads {
		t.Fatal("older settlement replay escaped the latest activation slot", err)
	}
}

func TestFlightpathSettlementAcceptanceScopeMustComeFromTheActualFirstReply(t *testing.T) {
	x := newFlightpathActivationRig(t)
	flightpathActivate(t, x, flightpathActivationRequest(t, x))
	x.packet(t, opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0})
	if !x.observation(t).Activation.ActivationAccepted || x.observation(t).Activation.AcceptanceScope != nil {
		t.Fatal("legacy response invented an acceptance scope", x.observation(t))
	}
	r := x.request(t, "arrival")
	r.ActivationRevision, r.CompletionRevision = 1, 2
	reads, sends := x.client.worldReads, x.client.sendCalls
	if _, err := x.s.QueryFlightpath(context.Background(), r); status.Code(err) != codes.FailedPrecondition || x.client.worldReads != reads || x.client.sendCalls != sends {
		t.Fatal("missing actual acceptance boundary authorized an arrival query", err)
	}
	scope := &modv1.TransportScope{ConnectionId: "late-duplicate", SelfGuid: 1, VisitBound: true, VisitRevision: 9, MotionRevision: 10}
	flightpathSettlementPacket(t, x, opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0}, scope)
	if x.observation(t).Activation.AcceptanceScope != nil {
		t.Fatal("duplicate response retroactively supplied a missing booking boundary")
	}
	y := newFlightpathActivationRig(t)
	flightpathActivate(t, y, flightpathActivationRequest(t, y))
	scope.ConnectionId = "actual-first-reply"
	flightpathSettlementPacket(t, y, opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0}, scope)
	scope.ConnectionId = "caller-mutation"
	view := y.observation(t)
	if view.Activation.AcceptanceScope.GetConnectionId() != "actual-first-reply" {
		t.Fatal("received acceptance scope was not retained immutably", view)
	}
	view.Activation.AcceptanceScope.MotionRevision = 999
	if y.observation(t).Activation.AcceptanceScope.MotionRevision != 10 {
		t.Fatal("returned acceptance scope aliases native history")
	}
}

func TestFlightpathSettlementArrivalQueryRequiresCurrentPostAcceptanceRetiredProof(t *testing.T) {
	for _, change := range []string{"retirement not current", "not retired", "completion pending", "stale completion", "source predates acceptance", "legacy movement scope",
		"old movement", "inferred pose", "pose mismatch", "taxi flag", "source inactive", "foreign connection", "World visit"} {
		t.Run(change, func(t *testing.T) {
			x := newFlightpathSettlementRig(t)
			r := flightpathSettlementArrivalRequest(t, x)
			departure := x.observation(t).ActivationMenu
			x.client.beforeWorld = func() {
				w := x.f.world
				switch change {
				case "retirement not current":
					w.SplineCompletion.RetirementCurrent = false
				case "not retired":
					w.SplineCompletion.Operation.Retired = false
				case "completion pending":
					w.SplineCompletion.Operation.Pending = true
				case "stale completion":
					w.SplineCompletion.Revision++
				case "source predates acceptance":
					w.SplineCompletion.Operation.TransportScope.SplineSourceRevision = 10
				case "legacy movement scope":
					w.SelfMovementFacts.TransportScope = nil
				case "old movement":
					w.SelfMovementFacts.Revision = 1
				case "inferred pose":
					w.Self.PositionInferred = true
				case "pose mismatch":
					w.SelfMovementFacts.Info.X++
				case "taxi flag":
					w.Self.UnitFlags = 0x100000
				case "source inactive":
					w.SelfMotionAuthority.TransportScope.SplineActive = false
				case "foreign connection":
					w.SelfMotionAuthority.TransportScope.ConnectionId = "other-connection"
				case "World visit":
					w.VisitRevision++
				}
			}
			view, err := x.s.QueryFlightpath(context.Background(), r)
			if err != nil || view.GetOperation().GetPhase() != "not_dispatched" || view.Operation.Pending || view.Operation.TransportWritten ||
				view.Operation.TransportUnknown || view.Operation.ActivationRevision != 1 || view.Operation.CompletionRevision != 2 ||
				view.Operation.Error == "" || !view.Activation.Pending || view.Activation.ArrivalKnown || !proto.Equal(view.ActivationMenu, departure) ||
				x.client.worldReads != 1 || x.client.sendCalls != 0 || len(x.f.packets) != 1 {
				t.Fatal("unqualified World proof reached arrival query or lost the zero-send receipt", change, view, err)
			}
			reads := x.client.worldReads
			if again, err := x.s.QueryFlightpath(context.Background(), r); err != nil || !proto.Equal(again, view) || x.client.worldReads != reads || x.client.sendCalls != 0 {
				t.Fatal("arrival refusal retried instead of returning its retained CAS", again, err)
			}
		})
	}
}

func TestFlightpathSettlementArrivalRepliesNeedExactScopedOwnQuery(t *testing.T) {
	for _, change := range []string{"missing", "connection", "body", "visit", "map", "older motion", "source", "ID", "generation", "inactive", "foreign NPC", "malformed"} {
		t.Run(change, func(t *testing.T) {
			x := newFlightpathSettlementRig(t)
			r := flightpathSettlementArrivalRequest(t, x)
			x.client.beforeSend = func(_ *modv1.SendPacketRequest) {
				if handled, err := x.s.flightpathPacketLocked(opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, 2, 2, 12),
					x.f.world.SelfMotionAuthority.TransportScope); !handled || err != nil || x.s.flightpath.operation.MenuKnown {
					t.Fatal("arrival reply before own send acquired menu authority", handled, err)
				}
			}
			if view, err := x.s.QueryFlightpath(context.Background(), r); err != nil || !view.GetOperation().GetPending() {
				t.Fatal(view, err)
			}
			scope := cloneFlightpathScope(x.f.world.SelfMotionAuthority.TransportScope)
			npc := x.npc
			switch change {
			case "missing":
				scope = nil
			case "connection":
				scope.ConnectionId += "wrong"
			case "body":
				scope.SelfGuid++
			case "visit":
				scope.VisitRevision++
			case "map":
				scope.MapId++
			case "older motion":
				scope.MotionRevision--
			case "source":
				scope.SplineSourceRevision++
			case "ID":
				scope.SplineId++
			case "generation":
				scope.SplineRevision++
			case "inactive":
				scope.SplineActive = false
			case "foreign NPC":
				npc++
			}
			payload := flightpathMenuPayload(npc, 2, 2, 12)
			if change == "malformed" {
				payload = payload[:71]
			}
			flightpathSettlementPacket(t, x, opcode.SMSGShowtaxinodes, payload, scope)
			if op := x.observation(t).Operation; !op.Pending || op.MenuKnown || op.SourceNode != 0 {
				t.Fatal("unbound/malformed response certified an arrival menu", change, op)
			}
			flightpathSettlementObserveMenu(t, x, 2)
			if op := x.observation(t).Operation; op.Pending || !op.MenuKnown || op.SourceNode != 2 || x.client.sendCalls != 1 {
				t.Fatal("actual matching response could not reconcile without resend", op)
			}
		})
	}
}

func TestFlightpathSettlementUnknownArrivalQueryNeverReplaysAndCanReconcileLateMenu(t *testing.T) {
	x := newFlightpathSettlementRig(t)
	r := flightpathSettlementArrivalRequest(t, x)
	x.f.failSend = true
	view, err := x.s.QueryFlightpath(context.Background(), r)
	if err != nil || !view.Operation.Pending || !view.Operation.TransportUnknown || view.Operation.Phase != "transport_unknown" {
		t.Fatal("lost arrival query was not retained as unknown", view, err)
	}
	x.f.failSend = false
	x.advance(time.Minute)
	if _, err := x.s.Status(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatal(err)
	}
	reads := x.client.worldReads
	if again, err := x.s.QueryFlightpath(context.Background(), r); err != nil || !proto.Equal(again, view) || x.client.worldReads != reads || x.client.sendCalls != 1 {
		t.Fatal("expired unresolved arrival CAS sent again", again, err)
	}
	next := flightpathSettlementArrivalRequest(t, x)
	if _, err := x.s.QueryFlightpath(context.Background(), next); status.Code(err) != codes.FailedPrecondition || x.client.sendCalls != 1 {
		t.Fatal("new query bypassed unresolved arrival custody", err)
	}
	flightpathSettlementObserveMenu(t, x, 2)
	if op := x.observation(t).Operation; op.Pending || !op.MenuKnown || !op.TransportUnknown || op.SourceNode != 2 {
		t.Fatal("late actual scoped menu could not reconcile its one original write", op)
	}
	if result, err := x.s.SettleFlightpath(context.Background(), flightpathSettlementRequest(t, x)); err != nil || result.Activation.Pending || x.client.sendCalls != 1 {
		t.Fatal("lease expiry incorrectly prevented state-only settlement of qualified evidence", result, err)
	}
}

func TestFlightpathSettlementRejectsWrongNodeAndHistoricalOrChangedEvidence(t *testing.T) {
	for _, change := range []string{"actual wrong node", "not current", "inferred", "new source", "inactive", "old movement", "authority request", "movement request", "flags request", "scope request", "legacy acceptance"} {
		t.Run(change, func(t *testing.T) {
			x := newFlightpathSettlementRig(t)
			flightpathSettlementReady(t, x)
			if change == "actual wrong node" {
				// Obtain a real wrong-node menu for this explicit query, rather than
				// fabricating source advancement in the cached receipt.
				y := newFlightpathSettlementRig(t)
				if _, err := y.s.QueryFlightpath(context.Background(), flightpathSettlementArrivalRequest(t, y)); err != nil {
					t.Fatal(err)
				}
				flightpathSettlementObserveMenu(t, y, 12)
				x = y
			}
			r := flightpathSettlementRequest(t, x)
			switch change {
			case "not current":
				x.f.world.SplineCompletion.RetirementCurrent = false
			case "inferred":
				x.f.world.Self.PositionInferred = true
			case "new source":
				x.f.world.SelfMotionAuthority.TransportScope.SplineRevision++
			case "inactive":
				x.f.world.SelfMotionAuthority.TransportScope.SplineActive = false
			case "old movement":
				x.f.world.SelfMovementFacts.Revision = 1
			case "authority request":
				r.AuthorityRevision++
			case "movement request":
				r.MovementRevision++
			case "flags request":
				r.UnitFlagsRevision++
			case "scope request":
				r.TransportScope.MotionRevision++
			case "legacy acceptance":
				x.s.flightpathActivation.operation.AcceptanceScope = nil
			}
			sends := x.client.sendCalls
			if view, err := x.s.SettleFlightpath(context.Background(), r); err == nil || view != nil || !x.observation(t).Activation.Pending ||
				x.observation(t).Activation.ArrivalKnown || x.client.sendCalls != sends || x.s.flightpath.certifiedArrivalMenuRevision != 0 {
				t.Fatal("wrong/old factual evidence retired or certified the booking", change, view, err)
			}
		})
	}
}

func TestFlightpathSettlementRechecksSnapshotCallbacksAndPreservesReplacementCustody(t *testing.T) {
	for _, change := range []string{"session", "body", "visit", "World visit", "retirement", "actual pose", "activation pointer", "menu pointer", "cancel"} {
		t.Run(change, func(t *testing.T) {
			x := newFlightpathSettlementRig(t)
			flightpathSettlementReady(t, x)
			r := flightpathSettlementRequest(t, x)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var replacedActivation *actionsv1.FlightpathActivationOperation
			var replacedMenu *actionsv1.FlightpathOperation
			x.client.beforeWorld = func() {
				switch change {
				case "session":
					x.s.sessionID += "replaced"
				case "body":
					x.s.selfGUID++
				case "visit":
					x.s.encounterVisit.revision++
				case "World visit":
					x.f.world.VisitRevision++
				case "retirement":
					x.f.world.SplineCompletion.RetirementCurrent = false
				case "actual pose":
					x.f.world.Self.X++
				case "activation pointer":
					replacedActivation = proto.Clone(x.s.flightpathActivation.operation).(*actionsv1.FlightpathActivationOperation)
					x.s.flightpathActivation.operation = replacedActivation
				case "menu pointer":
					replacedMenu = proto.Clone(x.s.flightpath.operation).(*actionsv1.FlightpathOperation)
					x.s.flightpath.operation = replacedMenu
				case "cancel":
					cancel()
				}
			}
			sends, reads := x.client.sendCalls, x.client.worldReads
			if _, err := x.s.SettleFlightpath(ctx, r); err == nil || x.client.worldReads != reads+1 || x.client.sendCalls != sends ||
				!x.observation(t).Activation.Pending || x.observation(t).Activation.ArrivalKnown {
				t.Fatal("changed snapshot custody was certified", change, err)
			}
			if replacedActivation != nil && x.s.flightpathActivation.operation != replacedActivation ||
				replacedMenu != nil && x.s.flightpath.operation != replacedMenu {
				t.Fatal("state-only refusal erased replacement custody")
			}
		})
	}
}

func TestFlightpathSettlementAllowsHarmlessCurrentScopeCatchupWithoutRelabelingOldProof(t *testing.T) {
	x := newFlightpathSettlementRig(t)
	if _, err := x.s.QueryFlightpath(context.Background(), flightpathSettlementArrivalRequest(t, x)); err != nil {
		t.Fatal(err)
	}
	replyScope := cloneFlightpathScope(x.f.world.SelfMotionAuthority.TransportScope)
	replyScope.MotionRevision++
	flightpathSettlementPacket(t, x, opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, 2, 2, 12), replyScope)
	if _, err := x.s.SettleFlightpath(context.Background(), flightpathSettlementRequest(t, x)); err == nil {
		t.Fatal("menu ingress was used before World caught up to its scope")
	}
	historical := proto.Clone(x.f.world.SplineCompletion.Operation.RetirementScope)
	x.f.world.SelfMotionAuthority.TransportScope.MotionRevision++
	x.f.world.SelfMotionAuthority.Revision++
	view, err := x.s.SettleFlightpath(context.Background(), flightpathSettlementRequest(t, x))
	if err != nil || view.Activation.Pending || !view.Activation.ArrivalKnown ||
		!proto.Equal(x.f.world.SplineCompletion.Operation.RetirementScope, historical) || x.client.sendCalls != 1 {
		t.Fatal("harmless current catchup required rewriting old retirement or another packet", view, err)
	}
}

func TestFlightpathSettlementStateOnlyKeepsNewerLeaseAndUnrelatedControllerState(t *testing.T) {
	x := newFlightpathSettlementRig(t)
	flightpathSettlementReady(t, x)
	r := flightpathSettlementRequest(t, x)
	x.advance(time.Minute)
	if _, err := x.s.Status(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatal(err)
	}
	x.s.state.InteractionOwner, x.s.interactionToken = "trade", "new-owner-token"
	x.s.state.Traveling = true // a new controller's state is not ours to clear
	sends := x.client.sendCalls
	view, err := x.s.SettleFlightpath(context.Background(), r)
	if err != nil || view.Activation.Pending || !view.Activation.ArrivalKnown || x.client.sendCalls != sends ||
		x.s.state.InteractionOwner != "trade" || x.s.interactionToken != "new-owner-token" || !x.s.state.Traveling {
		t.Fatal("metadata settlement acquired/released/overwrote unrelated custody", view, err)
	}
}
