package buddyinventory

import (
	"context"
	"time"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/types/known/emptypb"
)

// This is an owner-selected receipt, not a list of gear to consume. It survives
// the finite UI worker so later P commands can read proof without another cast.
type addonDisenchant struct {
	session, request string
	guid             uint64
	entry, bag, slot uint32
	inventory        uint64
}

func (d *addonDisenchant) selected(s *professionsv1.State) bool {
	i := s.GetDisenchantItem()
	return s != nil && s.SessionId == d.session && s.SelfGuid == 4 && s.InventoryKnown && s.InventoryRevision == d.inventory && i != nil && i.ItemGuid == d.guid && i.Entry == d.entry && i.Bag == d.bag && i.Slot == d.slot && i.StackCount == 1
}

func (d *addonDisenchant) outcome(s *professionsv1.State) (string, string) {
	if s == nil || s.SessionId != d.session || s.SelfGuid != 4 {
		return "UNCONFIRMED", "de_session_unconfirmed_keep_connected"
	}
	o := s.Operation
	if o == nil || o.OperationId == "" || o.RequestId != d.request || o.SkillId != 333 || o.SpellId != 13262 || o.TargetGuid != d.guid || o.TargetEntry != d.entry || o.SourceBag != d.bag || o.SourceSlot != d.slot || o.BaselineInventoryRevision != d.inventory {
		return "UNCONFIRMED", "de_receipt_unconfirmed_keep_connected"
	}
	if s.Active {
		return "PENDING", "de_materials_pending_keep_connected"
	}
	if o.CleanupUnconfirmed || !o.LeaseReleased {
		return "UNCONFIRMED", "de_cleanup_pending_keep_connected"
	}
	if o.Status == professionsv1.AttemptStatus_COMPLETED && o.OwnGoObserved && o.LootWindowObserved && o.LootSlotsRemoved > 0 && o.OwnLootedItemsObserved > 0 && o.LootReleaseObserved && o.InputConsumedObserved && o.OutputInventoryObserved && s.InventoryKnown && o.ObservedInventoryRevision > d.inventory && o.ObservedInventoryRevision <= s.InventoryRevision {
		return "CONFIRMED", "selected_item_materials_verified"
	}
	// Only an actually observed server cast rejection without an item window,
	// or a pre-cast block, may free the UI for another selected operation.
	if o.Status == professionsv1.AttemptStatus_SERVER_REJECTED && o.FailureCode != 0 && !o.OwnGoObserved && !o.LootWindowObserved && !o.InputConsumedObserved {
		return "SERVER_REJECTED", "de_server_rejected_no_material_window"
	}
	if o.Status == professionsv1.AttemptStatus_BLOCKED && !o.CastTransportWritten && !o.OwnStartObserved && !o.OwnGoObserved && !o.LootWindowObserved && !o.InputConsumedObserved {
		return "BLOCKED", "de_blocked_before_selected_item_cast"
	}
	return "UNCONFIRMED", "de_outcome_pending_keep_connected_no_retry"
}

func (b *addonBridge) disenchant(ctx context.Context, c *addonCommand, inventory *inventoryv1.State, item *worldv1.BackpackSlot) {
	ctx, finish := context.WithTimeout(ctx, 15*time.Second)
	defer finish()
	if b.service.selfGUID != 4 || inventory.SelfGuid != 4 || item == nil || item.StackCount != 1 {
		b.finish(c, "BLOCKED", "mira_selected_single_carried_item_required")
		return
	}
	d := &addonDisenchant{session: inventory.SessionId, request: "addon_d_" + c.nonce, guid: item.ItemGuid, entry: item.Entry, bag: item.Bag, slot: item.Slot, inventory: c.revision}
	inspect, cancel := context.WithTimeout(ctx, 3*time.Second)
	var state professionsv1.State
	err := b.service.invoke(inspect, "buddy-professions", "inspect_disenchant", &professionsv1.DisenchantInspectRequest{SessionId: d.session, TargetGuid: d.guid}, &state)
	for err == nil && d.selected(&state) && !state.DisenchantItem.Ready && !state.DisenchantItem.TemplateKnown {
		select {
		case <-inspect.Done():
			err = inspect.Err()
		case <-time.After(100 * time.Millisecond):
			err = b.service.invoke(inspect, "buddy-professions", "state", &emptypb.Empty{}, &state)
		}
	}
	cancel()
	if err != nil || !d.selected(&state) || !state.DisenchantItem.Ready || state.Active || state.Closing || state.StopPrepared || !state.SkillsKnown || !state.SpellsKnown {
		b.finish(c, "BLOCKED", "selected_de_inspection_not_ready_refresh")
		return
	}
	// Reserve the receipt before the only Attempt call, including a lost reply.
	b.mu.Lock()
	if r := b.requests[c.nonce]; r != nil {
		r.disenchant = d
	}
	b.mu.Unlock()
	request := &professionsv1.AttemptRequest{SessionId: d.session, RequestId: d.request, SkillId: 333, SpellId: 13262, ExpectedRevision: state.Revision, ExpectedSkillsRevision: state.SkillsRevision, ExpectedInventoryRevision: state.InventoryRevision, TargetGuid: d.guid}
	call, stop := context.WithTimeout(ctx, 3*time.Second)
	err = b.service.invoke(call, "buddy-professions", "attempt", request, &state)
	stop()
	if err != nil {
		b.finish(c, "UNCONFIRMED", "de_attempt_reply_unknown_keep_connected")
		return
	}
	pendingSent := false
	for {
		status, reason := d.outcome(&state)
		if status != "PENDING" {
			b.finish(c, status, reason)
			return
		}
		if !pendingSent {
			b.finish(c, status, reason)
			pendingSent = true
		}
		call, stop = context.WithTimeout(ctx, time.Second)
		select {
		case <-call.Done():
			err = call.Err()
		case <-time.After(150 * time.Millisecond):
			err = b.service.invoke(call, "buddy-professions", "state", &emptypb.Empty{}, &state)
		}
		stop()
		if err != nil {
			b.finish(c, "UNCONFIRMED", "de_materials_pending_keep_connected")
			return
		}
	}
}

func (b *addonBridge) pollDisenchant(ctx context.Context, c *addonCommand) {
	b.mu.Lock()
	r := b.requests[c.nonce]
	var d *addonDisenchant
	if r != nil && r.snapshot == c.snapshot {
		d = r.disenchant
	}
	b.mu.Unlock()
	if d == nil || b.service.selfGUID != 4 {
		return
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	state := &professionsv1.State{}
	if err := b.service.invoke(call, "buddy-professions", "state", &emptypb.Empty{}, state); err != nil {
		b.finish(c, "UNCONFIRMED", "de_receipt_unavailable_keep_connected")
		return
	}
	status, reason := d.outcome(state)
	b.finish(c, status, reason)
}
