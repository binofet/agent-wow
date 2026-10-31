package buddyquests

import (
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"

	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Independently positioned golden: InitializeQueryData source header word19;
// the third word of each objective quartet is the dropped source item.
func protectedJournalFixture(id uint32) []byte {
	p := journalFixture(id)
	binary.LittleEndian.PutUint32(p[76:], 5000)
	offset := 260
	for range 5 {
		for p[offset] != 0 {
			offset++
		}
		offset++
	}
	binary.LittleEndian.PutUint32(p[offset+8:], 5001)
	return p
}

func TestJournalProtectsProvidedAndDroppedItemsWithoutInventingProgress(t *testing.T) {
	p := protectedJournalFixture(332)
	entry, err := decodeQuestTemplate(p)
	if err != nil || !entry.ItemProtectionKnown {
		t.Fatal(entry, err)
	}
	protected := map[int32]bool{}
	for _, target := range entry.Targets {
		if target.Kind == "SOURCE_ITEM" {
			protected[target.TargetId] = true
			if target.CurrentKnown || target.CurrentCount != 0 || target.RequiredCount != 0 {
				t.Fatal(target)
			}
		}
	}
	if len(protected) != 2 || !protected[5000] || !protected[5001] {
		t.Fatal(entry)
	}
	s, f := NewService(nil, "body", 1), fixture()
	s.journal.templates = map[uint32]*questsv1.QuestJournalEntry{332: entry}
	f.world.SelfQuestLog[0].QuestId, f.world.SelfQuestLog[0].Complete = 332, true
	for _, target := range s.journalSnapshotLocked(f.world).Entries[0].Targets {
		if target.Kind == "SOURCE_ITEM" && (target.CurrentKnown || target.CurrentCount != 0) {
			t.Fatal("source item became progress", target)
		}
	}
	for n := range len(p) {
		if entry, err := decodeQuestTemplate(p[:n]); err == nil || entry != nil && entry.ItemProtectionKnown {
			t.Fatalf("truncated protection at%d", n)
		}
	}
	binary.LittleEndian.PutUint32(p[76:], math.MaxUint32)
	if _, err := decodeQuestTemplate(p); err == nil {
		t.Fatal("accepted unrepresentable item")
	}
}

func TestJournalRequestedOwnerTemplatesStaySeparateAndSurviveOwnPolling(t *testing.T) {
	f := fixture()
	f.world.SelfQuestLog[0].QuestId = 47
	s := NewService(f, "body", 1)
	request := &questsv1.JournalRequest{RequestedQuestIds: []uint32{353, 332}}
	first, err := s.Journal(context.Background(), request)
	if err != nil || len(first.Entries) != 1 || first.Entries[0].QuestId != 47 || len(first.RequestedTemplates) != 2 {
		t.Fatal(first, err)
	}
	for _, id := range []uint32{332, 353} {
		if q := s.journal.queries[id]; q.at.IsZero() {
			t.Fatal("request wasn't reserved before write", id)
		}
	}
	_, _ = s.Journal(context.Background(), &questsv1.JournalRequest{})
	for _, id := range []uint32{332, 353} {
		s.handleJournalPacket(&modv1.WorldPacket{Opcode: opcode.SMSGQuestQueryResponse, Payload: protectedJournalFixture(id)})
	}
	second, err := s.Journal(context.Background(), request)
	if err != nil || len(second.Entries) != 1 || len(second.RequestedTemplates) != 2 {
		t.Fatal(second, err)
	}
	for _, entry := range second.RequestedTemplates {
		if !entry.TemplateKnown || !entry.ItemProtectionKnown || entry.LogStateKnown || entry.Complete || entry.ObservedQuestgiverGuid != 0 {
			t.Fatal(entry)
		}
	}
	for _, p := range f.packets {
		if p.Opcode != opcode.CMSGQuestQuery || len(p.Payload) != 4 {
			t.Fatal("static lookup mutated quests", p)
		}
	}
	if f.acquired != 0 || f.released != 0 || s.state.ActionInFlight || s.state.LastAcceptance != nil {
		t.Fatal("lookup borrowed interaction authority", s.state)
	}
	second.RequestedTemplates[0].Targets[0].TargetId = 999
	third, _ := s.Journal(context.Background(), request)
	if third.RequestedTemplates[0].Targets[0].TargetId == 999 {
		t.Fatal("lookup exposed mutable cache")
	}
}

func TestJournalStaticLookupInputCacheAndQueryRateAreBounded(t *testing.T) {
	f := fixture()
	s := NewService(f, "body", 1)
	clock := time.Unix(100, 0)
	s.now = func() time.Time { return clock }
	for _, ids := range [][]uint32{{0}, {1, 1}, {math.MaxUint32}, make([]uint32, 26)} {
		if _, err := s.Journal(context.Background(), &questsv1.JournalRequest{RequestedQuestIds: ids}); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	if len(f.packets) != 0 {
		t.Fatal("invalid IDs queried")
	}
	ids := make([]uint32, 25)
	for i := range ids {
		ids[i] = uint32(300 + i)
	}
	for range 4 {
		_, _ = s.Journal(context.Background(), &questsv1.JournalRequest{RequestedQuestIds: ids})
	}
	if len(f.packets) != 8 {
		t.Fatal("per-second query cap", len(f.packets))
	}
	clock = clock.Add(time.Second)
	_, _ = s.Journal(context.Background(), &questsv1.JournalRequest{RequestedQuestIds: ids})
	if len(f.packets) != 16 {
		t.Fatal("next bounded batch missing", len(f.packets))
	}
	for i := range ids {
		ids[i] += 100
	}
	_, _ = s.Journal(context.Background(), &questsv1.JournalRequest{RequestedQuestIds: ids})
	if len(s.journal.requested) != 25 || len(s.journal.queries) > 25 {
		t.Fatal("lookup leaked historical cache")
	}
	s.handleJournalPacket(&modv1.WorldPacket{Opcode: opcode.SMSGQuestQueryResponse, Payload: protectedJournalFixture(300)})
	if s.journal.templates[300] != nil {
		t.Fatal("retired lookup accepted stale packet")
	}
}
