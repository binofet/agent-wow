package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"reflect"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type flightpathSession struct {
	*fakeSession
	beforeWorld func()
	beforeSend  func(*modv1.SendPacketRequest)
	rejectSend  bool
	worldReads  int
	sendCalls   int
	order       []string
}

func (f *flightpathSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "buddy-world" && r.Method == "snapshot" {
		f.worldReads++
		f.order = append(f.order, "world")
		if f.beforeWorld != nil {
			f.beforeWorld()
		}
	}
	return f.fakeSession.InvokeModule(ctx, r, opts...)
}

func (f *flightpathSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	f.sendCalls++
	f.order = append(f.order, "send")
	if f.beforeSend != nil {
		f.beforeSend(r)
	}
	if f.rejectSend {
		return nil, modcb.PacketNotDispatchedError("fixture rejected before game transport")
	}
	return f.fakeSession.SendPacket(ctx, r, opts...)
}

type flightpathRig struct {
	s       *Service
	f       *fakeSession
	client  *flightpathSession
	advance func(time.Duration)
	npc     uint64
	token   string
}

func newFlightpathRig(t *testing.T) *flightpathRig {
	t.Helper()
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	f.world.Owner = nil
	f.world.SelfCopperKnown, f.world.SelfCopper, f.world.SelfCopperRevision = true, 12345, 7
	const npc = uint64(0xf130000202000001)
	f.world.NearbyCreatures = []*worldv1.CreatureState{{Guid: npc, Entry: 514, NpcFlags: 0x2000,
		Present: true, MapKnown: true, PositionKnown: true, PositionRevision: 1, X: 2,
		AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, MaxHealth: 100, UnitFlagsKnown: true}}
	entry := binary.LittleEndian.AppendUint32(nil, f.world.Self.MapId)
	for _, p := range []float32{f.world.Self.X, f.world.Self.Y, f.world.Self.Z, f.world.Self.Orientation} {
		entry = binary.LittleEndian.AppendUint32(entry, math.Float32bits(p))
	}
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLoginVerifyWorld, Payload: entry}); err != nil {
		t.Fatal(err)
	}
	f.world.VisitRevision = 1
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "exploration", LeaseMs: 15000})
	if err != nil || lease.GetToken() == "" || len(f.packets) != 0 || !s.encounterVisit.bound || s.encounterVisit.revision != 1 {
		t.Fatal("could not establish the real bound, idle exploration fixture", lease, err, f.packets)
	}
	client := &flightpathSession{fakeSession: f}
	s.session = client
	return &flightpathRig{s: s, f: f, client: client, advance: advance, npc: npc, token: lease.Token}
}

func (x *flightpathRig) observation(t *testing.T) *actionsv1.FlightpathObservationResponse {
	t.Helper()
	reads, sends := x.client.worldReads, x.client.sendCalls
	view, err := x.s.FlightpathObservation(context.Background(), &emptypb.Empty{})
	if err != nil || x.client.worldReads != reads || x.client.sendCalls != sends {
		t.Fatal("flightpath observation dispatched a query", view, err)
	}
	return view
}

func (x *flightpathRig) request(t *testing.T, mode string) *actionsv1.FlightpathQueryRequest {
	t.Helper()
	return &actionsv1.FlightpathQueryRequest{SessionId: x.s.sessionID, SelfGuid: x.s.selfGUID, VisitId: 1,
		Token: x.token, NpcGuid: x.npc, Mode: mode, ExpectedRevision: x.observation(t).Revision}
}

func (x *flightpathRig) query(t *testing.T, mode string) (*actionsv1.FlightpathObservationResponse, *actionsv1.FlightpathQueryRequest) {
	t.Helper()
	r := x.request(t, mode)
	view, err := x.s.QueryFlightpath(context.Background(), r)
	if err != nil || view.GetOperation() == nil {
		t.Fatal("actual flightpath query was not admitted", view, err)
	}
	return view, r
}

func (x *flightpathRig) packet(t *testing.T, op uint32, payload []byte) {
	t.Helper()
	if _, err := x.s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: payload}); err != nil {
		t.Fatal(err)
	}
}

func (x *flightpathRig) unknownBaseline(t *testing.T) {
	t.Helper()
	x.query(t, "status")
	x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 0))
	view := x.observation(t)
	if view.Operation.Pending || !view.Operation.StatusKnown || view.Operation.NodeKnown || view.Operation.Phase != "status_observed" {
		t.Fatal("fixture did not obtain a real unknown-node status", view)
	}
}

func TestFlightpathQueriesLearnThenRequireExplicitMenuQueryWithoutTravelOrSpend(t *testing.T) {
	x := newFlightpathRig(t)
	x.client.beforeSend = func(packet *modv1.SendPacketRequest) {
		op := x.s.flightpath.operation
		if op == nil || !op.Pending || op.Revision == 0 || op.NpcGuid != x.npc || x.s.flightpath.request == nil ||
			len(packet.Payload) != 8 || binary.LittleEndian.Uint64(packet.Payload) != x.npc {
			t.Fatal("query was not reserved with its exact receipt before transport", op, packet)
		}
	}
	x.unknownBaseline(t)
	x.query(t, "discover")
	x.packet(t, opcode.SMSGNewTaxiPath, nil)
	if op := x.observation(t).Operation; !op.Pending || !op.LearningNoticeObserved || op.Learned || op.MenuKnown || op.SourceNode != 0 {
		t.Fatal("identity-free notice invented a learned node or menu", op)
	}
	x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1))
	learned := x.observation(t)
	if learned.Operation.Pending || !learned.Operation.Learned || !learned.Operation.StatusKnown || !learned.Operation.NodeKnown ||
		learned.Operation.MenuKnown || len(learned.Operation.KnownNodes) != 0 || learned.Operation.Phase != "learned" || len(x.f.packets) != 2 {
		t.Fatal("learning was not distinguished from the later explicit menu query", learned, x.f.packets)
	}
	x.query(t, "discover")
	x.packet(t, opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, 1, 1, 32, 448))
	menu := x.observation(t).Operation
	if menu.Pending || !menu.MenuKnown || menu.SourceNode != 1 || !reflect.DeepEqual(menu.KnownNodes, []uint32{1, 32, 448}) ||
		menu.Learned || menu.Phase != "menu_observed" || len(x.f.packets) != 3 {
		t.Fatal("menu receipt did not preserve only its actual server mask", menu, x.f.packets)
	}
	for i, packet := range x.f.packets {
		want := uint32(opcode.CMSGTaxiqueryavailablenodes)
		if i == 0 {
			want = opcode.CMSGTaxinodeStatusQuery
		}
		if packet.Opcode != want || !bytes.Equal(packet.Payload, binary.LittleEndian.AppendUint64(nil, x.npc)) {
			t.Fatal("discovery sent an activation, unrelated action or nonexact query", packet)
		}
	}
	if !reflect.DeepEqual(x.client.order, []string{"world", "send", "world", "send", "world", "send"}) ||
		x.f.world.SelfCopper != 12345 || x.f.world.SelfCopperRevision != 7 || x.s.state.Traveling || x.s.state.Moving {
		t.Fatal("query had a post-world dependency, spent copper or started travel", x.client.order, x.s.stateLocked())
	}
}

func TestFlightpathCASReplayAndConflictsNeverRepeatWorldOrTransport(t *testing.T) {
	x := newFlightpathRig(t)
	first, request := x.query(t, "status")
	reads, sends := x.client.worldReads, x.client.sendCalls
	for _, settled := range []bool{false, true} {
		if settled {
			x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1))
			first = x.observation(t)
		}
		replayed, err := x.s.QueryFlightpath(context.Background(), proto.Clone(request).(*actionsv1.FlightpathQueryRequest))
		if err != nil || !proto.Equal(replayed, first) || x.client.worldReads != reads || x.client.sendCalls != sends {
			t.Fatal("exact CAS replay dispatched another query or lost its receipt", replayed, err)
		}
	}
	for _, field := range []string{"mode", "NPC", "token", "session", "body", "visit"} {
		bad := proto.Clone(request).(*actionsv1.FlightpathQueryRequest)
		switch field {
		case "mode":
			bad.Mode = "discover"
		case "NPC":
			bad.NpcGuid++
		case "token":
			bad.Token = "another-token"
		case "session":
			bad.SessionId = "another-session"
		case "body":
			bad.SelfGuid++
		case "visit":
			bad.VisitId++
		}
		if _, err := x.s.QueryFlightpath(context.Background(), bad); status.Code(err) != codes.Aborted {
			t.Fatal("conflicting stale CAS was accepted", field, err)
		}
	}
	x.query(t, "status")
	reads, sends = x.client.worldReads, x.client.sendCalls
	if _, err := x.s.QueryFlightpath(context.Background(), request); status.Code(err) != codes.Aborted ||
		x.client.worldReads != reads || x.client.sendCalls != sends {
		t.Fatal("an older replaced CAS receipt replayed its write", err)
	}
	// Returned receipts must never alias the module's current mutable state.
	view := x.observation(t)
	view.Operation.Phase, view.Operation.NpcGuid = "caller-mutation", 1
	if current := x.observation(t).Operation; current.Phase == "caller-mutation" || current.NpcGuid != x.npc {
		t.Fatal("read response aliases the native reservation", current)
	}
}

func TestFlightpathUnknownTransportSurvivesTimeLeaseReadsAndSourceEditsWithoutReplay(t *testing.T) {
	x := newFlightpathRig(t)
	x.unknownBaseline(t)
	x.f.failSend = true
	unknown, request := x.query(t, "discover")
	if !unknown.Operation.Pending || !unknown.Operation.TransportUnknown || unknown.Operation.TransportWritten || unknown.Operation.Phase != "transport_unknown" {
		t.Fatal("ambiguous send was treated as a safe rejection", unknown)
	}
	x.f.failSend = false
	x.advance(time.Hour)
	if _, err := x.s.Status(context.Background(), &emptypb.Empty{}); err != nil || x.s.state.InteractionOwner != "" {
		t.Fatal("fixture did not expire its original idle lease", err)
	}
	// Exercise the public source CAS path while the native reservation remains
	// unresolved. Reloading Lua must not manufacture a taxi retry opportunity.
	var revision string
	for _, source := range []string{"return {decide=function(input) return {} end}", "return {decide=function(input) return {} end} -- edited"} {
		view, err := x.s.ReloadBehavior(context.Background(), &actionsv1.BehaviorReloadRequest{
			SessionId: x.s.sessionID, SelfGuid: x.s.selfGUID, ExpectedRevision: revision, Source: source})
		if err != nil || view.Revision == revision {
			t.Fatal("fixture did not perform a real Lua source edit", view, err)
		}
		revision = view.Revision
	}
	reads, sends := x.client.worldReads, x.client.sendCalls
	for range 3 {
		view := x.observation(t)
		if !proto.Equal(view.Operation, unknown.Operation) {
			t.Fatal("time, lease expiry or read discarded the unknown reservation", view)
		}
		if replay, err := x.s.QueryFlightpath(context.Background(), request); err != nil || !proto.Equal(replay.Operation, unknown.Operation) {
			t.Fatal("exact unknown replay did not return its retained receipt", replay, err)
		}
		if _, err := x.s.QueryFlightpath(context.Background(), x.request(t, "status")); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("a newer query bypassed the unresolved send", err)
		}
	}
	if x.client.worldReads != reads || x.client.sendCalls != sends {
		t.Fatal("unresolved reads or repeats reached a dependency or transport", x.client.order)
	}
	x.packet(t, opcode.SMSGNewTaxiPath, nil)
	x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc+1, 1))
	if !x.observation(t).Operation.Pending {
		t.Fatal("foreign status settled a learning notice")
	}
	x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1))
	op := x.observation(t).Operation
	if op.Pending || !op.Learned || !op.TransportUnknown || op.TransportWritten || x.client.sendCalls != sends {
		t.Fatal("late actual learning proof did not reconcile without a resend", op, x.client.order)
	}
}

func TestFlightpathMalformedForeignAndOldVisitPacketsCannotSettle(t *testing.T) {
	x := newFlightpathRig(t)
	// Incidental packets do not manufacture an operation or a learn baseline.
	x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1))
	x.packet(t, opcode.SMSGNewTaxiPath, nil)
	x.packet(t, opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, 1, 1))
	if x.observation(t).Operation != nil {
		t.Fatal("unsolicited packet manufactured a taxi request receipt")
	}
	if _, err := x.s.QueryFlightpath(context.Background(), x.request(t, "discover")); status.Code(err) != codes.FailedPrecondition || len(x.f.packets) != 0 {
		t.Fatal("incidental data authorized discovery without an owned baseline", err)
	}
	x.unknownBaseline(t)
	x.query(t, "discover")
	before := x.observation(t).Operation
	malformed := x.s.state.MalformedPackets
	for _, packet := range []struct {
		op   uint32
		data []byte
	}{
		{opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 2)},
		{opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1)[:8]},
		{opcode.SMSGNewTaxiPath, []byte{0}},
		{opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, 1, 1)[:71]},
	} {
		x.packet(t, packet.op, packet.data)
	}
	if !proto.Equal(before, x.observation(t).Operation) || x.s.state.MalformedPackets != malformed+4 {
		t.Fatal("malformed packets changed an operation or escaped accounting", x.observation(t))
	}
	x.packet(t, opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc+1, 1, 1))
	x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc+1, 1))
	x.packet(t, opcode.SMSGNewTaxiPath, nil)
	if op := x.observation(t).Operation; !op.Pending || op.Learned || op.MenuKnown {
		t.Fatal("foreign identity or an identity-free notice completed the request", op)
	}
	x.packet(t, opcode.SMSGTransferPending, binary.LittleEndian.AppendUint32(nil, 1))
	for _, packet := range []struct {
		op   uint32
		data []byte
	}{
		{opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1)},
		{opcode.SMSGNewTaxiPath, nil},
		{opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, 1, 1)},
	} {
		x.packet(t, packet.op, packet.data)
	}
	view := x.observation(t)
	if view.VisitBound || !view.Operation.Pending || view.Operation.Learned || view.Operation.MenuKnown || len(x.f.packets) != 2 {
		t.Fatal("old/unbound visit replies settled or retried a reservation", view, x.f.packets)
	}
}

func TestFlightpathTypedPretransportRefusalIsDefiniteButNeverReplaysItsCAS(t *testing.T) {
	x := newFlightpathRig(t)
	x.client.rejectSend = true
	view, request := x.query(t, "status")
	if view.Operation.Pending || view.Operation.TransportUnknown || view.Operation.TransportWritten || view.Operation.Phase != "not_dispatched" || len(x.f.packets) != 0 {
		t.Fatal("typed pretransport rejection was not definite", view, x.f.packets)
	}
	x.client.rejectSend = false
	reads, sends := x.client.worldReads, x.client.sendCalls
	if repeated, err := x.s.QueryFlightpath(context.Background(), request); err != nil || !proto.Equal(repeated, view) ||
		x.client.worldReads != reads || x.client.sendCalls != sends {
		t.Fatal("same definite CAS automatically retried", repeated, err)
	}
	x.query(t, "status")
	if len(x.f.packets) != 1 {
		t.Fatal("explicit new CAS could not request a new status after definite refusal", x.f.packets)
	}
}

func assertFlightpathNotDispatched(t *testing.T, view *actionsv1.FlightpathObservationResponse, request *actionsv1.FlightpathQueryRequest) {
	t.Helper()
	op := view.GetOperation()
	if op == nil || view.Revision != request.ExpectedRevision+1 || op.Revision != view.Revision ||
		op.SessionId != request.SessionId || op.SelfGuid != request.SelfGuid || op.VisitId != request.VisitId ||
		op.NpcGuid != request.NpcGuid || op.Mode != request.Mode || op.Phase != "not_dispatched" ||
		op.Pending || op.TransportWritten || op.TransportUnknown || op.StatusKnown || op.NodeKnown ||
		op.LearningNoticeObserved || op.Learned || op.MenuKnown || op.SourceNode != 0 || len(op.KnownNodes) != 0 ||
		op.Error == "" || len(op.Error) > 240 {
		t.Fatal("known zero-send refusal lost its exact retained operation receipt", view)
	}
	for _, value := range []byte(op.Error) {
		if value < 32 || value > 126 {
			t.Fatal("known refusal retained an unbounded diagnostic byte", op.Error)
		}
	}
}

func TestFlightpathFinalWorldCallbackRechecksNativeBodyLeaseAndFlightmaster(t *testing.T) {
	for _, change := range []string{"lease token", "lease owner", "lease expiry", "session", "self body", "World body", "World visit", "Actions visit",
		"NPC removed", "NPC flags", "NPC entry", "NPC range", "NPC age", "NPC dead", "NPC unit known", "NPC moving",
		"cast", "moving", "uncertain motion", "body input", "control lost", "control restoring", "raw movement", "combat", "channel unknown", "channel active"} {
		t.Run(change, func(t *testing.T) {
			x := newFlightpathRig(t)
			r := x.request(t, "status")
			injected := false
			x.client.beforeWorld = func() {
				injected = true
				switch change {
				case "lease token":
					x.s.interactionToken = "replacement-token"
				case "lease owner":
					x.s.state.InteractionOwner = "trade"
				case "lease expiry":
					x.advance(15 * time.Second)
				case "session":
					x.s.sessionID = "replacement-session"
				case "self body":
					x.s.selfGUID++
				case "World body":
					x.f.world.Self.Guid++
				case "World visit":
					x.f.world.VisitRevision++
				case "Actions visit":
					x.s.encounterVisit.revision++
				case "NPC removed":
					x.f.world.NearbyCreatures = nil
				case "NPC flags":
					x.f.world.NearbyCreatures[0].NpcFlags = 0x80
				case "NPC entry":
					x.f.world.NearbyCreatures[0].Entry++
				case "NPC range":
					x.f.world.NearbyCreatures[0].X = 6
				case "NPC age":
					x.f.world.NearbyCreatures[0].PositionAgeMs = 2001
				case "NPC dead":
					x.f.world.NearbyCreatures[0].Alive = false
				case "NPC unit known":
					x.f.world.NearbyCreatures[0].UnitFlagsKnown = false
				case "NPC moving":
					x.f.world.NearbyCreatures[0].MovementFlags = 1
				case "cast":
					x.s.state.CastPending = true
				case "moving":
					x.s.state.Moving = true
				case "uncertain motion":
					x.s.state.MotionUnconfirmed = true
				case "body input":
					x.s.bodyMoving = true
				case "control lost":
					x.s.clientControlLost = true
				case "control restoring":
					x.s.clientControlRestoring = true
				case "raw movement":
					x.f.world.Self.MovementFlags = 1
				case "combat":
					x.f.world.Self.UnitFlags = 0x80000
				case "channel unknown":
					x.f.world.Self.ChannelObjectKnown = false
				case "channel active":
					x.f.world.Self.ChannelSpellId = 13262
				}
			}
			view, err := x.s.QueryFlightpath(context.Background(), r)
			if err != nil || !injected || x.client.worldReads != 1 || x.client.sendCalls != 0 || len(x.f.packets) != 0 {
				t.Fatal("late admission change reached query transport or lost its definite receipt", change, view, err, x.client.order)
			}
			assertFlightpathNotDispatched(t, view, r)
			if !proto.Equal(view.Operation, x.observation(t).Operation) {
				t.Fatal("late refusal was not retained for read-only reconciliation")
			}
			if change == "lease token" && x.s.interactionToken != "replacement-token" || change == "lease owner" && x.s.state.InteractionOwner != "trade" {
				t.Fatal("rejected old request overwrote replacement custody")
			}
		})
	}
}

func TestFlightpathFinalClockCannotAuthorizeChangedCustodyOrNonfiniteClientPose(t *testing.T) {
	for _, change := range []string{"lease", "session", "visit", "control", "NaN", "Inf"} {
		t.Run(change, func(t *testing.T) {
			x := newFlightpathRig(t)
			r := x.request(t, "status")
			clock, armed, injected := x.s.now, false, false
			x.client.beforeWorld = func() { armed = true }
			x.s.now = func() time.Time {
				if armed && !injected {
					injected = true
					switch change {
					case "lease":
						x.s.interactionToken = "replacement-after-world"
					case "session":
						x.s.sessionID = "replacement-after-world"
					case "visit":
						x.s.encounterVisit.revision++
					case "control":
						x.s.clientControlLost = true
					case "NaN":
						x.s.pos.x = float32(math.NaN())
					case "Inf":
						x.s.pos.x = float32(math.Inf(1))
					}
				}
				return clock()
			}
			view, err := x.s.QueryFlightpath(context.Background(), r)
			if err != nil || !injected || x.client.worldReads != 1 || x.client.sendCalls != 0 || len(x.f.packets) != 0 {
				t.Fatal("post-world clock mutation reached query transport or lost its definite receipt", change, view, err, x.client.order)
			}
			assertFlightpathNotDispatched(t, view, r)
			if change == "lease" && x.s.interactionToken != "replacement-after-world" {
				t.Fatal("rejected query erased replacement lease custody")
			}
		})
	}
}

func TestFlightpathFailedWorldReadRetainsDefiniteReceiptAndCannotReplay(t *testing.T) {
	x := newFlightpathRig(t)
	r := x.request(t, "status")
	x.f.failSnapshot = true
	view, err := x.s.QueryFlightpath(context.Background(), r)
	if err != nil || x.client.worldReads != 1 || x.client.sendCalls != 0 || len(x.f.packets) != 0 {
		t.Fatal("failed World read was not a definite zero-send receipt", view, err, x.client.order)
	}
	assertFlightpathNotDispatched(t, view, r)
	x.f.failSnapshot = false
	repeated, err := x.s.QueryFlightpath(context.Background(), r)
	if err != nil || !proto.Equal(repeated, view) || x.client.worldReads != 1 || x.client.sendCalls != 0 {
		t.Fatal("re-reading a failed query's CAS retried the World read or send", repeated, err, x.client.order)
	}
	x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1))
	if !proto.Equal(x.observation(t), view) {
		t.Fatal("an incidental reply settled an operation which never entered send")
	}
}

func TestFlightpathRepliesBeforeSendEntryCannotSettleReservedOperation(t *testing.T) {
	for _, mode := range []string{"status", "discover"} {
		t.Run(mode, func(t *testing.T) {
			x := newFlightpathRig(t)
			if mode == "discover" {
				x.unknownBaseline(t)
			}
			r := x.request(t, mode)
			injected := false
			x.client.beforeWorld = func() {
				injected = true
				if x.s.flightpath.operation.GetPhase() != "reserved" || !x.s.flightpath.operation.GetPending() {
					t.Fatal("fixture did not reach the pre-send native reservation")
				}
				for _, packet := range []struct {
					op   uint32
					data []byte
				}{
					{opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1)},
					{opcode.SMSGNewTaxiPath, nil},
					{opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, 1, 1)},
				} {
					// Query owns Service.mu across this callback; call the same
					// transactional packet hook without trying to reacquire it.
					if handled, err := x.s.flightpathPacketLocked(packet.op, packet.data); !handled || err != nil {
						t.Fatal("valid incidental packet did not reach the real receipt hook", handled, err)
					}
				}
			}
			view, err := x.s.QueryFlightpath(context.Background(), r)
			op := view.GetOperation()
			if err != nil || !injected || op == nil || !op.Pending || op.Phase != "requested" || !op.TransportWritten ||
				op.StatusKnown || op.LearningNoticeObserved || op.Learned || op.MenuKnown {
				t.Fatal("pre-send reply was attributed to an operation which had not sent", view, err)
			}
			if mode == "discover" {
				x.packet(t, opcode.SMSGNewTaxiPath, nil)
			}
			x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1))
			op = x.observation(t).Operation
			if op.Pending || !op.StatusKnown || mode == "discover" && !op.Learned || mode == "status" && op.Phase != "status_observed" {
				t.Fatal("actual post-send matching proof could not settle the reserved query", op)
			}
		})
	}
}
