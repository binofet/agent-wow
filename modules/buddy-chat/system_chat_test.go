package buddychat

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	chatv1 "github.com/agent-wow/agent-wow/modules/buddy-chat/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func systemGolden(t *testing.T) []byte {
	t.Helper()
	// ChatHandler::SendSysMessage / BuildChatPacket default branch:
	// SYSTEM=0, UNIVERSAL=0, sender/flags/receiver=0, 18 bytes including NUL,
	// "Player not found.", tag=0. No player or GM sender-name prefix.
	p, err := hex.DecodeString("0000000000000000000000000000000000000000000000000012000000506c61796572206e6f7420666f756e642e0000")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func systemPacket(text string) []byte {
	p := makePlayerChat(chatv1.Channel_CHANNEL_UNSPECIFIED, 0, 0, text)
	binary.LittleEndian.PutUint32(p[1:], 0) // LANG_UNIVERSAL
	return p
}

func TestDecodeSystemChatUsesExistingReadOnlyRepresentation(t *testing.T) {
	got, err := decodeChat(systemGolden(t))
	if err != nil || got.Channel != chatv1.Channel_CHANNEL_UNSPECIFIED || got.Language != 0 || got.SenderGuid != 0 || got.ReceiverGuid != 0 || got.ChatTag != 0 || got.Text != "Player not found." {
		t.Fatalf("server-system fields changed: %v, %v", got, err)
	}
	for _, text := range []string{"Map: 349 X: 123.5 Y: -42.0 Z: 7.0", "|cffff0000Target introuvable é|r", strings.Repeat("é", 127) + "!"} {
		got, err := decodeChat(systemPacket(text))
		if err != nil || got.Text != text || got.SenderGuid != 0 {
			t.Fatalf("lost bounded server diagnostic: %v, %v", got, err)
		}
	}
	if _, err := decodeChat(systemPacket(strings.Repeat("é", 128))); err == nil {
		t.Fatal("system text escaped the existing 255-byte bound")
	}
}

func TestDecodeSystemChatRetainsIdentityAndEncodingGuards(t *testing.T) {
	valid := systemGolden(t)
	for end := 0; end < len(valid); end++ {
		if _, err := decodeChat(valid[:end]); err == nil {
			t.Fatalf("accepted truncated system packet at %d", end)
		}
	}
	for _, mutate := range []func([]byte) []byte{
		func(p []byte) []byte { binary.LittleEndian.PutUint32(p[25:], 0xffffffff); return p },
		func(p []byte) []byte { binary.LittleEndian.PutUint32(p[25:], 0); return p },
		func(p []byte) []byte { binary.LittleEndian.PutUint32(p[25:], 257); return p },
		func(p []byte) []byte { p[len(p)-2] = 'x'; return p },
		func(p []byte) []byte { p[30] = 0; return p },
		func(p []byte) []byte { p[29] = 0xff; return p },
		func(p []byte) []byte { return append(p, 0) },
	} {
		if _, err := decodeChat(mutate(append([]byte(nil), valid...))); err == nil || errors.Is(err, errUnsupportedChat) {
			t.Fatalf("malformed system packet was not classified malformed: %v", err)
		}
	}
	for _, mutate := range []func([]byte){
		func(p []byte) { binary.LittleEndian.PutUint64(p[5:], 3) },
		func(p []byte) { binary.LittleEndian.PutUint64(p[5:], 87) },
		func(p []byte) { binary.LittleEndian.PutUint32(p[1:], 7) },
		func(p []byte) { binary.LittleEndian.PutUint32(p[1:], 0xffffffff) },
	} {
		p := append([]byte(nil), valid...)
		mutate(p)
		if _, err := decodeChat(p); !errors.Is(err, errUnsupportedChat) {
			t.Fatalf("accepted a system message with player/addon identity: %v", err)
		}
	}
}

func TestSystemDiagnosticsPollWithoutSendOrOwnerAuthority(t *testing.T) {
	ctx := context.Background()
	session := &sessionRecorder{}
	callback := connectTestGRPC(t, func(server *grpc.Server) { modv1.RegisterSessionServer(server, session) })
	s := NewService(modv1.NewSessionClient(callback), 87)
	connection := connectTestGRPC(t, func(server *grpc.Server) { chatv1.RegisterBuddyChatServer(server, s) })
	client := chatv1.NewBuddyChatClient(connection)
	for _, p := range []*modv1.WorldPacket{
		{Opcode: opcode.SMSGMessagechat, Payload: []byte{0}}, // malformed, not session-fatal
		{Opcode: opcode.SMSGMessagechat, Payload: systemGolden(t)},
		{Opcode: opcode.SMSGMessagechat, Payload: systemPacket("WBTEAM1\tR|epoch12345678|abcdef123456|1|4|RESET")},
		{Opcode: opcode.SMSGMessagechat, Payload: systemPacket("WBQUEST1\t1|abcdef123456|1|1|1|0|L=40")},
		{Opcode: opcode.SMSGMessagechat, Payload: makePlayerChat(chatv1.Channel_WHISPER, 3, 87, "ordinary owner")},
		{Opcode: opcode.SMSGGMMessagechat, Payload: gmFixture(t)},
	} {
		if _, err := client.OnPacket(ctx, p); err != nil {
			t.Fatal("diagnostic terminated the session", err)
		}
	}
	result, err := client.Poll(ctx, &chatv1.PollRequest{})
	if err != nil || len(result.Messages) != 5 || result.MalformedPackets != 1 || result.NextSequence != 5 {
		t.Fatalf("system diagnostics not observable through existing Poll: %v, %v", result, err)
	}
	for _, m := range result.Messages[:3] {
		if m.SenderGuid != 0 || m.Channel != chatv1.Channel_CHANNEL_UNSPECIFIED || m.Self {
			t.Fatal("server diagnostic acquired player identity", m)
		}
	}
	if result.Messages[0].Text != "Player not found." || result.Messages[3].SenderGuid != 3 || result.Messages[3].Self || result.Messages[4].SenderGuid != 87 || !result.Messages[4].Self {
		t.Fatal("ordinary owner/GM whisper behavior changed", result)
	}
	feed, err := client.OwnerFeed(ctx, &emptypb.Empty{})
	if err != nil || feed.Available || feed.ReceivedPackets != 0 {
		t.Fatal("system text gained owner addon authority", feed, err)
	}
	management, err := client.ManagementRequests(ctx, &chatv1.ManagementRequestsRequest{})
	if err != nil || len(management.Requests) != 0 || management.LatestSequence != 0 {
		t.Fatal("system text became an owner management request", management, err)
	}
	for _, request := range []*chatv1.SendRequest{
		{Channel: chatv1.Channel_CHANNEL_UNSPECIFIED, Language: chatv1.Language_COMMON, Text: "server diagnostic"},
		{Channel: chatv1.Channel_SAY, Language: chatv1.Language_COMMON, Text: ".gps"},
		{Channel: chatv1.Channel_WHISPER, Language: chatv1.Language_COMMON, Recipient: "Joel", Text: ".groupsummon Arlen"},
	} {
		if _, err := client.Send(ctx, request); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("system or GM command Send was enabled: %v", err)
		}
	}
	if len(session.packets) != 0 {
		t.Fatal("read-only system observation caused a packet write", session.packets)
	}
}
