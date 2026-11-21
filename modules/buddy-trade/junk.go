package buddytrade

import (
	"context"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/gamecatalog"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

var grayArmorSlots = map[uint32]uint32{1: 0, 3: 2, 5: 4, 6: 5, 7: 6, 8: 7, 9: 8, 10: 9, 16: 14, 20: 4}

// Called under mu with the fresh inventory snapshot used to bind the mutation.
// Only basic armor with no other benefits and a verified replacement qualifies.
func (s *Service) grayArmorJunk(w *worldv1.SnapshotResponse, item *worldv1.BackpackSlot) bool {
	meta := s.lookup.items[item.Entry]
	slot, armor := grayArmorSlots[meta.GetInventoryType()]
	if !armor || !meta.GetAvailable() || !meta.GetArmorComparisonKnown() || meta.ItemClass != 4 || meta.Subclass < 1 || meta.Subclass > 4 ||
		meta.Quality != 0 || meta.SellPrice == 0 || meta.Flags != 0 || meta.Flags2 != 0 || meta.HasSpell || len(meta.Stats) != 0 ||
		meta.RequiredSkill != 0 || meta.RequiredSkillRank != 0 || meta.RequiredSpell != 0 || meta.RequiredHonorRank != 0 ||
		meta.RequiredCityRank != 0 || meta.RequiredReputationFaction != 0 || meta.RequiredReputationRank != 0 ||
		item.StackCount != 1 || !item.EnhancementsKnown || item.HasEnhancements || !w.SelfEquipmentKnown || !w.SelfQuestLogKnown {
		return false
	}
	for _, quest := range w.SelfQuestLog {
		for _, required := range gamecatalog.RequiredQuestItems(quest.GetQuestId()) {
			if required.ItemID == item.Entry {
				return false
			}
		}
	}
	for _, worn := range w.SelfEquipment {
		if worn.GetSlot() != slot || !worn.GetSlotKnown() || !worn.GetMetadataKnown() || worn.GetItemGuid() == 0 || worn.GetItemGuid() == item.ItemGuid ||
			!worn.GetDurabilityKnown() || worn.GetMaxDurability() > 0 && worn.GetDurability() == 0 {
			continue
		}
		replacement := s.lookup.items[worn.Entry]
		wornSlot, knownSlot := grayArmorSlots[replacement.GetInventoryType()]
		return knownSlot && wornSlot == slot && replacement.GetAvailable() && replacement.GetItemClass() == 4 && replacement.GetArmor() >= meta.Armor
	}
	return false
}

func (s *Service) JunkDisposal(_ context.Context, r *tradev1.JunkToggle) (*tradev1.State, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "enabled required")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	s.state.JunkDisposalEnabled = r.Enabled
	s.state.Revision++
	if !r.Enabled {
		s.state.JunkReason = "disabled"
	}
	s.mu.Unlock()
	return s.snapshot(), nil
}

// Every logical backpack slot must be present exactly once. Empty slots and
// partially known objects deliberately prevent disposal.
func knownBackpackReferences(w *worldv1.SnapshotResponse) bool {
	if w == nil || !w.BackpackKnown || len(w.Backpack) != 16 {
		return false
	}
	var seen [16]bool
	guids := make(map[uint64]bool, 16)
	for _, item := range w.Backpack {
		if item == nil || item.Bag != 0 || item.Slot >= 16 || seen[item.Slot] || item.ItemGuid != 0 && guids[item.ItemGuid] {
			return false
		}
		seen[item.Slot], guids[item.ItemGuid] = true, true
	}
	return true
}
func fullBackpack(w *worldv1.SnapshotResponse) bool {
	if !knownBackpackReferences(w) {
		return false
	}
	for _, item := range w.Backpack {
		if item.ItemGuid == 0 || !item.MetadataKnown || item.Entry == 0 || item.StackCount == 0 {
			return false
		}
	}
	return true
}

func (s *Service) junkReason(reason string) { s.mu.Lock(); s.state.JunkReason = reason; s.mu.Unlock() }

func (s *Service) junkSafe(ctx context.Context, w *worldv1.SnapshotResponse) error {
	if err := s.junkWorldSafe(w); err != nil {
		return err
	}
	a := &actionsv1.State{}
	if err := s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, a); err != nil {
		return err
	}
	if !junkActionsSafe(a) {
		return status.Error(codes.FailedPrecondition, "inventory interaction, casting or movement is busy")
	}
	return nil
}

func (s *Service) DestroyJunk(ctx context.Context, r *tradev1.JunkRequest) (*tradev1.State, error) {
	if r == nil || r.ExpectedItemGuid == 0 {
		return nil, status.Error(codes.InvalidArgument, "exact carried slot and item GUID required")
	}
	legacy := r.BackpackSlot < 16 && r.SourceBag == 0 && r.SourceSlot == 0 && r.InventoryRevision == 0
	carried := r.SourceBag >= 1 && r.SourceBag <= 4 && r.SourceSlot < 36 && r.InventoryRevision != 0 &&
		r.BackpackRevision == 0 && r.BackpackSlot == 0
	if !legacy && !carried {
		return nil, status.Error(codes.InvalidArgument, "one exact inventory source is required")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	call, cancel := context.WithTimeout(ctx, junkWriteTimeout)
	defer cancel()
	if err := s.destroyJunk(call, r); err != nil {
		return nil, err
	}
	return s.snapshot(), nil
}

// opMu serializes all outgoing inventory/trade mutations. The actions lease
// excludes loot, mail, quest and training mutations while this write settles.
func (s *Service) destroyJunk(ctx context.Context, r *tradev1.JunkRequest) error {
	ctx, cancel := context.WithTimeout(ctx, junkWriteTimeout)
	defer cancel()
	s.mu.Lock()
	err := s.boundLocked(r.SessionId, r.Revision)
	busy := active(s.state.Phase) || s.state.Phase == "CANCEL_SENT" || s.pendingBegin || s.pendingCancel || s.junkPending != nil
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if busy {
		return status.Error(codes.FailedPrecondition, "trade or inventory mutation awaits confirmation")
	}
	w, err := s.inventory(ctx)
	if err != nil {
		return err
	}
	initial := junkSource(w, r)
	if initial == nil {
		return status.Error(codes.FailedPrecondition, "full capacity and exact inventory source required")
	}
	// Do not interrupt an existing interaction; acquire may stop ordinary follow.
	if err := s.junkSafe(ctx, w); err != nil {
		return err
	}
	if err := s.acquire(ctx); err != nil {
		return err
	}
	reserved := false
	defer func() {
		if !reserved {
			s.release(ctx)
		}
	}()
	w, err = s.inventory(ctx)
	if err != nil {
		return err
	}
	selected := junkSource(w, r)
	if selected == nil || r.InventoryRevision != 0 &&
		(selected.Entry != initial.Entry || selected.StackCount != initial.StackCount) {
		return status.Error(codes.FailedPrecondition, "inventory source changed before disposal")
	}
	w, selected, guard, err := s.junkFinalWorld(ctx, r, w)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if err := s.boundLocked(r.SessionId, r.Revision); err != nil {
		s.mu.Unlock()
		return err
	}
	meta, known := s.metadata[selected.Entry]
	if active(s.state.Phase) || s.state.Phase == "CANCEL_SENT" || s.pendingBegin || s.pendingCancel || s.junkPending != nil || !known ||
		(!meta.disposable() && !s.grayArmorJunk(w, selected)) {
		s.mu.Unlock()
		return status.Error(codes.FailedPrecondition, "only fully queried gray junk with no remaining use is disposable")
	}
	until := s.now().Add(5 * time.Second)
	if err := s.junkDispatchReady(ctx, guard); err != nil {
		s.mu.Unlock()
		return err
	}
	// Reserve before SendPacket: an ambiguous error must never resend a destroy.
	s.junkPending = &tradev1.JunkDisposal{ItemGuid: selected.ItemGuid, Entry: selected.Entry,
		Count: selected.StackCount, Name: s.names[selected.Entry], Status: "AWAITING_SERVER_SLOT_UPDATE"}
	bag, slot := byte(255), byte(23+selected.Slot)
	if r.InventoryRevision != 0 {
		s.junkPending.SourceBag, s.junkPending.SourceSlot = selected.Bag, selected.Slot
		s.junkPending.InventoryRevision = w.InventoryRevision
		s.carriedJunkProof = &carriedJunkProof{revision: w.InventoryRevision, presentRevision: w.InventoryRevision,
			equipmentRevision: w.SelfEquipmentRevision}
		bag, slot = byte(18+selected.Bag), byte(selected.Slot)
	} else {
		s.carriedJunkProof = nil
		s.junkPending.BackpackSlot, s.junkPending.BackpackRevision = selected.Slot, w.BackpackRevision
	}
	s.state.LastJunkDisposal = proto.Clone(s.junkPending).(*tradev1.JunkDisposal)
	s.state.JunkReason = "waiting_for_server_inventory_update"
	s.state.Revision++
	s.junkUntil = until
	s.mu.Unlock()
	reserved = true
	// ItemPackets::DestroyItem: bag, inventory slot, count(0=whole stack), 3 unused bytes.
	if err := s.send(ctx, opcode.CMSGDestroyitem, []byte{bag, slot, 0, 0, 0, 0}); err != nil {
		s.mu.Lock()
		s.state.LastJunkDisposal.Status = "TRANSPORT_UNCONFIRMED_NO_RETRY"
		s.state.JunkReason = "transport_unconfirmed_no_retry"
		s.mu.Unlock()
	}
	return nil
}

func (s *Service) junkTick(ctx context.Context) {
	s.mu.Lock()
	pending := s.junkPending
	enabled := s.state.JunkDisposalEnabled
	s.mu.Unlock()
	if pending == nil && (!enabled || s.now().Before(s.junkNext)) {
		s.release(ctx)
		return
	}
	s.junkNext = s.now().Add(time.Second)
	w, err := s.inventory(ctx)
	if err != nil {
		s.junkReason("inventory_unavailable")
		if pending != nil && !s.now().Before(s.junkUntil) {
			s.release(ctx)
		}
		return
	}
	if pending != nil {
		removed, slotSeen := true, false
		for _, item := range w.Backpack {
			if item == nil {
				continue
			}
			if item.ItemGuid == pending.ItemGuid {
				removed = false
			}
			if item.Bag == 0 && item.Slot == pending.BackpackSlot {
				slotSeen = true
			}
		}
		confirmed := knownBackpackReferences(w) && slotSeen && removed && w.BackpackRevision > pending.BackpackRevision
		if pending.InventoryRevision != 0 {
			s.mu.Lock()
			confirmed = s.carriedJunkProof.removalConfirmed(w, pending)
			s.mu.Unlock()
			confirmed = confirmed && w.Self.GetPresent() && w.Self.GetGuid() == s.selfGUID
		}
		if confirmed {
			s.mu.Lock()
			s.state.LastJunkDisposal.Status = "SERVER_SLOT_REMOVAL_CONFIRMED"
			s.state.JunkStacksDestroyed++
			s.state.JunkReason = "server_confirmed_junk_stack_removed"
			s.state.Revision++
			s.junkPending = nil
			s.carriedJunkProof = nil
			s.mu.Unlock()
			s.release(ctx)
			return
		}
		if !s.now().Before(s.junkUntil) {
			s.mu.Lock()
			s.state.LastJunkDisposal.Status = "UNCONFIRMED_NO_RETRY"
			s.state.JunkReason = "slot_update_unconfirmed_disposal_blocked"
			s.mu.Unlock()
			s.release(ctx)
		}
		return
	}
	s.release(ctx)
	if !fullBackpack(w) {
		s.junkReason("backpack_has_space_or_is_not_fully_known")
		return
	}
	s.mu.Lock()
	var selected *worldv1.BackpackSlot
	for _, item := range w.Backpack {
		if s.metadata[item.Entry].disposable() && (selected == nil || item.Slot < selected.Slot) {
			selected = item
		}
	}
	bound := &tradev1.JunkRequest{SessionId: s.state.SessionId, Revision: s.state.Revision, BackpackRevision: w.BackpackRevision}
	if selected != nil {
		bound.BackpackSlot, bound.ExpectedItemGuid = selected.Slot, selected.ItemGuid
	}
	s.mu.Unlock()
	if selected == nil {
		s.junkReason("waiting_for_metadata_or_no_safe_gray_junk")
		return
	}
	if err := s.destroyJunk(ctx, bound); err != nil {
		s.junkReason("disposal_safety_gate_busy_or_changed")
	}
}
