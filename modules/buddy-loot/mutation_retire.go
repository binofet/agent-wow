package buddyloot

import (
	"context"
	"math"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
)

// Item pushes have no loot-source GUID. Once an item request is uncertain,
// never attribute a later push of that item ID to a different source in this
// body. Actual owned inventory remains independent observation, not a claim
// that the old request succeeded.
const maxUnconfirmedLootItems = 128

// A release response does not settle an outstanding mutation, including when
// disable or an ambiguous write led to it. Negative responses cannot retire
// the source. Callbacks only reserve state; Tick cleans up.
func (s *Service) reserveMutationQuarantine() {
	s.cleanup = ordinaryLootCleanup{source: s.pendingGUID(), timedOutPhase: s.state.Phase, releaseRequested: s.state.Phase == "waiting_release"}
	if s.retired == nil {
		s.retired = map[uint64]bool{}
	}
	if s.retired[s.cleanup.source] || len(s.retired) < maxRetiredLootSources {
		s.retired[s.cleanup.source] = true
	}
	s.state.Phase = "loot_mutation_unconfirmed"
	s.state.Reason = "loot_mutation_outcome_unconfirmed_no_retry"
}

func (s *Service) quarantineReleasedMutation(positive bool) {
	s.reserveMutationQuarantine()
	s.cleanup.releaseObserved, s.cleanup.releaseRejected = positive, !positive
	s.state.Reason = "loot_server_closed_with_unconfirmed_mutation"
	if !positive {
		s.state.Reason = "loot_server_release_rejected_with_unconfirmed_mutation"
	}
}

func completeOwnItemCounts(w *worldv1.SnapshotResponse, self uint64) (map[uint32]uint64, bool) {
	if w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.Present || !w.InventoryKnown || w.InventoryRevision == 0 || len(w.InventoryBags) != 5 {
		return nil, false
	}
	validGUID := func(g uint64) bool { return g>>48 == 0x4000 && uint32(g) != 0 }
	bags, guids := [5]bool{}, map[uint64]bool{}
	counts, total := map[uint32]uint64{}, 0
	for _, b := range w.InventoryBags {
		if b == nil || b.Bag > 4 || bags[b.Bag] || !b.SlotKnown || !b.ContentsKnown || b.SlotCount > 36 || len(b.Slots) != int(b.SlotCount) {
			return nil, false
		}
		bags[b.Bag] = true
		if b.Bag == 0 {
			if b.ContainerGuid != 0 || b.Entry != 0 || b.MetadataKnown || b.SlotCount != 16 {
				return nil, false
			}
		} else if b.ContainerGuid == 0 {
			if b.Entry != 0 || b.MetadataKnown || b.SlotCount != 0 {
				return nil, false
			}
		} else {
			if !validGUID(b.ContainerGuid) || guids[b.ContainerGuid] || !b.MetadataKnown || b.Entry == 0 || b.SlotCount == 0 || b.Entry == 41599 && b.SlotCount != 20 {
				return nil, false
			}
			guids[b.ContainerGuid] = true
		}
		total += len(b.Slots)
		if total > 160 {
			return nil, false
		}
		seen := [36]bool{}
		for _, slot := range b.Slots {
			if slot == nil || slot.Bag != b.Bag || slot.Slot >= b.SlotCount || seen[slot.Slot] {
				return nil, false
			}
			seen[slot.Slot] = true
			if slot.ItemGuid == 0 {
				if slot.Entry != 0 || slot.StackCount != 0 || slot.MetadataKnown {
					return nil, false
				}
			} else {
				if !validGUID(slot.ItemGuid) || guids[slot.ItemGuid] || !slot.MetadataKnown || slot.Entry == 0 || slot.StackCount == 0 {
					return nil, false
				}
				guids[slot.ItemGuid] = true
				counts[slot.Entry] += uint64(slot.StackCount)
			}
		}
	}
	return counts, true
}

func (s *Service) retireUnconfirmedItem(ctx context.Context) bool {
	if !s.cleanup.releaseObserved || s.cleanup.safeToContinue || !s.retired[s.cleanup.source] || !s.itemRequested || s.index >= len(s.items) || s.moneySent && !s.moneyCleared {
		return false
	}
	item := s.items[s.index]
	if item.id == 0 || !s.unconfirmedItems[item.id] && len(s.unconfirmedItems) >= maxUnconfirmedLootItems {
		return false
	}
	w := &worldv1.SnapshotResponse{}
	if ctx.Err() != nil || s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, w) != nil {
		return false
	}
	if _, known := completeOwnItemCounts(w, s.selfGUID); !known || w.InventoryRevision < s.inventoryWriteRevision {
		return false
	}
	p := w.Self
	if !p.AliveKnown || !p.Alive || !p.HealthKnown || p.Health == 0 || p.Health > p.MaxHealth || !p.MapKnown || !p.PositionKnown || math.IsNaN(float64(p.X)) || math.IsInf(float64(p.X), 0) || math.IsNaN(float64(p.Y)) || math.IsInf(float64(p.Y), 0) || math.IsNaN(float64(p.Z)) || math.IsInf(float64(p.Z), 0) || !p.UnitFlagsKnown || p.UnitFlags&0x80000 != 0 || ctx.Err() != nil {
		return false
	}
	if s.unconfirmedItems == nil {
		s.unconfirmedItems = map[uint32]bool{}
	}
	s.unconfirmedItems[item.id] = true
	s.state.Reason = "loot_item_outcome_unknown_source_retired_after_release_and_inventory"
	s.finish(ctx)
	s.cleanup = ordinaryLootCleanup{}
	return true
}

// A tainted item may advance only from a newer complete owned count plus the
// current window's slot removal. No GUID-less push or source-success receipt
// is created, and no request is ever resent.
func (s *Service) unconfirmedItemDelta(w *worldv1.SnapshotResponse) bool {
	if !s.itemRequested || !s.removed || s.index >= len(s.items) || !s.itemCountKnown || w == nil || w.InventoryRevision <= s.inventoryWriteRevision {
		return false
	}
	item := s.items[s.index]
	if !s.unconfirmedItems[item.id] {
		return false
	}
	counts, known := completeOwnItemCounts(w, s.selfGUID)
	return known && counts[item.id] >= s.itemCountBefore+uint64(item.count)
}
