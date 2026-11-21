package buddyworld

import (
	"context"
	"io"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type combatJournal struct {
	sessionID string
	events    []*worldv1.CombatEvent
	gaps      []*worldv1.CombatEventGap
	durable   io.Writer
}

// ConfigureCombatJournal binds the read stream to the authenticated runtime
// session. The optional writer receives append-only JSONL with the full raw game
// packet body. A failed writer never stops gameplay or evicts readable events.
// Call once during module startup, before any packets are delivered.
func (s *Service) ConfigureCombatJournal(sessionID string, durable io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.combat.sessionID, s.combat.durable = sessionID, durable
}

func (s *Service) observeCombatPacketLocked(op uint32, payload []byte, at time.Time) {
	event := decodeCombatEvent(op, payload, s.state.players[0].guid)
	if event == nil {
		return
	}
	event.Sequence = uint64(len(s.combat.events)) + 1
	if ms := at.UnixMilli(); ms > 0 {
		event.ReceivedAtUnixMs = uint64(ms)
	}
	event.Opcode = op
	event.RawPayload = append([]byte(nil), payload...)
	s.combat.events = append(s.combat.events, event)
	// RAM retains the event even if the filesystem is full or unavailable.
	durable := false
	if s.combat.durable != nil {
		if data, err := protojson.Marshal(event); err == nil {
			data = append(data, '\n')
			n, err := s.combat.durable.Write(data)
			durable = err == nil && n == len(data)
		}
	}
	if !durable {
		gaps := s.combat.gaps
		if len(gaps) != 0 && gaps[len(gaps)-1].ThroughSequence+1 == event.Sequence {
			gaps[len(gaps)-1].ThroughSequence = event.Sequence
		} else {
			s.combat.gaps = append(gaps, &worldv1.CombatEventGap{FromSequence: event.Sequence, ThroughSequence: event.Sequence, Reason: "durability_unavailable"})
		}
	}
}

// CombatEvents pages actual received packet evidence, independently of snapshot
// sampling and gameplay policy. Reading never consumes or truncates the stream.
func (s *Service) CombatEvents(_ context.Context, request *worldv1.CombatEventsRequest) (*worldv1.CombatEventsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if request == nil || s.combat.sessionID == "" || request.SessionId != s.combat.sessionID {
		return nil, status.Error(codes.FailedPrecondition, "combat cursor requires the current authenticated session_id")
	}
	latest := uint64(len(s.combat.events))
	response := &worldv1.CombatEventsResponse{SessionId: s.combat.sessionID, SelfGuid: s.state.players[0].guid, LatestSequence: latest, NextSequence: request.AfterSequence}
	if latest != 0 {
		response.FirstSequence = 1
	}
	if request.AfterSequence > latest {
		response.NextSequence = latest
		response.Gaps = append(response.Gaps, &worldv1.CombatEventGap{FromSequence: latest + 1, ThroughSequence: request.AfterSequence, Reason: "cursor_ahead"})
		return response, nil
	}
	limit := uint64(request.Limit)
	if limit == 0 {
		limit = 128
	}
	end := request.AfterSequence + min(limit, latest-request.AfterSequence)
	for _, event := range s.combat.events[request.AfterSequence:end] {
		response.Events = append(response.Events, proto.Clone(event).(*worldv1.CombatEvent))
	}
	response.NextSequence, response.HasMore = end, end < latest
	for _, gap := range s.combat.gaps {
		if gap.ThroughSequence > request.AfterSequence && gap.FromSequence <= end {
			response.Gaps = append(response.Gaps, &worldv1.CombatEventGap{FromSequence: max(gap.FromSequence, request.AfterSequence+1), ThroughSequence: min(gap.ThroughSequence, end), Reason: gap.Reason})
		}
	}
	return response, nil
}
