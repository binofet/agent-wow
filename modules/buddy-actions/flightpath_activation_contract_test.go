package buddyactions

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestFlightpathActivationPublishedContractsPreserveCurrentAPIs(t *testing.T) {
	api := actionsv1.File_modules_buddy_actions_actions_proto
	for _, module := range []string{"buddy-actions", "buddy-inventory", "buddy-loot"} {
		t.Run(module, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", module, "module.pb"))
			if err != nil {
				t.Fatal(err)
			}
			set := &descriptorpb.FileDescriptorSet{}
			if err := proto.Unmarshal(body, set); err != nil {
				t.Fatal(err)
			}
			files, err := protodesc.NewFiles(set)
			if err != nil {
				t.Fatal("incomplete release import closure", err)
			}
			file, err := files.FindFileByPath(api.Path())
			if err != nil || !proto.Equal(protodesc.ToFileDescriptorProto(file), protodesc.ToFileDescriptorProto(api)) {
				t.Fatal("published Actions API differs from compiled activation API", err)
			}
		})
	}
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	actions, ok := registry.Lookup("buddy-actions")
	if !ok {
		t.Fatal("missing Actions manifest")
	}
	for alias, name := range map[string]string{
		"activate_flightpath": "ActivateFlightpath", "query_flightpath": "QueryFlightpath",
		"flightpath_observation": "FlightpathObservation", "flightpath_reference": "FlightpathReference",
		"settle_flightpath":         "SettleFlightpath",
		"adopt_flightpath_transfer": "AdoptFlightpathTransfer",
		"cast_spell":                "CastSpell", "cancel_spell": "CancelSpell", "break_control": "BreakControl",
	} {
		method, ok := actions.RPC(alias)
		if !ok || method.Path() != "/warcraftbuddy.actions.v1.BuddyActions/"+name || api.Services().ByName("BuddyActions").Methods().ByName(method.Descriptor().Name()) == nil {
			t.Fatal("manifest removed or rerouted a current RPC", alias)
		}
	}
	if actions.Manifest().ScriptAPI["activate_flightpath"].Effect != moddisc.EffectMutation {
		t.Fatal("potentially spending activation advertised as a read")
	}
	if actions.Manifest().ScriptAPI["settle_flightpath"].Effect != moddisc.EffectMutation {
		t.Fatal("booking settlement must be advertised as a mutation")
	}
	method, ok := actions.Packet(opcode.SMSGActivatetaxireply)
	if !ok || method.Path() != "/warcraftbuddy.actions.v1.BuddyActions/OnPacket" {
		t.Fatal("activation reply not subscribed to the native handler")
	}
}

func TestFlightpathActivationPublishedRouteReachesOneCompiledWrite(t *testing.T) {
	x := newFlightpathRig(t)
	x.query(t, "status")
	x.packet(t, opcode.SMSGTaxinodeStatus, flightpathStatusPayload(x.npc, 1))
	x.query(t, "discover")
	x.packet(t, opcode.SMSGShowtaxinodes, flightpathMenuPayload(x.npc, 12, 2, 12))
	view := x.observation(t)
	r := &actionsv1.FlightpathActivationRequest{SessionId: x.s.sessionID, SelfGuid: x.s.selfGUID,
		VisitId: 1, Token: x.token, NpcGuid: x.npc, SourceNode: 12, DestinationNode: 2,
		MenuRevision: view.Revision, ExpectedRevision: view.ActivationRevision,
		ExpectedCopperRevision: x.f.world.SelfCopperRevision, ExpectedCopper: x.f.world.SelfCopper, AcceptUnknownFare: true}
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	actions, ok := registry.Lookup("buddy-actions")
	if !ok {
		t.Fatal("missing Actions manifest")
	}
	method, ok := actions.RPC("activate_flightpath")
	if !ok {
		t.Fatal("missing reflected activation route")
	}
	input, output := dynamicpb.NewMessage(method.Descriptor().Input()), dynamicpb.NewMessage(method.Descriptor().Output())
	body, err := protojson.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := protojson.Unmarshal(body, input); err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(64 << 10)
	server := grpc.NewServer()
	actionsv1.RegisterBuddyActionsServer(server, x.s)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///activation-contract", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := conn.Invoke(ctx, method.Path(), input, output); err != nil {
		t.Fatal("published activation RPC did not route to its compiled primitive", err)
	}
	body, err = protojson.Marshal(output)
	var response actionsv1.FlightpathObservationResponse
	if err != nil || protojson.Unmarshal(body, &response) != nil {
		t.Fatal("activation response disagrees with its published descriptor", err)
	}
	op := response.Activation
	if op == nil || !op.Pending || !op.TransportWritten || op.TransportUnknown || op.ActivationAccepted || op.ReplyKnown ||
		op.LiveFareKnown || op.LiveRouteKnown || op.FlightStartedKnown || op.ArrivalKnown || !op.MoneyBeforeKnown ||
		op.MoneyBeforeCopper != r.ExpectedCopper || op.MoneyBeforeRevision != r.ExpectedCopperRevision ||
		op.SessionId != r.SessionId || op.SelfGuid != r.SelfGuid || op.VisitId != r.VisitId || op.NpcGuid != r.NpcGuid ||
		op.SourceNode != 12 || op.DestinationNode != 2 || op.MenuRevision != r.MenuRevision || response.ActivationRevision != op.Revision {
		t.Fatal("compiled activation receipt invented an outcome or lost its exact bindings", &response)
	}
	want := binary.LittleEndian.AppendUint64(nil, r.NpcGuid)
	want = binary.LittleEndian.AppendUint32(want, 12)
	want = binary.LittleEndian.AppendUint32(want, 2)
	if len(x.f.packets) != 3 || x.client.worldReads != 3 || x.client.sendCalls != 3 ||
		x.f.packets[2].Opcode != opcode.CMSGActivatetaxi || string(x.f.packets[2].Payload) != string(want) || x.s.state.Traveling || x.s.state.Moving {
		t.Fatal("activation did not issue exactly its one normal packet", x.f.packets)
	}
}

func TestFlightpathSettlementPublishedRouteReachesCompiledStateOnlyHandler(t *testing.T) {
	x := newFlightpathSettlementRig(t)
	flightpathSettlementReady(t, x)
	request := flightpathSettlementRequest(t, x)
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	module, ok := registry.Lookup("buddy-actions")
	if !ok {
		t.Fatal("missing Actions manifest")
	}
	method, ok := module.RPC("settle_flightpath")
	if !ok || method.Path() != actionsv1.BuddyActions_SettleFlightpath_FullMethodName ||
		method.Descriptor().Input().FullName() != "warcraftbuddy.actions.v1.SettleFlightpathRequest" ||
		method.Descriptor().Output().FullName() != "warcraftbuddy.actions.v1.FlightpathObservationResponse" {
		t.Fatal("settlement alias disagrees with compiled API", method)
	}
	listener := bufconn.Listen(64 << 10)
	server := grpc.NewServer()
	actionsv1.RegisterBuddyActionsServer(server, x.s)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///settlement-contract", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	before := len(x.f.packets)
	response := new(actionsv1.FlightpathObservationResponse)
	if err := conn.Invoke(ctx, method.Path(), request, response); err != nil {
		t.Fatal("published settlement RPC did not reach the native handler", err)
	}
	if !response.GetActivation().GetArrivalKnown() || response.GetActivation().GetPending() || len(x.f.packets) != before {
		t.Fatal("state-only settlement lost arrival proof or sent another booking", response, x.f.packets)
	}
}
