package buddyactions

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/emptypb"
)

func selectionFixture(t *testing.T) (*Service, *fakeSession) {
	t.Helper()
	s, f, _ := rig(t)
	s.sessionID = "selection-body"
	return s, f
}

func selectionRequest(target uint64) *actionsv1.SelectTargetRequest {
	return &actionsv1.SelectTargetRequest{SessionId: "selection-body", SelfGuid: 1, TargetGuid: target}
}

func TestSelectTargetWritesOnlyExactObservedSelectionAndClear(t *testing.T) {
	const corpse = uint64(0xf101000000000001)
	for _, scenario := range []string{"owner", "nearby_player", "exact_creature", "corpse", "self", "clear"} {
		t.Run(scenario, func(t *testing.T) {
			s, f := selectionFixture(t)
			s.state.Following, s.state.Assisting, s.state.AutoAttacking, s.autoTarget = true, true, true, 9
			target := uint64(3)
			switch scenario {
			case "nearby_player":
				target = 42
				f.world.NearbyPlayers = []*worldv1.PlayerState{{Guid: target, Present: true, MapKnown: true}}
			case "exact_creature":
				target = sheepTarget
				f.lookupCreatures = map[uint64]*worldv1.CreatureState{target: {Guid: target, Present: true, MapKnown: true}}
			case "corpse":
				target = corpse
				f.world.PlayerCorpses = []*worldv1.CorpseState{{Guid: target, Present: true, MapKnown: true}}
			case "self":
				target = 1
			case "clear":
				target = 0
			}
			out, err := s.SelectTarget(context.Background(), selectionRequest(target))
			if err != nil || !out.GetTransportWritten() || out.GetSelectionObserved() || out.GetPhase() != "transport_written" ||
				out.GetSessionId() != s.sessionID || out.GetSelfGuid() != 1 || out.GetTargetGuid() != target || len(f.packets) != 1 {
				t.Fatal("normal selection must report transport without inventing a server echo", out, err, f.packets)
			}
			packet := f.packets[0]
			if packet.Opcode != opcode.CMSGSetSelection || len(packet.Payload) != 8 || binary.LittleEndian.Uint64(packet.Payload) != target {
				t.Fatal("selection did not use the exact normal LE64 packet", packet)
			}
			if !s.state.Following || !s.state.Assisting || !s.state.AutoAttacking || s.autoTarget != 9 || f.world.Self.TargetKnown {
				t.Fatal("UI selection changed combat intent or fabricated a World target", &s.state)
			}
		})
	}
}

func TestSelectTargetRejectsForeignBodyAndUnobservedOrForeignMapTargets(t *testing.T) {
	for _, scenario := range []string{"foreign_session", "foreign_self", "observed_other_body", "closed", "unknown_target", "other_map", "not_present", "game_object"} {
		t.Run(scenario, func(t *testing.T) {
			s, f := selectionFixture(t)
			r := selectionRequest(3)
			switch scenario {
			case "foreign_session":
				r.SessionId = "another-body"
			case "foreign_self":
				r.SelfGuid = 2
			case "observed_other_body":
				f.world.Self.Guid = 2
			case "closed":
				s.closed = true
			case "unknown_target":
				r.TargetGuid = 99
			case "other_map":
				f.world.Owner.MapId = 1
			case "not_present":
				f.world.Owner.Present = false
			case "game_object":
				r.TargetGuid = 0xf110000001000001
				f.world.QuestObjects = []*worldv1.GameObjectState{{Guid: r.TargetGuid, Present: true, MapKnown: true}}
			}
			if _, err := s.SelectTarget(context.Background(), r); status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
				t.Fatal("selection escaped its exact body and observed map", err, f.packets)
			}
		})
	}
}

type selectionEchoSession struct{ *fakeSession }

func (f *selectionEchoSession) SendPacket(ctx context.Context, request *modv1.SendPacketRequest, options ...grpc.CallOption) (*emptypb.Empty, error) {
	out, err := f.fakeSession.SendPacket(ctx, request, options...)
	if err == nil {
		f.mu.Lock()
		f.world.Self.TargetKnown, f.world.Self.TargetGuid = true, binary.LittleEndian.Uint64(request.Payload)
		f.world.ObservedPackets++
		f.mu.Unlock()
	}
	return out, err
}

func TestSelectTargetSeparatesObservedChangeFromTransportAndFailure(t *testing.T) {
	s, f := selectionFixture(t)
	f.world.Self.TargetKnown, f.world.Self.TargetGuid = true, 9
	s.session = &selectionEchoSession{fakeSession: f}
	out, err := s.SelectTarget(context.Background(), selectionRequest(3))
	if err != nil || !out.GetSelectionObserved() || !out.GetObservedTargetKnown() || out.GetObservedTargetGuid() != 3 || out.GetPhase() != "selection_observed" {
		t.Fatal("actual World selection change was not reflected", out, err)
	}
	out, err = s.SelectTarget(context.Background(), selectionRequest(0))
	if err != nil || !out.GetSelectionObserved() || out.GetObservedTargetGuid() != 0 {
		t.Fatal("actual cleared World selection was not reflected", out, err)
	}
	s.session = f
	f.failSend = true
	s.state.Following, s.state.Assisting = true, true
	out, err = s.SelectTarget(context.Background(), selectionRequest(3))
	if err != nil || out.GetTransportWritten() || out.GetSelectionObserved() || out.GetPhase() != "transport_unconfirmed" || !s.state.Following || !s.state.Assisting {
		t.Fatal("ambiguous selection transport changed unrelated gameplay intent", out, err, &s.state)
	}
}

func TestSelectTargetPublishedContractRoutesExactRPC(t *testing.T) {
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	actions, ok := registry.Lookup("buddy-actions")
	if !ok {
		t.Fatal("actions manifest missing")
	}
	method, ok := actions.RPC("select_target")
	if !ok || actions.Manifest().ScriptAPI["select_target"].Effect != moddisc.EffectMutation {
		t.Fatal("selection contract missing from release manifest")
	}
	s, f := selectionFixture(t)
	listener := bufconn.Listen(64 << 10)
	server := grpc.NewServer()
	actionsv1.RegisterBuddyActionsServer(server, s)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///selection", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	descriptor := method.Descriptor()
	input, output := dynamicpb.NewMessage(descriptor.Input()), dynamicpb.NewMessage(descriptor.Output())
	if err := protojson.Unmarshal([]byte(`{"sessionId":"selection-body","selfGuid":"1","targetGuid":"3"}`), input); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := conn.Invoke(ctx, method.Path(), input, output); err != nil {
		t.Fatal("published selection method cannot route to the binary", err)
	}
	field := descriptor.Output().Fields().ByJSONName("transportWritten")
	if field == nil || !output.Get(field).Bool() || countPackets(f, opcode.CMSGSetSelection) != 1 {
		t.Fatal("release contract did not report the exact selection write", output, f.packets)
	}
}
