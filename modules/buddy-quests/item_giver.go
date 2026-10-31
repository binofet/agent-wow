package buddyquests

import (
	"context"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Public item_template:1972 Westfall Deed starts184. Item1971 is the
// quest-supplied delivery item; it cannot start this quest.
const deedStarterEntry, deedQuestID uint32 = 1972, 184

type itemGiverBinding struct {
	inventoryRevision       uint64
	guid                    uint64
	bag, slot, entry, count uint32
}
type itemGiverOffer struct {
	binding  itemGiverBinding
	revision uint64
}

func itemGiverGUID(guid uint64) bool { return guid>>48 == 0x4000 && guid&0xffffffffffff != 0 }

// A GUID is not ownership. Require the whole coherent carried layout and the
// exact individually observed item, including equipped bag contents.
func ownedDeed(w *worldv1.SnapshotResponse, guid uint64) (itemGiverBinding, error) {
	fail := func() (itemGiverBinding, error) {
		return itemGiverBinding{}, status.Error(codes.FailedPrecondition, "actual complete owned Westfall Deed inventory binding unavailable")
	}
	if w == nil || !w.InventoryKnown || w.InventoryRevision == 0 || len(w.InventoryBags) != 5 || !itemGiverGUID(guid) {
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
		} else if !bag.MetadataKnown || !itemGiverGUID(bag.ContainerGuid) || bag.SlotCount == 0 || bag.SlotCount > 36 {
			return fail()
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
				if !slot.MetadataKnown || slot.Entry != deedStarterEntry || slot.StackCount != 1 {
					return fail()
				}
				found = itemGiverBinding{w.InventoryRevision, guid, bag.Bag, slot.Slot, slot.Entry, slot.StackCount}
			}
		}
	}
	if found.guid == 0 {
		return fail()
	}
	return found, nil
}

func itemGiverScene(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64) bool {
	if w == nil || w.Self == nil || w.Self.Guid != self || self == 0 || !w.Self.Present || !w.Self.LevelKnown || w.Self.Level < 8 || !w.Self.AliveKnown || !w.Self.Alive ||
		!w.Self.UnitFlagsKnown || w.Self.UnitFlags&(0x1|0x4|0x40000|0x80000|0x100000|0x400000|0x800000|0x1000000|0x2000000|0x8000000) != 0 ||
		!w.Self.MovementFlagsKnown || w.Self.MovementFlags & ^uint32(0x1ff) != 0 || w.Self.ExtraMovementFlags != 0 {
		return false
	}
	return a != nil && !a.Moving && !a.MotionUnconfirmed && !a.CastPending && !a.AutoAttacking && !a.AutoAttackUnconfirmed && !a.Traveling && !a.Practicing && questPositionBound(w, a, self)
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
	binding, err := ownedDeed(w, guid)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	expected := s.activeItemGiver
	matches := expected != nil && *expected == binding
	s.mu.Unlock()
	if !matches || !itemGiverScene(w, a, s.selfGUID) || a.InteractionOwner != "quests" || a.InteractionLeaseRemainingMs < 1000 {
		return nil, status.Error(codes.FailedPrecondition, "owned quest starter, current safe body or lease changed")
	}
	return w, nil
}

// Called after reserve and before acquiring a lease, so an unowned/arbitrary
// item cannot even stop formation. For selected offers retain its exact epoch.
func (s *Service) bindItemGiver(ctx context.Context, guid uint64, fromOffer bool) error {
	w, err := s.world(ctx)
	if err != nil {
		return err
	}
	binding, err := ownedDeed(w, guid)
	if err != nil {
		return err
	}
	if !w.Self.LevelKnown || w.Self.Level < 8 {
		return status.Error(codes.FailedPrecondition, "deed quest requires actual known level8")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deedAcceptReserved {
		return status.Error(codes.FailedPrecondition, "deed acceptance already reserved for this body")
	}
	if !s.state.QuestLogKnown || s.inLogLocked(deedQuestID) {
		return status.Error(codes.FailedPrecondition, "own deed quest log is unknown or already contains184")
	}
	if fromOffer && (s.itemGiverOffer == nil || s.itemGiverOffer.binding != binding || s.state.Offer == nil || s.itemGiverOffer.revision != s.state.Offer.Revision) {
		return status.Error(codes.FailedPrecondition, "quest starter inventory changed since the actual offer")
	}
	s.activeItemGiver = &binding
	return nil
}
