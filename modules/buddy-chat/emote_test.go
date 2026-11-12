package buddychat

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"

	chatv1 "github.com/agent-wow/agent-wow/modules/buddy-chat/api"
	"github.com/agent-wow/agent-wow/pkg/emotecatalog"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestEncodeEmoteWireLayout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		emote  chatv1.Emote
		target uint64
		want   string
	}{
		{"untargeted wave", chatv1.Emote_WAVE, 0, "65000000000000000000000000000000"},
		{"bow to player", chatv1.Emote_BOW, 123, "11000000000000007b00000000000000"},
		{"cheer with realm GUID", chatv1.Emote_CHEER, 0x0000123455667788, "15000000000000008877665534120000"},
		{"numeric dance", chatv1.Emote(34), 0, "22000000000000000000000000000000"},
		{"numeric laugh to player", chatv1.Emote(60), 123, "3c000000000000007b00000000000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := encodeEmote(&chatv1.EmoteRequest{Emote: tc.emote, TargetGuid: tc.target})
			if err != nil || hex.EncodeToString(payload) != tc.want {
				t.Fatalf("wrong CMSG_TEXT_EMOTE payload: %x, %v; want %s", payload, err, tc.want)
			}
		})
	}
}

func TestEncodeEmoteRejectsUnknownActionsAndInvalidTargets(t *testing.T) {
	for _, request := range []*chatv1.EmoteRequest{
		nil,
		{},
		{Emote: chatv1.Emote(65535)},
		{Emote: chatv1.Emote(-1)},
		{Emote: chatv1.Emote(999)},
		{Emote: chatv1.Emote_WAVE, TargetGuid: 0xf130000000000001},
		{Emote: chatv1.Emote_WAVE, TargetGuid: 1 << 48},
		{Emote: chatv1.Emote_WAVE, TargetGuid: 1 << 32},
		{Emote: chatv1.Emote_WAVE, TargetGuid: ^uint64(0)},
	} {
		if _, err := encodeEmote(request); err == nil {
			t.Errorf("accepted unsupported emote or invalid target: %v", request)
		}
	}
}

func TestEncodeEveryCatalogTextEmote(t *testing.T) {
	for _, entry := range emotecatalog.All().Emotes {
		payload, err := encodeEmote(&chatv1.EmoteRequest{Emote: chatv1.Emote(entry.ID), TargetGuid: 123})
		if err != nil || len(payload) != 16 || binary.LittleEndian.Uint32(payload) != entry.ID || binary.LittleEndian.Uint32(payload[4:]) != 0 || binary.LittleEndian.Uint64(payload[8:]) != 123 {
			t.Fatalf("catalog emote %s/%d did not retain normal wire layout: %x, %v", entry.Name, entry.ID, payload, err)
		}
	}
}

func TestEmotesCatalogActualSessionReadOnlyAndDetached(t *testing.T) {
	session := &sessionRecorder{}
	sessionConnection := connectTestGRPC(t, func(server *grpc.Server) { modv1.RegisterSessionServer(server, session) })
	service := NewService(modv1.NewSessionClient(sessionConnection), 42)
	connection := connectTestGRPC(t, func(server *grpc.Server) { chatv1.RegisterBuddyChatServer(server, service) })
	client := chatv1.NewBuddyChatClient(connection)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := client.Emotes(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	poll, err := client.Poll(ctx, &chatv1.PollRequest{})
	if err != nil || got.SessionId == "" || got.SessionId != poll.SessionId || got.SelfGuid != 42 || got.Revision != emotecatalog.Revision() || len(got.Emotes) != len(emotecatalog.All().Emotes) || len(session.packets) != 0 || poll.LatestSequence != 0 {
		t.Fatal("catalog lost body/schema identity or changed gameplay/chat state", got, poll, err)
	}
	for i, entry := range emotecatalog.All().Emotes {
		if got.Emotes[i].Id != entry.ID || got.Emotes[i].Name != entry.Name {
			t.Fatal("installed metadata differs from accepted native catalog", got.Emotes[i], entry)
		}
	}
	direct, err := service.Emotes(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	direct.Emotes[0].Name = "forged"
	again, _ := service.Emotes(ctx, &emptypb.Empty{})
	other, _ := NewService(nil, 87).Emotes(ctx, &emptypb.Empty{})
	if again.Emotes[0].Name != "AGREE" || other.SessionId == got.SessionId || other.SelfGuid != 87 || other.Revision != got.Revision || len(session.packets) != 0 {
		t.Fatal("catalog was mutable or confused runtime identity with contract revision")
	}
	cancel()
	if result, err := service.Emotes(ctx, &emptypb.Empty{}); result != nil || status.Code(err) != codes.Canceled || len(session.packets) != 0 {
		t.Fatal("canceled metadata read changed gameplay", result, err)
	}
}

func TestServiceGRPCEmoteUsesSessionCallback(t *testing.T) {
	session := &sessionRecorder{}
	sessionConnection := connectTestGRPC(t, func(server *grpc.Server) { modv1.RegisterSessionServer(server, session) })
	service := NewService(modv1.NewSessionClient(sessionConnection), 42)
	connection := connectTestGRPC(t, func(server *grpc.Server) { chatv1.RegisterBuddyChatServer(server, service) })
	client := chatv1.NewBuddyChatClient(connection)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := client.Emote(ctx, &chatv1.EmoteRequest{Emote: chatv1.Emote_WAVE, TargetGuid: 123})
	if err != nil || !result.TransportWritten {
		t.Fatalf("emote callback failed: %v, %v", result, err)
	}
	if len(session.packets) != 1 || session.packets[0].Opcode != uint32(opcode.CMSGTextEmote) || hex.EncodeToString(session.packets[0].Payload) != "65000000000000007b00000000000000" {
		t.Fatalf("wrong callback packet for wave: %v", session.packets)
	}
	// Protobuf preserves valid numeric enum values even without a symbolic name.
	result, err = client.Emote(ctx, &chatv1.EmoteRequest{Emote: chatv1.Emote(34)})
	if err != nil || !result.TransportWritten || len(session.packets) != 2 || session.packets[1].Opcode != uint32(opcode.CMSGTextEmote) || hex.EncodeToString(session.packets[1].Payload) != "22000000000000000000000000000000" {
		t.Fatalf("numeric DANCE lost its ordinary session callback: %v, %v", result, err)
	}
	for _, request := range []*chatv1.EmoteRequest{
		{Emote: chatv1.Emote(999)},
		{Emote: chatv1.Emote_WAVE, TargetGuid: 0xf130000000000001},
	} {
		if _, err := client.Emote(ctx, request); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid emote did not fail before callback: %v", err)
		}
	}
	if len(session.packets) != 2 {
		t.Fatal("invalid emote generated a gameplay packet")
	}
	poll, err := client.Poll(ctx, &chatv1.PollRequest{})
	if err != nil || len(poll.Messages) != 0 || poll.LatestSequence != 0 || len(session.packets) != 2 {
		t.Fatalf("explicit emote changed chat history or generated extra actions: %v, %v", poll, err)
	}
}

func TestServiceEmoteFailureAndCancellationDoNotRetry(t *testing.T) {
	session := &sessionRecorder{err: status.Error(codes.Unavailable, "transport closed")}
	connection := connectTestGRPC(t, func(server *grpc.Server) { modv1.RegisterSessionServer(server, session) })
	service := NewService(modv1.NewSessionClient(connection), 42)
	request := &chatv1.EmoteRequest{Emote: chatv1.Emote_WAVE}
	result, err := service.Emote(context.Background(), request)
	if status.Code(err) != codes.Unavailable || result != nil || len(session.packets) != 1 {
		t.Fatalf("failed emote was retried or misreported: %v, %v, %d writes", result, err, len(session.packets))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Emote(ctx, request); status.Code(err) != codes.Canceled || len(session.packets) != 1 {
		t.Fatalf("canceled emote generated a packet: %v", err)
	}
	if _, err := NewService(nil, 42).Emote(context.Background(), request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("emote without a session callback was accepted", err)
	}
}
