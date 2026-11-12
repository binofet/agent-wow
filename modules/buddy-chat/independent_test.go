package buddychat

import (
	"context"
	"encoding/hex"
	"testing"

	chatv1 "github.com/agent-wow/agent-wow/modules/buddy-chat/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
)

func TestIndependentChatNeverGrantsOwnerAddonAuthority(t *testing.T) {
	// Literal build12340 PARTY/ADDON packet, authenticated sender GUID3.
	golden, err := hex.DecodeString("02ffffffff030000000000000000000000000000000000000036000000574251554553543109317c6131623263336434653566367c317c317c317c327c4c3d333b513d372c303b513d31382c313b433d33330000")
	if err != nil {
		t.Fatal(err)
	}
	for _, self := range []uint64{3, 6} {
		s := NewIndependentService(nil, self)
		for _, p := range [][]byte{golden, ownerFeedPacket("1|b1b2c3d4e5f6|2|1|2|1|L=3"), ownerFeedPacket("malformed")} {
			if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGMessagechat, Payload: p}); err != nil {
				t.Fatal(err)
			}
		}
		feed := ownerState(t, s)
		if feed.Available || feed.Assembling || feed.Revision != 0 || feed.ReceivedPackets != 0 || len(feed.Quests) != 0 || len(feed.CompletedQuestIds) != 0 || feed.BodySessionId == "" {
			t.Fatal("independent character accepted owner feed authority", feed)
		}
		poll, err := s.Poll(context.Background(), &chatv1.PollRequest{})
		if err != nil || len(poll.Messages) != 0 || poll.LatestSequence != 0 || poll.IgnoredPackets != 3 || poll.MalformedPackets != 0 {
			t.Fatal("owner addon entered ordinary conversation", poll, err)
		}
	}
}

func TestIndependentChatRetainsOrdinaryChatSelfIdentityAndEmotes(t *testing.T) {
	recorder := &sessionRecorder{}
	conn := connectTestGRPC(t, func(server *grpc.Server) { modv1.RegisterSessionServer(server, recorder) })
	s := NewIndependentService(modv1.NewSessionClient(conn), 3)
	ctx := context.Background()
	sent, err := s.Send(ctx, &chatv1.SendRequest{Channel: chatv1.Channel_PARTY, Language: chatv1.Language_ORCISH, Text: "Ready"})
	if err != nil || !sent.TransportWritten {
		t.Fatal(sent, err)
	}
	emote, err := s.Emote(ctx, &chatv1.EmoteRequest{Emote: chatv1.Emote_WAVE, TargetGuid: 6})
	if err != nil || !emote.TransportWritten {
		t.Fatal(emote, err)
	}
	for _, sender := range []uint64{3, 6} {
		_, err := s.OnPacket(ctx, &modv1.WorldPacket{Opcode: opcode.SMSGMessagechat, Payload: makePlayerChat(chatv1.Channel_PARTY, sender, 0, "ordinary chat")})
		if err != nil {
			t.Fatal(err)
		}
	}
	poll, err := s.Poll(ctx, &chatv1.PollRequest{})
	if err != nil || len(poll.Messages) != 2 || !poll.Messages[0].Self || poll.Messages[1].Self || poll.Messages[1].SenderGuid != 6 {
		t.Fatal("independent chat lost actual self identity", poll, err)
	}
	if len(recorder.packets) != 2 || recorder.packets[0].Opcode != opcode.CMSGMessagechat || recorder.packets[1].Opcode != opcode.CMSGTextEmote {
		t.Fatal("ordinary independent chat or emotes lost transport", recorder.packets)
	}
}
