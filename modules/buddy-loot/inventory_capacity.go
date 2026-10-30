package buddyloot

import (
	"context"
	"math"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Capacity is a property of the complete private carried layout. Unknown
// containers never imply empty slots; specialty bags never imply general space.
// The initial ordinary-bag scope is the explicitly supported bag41599.
func ownKnownFreeSlot(w *worldv1.SnapshotResponse, self uint64) bool {
	if w == nil || w.Self == nil || w.Self.Guid != self || !w.Self.Present || !w.InventoryKnown || w.InventoryRevision == 0 || len(w.InventoryBags) != 5 {
		return false
	}
	validGUID := func(g uint64) bool { return g>>48 == 0x4000 && uint32(g) != 0 }
	seenBags := [5]bool{}
	guids := map[uint64]bool{}
	free, total := false, 0
	for _, b := range w.InventoryBags {
		if b == nil || b.Bag > 4 || seenBags[b.Bag] || !b.SlotKnown || !b.ContentsKnown || b.SlotCount > 36 || len(b.Slots) != int(b.SlotCount) {
			return false
		}
		seenBags[b.Bag] = true
		if b.Bag == 0 {
			if b.ContainerGuid != 0 || b.Entry != 0 || b.MetadataKnown || b.SlotCount != 16 {
				return false
			}
		} else if b.ContainerGuid == 0 {
			if b.Entry != 0 || b.MetadataKnown || b.SlotCount != 0 {
				return false
			}
		} else {
			if !validGUID(b.ContainerGuid) || guids[b.ContainerGuid] || !b.MetadataKnown || b.Entry == 0 || b.SlotCount == 0 || b.Entry == 41599 && b.SlotCount != 20 {
				return false
			}
			guids[b.ContainerGuid] = true
		}
		total += len(b.Slots)
		if total > 160 {
			return false
		}
		seen := [36]bool{}
		for _, item := range b.Slots {
			if item == nil || item.Bag != b.Bag || item.Slot >= b.SlotCount || seen[item.Slot] {
				return false
			}
			seen[item.Slot] = true
			if item.ItemGuid == 0 {
				if item.Entry != 0 || item.StackCount != 0 || item.MetadataKnown {
					return false
				}
				free = free || b.Bag == 0 || b.Entry == 41599
			} else {
				if !validGUID(item.ItemGuid) || guids[item.ItemGuid] || item.MetadataKnown && (item.Entry == 0 || item.StackCount == 0) || !item.MetadataKnown && (item.Entry != 0 || item.StackCount != 0) {
					return false
				}
				guids[item.ItemGuid] = true
			}
		}
	}
	// A container appearing later in bag order must not alias an earlier item.
	return free
}

func (s *Service) pauseForCapacity() {
	s.state.InventoryPaused = true
	if s.token != "" {
		s.pausedSource, s.pausedGather = s.pendingGUID(), s.questGather
	}
	s.state.Reason = "complete_carried_inventory_and_known_general_free_slot_required"
}

func stoppedLootLease(a *actionsv1.State) bool {
	return a != nil && a.InteractionOwner == "loot" && a.InteractionLeaseRemainingMs >= 1000 && !a.Moving && !a.MotionUnconfirmed && !a.CastPending && !a.AutoAttacking && !a.AutoAttackRequested && !a.AutoAttackUnconfirmed && a.Position != nil && a.Position.Known && a.Position.MapKnown
}

func (s *Service) openingCapacity(ctx context.Context) *worldv1.SnapshotResponse {
	baseline := &worldv1.SnapshotResponse{}
	if s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, baseline) != nil || !ownKnownFreeSlot(baseline, s.selfGUID) {
		s.pauseForCapacity()
		s.finish(ctx)
		return nil
	}
	// Status may refresh its own world observation. Renew the exact token next;
	// an identical owner label alone cannot prove this operation still owns it.
	preliminary := &actionsv1.State{}
	lease := &actionsv1.InteractionLease{}
	if ctx.Err() != nil || s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, preliminary) != nil || !stoppedLootLease(preliminary) || s.token == "" {
		s.state.Reason = "loot_exact_owned_stopped_lease_required_before_open"
		s.finish(ctx)
		return nil
	}
	// Retain progress already seen by this operation before a later response can
	// regress it. This is pure validation against the same bound quest and GUID.
	if s.questColdridge && !s.coldridgeGoal.context(baseline, preliminary.Position, s.selfGUID, s.pendingGUID()) {
		s.state.Reason = "coldridge_loot_scope_changed_before_final_observation"
		s.finish(ctx)
		return nil
	}
	if s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "loot", Token: s.token, LeaseMs: 15000}, lease) != nil || lease.Token != s.token || !stoppedLootLease(lease.State) {
		s.state.Reason = "loot_exact_owned_stopped_lease_required_before_open"
		s.finish(ctx)
		return nil
	}
	// This is the final RPC before the write. Check all capacity and authority
	// from this response, against the renewed stopped position. A newer actual
	// self position invalidates that position, even if the target remains near it.
	w := &worldv1.SnapshotResponse{}
	if s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, w) != nil || !ownKnownFreeSlot(w, s.selfGUID) {
		s.pauseForCapacity()
		s.finish(ctx)
		return nil
	}
	before, after := baseline.Self, w.Self
	if ctx.Err() != nil || !before.MapKnown || !before.PositionKnown || before.PositionRevision == 0 || !after.MapKnown || !after.PositionKnown || after.PositionRevision != before.PositionRevision || after.MapId != before.MapId || after.MapId != lease.State.Position.MapId || !s.openingContext(w, lease.State) {
		s.state.Reason = "loot_final_capacity_observation_source_or_context_changed"
		s.finish(ctx)
		return nil
	}
	return w
}

// Pure validation: no dependency read may supersede the final world response.
func (s *Service) openingContext(w *worldv1.SnapshotResponse, a *actionsv1.State) bool {
	if w.Self == nil || w.Self.Guid != s.selfGUID || !w.Self.Present || !w.Self.AliveKnown || !w.Self.Alive || !w.Self.UnitFlagsKnown || w.Self.UnitFlags&0x80000 != 0 {
		return false
	}
	if s.questGather {
		if !harvestContext(w, a, s.selfGUID, true) || !gatherObjectCurrent(w, a, s.pendingGUID()) {
			return false
		}
		for _, g := range w.QuestObjects {
			if g != nil && g.Guid == s.pendingGUID() && g.PositionRevision == s.gatherObjectRevision {
				return true
			}
		}
		return false
	}
	if s.questWolf && !wolfQuestContext(w, a.Position, s.selfGUID) || s.questColdridge && !s.coldridgeGoal.context(w, a.Position, s.selfGUID, s.pendingGUID()) {
		return false
	}
	if !s.questWolf && !s.questColdridge {
		p, owner := a.Position, w.Owner
		if !activeOwnerReady(w) || p == nil || !p.Known || !p.MapKnown || !owner.MapKnown || owner.MapId != p.MapId || !owner.PositionKnown {
			return false
		}
		distance := math.Pow(float64(p.X-owner.X), 2) + math.Pow(float64(p.Y-owner.Y), 2) + math.Pow(float64(p.Z-owner.Z), 2)
		if math.IsNaN(distance) || math.IsInf(distance, 0) || distance > 30*30 {
			return false
		}
	}
	if len(w.LootableCorpses) > 64 {
		return false
	}
	var corpse *worldv1.CreatureState
	for _, c := range w.LootableCorpses {
		if c != nil && c.Guid == s.pendingGUID() {
			if corpse != nil || s.questWolf && c.Entry != 299 || s.questColdridge && !s.coldridgeGoal.matchesCorpse(c) {
				return false
			}
			corpse = c
		}
	}
	return eligible(w.Self, a.Position, corpse)
}
