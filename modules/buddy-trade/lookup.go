package buddytrade

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"time"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type itemLookupRuntime struct {
	items     map[uint32]*tradev1.ItemDetails
	requested map[uint32]time.Time
	next      time.Time
}

// LookupItems requests static template facts, never ownership, proficiency or
// permission to equip. At most six new queries share a one-second rate limit;
// the existing queried set bounds all item-query traffic to 128 IDs per body.
func (s *Service) LookupItems(ctx context.Context, r *tradev1.LookupItemsRequest) (*tradev1.LookupItemsResponse, error) {
	if r == nil || len(r.ItemIds) == 0 || len(r.ItemIds) > 6 {
		return nil, status.Error(codes.InvalidArgument, "one to six unique item IDs required")
	}
	seen := make(map[uint32]bool, 6)
	for _, id := range r.ItemIds {
		if id == 0 || id >= 0x80000000 || seen[id] {
			return nil, status.Error(codes.InvalidArgument, "unique positive item IDs below 2^31 required")
		}
		seen[id] = true
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	s.mu.Lock()
	if s.lookup.items == nil {
		s.lookup.items = map[uint32]*tradev1.ItemDetails{}
		s.lookup.requested = map[uint32]time.Time{}
	}
	now := s.now()
	query := make([]uint32, 0, 6)
	for _, id := range r.ItemIds {
		if s.lookup.items[id] != nil {
			continue
		}
		if s.queried[id] {
			if _, exists := s.lookup.requested[id]; !exists {
				s.lookup.requested[id] = now
			}
			continue
		}
		if len(s.queried) >= 128 || now.Before(s.lookup.next) {
			continue
		}
		// Reserve before the write, including synchronous response callbacks.
		s.queried[id] = true
		s.lookup.requested[id] = now
		query = append(query, id)
	}
	if len(query) != 0 {
		s.lookup.next = now.Add(time.Second)
	}
	s.mu.Unlock()
	for _, id := range query {
		var err error
		if s.session == nil {
			err = status.Error(codes.FailedPrecondition, "session unavailable")
		} else {
			_, err = s.session.SendPacket(call, &modv1.SendPacketRequest{Opcode: opcode.CMSGItemQuerySingle, Payload: u32(id)})
		}
		s.mu.Lock()
		if err == nil {
			s.state.TransportWrites++
		} else if s.lookup.items[id] == nil {
			// A late complete response can still establish the actual facts.
			s.lookup.items[id] = &tradev1.ItemDetails{ItemId: id, Reason: "item_query_transport_unconfirmed"}
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := &tradev1.LookupItemsResponse{SessionId: s.state.SessionId}
	for _, id := range r.ItemIds {
		item := s.lookup.items[id]
		if item != nil {
			result.Items = append(result.Items, proto.Clone(item).(*tradev1.ItemDetails))
			continue
		}
		item = &tradev1.ItemDetails{ItemId: id}
		if s.queried[id] {
			item.Pending = s.now().Sub(s.lookup.requested[id]) < 5*time.Second
			item.Reason = "waiting_for_complete_server_item_query"
			if !item.Pending {
				item.Reason = "server_item_query_unconfirmed"
			}
		} else if len(s.queried) >= 128 {
			item.Reason = "item_query_body_capacity_reached"
		} else {
			item.Pending = true
			item.Reason = "item_query_rate_limited"
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

// Called only under Service.mu by the normal server packet callback. Static
// facts from unsolicited, malformed or incomplete responses never enter cache.
func (s *Service) observeItemLookupLocked(payload []byte) {
	item, err := parseLookupItem(payload)
	if err != nil || !s.queried[item.GetItemId()] {
		return
	}
	if s.lookup.items == nil {
		s.lookup.items = map[uint32]*tradev1.ItemDetails{}
		s.lookup.requested = map[uint32]time.Time{}
	}
	if len(s.lookup.items) < 128 || s.lookup.items[item.ItemId] != nil {
		s.lookup.items[item.ItemId] = item
	}
}

// ItemHandler::HandleItemQuerySingleOpcode supplies all fields in this order:
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/ItemHandler.cpp
// The existing full metadata decoder validates names, counts and the exact
// trailing frame before these descriptive fields can be exposed.
func parseLookupItem(data []byte) (*tradev1.ItemDetails, error) {
	entry, name, err := parseItemName(data)
	if err != nil || entry == 0 {
		return nil, malformed
	}
	if len(data) == 4 {
		return &tradev1.ItemDetails{ItemId: entry, Reason: "server_item_unavailable"}, nil
	}
	meta, err := parseItemMetadata(data)
	if err != nil || !meta.known {
		return nil, malformed
	}
	at := 16 + len(name) + 4
	read := func() uint32 { v := binary.LittleEndian.Uint32(data[at:]); at += 4; return v }
	var prefix [22]uint32
	for i := range prefix {
		prefix[i] = read()
	}
	item := &tradev1.ItemDetails{ItemId: entry, Available: true, Reason: "server_item_template_observed", Name: name,
		ItemClass: meta.class, Subclass: meta.subclass, Quality: meta.quality, Flags: meta.flags, Flags2: meta.flags2, InventoryType: meta.inventoryType,
		BuyPrice: prefix[4], SellPrice: prefix[5], AllowableClass: prefix[7], AllowableRace: prefix[8], ItemLevel: prefix[9], RequiredLevel: prefix[10],
		RequiredSkill: prefix[11], RequiredSkillRank: prefix[12], RequiredSpell: prefix[13], RequiredHonorRank: prefix[14], RequiredCityRank: prefix[15], RequiredReputationFaction: prefix[16], RequiredReputationRank: prefix[17], HasSpell: meta.hasSpell}
	for range prefix[21] {
		item.Stats = append(item.Stats, &tradev1.ItemStat{Type: read(), Value: int32(read())})
	}
	scalingDistribution, scalingValue := read(), read()
	comparisonKnown := meta.class == 4 && meta.containerSlots == 0 && scalingDistribution == 0 && scalingValue == 0
	for range 2 {
		minimum, maximum, school := math.Float32frombits(read()), math.Float32frombits(read()), read()
		if math.IsNaN(float64(minimum)) || math.IsInf(float64(minimum), 0) || math.IsNaN(float64(maximum)) || math.IsInf(float64(maximum), 0) || minimum < 0 || maximum < minimum || school > 6 {
			return nil, malformed
		}
		item.Damages = append(item.Damages, &tradev1.WeaponDamage{Minimum: minimum, Maximum: maximum, School: school})
	}
	item.Armor = read()
	for range 6 {
		resistance := read()
		comparisonKnown = comparisonKnown && resistance == 0
	}
	item.DelayMs = read()
	ammo, rangeModifier := read(), read()
	comparisonKnown = comparisonKnown && ammo == 0 && rangeModifier == 0
	// The full metadata decoder already validated these exact fields. Preserve
	// ordinary BOE/BOP and durability, but do not compare hidden set/scaling,
	// random-affix/socket/block/zone/temporary/quest or armor-modifier benefits.
	at += 5 * 24
	bonding := read()
	end := bytes.IndexByte(data[at:], 0)
	at += end + 1
	var tail [29]uint32
	for i := range tail {
		tail[i] = read()
	}
	for _, i := range []int{0, 1, 2, 3, 4, 7, 8, 9, 10, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 25, 26, 27, 28} {
		comparisonKnown = comparisonKnown && tail[i] == 0
	}
	item.ArmorComparisonKnown = comparisonKnown && bonding <= 2 && prefix[18] == 0 && prefix[19] == 1
	return item, nil
}
