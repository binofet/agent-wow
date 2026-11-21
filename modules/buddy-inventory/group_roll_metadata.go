package buddyinventory

import (
	"context"
	"encoding/binary"
	"time"

	buddytrade "github.com/agent-wow/agent-wow/modules/buddy-trade"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

// Roll templates share Inventory's existing solicited-query cache and limits.
// Querying an offered entry establishes static facts only, never ownership or
// a vote. Normal State projections still enumerate only actual carried gear.
func (s *Service) rollItemMetadata(ctx context.Context, ids []uint32) []*tradev1.ItemDetails {
	s.mu.Lock()
	query := []uint32{}
	if !s.closed && s.session != nil && s.active == nil && ctx.Err() == nil && !s.now().Before(s.queryAfter) {
		for _, id := range ids {
			if id != 0 && id < 1<<31 && !s.queried[id] {
				query = append(query, id)
				if len(query) == 8 {
					break
				}
			}
		}
		if len(s.queried)+len(query) > 256 {
			query = nil
		}
		for _, id := range query {
			s.queried[id] = true // Reserve before the callback can deliver a reply.
		}
		if len(query) != 0 {
			s.queryAfter = s.now().Add(time.Second)
		}
	}
	s.mu.Unlock()
	for _, id := range query {
		s.sendMu.Lock()
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if !closed && ctx.Err() == nil {
			_, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: opcode.CMSGItemQuerySingle, Payload: binary.LittleEndian.AppendUint32(nil, id)})
			if err == nil {
				s.mu.Lock()
				s.state.TransportWrites++
				s.mu.Unlock()
			}
		}
		s.sendMu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]*tradev1.ItemDetails, 0, len(ids))
	for _, id := range ids {
		if item := s.rollItemTemplates[id]; item != nil {
			rows = append(rows, proto.Clone(item).(*tradev1.ItemDetails))
			continue
		}
		row := &tradev1.ItemDetails{ItemId: id}
		switch {
		case s.closed:
			row.Reason = "inventory_closing"
		case id == 0 || id >= 1<<31:
			row.Reason = "item_template_id_unsupported"
		case s.queried[id]:
			row.Pending, row.Reason = true, "waiting_for_complete_server_item_query"
		case len(s.queried) >= 256:
			row.Reason = "item_query_body_capacity_reached"
		default:
			row.Pending, row.Reason = true, "item_query_rate_limited_or_busy"
		}
		rows = append(rows, row)
	}
	return rows
}

// Called only under mu after the ordinary complete parser established a
// solicited entry. The shared public decoder proves every reflected property;
// Inventory does not infer comparison coverage from damage or armor values.
func (s *Service) observeRollItemMetadataLocked(data []byte) {
	item, err := buddytrade.DecodeItemTemplateQuery(data)
	if err != nil || !item.GetAvailable() || item.GetPending() || !s.queried[item.GetItemId()] {
		return
	}
	if s.rollItemTemplates == nil {
		s.rollItemTemplates = map[uint32]*tradev1.ItemDetails{}
	}
	if len(s.rollItemTemplates) < 256 || s.rollItemTemplates[item.ItemId] != nil {
		s.rollItemTemplates[item.ItemId] = proto.Clone(item).(*tradev1.ItemDetails)
	}
}
