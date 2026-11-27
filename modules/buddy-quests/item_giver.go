package buddyquests

import (
	"context"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/gamecatalog"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type itemGiverBinding struct {
	visit, inventoryRevision, containerGUID, guid uint64
	bag, slot, entry, count, questID, mapID       uint32
	repeatable                                    bool
}
type itemGiverOffer struct {
	binding  itemGiverBinding
	revision uint64
}

type itemQuestReservation struct {
	guid, visit, baselineLogRevision, rejectedFullLogRevision uint64
	rewarded                                                  bool
}

// A quest-identified realm failure may arrive after the write callback was
// lost. A World read begun after that packet must still see its original visit.
type itemQuestFailure struct {
	acceptance *questsv1.Acceptance
	visit      uint64
	reason     uint32
}

func (s *Service) itemQuestReservedLocked(binding itemGiverBinding) bool {
	prior, ok := s.reservedItemQuests[binding.questID]
	if prior.rejectedFullLogRevision != 0 && s.state.QuestLogKnown &&
		s.state.QuestLogRevision > prior.rejectedFullLogRevision && s.inLogLocked(0) {
		return false // A fresh offer can use the newly observed empty slot.
	}
	return ok && (!binding.repeatable || !prior.rewarded || prior.guid == binding.guid)
}

func itemGiverGUID(guid uint64) bool { return guid>>48 == 0x4000 && guid&0xffffffffffff != 0 }

// A GUID is not ownership. Require the whole coherent carried layout and the
// exact individually observed item, including equipped bag contents.
func ownedQuestStarter(w *worldv1.SnapshotResponse, guid uint64) (itemGiverBinding, error) {
	fail := func() (itemGiverBinding, error) {
		return itemGiverBinding{}, status.Error(codes.FailedPrecondition, "eligible owned manual quest starter and complete carried inventory required")
	}
	if w.GetVisitRevision() == 0 || !w.GetSelf().GetLevelKnown() || w.Self.Level == 0 || !w.Self.MapKnown ||
		!w.InventoryKnown || w.InventoryRevision == 0 || len(w.InventoryBags) != 5 || !itemGiverGUID(guid) {
		return fail()
	}
	var found itemGiverBinding
	bags := [5]bool{}
	items := map[uint64]bool{}
	for _, bag := range w.InventoryBags {
		if bag == nil || bag.Bag > 4 || bags[bag.Bag] || !bag.SlotKnown || !bag.ContentsKnown {
			return fail()
		}
		bags[bag.Bag] = true
		if bag.Bag == 0 {
			if bag.ContainerGuid != 0 || bag.SlotCount != 16 {
				return fail()
			}
		} else if bag.ContainerGuid == 0 {
			if bag.SlotCount != 0 || len(bag.Slots) != 0 {
				return fail()
			}
			continue
		} else if !bag.MetadataKnown || bag.Entry == 0 || !itemGiverGUID(bag.ContainerGuid) || items[bag.ContainerGuid] || bag.SlotCount == 0 || bag.SlotCount > 36 {
			return fail()
		} else {
			items[bag.ContainerGuid] = true
		}
		if len(bag.Slots) != int(bag.SlotCount) {
			return fail()
		}
		seen := make([]bool, bag.SlotCount)
		for _, slot := range bag.Slots {
			if slot == nil || slot.Bag != bag.Bag || slot.Slot >= bag.SlotCount || seen[slot.Slot] {
				return fail()
			}
			seen[slot.Slot] = true
			if slot.ItemGuid == 0 {
				if slot.Entry != 0 || slot.StackCount != 0 {
					return fail()
				}
				continue
			}
			if !itemGiverGUID(slot.ItemGuid) || items[slot.ItemGuid] {
				return fail()
			}
			items[slot.ItemGuid] = true
			if slot.ItemGuid == guid {
				starter, ok := gamecatalog.ManualQuestStarter(slot.Entry)
				if !slot.MetadataKnown || !ok || w.Self.Level < starter.MinLevel || slot.StackCount == 0 || slot.StackCount > starter.MaxStack {
					return fail()
				}
				found = itemGiverBinding{visit: w.VisitRevision, mapID: w.Self.MapId, inventoryRevision: w.InventoryRevision, containerGUID: bag.ContainerGuid,
					guid: guid, bag: bag.Bag, slot: slot.Slot, entry: slot.Entry, count: slot.StackCount, questID: starter.QuestID, repeatable: starter.Repeatable}
			}
		}
	}
	if found.guid == 0 {
		return fail()
	}
	return found, nil
}

func itemGiverScene(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64) bool {
	if w == nil || w.Self == nil || w.Self.Guid != self || self == 0 || !w.Self.Present || !w.Self.AliveKnown || !w.Self.Alive ||
		!w.Self.UnitFlagsKnown || w.Self.UnitFlags&(0x1|0x4|0x40000|0x80000|0x100000|0x400000|0x800000|0x1000000|0x2000000|0x8000000) != 0 ||
		!w.Self.MovementFlagsKnown || !movementflags.GroundCompatible(w.Self.MovementFlags, w.Self.ExtraMovementFlags) {
		return false
	}
	return a != nil && !a.Moving && !a.MotionUnconfirmed && !a.CastPending && !a.AutoAttacking && !a.AutoAttackUnconfirmed && !a.Traveling && !a.Practicing && questPositionBound(w, a, self)
}

func (s *Service) itemQuestAvailableLocked(w *worldv1.SnapshotResponse, binding itemGiverBinding) bool {
	return w.SelfQuestLogKnown && w.SelfQuestLogRevision != 0 && w.SelfQuestLogRevision == s.state.QuestLogRevision &&
		s.state.QuestLogKnown && s.inLogLocked(0) && !s.inLogLocked(binding.questID) && !s.itemQuestReservedLocked(binding)
}

func (s *Service) itemGate(ctx context.Context, guid uint64) (*worldv1.SnapshotResponse, error) {
	a, err := s.actions(ctx)
	if err != nil {
		return nil, err
	}
	w, err := s.world(ctx)
	if err != nil {
		return nil, err
	}
	binding, err := ownedQuestStarter(w, guid)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	expected := s.activeItemGiver
	matches := expected != nil && *expected == binding && s.itemQuestAvailableLocked(w, binding)
	s.mu.Unlock()
	if !matches || !itemGiverScene(w, a, s.selfGUID) || a.InteractionOwner != "quests" || a.InteractionLeaseRemainingMs < 1000 {
		return nil, status.Error(codes.FailedPrecondition, "owned quest starter, current safe body or lease changed")
	}
	return w, nil
}

// Called after reserve and before acquiring a lease, so an unowned/arbitrary
// item cannot even stop formation. For selected offers retain its exact epoch.
func (s *Service) bindItemGiver(ctx context.Context, guid uint64, fromOffer bool) (uint32, error) {
	w, err := s.world(ctx)
	if err != nil {
		return 0, err
	}
	binding, err := ownedQuestStarter(w, guid)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.itemQuestAvailableLocked(w, binding) {
		return 0, status.Error(codes.FailedPrecondition, "item quest already reserved, active or own quest log unavailable")
	}
	if fromOffer && (s.itemGiverOffer == nil || s.itemGiverOffer.binding != binding || s.state.Offer == nil || s.itemGiverOffer.revision != s.state.Offer.Revision) {
		return 0, status.Error(codes.FailedPrecondition, "quest starter inventory or visit changed since the actual offer")
	}
	s.activeItemGiver = &binding
	return binding.questID, nil
}
