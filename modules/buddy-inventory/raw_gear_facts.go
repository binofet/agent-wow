package buddyinventory

import (
	"context"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// RawGearFacts reads the existing cache only. It neither refreshes observations
// nor queries metadata, and cached revisions do not establish current freshness.
// Consumers must bind these facts to their own current body and world frame.
func (s *Service) RawGearFacts(_ context.Context, _ *emptypb.Empty) (*inventoryv1.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cached := s.state
	out := &inventoryv1.State{
		SessionId: cached.SessionId, SelfGuid: cached.SelfGuid, Revision: cached.Revision,
		InventoryKnown: cached.InventoryKnown, InventoryRevision: cached.InventoryRevision,
		InventoryBags: cached.InventoryBags, EquipmentKnown: cached.EquipmentKnown,
		EquipmentRevision: cached.EquipmentRevision, Equipment: cached.Equipment,
		Closing: cached.Closing,
	}
	for _, row := range cached.Items {
		if row == nil {
			out.Items = append(out.Items, nil)
			continue
		}
		out.Items = append(out.Items, &inventoryv1.ItemEligibility{
			Item: row.Item, MetadataKnown: row.MetadataKnown, GearFacts: row.GearFacts,
		})
	}
	if facts := cached.GearContext; facts != nil {
		out.GearContext = &inventoryv1.GearContext{
			ClassKnown: facts.ClassKnown, ClassId: facts.ClassId,
			RaceKnown: facts.RaceKnown, RaceId: facts.RaceId,
			LevelKnown: facts.LevelKnown, Level: facts.Level, Templates: facts.Templates,
		}
	}
	// Clone while holding mu: no returned nested message, map, or slice aliases
	// the cache. Policy eligibility and operation receipts are not projected.
	return proto.Clone(out).(*inventoryv1.State), nil
}
