package buddychat

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	chatv1 "github.com/agent-wow/agent-wow/modules/buddy-chat/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type sessionRecorder struct {
	modv1.UnimplementedSessionServer
	mu      sync.Mutex
	packets []*modv1.SendPacketRequest
	err     error
}

func (s *sessionRecorder) SendPacket(_ context.Context, request *modv1.SendPacketRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.packets = append(s.packets, proto.Clone(request).(*modv1.SendPacketRequest))
	return &emptypb.Empty{}, s.err
}

func TestServiceGRPCSendAndPoll(t *testing.T) {
	// Exercise generated BuddyChat RPCs and the real Session callback client
	// over gRPC, without Docker or a game account.
	session := &sessionRecorder{}
	sessionConnection := connectTestGRPC(t, func(server *grpc.Server) { modv1.RegisterSessionServer(server, session) })
	service := NewService(modv1.NewSessionClient(sessionConnection), 42)
	connection := connectTestGRPC(t, func(server *grpc.Server) { chatv1.RegisterBuddyChatServer(server, service) })
	client := chatv1.NewBuddyChatClient(connection)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := client.Send(ctx, &chatv1.SendRequest{Channel: chatv1.Channel_SAY, Language: chatv1.Language_COMMON, Text: "hello"})
	if err != nil || !response.TransportWritten {
		t.Fatalf("send failed: %v, %v", response, err)
	}
	if len(session.packets) != 1 || session.packets[0].Opcode != uint32(opcode.CMSGMessagechat) || fmt.Sprintf("%x", session.packets[0].Payload) != "010000000700000068656c6c6f00" {
		t.Fatalf("wrong callback packet: %v", session.packets)
	}
	if _, err := client.Send(ctx, &chatv1.SendRequest{Channel: chatv1.Channel_WHISPER, Language: chatv1.Language_COMMON, Text: "unsupported"}); status.Code(err) != codes.InvalidArgument {
		t.Fatal("invalid send did not fail before callback", err)
	}
	if len(session.packets) != 1 {
		t.Fatal("invalid send reached the server callback")
	}
	for _, sender := range []uint64{42, 123} {
		_, err := client.OnPacket(ctx, &modv1.WorldPacket{Opcode: uint32(opcode.SMSGMessagechat), Payload: makePlayerChat(chatv1.Channel_SAY, sender, 0, "hello")})
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := client.Poll(ctx, &chatv1.PollRequest{})
	if err != nil || len(result.Messages) != 2 || !result.Messages[0].Self || result.Messages[1].Self || result.NextSequence != 2 {
		t.Fatalf("poll lost messages or echo identity: %v, %v", result, err)
	}
	if len(session.packets) != 1 {
		t.Fatal("receiving chat automatically sent a reply")
	}
}

func TestServiceWriteFailureIsNotRetriedOrReportedAsDelivery(t *testing.T) {
	session := &sessionRecorder{err: status.Error(codes.Unavailable, "transport closed")}
	connection := connectTestGRPC(t, func(server *grpc.Server) { modv1.RegisterSessionServer(server, session) })
	service := NewService(modv1.NewSessionClient(connection), 42)
	result, err := service.Send(context.Background(), &chatv1.SendRequest{Channel: chatv1.Channel_PARTY, Language: chatv1.Language_ORCISH, Text: "hello"})
	if status.Code(err) != codes.Unavailable || result != nil || len(session.packets) != 1 {
		t.Fatalf("write failure retried or misreported: %v, %v, %d writes", result, err, len(session.packets))
	}
}

func TestWhisperInformIsAlwaysAnOutboundEcho(t *testing.T) {
	service := NewService(nil, 1)
	// AzerothCore's WHISPER_INFORM deliberately identifies the recipient (Joel),
	// so comparing only sender GUID would turn our own whisper into fresh input.
	service.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: uint32(opcode.SMSGMessagechat), Payload: makePlayerChat(chatv1.Channel_WHISPER_INFORM, 3, 3, "private reply")})
	service.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: uint32(opcode.SMSGMessagechat), Payload: makePlayerChat(chatv1.Channel_WHISPER, 3, 3, "new question")})
	result, err := service.Poll(context.Background(), &chatv1.PollRequest{})
	if err != nil || len(result.Messages) != 2 || !result.Messages[0].Self || result.Messages[1].Self {
		t.Fatalf("whisper echo could cause a response loop: %v, %v", result, err)
	}
}

func TestPollSessionBindingSurvivesCounterCatchup(t *testing.T) {
	first, replacement := NewService(nil, 1), NewService(nil, 1)
	old, _ := first.Poll(context.Background(), &chatv1.PollRequest{})
	if old.SessionId == "" || old.SessionId == replacement.sessionID {
		t.Fatal("poll sessions are not independently identified")
	}
	for range 3 {
		replacement.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: uint32(opcode.SMSGMessagechat), Payload: makePlayerChat(chatv1.Channel_SAY, 3, 0, "new body")})
	}
	if _, err := replacement.Poll(context.Background(), &chatv1.PollRequest{SessionId: old.SessionId, AfterSequence: 1}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("stale cursor accepted after new ring caught up", err)
	}
	current, err := replacement.Poll(context.Background(), &chatv1.PollRequest{SessionId: replacement.sessionID, AfterSequence: 1})
	if err != nil || current.SessionId != replacement.sessionID || len(current.Messages) != 2 {
		t.Fatalf("valid session binding rejected: %v, %v", current, err)
	}
}

func TestServiceMalformedPacketsDoNotBreakPolling(t *testing.T) {
	service := NewService(nil, 42)
	for _, packet := range []*modv1.WorldPacket{
		nil,
		{Opcode: uint32(opcode.SMSGMessagechat), Payload: []byte{12}},
		{Opcode: uint32(opcode.SMSGMessagechat), Payload: []byte{1, 7}},
		{Opcode: uint32(opcode.SMSGMessagechat), Payload: makePlayerChat(chatv1.Channel_WHISPER, 123, 42, "survived")},
	} {
		if _, err := service.OnPacket(context.Background(), packet); err != nil {
			t.Fatal("packet error would terminate the owning gameplay session", err)
		}
	}
	result, err := service.Poll(context.Background(), &chatv1.PollRequest{})
	if err != nil || result.IgnoredPackets != 2 || result.MalformedPackets != 1 || len(result.Messages) != 1 || result.Messages[0].Text != "survived" {
		t.Fatalf("service did not recover from malformed chat: %v, %v", result, err)
	}
}

func TestServiceRingCursorOverflowAndPagination(t *testing.T) {
	service := NewService(nil, 42)
	for i := 1; i <= messageCapacity+3; i++ {
		service.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: uint32(opcode.SMSGMessagechat), Payload: makePlayerChat(chatv1.Channel_PARTY, 123, 0, fmt.Sprintf("message %d", i))})
	}
	first, err := service.Poll(context.Background(), &chatv1.PollRequest{Limit: 2})
	if err != nil || !first.CursorLost || first.OldestSequence != 4 || first.LatestSequence != 259 || first.NextSequence != 5 || len(first.Messages) != 2 || first.Messages[0].Text != "message 4" {
		t.Fatalf("ring overflow not reported correctly: %v, %v", first, err)
	}
	first.Messages[0].Text = "caller mutation"
	repeated, _ := service.Poll(context.Background(), &chatv1.PollRequest{AfterSequence: 3, Limit: 1})
	if repeated.CursorLost || repeated.Messages[0].Text != "message 4" {
		t.Fatal("poll mutated the ring or incorrectly marked a boundary cursor lost")
	}
	remaining, _ := service.Poll(context.Background(), &chatv1.PollRequest{AfterSequence: first.NextSequence, Limit: 256})
	if remaining.CursorLost || len(remaining.Messages) != 254 || remaining.Messages[0].Sequence != 6 || remaining.NextSequence != 259 {
		t.Fatalf("pagination skipped or duplicated messages: %v", remaining)
	}
	empty, _ := service.Poll(context.Background(), &chatv1.PollRequest{AfterSequence: 259})
	if len(empty.Messages) != 0 || empty.NextSequence != 259 {
		t.Fatal("caught-up cursor did not remain stable", empty)
	}
	for _, request := range []*chatv1.PollRequest{nil, {AfterSequence: 260}, {Limit: 257}} {
		if _, err := service.Poll(context.Background(), request); status.Code(err) != codes.InvalidArgument {
			t.Fatal("invalid poll accepted", err)
		}
	}
}

func TestServiceConcurrentReceiveAndPoll(t *testing.T) {
	service := NewService(nil, 42)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				service.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: uint32(opcode.SMSGMessagechat), Payload: makePlayerChat(chatv1.Channel_SAY, 123, 0, "hello")})
				if _, err := service.Poll(context.Background(), &chatv1.PollRequest{}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	workers.Wait()
	result, err := service.Poll(context.Background(), &chatv1.PollRequest{Limit: 256})
	if err != nil || result.LatestSequence != 800 || len(result.Messages) != messageCapacity || !result.CursorLost {
		t.Fatalf("concurrent delivery lost sequence accounting: %v, %v", result, err)
	}
}

func TestModuleManifestAndDescriptorLoad(t *testing.T) {
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := registry.Lookup("buddy-chat")
	if !ok {
		t.Fatal("module not discovered")
	}
	for _, alias := range []string{"send", "poll", "emote", "owner_feed", "management_requests", "publish_team_status"} {
		if _, ok := definition.RPC(alias); !ok {
			t.Fatalf("missing public method %q", alias)
		}
	}
	if _, ok := definition.Packet(uint16(opcode.SMSGMessagechat)); !ok {
		t.Fatal("chat packet handler contract was not installed")
	}
}

func connectTestGRPC(t *testing.T, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	register(server)
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	connection, err := grpc.NewClient("passthrough:///module-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxRetryRPCBufferSize(0)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	return connection
}
