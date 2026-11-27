package buddyloot

import (
	"context"
	"github.com/agent-wow/agent-wow/pkg/harvestguard"
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
	return harvestguard.CompleteOwnItemCounts(w, self)
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
