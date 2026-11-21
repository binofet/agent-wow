package buddychat

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"

	chatv1 "github.com/agent-wow/agent-wow/modules/buddy-chat/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Source-shaped BuildChatPacket golden: WHISPER, Common, sender87, flags0,
// name length9/Lotharen+NUL, recipient4, text length6/hello+NUL, GM tag2.
const gmWhisperGolden = "0707000000570000000000000000000000090000004c6f74686172656e0004000000000000000600000068656c6c6f0002"

func gmFixture(t *testing.T) []byte {
	t.Helper()
	data, err := hex.DecodeString(gmWhisperGolden)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDecodeGMMessageChatRetainsActualSenderAndRecipient(t *testing.T) {
	data := gmFixture(t)
	for _, channel := range []chatv1.Channel{chatv1.Channel_WHISPER, chatv1.Channel_SAY, chatv1.Channel_PARTY, chatv1.Channel_PARTY_LEADER, chatv1.Channel_WHISPER_INFORM} {
		data[0] = byte(channel)
		got, err := decodeGMChat(data)
		if err != nil || got.Channel != channel || got.Language != 7 || got.SenderGuid != 87 || got.ReceiverGuid != 4 || got.Text != "hello" || got.ChatTag != 2 {
			t.Fatal(got, err)
		}
	}
	// A misleading display name cannot impersonate Joel's owner GUID.
	forged := append([]byte(nil), data[:17]...)
	forged = binary.LittleEndian.AppendUint32(forged, 5)
	forged = append(forged, []byte("Joel\x00")...)
	forged = append(forged, data[30:]...)
	got, err := decodeGMChat(forged)
	if err != nil || got.SenderGuid != 87 {
		t.Fatal("sender name replaced authenticated GUID", got, err)
	}
}

func TestGMChatMalformedPacketsAreLocalAndOrdinaryChatStillWorks(t *testing.T) {
	valid := gmFixture(t)
	s := NewService(nil, 4)
	for end := 0; end < len(valid); end++ {
		if _, err := decodeGMChat(valid[:end]); err == nil {
			t.Fatal("accepted truncated GM packet", end)
		}
		if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGGMMessagechat, Payload: valid[:end]}); err != nil {
			t.Fatal("malformed GM chat terminated session", err)
		}
	}
	for _, change := range []func([]byte) []byte{
		func(p []byte) []byte { binary.LittleEndian.PutUint32(p[17:], 0); return p },
		func(p []byte) []byte { binary.LittleEndian.PutUint32(p[17:], 0xffffffff); return p },
		func(p []byte) []byte { p[29] = 'x'; return p },
		func(p []byte) []byte { p[22] = 0; return p },
		func(p []byte) []byte { p[21] = 0xff; return p },
		func(p []byte) []byte { return append(p, 0) },
	} {
		bad := change(append([]byte(nil), valid...))
		if _, err := decodeGMChat(bad); err == nil {
			t.Fatal("accepted malformed GM sender/text")
		}
	}
	for _, packet := range []*modv1.WorldPacket{
		{Opcode: opcode.SMSGGMMessagechat, Payload: valid},
		{Opcode: opcode.SMSGMessagechat, Payload: makePlayerChat(chatv1.Channel_WHISPER, 3, 4, "ordinary owner")},
	} {
		if _, err := s.OnPacket(context.Background(), packet); err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.Poll(context.Background(), &chatv1.PollRequest{})
	if err != nil || len(result.Messages) != 2 || result.Messages[0].SenderGuid != 87 || result.Messages[0].Self || result.Messages[1].SenderGuid != 3 {
		t.Fatal(result, err)
	}
	// GM addon messages remain excluded from ordinary conversation, and are not
	// misrouted to owner-addon ingestion through the different packet layout.
	addon := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(addon[1:], 0xffffffff)
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGGMMessagechat, Payload: addon}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Poll(context.Background(), &chatv1.PollRequest{})
	if len(after.Messages) != 2 {
		t.Fatal("GM addon became conversational authority", after)
	}
}
