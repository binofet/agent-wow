package buddychat

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	chatv1 "github.com/agent-wow/agent-wow/modules/buddy-chat/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func ownerFeedPacket(body string) []byte {
	p := makePlayerChat(chatv1.Channel_PARTY, 3, 0, ownerFeedPrefix+body)
	binary.LittleEndian.PutUint32(p[1:], 0xffffffff)
	return p
}
func ingestOwner(t *testing.T, s *Service, body string) {
	t.Helper()
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: uint32(opcode.SMSGMessagechat), Payload: ownerFeedPacket(body)}); err != nil {
		t.Fatal("malformed addon must not end game session", err)
	}
}
func ownerState(t *testing.T, s *Service) *chatv1.OwnerFeedResponse {
	t.Helper()
	out, err := s.OwnerFeed(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOwnerFeedGoldenServerPacketAndIsolationFromConversation(t *testing.T) {
	// Literal BuildChatPacket PARTY/ADDON frame: actual sender3, flags/receiver0,
	// combined prefix+TAB+message length54 including NUL, and trailing chat tag0.
	p, err := hex.DecodeString("02ffffffff030000000000000000000000000000000000000036000000574251554553543109317c6131623263336434653566367c317c317c317c327c4c3d333b513d372c303b513d31382c313b433d33330000")
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(nil, 5)
	if _, err = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: uint32(opcode.SMSGMessagechat), Payload: p}); err != nil {
		t.Fatal(err)
	}
	got := ownerState(t, s)
	if !got.Available || got.Level != 3 || got.Revision != 1 || got.AddonSession != "a1b2c3d4e5f6" || got.BodySessionId == "" || len(got.Quests) != 2 || got.Quests[1].QuestId != 18 || got.Quests[1].State != 1 || len(got.CompletedQuestIds) != 1 || got.CompletedQuestIds[0] != 33 {
		t.Fatal(got)
	}
	chat, err := s.Poll(context.Background(), &chatv1.PollRequest{})
	if err != nil || len(chat.Messages) != 0 || chat.LatestSequence != 0 || chat.IgnoredPackets != 0 || chat.MalformedPackets != 0 {
		t.Fatal("owner data leaked into conversation", chat, err)
	}
	got.Quests[0].QuestId = 999
	got.CompletedQuestIds[0] = 999
	if after := ownerState(t, s); after.Quests[0].QuestId != 7 || after.CompletedQuestIds[0] != 33 {
		t.Fatal("caller mutated stored feed", after)
	}
}

func TestOwnerFeedAuthenticatesHeaderNotSelfNamePrefixOrPayload(t *testing.T) {
	for _, mode := range []string{"self", "peer", "npc", "leader", "say", "language", "prefix", "receiver", "flags"} {
		t.Run(mode, func(t *testing.T) {
			s := NewService(nil, 5)
			p := ownerFeedPacket("1|a1b2c3d4e5f6|1|1|1|0|L=3")
			switch mode {
			case "self":
				binary.LittleEndian.PutUint64(p[5:], 5)
			case "peer":
				binary.LittleEndian.PutUint64(p[5:], 4)
			case "npc":
				binary.LittleEndian.PutUint64(p[5:], 0xf130000337000001)
			case "leader":
				p[0] = byte(chatv1.Channel_PARTY_LEADER)
			case "say":
				p[0] = byte(chatv1.Channel_SAY)
			case "language":
				binary.LittleEndian.PutUint32(p[1:], 7)
			case "prefix":
				p[29] = 'X'
			case "receiver":
				binary.LittleEndian.PutUint64(p[17:], 5)
			case "flags":
				binary.LittleEndian.PutUint32(p[13:], 1)
			}
			if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: uint32(opcode.SMSGMessagechat), Payload: p}); err != nil {
				t.Fatal(err)
			}
			if got := ownerState(t, s); got.Available {
				t.Fatal("untrusted addon identity accepted", got)
			}
		})
	}
}

func TestOwnerFeedMultipartCommitsAtomicallyAndAgesReports(t *testing.T) {
	s := NewService(nil, 1)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	part2 := "1|a1b2c3d4e5f6|1|2|2|2|Q=18,1;R=33,f1300000c400002c,3;A=18,f130000337000210,0;C=783"
	ingestOwner(t, s, part2)
	if got := ownerState(t, s); got.Available || !got.Assembling {
		t.Fatal("partial batch became a quest snapshot", got)
	}
	now = now.Add(3 * time.Second)
	ingestOwner(t, s, part2)
	ingestOwner(t, s, "1|a1b2c3d4e5f6|1|1|2|2|L=3;Q=7,0")
	now = now.Add(2 * time.Second)
	got := ownerState(t, s)
	if !got.Available || got.Assembling || len(got.Quests) != 2 || len(got.Rewarded) != 1 || len(got.Accepted) != 1 || got.AgeMs != 5000 || got.Rewarded[0].AgeMs != 8000 || got.Accepted[0].AgeMs != 5000 || got.IgnoredPackets != 1 {
		t.Fatal(got)
	}
	ingestOwner(t, s, "1|a1b2c3d4e5f6|1|1|1|0|L=80")
	if after := ownerState(t, s); after.Level != 3 || after.AgeMs != 5000 || after.Revision != 1 {
		t.Fatal("replay refreshed or rewrote a snapshot", after)
	}
}

func TestOwnerFeedMalformedAndTruncatedPacketsNeverReplaceGoodSnapshot(t *testing.T) {
	good := ownerFeedPacket("1|a1b2c3d4e5f6|2|1|1|0|L=80")
	for end := 0; end < len(good); end++ {
		s := NewService(nil, 1)
		ingestOwner(t, s, "1|a1b2c3d4e5f6|1|1|1|0|L=3")
		if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: uint32(opcode.SMSGMessagechat), Payload: good[:end]}); err != nil {
			t.Fatal(end, err)
		}
		if got := ownerState(t, s); !got.Available || got.Revision != 1 || got.Level != 3 {
			t.Fatal(end, got)
		}
	}
	for _, mutate := range []func([]byte) []byte{
		func(p []byte) []byte { return append(p, 0) }, func(p []byte) []byte { p[len(p)-2] = 'X'; return p }, func(p []byte) []byte { binary.LittleEndian.PutUint32(p[25:], 0xffffffff); return p }, func(p []byte) []byte { p[40] = 0; return p },
	} {
		s := NewService(nil, 1)
		ingestOwner(t, s, "1|a1b2c3d4e5f6|1|1|1|0|L=3")
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: uint32(opcode.SMSGMessagechat), Payload: mutate(append([]byte(nil), good...))})
		if got := ownerState(t, s); got.Revision != 1 || got.Level != 3 {
			t.Fatal(got)
		}
	}
}

func TestOwnerFeedRejectsMalformedGrammarCountsDuplicatesAndEventGUIDs(t *testing.T) {
	for _, body := range []string{
		"2|a1b2c3d4e5f6|1|1|1|0|L=3", "1|A1b2c3d4e5f6|1|1|1|0|L=3", "1|a1b2c3d4e5f6|0|1|1|0|L=3", "1|a1b2c3d4e5f6|01|1|1|0|L=3", "1|a1b2c3d4e5f6|2147483648|1|1|1|L=3", "1|a1b2c3d4e5f6|1|13|13|0|L=3", "1|a1b2c3d4e5f6|1|1|1|26|L=3",
		"1|a1b2c3d4e5f6|1|1|1|0|L=0", "1|a1b2c3d4e5f6|1|1|1|0|L=81", "1|a1b2c3d4e5f6|1|1|1|0|L=3;L=3", "1|a1b2c3d4e5f6|1|1|1|0|Q=7,0", "1|a1b2c3d4e5f6|1|1|1|0|L=3;Q=7,0", "1|a1b2c3d4e5f6|1|1|1|2|L=3;Q=7,0;Q=7,1", "1|a1b2c3d4e5f6|1|1|1|1|L=3;Q=7,3", "1|a1b2c3d4e5f6|1|1|1|0|L=3;C=7;C=7", "1|a1b2c3d4e5f6|1|1|1|0|L=3;R=33,0000000000000003,0", "1|a1b2c3d4e5f6|1|1|1|0|L=3;A=33,f1300000c400002c,901", "1|a1b2c3d4e5f6|1|1|1|0|L=3;X=7", "1|a1b2c3d4e5f6|1|1|1|0|L=3;", "1|a1b2c3d4e5f6|1|1|1|0|L=3\n",
	} {
		s := NewService(nil, 1)
		ingestOwner(t, s, body)
		if got := ownerState(t, s); got.Available || got.MalformedPackets != 1 {
			t.Fatal(body, got)
		}
	}
}

func TestOwnerFeedIncompleteBatchExpiresAndConflictingPartRollsBack(t *testing.T) {
	s := NewService(nil, 1)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	ingestOwner(t, s, "1|a1b2c3d4e5f6|1|1|1|0|L=3")
	ingestOwner(t, s, "1|a1b2c3d4e5f6|2|1|2|0|L=4")
	now = now.Add(10 * time.Second)
	if got := ownerState(t, s); got.Assembling || got.IncompleteBatches != 1 || got.Revision != 1 || got.Level != 3 {
		t.Fatal(got)
	}
	ingestOwner(t, s, "1|a1b2c3d4e5f6|3|1|2|0|L=4")
	ingestOwner(t, s, "1|a1b2c3d4e5f6|3|1|2|0|L=80")
	if got := ownerState(t, s); got.Assembling || got.MalformedPackets != 1 || got.Revision != 1 {
		t.Fatal(got)
	}
}

func TestOwnerFeedBoundsCompletedHistoryAssemblyAndRetiredSessions(t *testing.T) {
	s := NewService(nil, 1)
	for part := 1; part <= 12; part++ {
		ingestOwner(t, s, fmt.Sprintf("1|a1b2c3d4e5f6|1|%d|12|0|%s", part, strings.Repeat("L=3;", 48)+"L=3"))
		if s.ownerFeed.pending != nil && s.ownerFeed.pending.bytes > 2160 {
			t.Fatal("assembly grew without bound")
		}
	}
	if got := ownerState(t, s); got.Available || got.MalformedPackets == 0 {
		t.Fatal(got)
	}
	s = NewService(nil, 1)
	for i := 0; i < 12; i++ {
		ingestOwner(t, s, fmt.Sprintf("1|%012x|1|1|1|0|L=3", i+1))
	}
	if len(s.ownerFeed.retired) != 8 {
		t.Fatal("retired-session memory not bounded", s.ownerFeed.retired)
	}
	ingestOwner(t, s, "1|00000000000b|2|1|1|0|L=80")
	if got := ownerState(t, s); got.AddonSession != "00000000000c" || got.Level != 3 {
		t.Fatal("retired UI session replaced fresh state", got)
	}
	// Sixty-four positive completed IDs fit across parts; a65th is rejected.
	for count := 64; count <= 65; count++ {
		s = NewService(nil, 1)
		parts := []string{"L=3"}
		for i := 1; i <= count; i++ {
			if len(parts[len(parts)-1])+len(fmt.Sprint(i))+3 > 160 {
				parts = append(parts, "")
			}
			if parts[len(parts)-1] != "" {
				parts[len(parts)-1] += ";"
			}
			parts[len(parts)-1] += fmt.Sprintf("C=%d", i)
		}
		for i, p := range parts {
			ingestOwner(t, s, fmt.Sprintf("1|a1b2c3d4e5f6|1|%d|%d|0|%s", i+1, len(parts), p))
		}
		got := ownerState(t, s)
		if count == 64 && (!got.Available || len(got.CompletedQuestIds) != 64) || count == 65 && got.Available {
			t.Fatal(count, got)
		}
	}
}

func TestOwnerFeedGeneratedRPCAndConcurrentReceiveAreBounded(t *testing.T) {
	s := NewService(nil, 5)
	connection := connectTestGRPC(t, func(server *grpc.Server) { chatv1.RegisterBuddyChatServer(server, s) })
	client := chatv1.NewBuddyChatClient(connection)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 1; i <= 50; i++ {
				revision := worker*50 + i
				ingestOwner(t, s, fmt.Sprintf("1|a1b2c3d4e5f6|%d|1|1|1|L=3;Q=7,0", revision))
				if _, err := client.OwnerFeed(context.Background(), &emptypb.Empty{}); err != nil {
					t.Error(err)
				}
			}
		}(worker)
	}
	wg.Wait()
	got, err := client.OwnerFeed(context.Background(), &emptypb.Empty{})
	if err != nil || !got.Available || got.Revision != 200 || len(got.Quests) != 1 || got.BodySessionId != s.sessionID {
		t.Fatal(got, err)
	}
	fresh := ownerState(t, NewService(nil, 5))
	if fresh.Available || fresh.BodySessionId == got.BodySessionId {
		t.Fatal("body restart retained owner state", fresh)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.OwnerFeed(ctx, &emptypb.Empty{}); status.Code(err) != codes.Canceled {
		t.Fatal(err)
	}
}

func FuzzOwnerFeedBoundedAndNeverFatal(f *testing.F) {
	f.Add(ownerFeedPacket("1|a1b2c3d4e5f6|1|1|1|1|L=3;Q=7,0"))
	f.Add([]byte{})
	f.Add(ownerFeedPacket("1|a1b2c3d4e5f6|1|2|2|0|C=783"))
	f.Fuzz(func(t *testing.T, p []byte) {
		s := NewService(nil, 5)
		if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: uint32(opcode.SMSGMessagechat), Payload: p}); err != nil {
			t.Fatal(err)
		}
		got := ownerState(t, s)
		if len(got.Quests) > 25 || len(got.CompletedQuestIds) > 64 || len(got.Rewarded) > 8 || len(got.Accepted) > 8 || s.ownerFeed.pending != nil && s.ownerFeed.pending.bytes > 2160 {
			t.Fatal("unbounded owner feed", got)
		}
	})
}
