package buddyquests

import (
	"context"
	"encoding/binary"
	"math"
	"time"

	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

type journalQuery struct {
	at       time.Time
	received bool
}
type journalRuntime struct {
	templates map[uint32]*questsv1.QuestJournalEntry
	queries   map[uint32]journalQuery
}

// Quest::InitializeQueryData in AzerothCore Quests/QuestDef.cpp is the packet
// producer. This is the ordinary client template query; it cannot accept,
// abandon, complete, or otherwise change a quest.
func decodeQuestTemplate(data []byte) (*questsv1.QuestJournalEntry, error) {
	if len(data) < 4 || len(data) > 65536 {
		return nil, malformed
	}
	id := binary.LittleEndian.Uint32(data)
	if id&0x80000000 != 0 {
		if len(data) != 4 || id&0x7fffffff == 0 {
			return nil, malformed
		}
		return &questsv1.QuestJournalEntry{QuestId: id & 0x7fffffff, Failure: "server_template_unavailable"}, nil
	}
	r := reader{data: data}
	header := [26]uint32{}
	for i := range header {
		header[i] = r.u32()
	}
	if id == 0 || header[1] > 2 {
		return nil, malformed
	}
	r.take(4*8 + 6*8 + 5*4*3) // guaranteed rewards, choices, reputation reward arrays
	r.u32()                   // POI continent
	x, y := math.Float32frombits(r.u32()), math.Float32frombits(r.u32())
	r.u32() // POI option
	if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) || math.IsNaN(float64(y)) || math.IsInf(float64(y), 0) {
		return nil, malformed
	}
	entry := &questsv1.QuestJournalEntry{QuestId: id, Title: r.text(512), Objectives: r.text(8192), Details: r.text(16384), AreaDescription: r.text(8192), TemplateKnown: true}
	r.text(8192) // completed description; not a server completion receipt
	for i := uint32(0); i < 4; i++ {
		target, count := r.u32(), r.u32()
		r.u32()
		r.u32() // dropped source item and unused source count
		if target == 0 && count != 0 {
			r.err = malformed
		}
		if target != 0 {
			kind := "CREATURE"
			value := target
			if target&0x80000000 != 0 {
				kind = "GAMEOBJECT"
				value = target & 0x7fffffff
			}
			if value == 0 || value > math.MaxInt32 || count == 0 {
				r.err = malformed
			}
			entry.Targets = append(entry.Targets, &questsv1.QuestObjective{Kind: kind, Index: i, TargetId: int32(value), RequiredCount: count})
		}
	}
	for i := uint32(0); i < 6; i++ {
		item, count := r.u32(), r.u32()
		if item == 0 && count != 0 || item > math.MaxInt32 {
			r.err = malformed
		}
		if item != 0 {
			if count == 0 {
				r.err = malformed
			}
			entry.Targets = append(entry.Targets, &questsv1.QuestObjective{Kind: "ITEM", Index: i, TargetId: int32(item), RequiredCount: count})
		}
	}
	texts := [4]string{}
	for i := range texts {
		texts[i] = r.text(4096)
	}
	for _, target := range entry.Targets {
		if (target.Kind == "CREATURE" || target.Kind == "GAMEOBJECT") && target.Index < 4 {
			target.Text = texts[target.Index]
		}
	}
	for _, pair := range [][2]uint32{{header[7], header[8]}, {header[9], header[10]}} {
		if pair[0] > 0 && pair[0] <= math.MaxInt32 && int32(pair[1]) > 0 {
			entry.Targets = append(entry.Targets, &questsv1.QuestObjective{Kind: "REPUTATION", TargetId: int32(pair[0]), RequiredCount: pair[1]})
		}
	}
	if header[22] > 0 {
		entry.Targets = append(entry.Targets, &questsv1.QuestObjective{Kind: "PLAYER_KILLS", RequiredCount: header[22]})
	}
	if money := int32(header[13]); money < 0 {
		entry.Targets = append(entry.Targets, &questsv1.QuestObjective{Kind: "MONEY", RequiredCount: uint32(-int64(money))})
	}
	if entry.Title == "" {
		r.err = malformed
	}
	return entry, r.done()
}

func (s *Service) handleJournalPacket(packet *modv1.WorldPacket) bool {
	if packet == nil || packet.Opcode != opcode.SMSGQuestQueryResponse {
		return false
	}
	entry, err := decodeQuestTemplate(packet.Payload)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.state.MalformedPackets++
		return true
	}
	if s.journal.queries == nil {
		s.state.IgnoredPackets++
		return true
	}
	query, ok := s.journal.queries[entry.QuestId]
	if !ok || query.received {
		s.state.IgnoredPackets++
		return true
	}
	if s.journal.templates == nil {
		s.journal.templates = map[uint32]*questsv1.QuestJournalEntry{}
	}
	if len(s.journal.templates) >= 128 && s.journal.templates[entry.QuestId] == nil {
		s.state.IgnoredPackets++
		return true
	}
	s.journal.templates[entry.QuestId] = entry
	query.received = true
	s.journal.queries[entry.QuestId] = query
	return true
}

func (s *Service) journalSnapshotLocked(w *worldv1.SnapshotResponse) *questsv1.JournalResponse {
	result := &questsv1.JournalResponse{SessionId: s.state.SessionId, QuestLogRevision: w.SelfQuestLogRevision}
	if !w.SelfQuestLogKnown || len(w.SelfQuestLog) != 25 {
		return result
	}
	for _, slot := range w.SelfQuestLog {
		if slot == nil || slot.QuestId == 0 {
			continue
		}
		entry := &questsv1.QuestJournalEntry{QuestId: slot.QuestId, Failure: "template_not_received"}
		if cached := s.journal.templates[slot.QuestId]; cached != nil {
			entry = proto.Clone(cached).(*questsv1.QuestJournalEntry)
		}
		entry.Slot = slot.Slot
		entry.LogStateKnown = true
		entry.LogState = slot.State
		entry.Complete = slot.Complete
		entry.Failed = slot.Failed
		entry.ObjectiveCounts = append([]uint32(nil), slot.ObjectiveCounts...)
		entry.ExpireTime = slot.ExpireTime
		if a := s.state.LastAcceptance; a != nil && a.QuestId == slot.QuestId && a.Status == questsv1.AcceptanceStatus_CONFIRMED_IN_LOG {
			entry.ObservedQuestgiverGuid = a.NpcGuid
		}
		if o := s.state.TurnInOffer; o != nil && o.QuestId == slot.QuestId {
			entry.ObservedRewardNpcGuid = o.NpcGuid
		}
		for _, goal := range entry.Targets {
			if (goal.Kind == "CREATURE" || goal.Kind == "GAMEOBJECT") && goal.Index < 4 && len(slot.ObjectiveCounts) == 4 {
				goal.CurrentKnown = true
				goal.CurrentCount = slot.ObjectiveCounts[goal.Index]
			}
			// Collection objectives cannot be counted from the backpack alone:
			// equipped bag contents are outside the current inventory scope.
			if slot.Complete {
				goal.CurrentKnown = true
				goal.CurrentCount = goal.RequiredCount
			}
		}
		result.Entries = append(result.Entries, entry)
	}
	return result
}

func (s *Service) Journal(ctx context.Context, r *questsv1.JournalRequest) (*questsv1.JournalResponse, error) {
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	w, err := s.world(call)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.journal.queries == nil {
		s.journal.queries = map[uint32]journalQuery{}
	}
	if s.journal.templates == nil {
		s.journal.templates = map[uint32]*questsv1.QuestJournalEntry{}
	}
	active := map[uint32]bool{}
	if w.SelfQuestLogKnown && len(w.SelfQuestLog) == 25 {
		for _, slot := range w.SelfQuestLog {
			if slot != nil && slot.QuestId != 0 {
				active[slot.QuestId] = true
			}
		}
	}
	// Keep only own active quest templates. A log mutation never authorizes a
	// model-supplied arbitrary query or an unbounded historical cache.
	for id := range s.journal.queries {
		if !active[id] {
			delete(s.journal.queries, id)
			delete(s.journal.templates, id)
		}
	}
	queries := []uint32{}
	for id := range active {
		q, queried := s.journal.queries[id]
		if !queried || (r != nil && r.Refresh && !q.received && s.now().Sub(q.at) >= 5*time.Second) {
			if len(queries) >= 8 {
				break
			}
			s.journal.queries[id] = journalQuery{at: s.now()}
			queries = append(queries, id)
		}
	}
	s.mu.Unlock()
	for _, id := range queries {
		if err := s.send(call, opcode.CMSGQuestQuery, binary.LittleEndian.AppendUint32(nil, id)); err != nil {
			s.mu.Lock()
			s.journal.templates[id] = &questsv1.QuestJournalEntry{QuestId: id, Failure: "template_query_write_unconfirmed"}
			s.mu.Unlock()
			break
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.journalSnapshotLocked(w), nil
}
