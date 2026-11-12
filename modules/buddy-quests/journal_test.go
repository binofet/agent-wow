package buddyquests

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Fixed public-producer header length:26 scalar words,4 guaranteed item pairs,
// 6 choice pairs,3 arrays of5 reputation words,4 POI words. No parser helpers.
func journalFixture(id uint32) []byte {
	p := make([]byte, 260)
	binary.LittleEndian.PutUint32(p, id)
	binary.LittleEndian.PutUint32(p[4:], 2)
	binary.LittleEndian.PutUint32(p[8:], 1)
	binary.LittleEndian.PutUint32(p[12:], 1)
	for _, text := range []string{"Kobold Camp Cleanup", "Kill eight Kobold Vermin.", "The kobolds are a threat.", "Northshire", "Return to the marshal."} {
		p = append(p, []byte(text)...)
		p = append(p, 0)
	}
	for _, word := range []uint32{6, 8, 0, 0, 0x8000002a, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 123, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0} {
		p = binary.LittleEndian.AppendUint32(p, word)
	}
	for _, text := range []string{"Kobold Vermin slain", "Inspect the crate", "", ""} {
		p = append(p, []byte(text)...)
		p = append(p, 0)
	}
	return p
}
func TestJournalTemplateTextsAndStructuredTargets(t *testing.T) {
	entry, err := decodeQuestTemplate(journalFixture(7))
	if err != nil {
		t.Fatal(err)
	}
	if !entry.TemplateKnown || entry.QuestId != 7 || entry.Title != "Kobold Camp Cleanup" || entry.Objectives != "Kill eight Kobold Vermin." || entry.Details != "The kobolds are a threat." || entry.AreaDescription != "Northshire" || len(entry.Targets) != 3 {
		t.Fatal(entry)
	}
	if goal := entry.Targets[0]; goal.Kind != "CREATURE" || goal.TargetId != 6 || goal.RequiredCount != 8 || goal.Text != "Kobold Vermin slain" || goal.CurrentKnown {
		t.Fatal(goal)
	}
	if goal := entry.Targets[1]; goal.Kind != "GAMEOBJECT" || goal.TargetId != 42 || goal.RequiredCount != 1 || goal.Text != "Inspect the crate" {
		t.Fatal(goal)
	}
	if goal := entry.Targets[2]; goal.Kind != "ITEM" || goal.TargetId != 123 || goal.RequiredCount != 2 || goal.CurrentKnown {
		t.Fatal(goal)
	}
}
func TestJournalTemplateRejectsEveryTruncationAndMalformedTail(t *testing.T) {
	p := journalFixture(7)
	for n := 0; n < len(p); n++ {
		if _, err := decodeQuestTemplate(p[:n]); err == nil {
			t.Fatalf("accepted truncation%d", n)
		}
	}
	if _, err := decodeQuestTemplate(append(p, 0)); err == nil {
		t.Fatal("accepted trailing bytes")
	}
	for _, change := range []func([]byte){func(p []byte) { binary.LittleEndian.PutUint32(p, 0) }, func(p []byte) { binary.LittleEndian.PutUint32(p[4:], 9) }, func(p []byte) { p[260] = 0xff }} {
		bad := append([]byte(nil), p...)
		change(bad)
		if _, err := decodeQuestTemplate(bad); err == nil {
			t.Fatal("accepted bad template")
		}
	}
	missing := binary.LittleEndian.AppendUint32(nil, 0x80000007)
	entry, err := decodeQuestTemplate(missing)
	if err != nil || entry.TemplateKnown || entry.QuestId != 7 || entry.Failure != "server_template_unavailable" {
		t.Fatal(entry, err)
	}
}
func TestJournalProgressKeepsCollectionUnknownAndJoinsServerCounters(t *testing.T) {
	s := NewService(nil, "journal-session", 1)
	template, err := decodeQuestTemplate(journalFixture(7))
	if err != nil {
		t.Fatal(err)
	}
	s.journal.templates = map[uint32]*questsv1.QuestJournalEntry{7: template}
	w := &worldv1.SnapshotResponse{SelfQuestLogKnown: true, SelfQuestLogRevision: 12}
	for i := range 25 {
		w.SelfQuestLog = append(w.SelfQuestLog, &worldv1.QuestLogEntry{Slot: uint32(i)})
	}
	w.SelfQuestLog[0] = &worldv1.QuestLogEntry{QuestId: 7, ObjectiveCounts: []uint32{3, 1, 0, 0}, State: 0}
	result := s.journalSnapshotLocked(w)
	entry := result.Entries[0]
	if entry.Targets[0].CurrentCount != 3 || !entry.Targets[0].CurrentKnown || entry.Targets[1].CurrentCount != 1 || entry.Targets[2].CurrentKnown || !entry.LogStateKnown {
		t.Fatal(entry)
	}
	w.SelfQuestLog[0].Complete = true
	entry = s.journalSnapshotLocked(w).Entries[0]
	if !entry.Complete || !entry.Targets[2].CurrentKnown || entry.Targets[2].CurrentCount != 2 {
		t.Fatal(entry)
	}
	if template.Targets[0].CurrentKnown {
		t.Fatal("snapshot mutated cached template")
	}
}
func TestJournalHandlerOnlyCachesRequestedOwnTemplatesAndPreservesGoodData(t *testing.T) {
	s := NewService(nil, "session", 1)
	s.journal.queries = map[uint32]journalQuery{7: {at: time.Now()}}
	packet := &modv1.WorldPacket{Opcode: opcode.SMSGQuestQueryResponse, Payload: journalFixture(7)}
	if !s.handleJournalPacket(packet) || s.journal.templates[7] == nil {
		t.Fatal("expected requested template")
	}
	packet.Payload = journalFixture(8)
	s.handleJournalPacket(packet)
	if s.journal.templates[8] != nil {
		t.Fatal("cached unsolicited template")
	}
	packet.Payload = journalFixture(7)[:20]
	s.handleJournalPacket(packet)
	if s.journal.templates[7].Title != "Kobold Camp Cleanup" || s.state.MalformedPackets != 1 {
		t.Fatal(s.state)
	}
	if s.handleJournalPacket(&modv1.WorldPacket{Opcode: opcode.SMSGGossipMessage}) {
		t.Fatal("hijacked other quest packet")
	}
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGQuestQueryResponse, Payload: journalFixture(7)})
}
func FuzzQuestTemplate(f *testing.F) {
	f.Add(journalFixture(7))
	f.Fuzz(func(t *testing.T, p []byte) { _, _ = decodeQuestTemplate(p) })
}
