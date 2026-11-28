package buddyworld

import (
	"context"
	"encoding/binary"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/gameobjectquery"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The response has an entry and no GUID or request ID. At most one query per
// entry is outstanding. Static knowledge is independent of the write verdict.
type shipTemplateQuery struct {
	guid, self, visit, birth uint64
	entry                    uint32
	scope                    *modv1.TransportScope
	write                    string
	reply                    *gameobjectquery.Template
	received                 bool
	revision                 uint64
	at                       time.Time
}

func (q *shipTemplateQuery) snapshot(p *observed, now time.Time) *worldv1.MovingTransportTemplate {
	m := q.reply
	if m == nil || q.write == "dispatching" || q.write == "not_dispatched" || q.birth != p.transportBirth ||
		p.fields[objectEntry] != m.Entry || p.known[8] && p.fields[8] != m.DisplayID {
		return nil
	}
	return &worldv1.MovingTransportTemplate{Entry: m.Entry, DisplayId: m.DisplayID, TaxiPathId: m.Data[0], MoveSpeed: m.Data[1], AccelerationRate: m.Data[2],
		Guid: q.guid, SelfGuid: q.self, VisitRevision: q.visit, Revision: q.revision, AgeMs: age(now, q.at)}
}

func (s *Service) shipSource(guid, self, visit, birth uint64, entry uint32) *observed {
	p, body := s.state.gameObjects[guid], &s.state.players[0]
	if p == nil || birth == 0 || p.transportBirth != birth || entry == 0 || p.fields[objectEntry] != entry ||
		!movingTransport(p) || !p.present || !p.mapKnown || p.mapID != body.mapID || self != body.guid || visit != s.state.visitRevision ||
		!scopeMatchesSelf(s.motion.scope, *body, visit) {
		return nil
	}
	return p
}

func (s *Service) QueryMovingTransportTemplate(ctx context.Context, req *worldv1.QueryMovingTransportTemplateRequest) (*worldv1.QueryMovingTransportTemplateResponse, error) {
	s.mu.Lock()
	if req == nil || req.SessionId == "" || req.SessionId != s.combat.sessionID {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "current session required")
	}
	p := s.shipSource(req.Guid, req.SelfGuid, req.VisitRevision, req.ObjectRevision, req.Entry)
	if p == nil || s.session == nil {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "current observed moving transport and body required")
	}
	response := func(q *shipTemplateQuery, p *observed) *worldv1.QueryMovingTransportTemplateResponse {
		return &worldv1.QueryMovingTransportTemplateResponse{Phase: q.write, TransportWritten: q.write == "transport_written", Template: q.snapshot(p, s.now())}
	}
	if q := p.shipQuery; q != nil {
		if !sameMotionVisit(q.scope, s.motion.scope) {
			s.mu.Unlock()
			return nil, status.Error(codes.FailedPrecondition, "moving transport observation belongs to a previous physical visit")
		}
		out := response(q, p)
		s.mu.Unlock()
		return out, nil
	}
	for entry, previous := range s.shipQueries {
		if !sameMotionVisit(previous.scope, s.motion.scope) {
			delete(s.shipQueries, entry)
		}
	}
	if s.shipQueries[req.Entry] != nil || len(s.shipQueries) >= maxGameObjects {
		s.mu.Unlock()
		return &worldv1.QueryMovingTransportTemplateResponse{Phase: "outstanding_template_query"}, nil
	}
	q := &shipTemplateQuery{guid: req.Guid, self: req.SelfGuid, visit: req.VisitRevision, entry: req.Entry,
		birth: p.transportBirth, scope: cloneMotionScope(s.motion.scope), write: "dispatching"}
	if s.shipQueries == nil {
		s.shipQueries = make(map[uint32]*shipTemplateQuery)
	}
	s.shipQueries[q.entry], p.shipQuery = q, q
	session := s.session
	s.mu.Unlock()

	_, err := session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: opcode.CMSGGameobjectQuery, Payload: gameobjectquery.Request(q.entry, q.guid)})

	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case err == nil:
		q.write = "transport_written"
	case modcb.PacketWasNotDispatched(err):
		q.write, q.reply = "not_dispatched", nil
	default:
		q.write = "write_outcome_unknown"
	}
	if (q.received || q.write == "not_dispatched") && s.shipQueries[q.entry] == q {
		delete(s.shipQueries, q.entry)
	}
	p = s.shipSource(q.guid, q.self, q.visit, q.birth, q.entry)
	if p == nil || p.shipQuery != q || !sameMotionVisit(s.motion.scope, q.scope) {
		return &worldv1.QueryMovingTransportTemplateResponse{Phase: "source_changed", TransportWritten: err == nil}, nil
	}
	return response(q, p), nil
}

func (s *Service) observeShipTemplateLocked(packet *modv1.WorldPacket, now time.Time) {
	data := packet.Payload
	if len(data) < 4 {
		s.ignored++
		return
	}
	raw := binary.LittleEndian.Uint32(data)
	q := s.shipQueries[raw&0x7fffffff]
	if q == nil || q.received || !sameMotionVisit(q.scope, packet.TransportScope) || packet.TransportScope.MotionRevision < q.scope.MotionRevision {
		s.ignored++
		return
	}
	missing := raw&0x80000000 != 0
	m, err := gameobjectquery.Decode(data)
	if missing && len(data) != 4 || !missing && (err != nil || m.Type != 15 || m.DisplayID == 0 || m.Data[0] == 0) {
		s.malformed++
		s.lastError = "invalid moving transport template response"
		return
	}
	p := s.shipSource(q.guid, q.self, q.visit, q.birth, q.entry)
	if p != nil && !missing && p.known[8] && p.fields[8] != m.DisplayID {
		s.ignored++
		return
	}
	q.received = true
	if q.write != "dispatching" {
		delete(s.shipQueries, q.entry)
	}
	if p == nil || p.shipQuery != q || !sameMotionVisit(s.motion.scope, q.scope) {
		s.ignored++
		return
	}
	q.revision, q.at = s.state.nextTransportSource(packet.Opcode).revision, now
	if !missing {
		q.reply = &m
	}
	s.observed++
}
