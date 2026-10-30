package buddytrade

import (
	"context"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

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
	if w == nil || w.Self == nil || w.Self.Guid != s.selfGUID || !w.Self.Present || !w.Self.AliveKnown || !w.Self.Alive || !w.Self.UnitFlagsKnown || w.Self.UnitFlags&0x80000 != 0 {
		return status.Error(codes.FailedPrecondition, "junk disposal requires observed alive noncombat character")
	}
	a := &actionsv1.State{}
	if err := s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, a); err != nil {
		return err
	}
	if a.Moving || a.MotionUnconfirmed || a.CastPending || a.AutoAttacking || a.AutoAttackRequested || a.AutoAttackUnconfirmed || a.Traveling || (a.InteractionOwner != "" && a.InteractionOwner != "trade") {
		return status.Error(codes.FailedPrecondition, "inventory interaction, casting or movement is busy")
	}
	return nil
}

func (s *Service) DestroyJunk(ctx context.Context, r *tradev1.JunkRequest) (*tradev1.State, error) {
	if r == nil || r.BackpackSlot >= 16 || r.ExpectedItemGuid == 0 {
		return nil, status.Error(codes.InvalidArgument, "exact backpack slot and item GUID required")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.destroyJunk(call, r); err != nil {
		return nil, err
	}
	return s.snapshot(), nil
}

// opMu serializes all outgoing inventory/trade mutations. The actions lease
// excludes loot, mail, quest and training mutations while this write settles.
func (s *Service) destroyJunk(ctx context.Context, r *tradev1.JunkRequest) error {
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
	if !fullBackpack(w) || w.BackpackRevision != r.BackpackRevision {
		return status.Error(codes.FailedPrecondition, "full backpack or exact inventory revision required")
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
	if !fullBackpack(w) || w.BackpackRevision != r.BackpackRevision {
		return status.Error(codes.FailedPrecondition, "backpack changed before disposal")
	}
	if err := s.junkSafe(ctx, w); err != nil {
		return err
	}
	var selected *worldv1.BackpackSlot
	for _, item := range w.Backpack {
		if item.Slot == r.BackpackSlot && item.ItemGuid == r.ExpectedItemGuid {
			selected = item
		}
	}
	if selected == nil {
		return status.Error(codes.FailedPrecondition, "selected item changed")
	}
	s.mu.Lock()
	meta, known := s.metadata[selected.Entry]
	if active(s.state.Phase) || s.state.Phase == "CANCEL_SENT" || s.pendingBegin || s.pendingCancel || s.junkPending != nil || !known || !meta.disposable() {
		s.mu.Unlock()
		return status.Error(codes.FailedPrecondition, "only fully queried gray miscellaneous junk is disposable")
	}
	// Reserve before SendPacket: an ambiguous error must never resend a destroy.
	s.junkPending = &tradev1.JunkDisposal{BackpackSlot: selected.Slot, ItemGuid: selected.ItemGuid, Entry: selected.Entry, Count: selected.StackCount, Name: s.names[selected.Entry], Status: "AWAITING_SERVER_SLOT_UPDATE", BackpackRevision: w.BackpackRevision}
	s.state.LastJunkDisposal = proto.Clone(s.junkPending).(*tradev1.JunkDisposal)
	s.state.JunkReason = "waiting_for_server_inventory_update"
	s.state.Revision++
	s.junkUntil = s.now().Add(5 * time.Second)
	s.mu.Unlock()
	reserved = true
	// ItemPackets::DestroyItem: bag, inventory slot, count(0=whole stack), 3 unused bytes.
	if err := s.send(ctx, opcode.CMSGDestroyitem, []byte{255, byte(23 + selected.Slot), 0, 0, 0, 0}); err != nil {
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
		if knownBackpackReferences(w) && slotSeen && removed && w.BackpackRevision > pending.BackpackRevision {
			s.mu.Lock()
			s.state.LastJunkDisposal.Status = "SERVER_SLOT_REMOVAL_CONFIRMED"
			s.state.JunkStacksDestroyed++
			s.state.JunkReason = "server_confirmed_junk_stack_removed"
			s.state.Revision++
			s.junkPending = nil
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
