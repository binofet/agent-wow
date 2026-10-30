package buddyinventory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type addonReceipt struct{ raw, snapshot, status, reason string }
type addonSnapshot struct {
	state *inventoryv1.State
	at    time.Time
}
type addonBridge struct {
	lifetime     context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	service      *Service
	now          func() time.Time
	queue        []*addonCommand
	outgoing     []string
	snapshots    map[string]addonSnapshot
	requests     map[string]*addonReceipt
	readNonces   map[string]time.Time
	busy, closed bool
	nextSend     time.Time
}

func newAddonBridge(s *Service) *addonBridge {
	lifetime, cancel := context.WithCancel(context.Background())
	return &addonBridge{service: s, now: time.Now, lifetime: lifetime, cancel: cancel, snapshots: map[string]addonSnapshot{}, requests: map[string]*addonReceipt{}, readNonces: map[string]time.Time{}}
}
func (b *addonBridge) receive(payload []byte) {
	c, matched, e := decodeInventoryAddon(payload)
	if !matched || e != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	if c.kind == "R" {
		for nonce, at := range b.readNonces {
			if b.now().Sub(at) > 30*time.Second {
				delete(b.readNonces, nonce)
			}
		}
		if _, exists := b.readNonces[c.nonce]; exists || len(b.readNonces) >= 128 || len(b.queue) >= 16 {
			return
		}
		b.readNonces[c.nonce] = b.now()
		b.queue = append(b.queue, c)
		return
	}
	if old := b.requests[c.nonce]; old != nil {
		if old.raw == c.raw {
			b.queueACKLocked(c.nonce, old.snapshot, old.status, old.reason)
		} else {
			b.queueACKLocked(c.nonce, c.snapshot, "BLOCKED", "request_nonce_payload_changed")
		}
		return
	}
	if len(b.requests) >= 128 || len(b.queue) >= 16 {
		b.queueACKLocked(c.nonce, c.snapshot, "BLOCKED", "bounded_request_history_full")
		return
	}
	b.requests[c.nonce] = &addonReceipt{raw: c.raw, snapshot: c.snapshot, status: "QUEUED", reason: "awaiting_verified_inventory_operation"}
	b.queueACKLocked(c.nonce, c.snapshot, "QUEUED", "awaiting_verified_inventory_operation")
	b.queue = append(b.queue, c)
}
func (b *addonBridge) queueACKLocked(nonce, snapshot, state, reason string) {
	if len(reason) > 48 {
		reason = reason[:48]
	}
	if len(b.outgoing) >= 256 {
		return
	}
	b.outgoing = append(b.outgoing, fmt.Sprintf("1|A|%s|%s|%s|%s", nonce, snapshot, state, reason))
}
func (b *addonBridge) finish(c *addonCommand, state, reason string) {
	b.mu.Lock()
	if r := b.requests[c.nonce]; r != nil {
		r.status, r.reason = state, reason
	}
	b.queueACKLocked(c.nonce, c.snapshot, state, reason)
	b.mu.Unlock()
	// A refreshed snapshot follows successful inventory changes. It retains the
	// request nonce; Lua may immediately replace it with a new explicit R nonce.
	if state == "CONFIRMED" {
		b.publish(c.nonce)
	}
}
func (b *addonBridge) tick(ctx context.Context) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	var send string
	if len(b.outgoing) > 0 && !b.now().Before(b.nextSend) {
		send = b.outgoing[0]
		b.outgoing = b.outgoing[1:]
		b.nextSend = b.now().Add(200 * time.Millisecond)
	}
	var next *addonCommand
	if !b.busy && len(b.queue) > 0 {
		next = b.queue[0]
		b.queue = b.queue[1:]
		b.busy = true
	}
	b.mu.Unlock()
	if send != "" {
		if packet, e := encodeInventoryAddon(send); e == nil && b.service.session != nil {
			call, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			// Chat is never a mutation retry. A dropped multipart reply is incomplete;
			// the owner can request a fresh nonce and coherent snapshot.
			_, _ = b.service.session.SendPacket(call, &modv1.SendPacketRequest{Opcode: opcode.CMSGMessagechat, Payload: packet})
			cancel()
		}
	}
	if next != nil {
		go func() {
			defer func() { b.mu.Lock(); b.busy = false; b.mu.Unlock() }()
			if next.kind == "R" {
				b.publish(next.nonce)
			} else {
				b.process(next)
			}
		}()
	}
}
func (b *addonBridge) close() {
	b.mu.Lock()
	b.closed = true
	if b.cancel != nil {
		b.cancel()
	}
	b.queue = nil
	b.outgoing = nil
	b.mu.Unlock()
}
func (b *addonBridge) publish(nonce string) {
	ctx, cancel := context.WithTimeout(b.lifetime, 3*time.Second)
	defer cancel()
	state, e := b.service.GetState(ctx, &emptypb.Empty{})
	if e != nil || state == nil {
		return
	}
	raw := make([]byte, 6)
	if _, e = rand.Read(raw); e != nil {
		return
	}
	id := hex.EncodeToString(raw)
	parts, e := inventoryAddonParts(nonce, id, state)
	if e != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || len(b.outgoing)+len(parts) > 256 {
		return
	}
	for k, s := range b.snapshots {
		if b.now().Sub(s.at) > 30*time.Second {
			delete(b.snapshots, k)
		}
	}
	if len(b.snapshots) >= 8 {
		var oldest string
		var at time.Time
		for k, s := range b.snapshots {
			if oldest == "" || s.at.Before(at) {
				oldest, at = k, s.at
			}
		}
		delete(b.snapshots, oldest)
	}
	b.snapshots[id] = addonSnapshot{state: proto.Clone(state).(*inventoryv1.State), at: b.now().Add(time.Duration(len(b.outgoing)+len(parts)) * 200 * time.Millisecond)}
	b.outgoing = append(b.outgoing, parts...)
}
func addonSlot(state *inventoryv1.State, bag, slot uint32) *worldv1.BackpackSlot {
	if state == nil || !state.InventoryKnown {
		return nil
	}
	for _, b := range state.InventoryBags {
		if b != nil && b.Bag == bag && b.SlotKnown && b.ContentsKnown {
			for _, s := range b.Slots {
				if s != nil && s.Slot == slot && s.Bag == bag {
					return s
				}
			}
		}
	}
	return nil
}
func addonRef(s *worldv1.BackpackSlot) *inventoryv1.ItemRef {
	return &inventoryv1.ItemRef{Bag: s.Bag, Slot: s.Slot, ItemGuid: s.ItemGuid, Entry: s.Entry, StackCount: s.StackCount}
}
func addonSameItem(a, z *worldv1.BackpackSlot) bool {
	return a != nil && z != nil && a.Bag == z.Bag && a.Slot == z.Slot && a.ItemGuid == z.ItemGuid && a.Entry == z.Entry && a.StackCount == z.StackCount && a.MetadataKnown && z.MetadataKnown
}
func addonUnresolved(state *inventoryv1.State) bool {
	if state == nil || state.Active || state.Closing {
		return true
	}
	if op := state.Operation; op != nil {
		return op.Status == "REQUESTED" || op.Status == "OUTCOME_UNCONFIRMED" || op.Status == "UNCONFIRMED" || op.Status == "WRITE_UNCONFIRMED" || !op.LeaseReleased
	}
	return false
}
func addonTradeItemBound(t *tradev1.State, session string, item *worldv1.BackpackSlot) bool {
	if t == nil || item == nil || t.SessionId != session || !t.BackpackKnown || t.PartnerGuid != 3 || t.Phase != "OPEN" || t.AcceptTransportWritten || t.BackpackRevision == 0 {
		return false
	}
	for _, owned := range t.Backpack {
		if owned != nil && owned.Slot == item.Slot && owned.ItemGuid == item.ItemGuid && owned.Entry == item.Entry && owned.Count == item.StackCount && owned.MetadataKnown {
			return true
		}
	}
	return false
}
func addonTradeOfferBound(t *tradev1.State, session string, item *worldv1.BackpackSlot) bool {
	if t == nil || t.SessionId != session || t.PartnerGuid != 3 || t.Phase != "OPEN" || t.AcceptTransportWritten || strings.Contains(t.Reason, "unconfirmed") {
		return false
	}
	for _, offered := range t.OwnRequestedItems {
		if offered != nil && offered.Slot == 0 && offered.ItemGuid == item.ItemGuid && offered.Entry == item.Entry && offered.Count == item.StackCount {
			return true
		}
	}
	return false
}
func (b *addonBridge) bound(ctx context.Context, c *addonCommand) (*inventoryv1.State, *inventoryv1.State, error) {
	b.mu.Lock()
	cached, ok := b.snapshots[c.snapshot]
	fresh := ok && b.now().Sub(cached.at) <= 30*time.Second
	b.mu.Unlock()
	if !fresh || !cached.state.InventoryKnown || cached.state.InventoryRevision != c.revision {
		return nil, nil, errors.New("snapshot_expired_or_unknown")
	}
	state, e := b.service.GetState(ctx, &emptypb.Empty{})
	if e != nil || state == nil || !state.InventoryKnown || addonUnresolved(state) || state.SessionId != cached.state.SessionId || state.SelfGuid != cached.state.SelfGuid || state.InventoryRevision != c.revision {
		return nil, nil, errors.New("inventory_changed_refresh_required")
	}
	if !proto.Equal(&worldv1.SnapshotResponse{InventoryBags: state.InventoryBags}, &worldv1.SnapshotResponse{InventoryBags: cached.state.InventoryBags}) {
		return nil, nil, errors.New("inventory_changed_refresh_required")
	}
	return state, cached.state, nil
}
func (b *addonBridge) wait(ctx context.Context, id string, state *inventoryv1.State, e error) (*inventoryv1.State, string) {
	if e != nil {
		return state, "operation_rejected"
	}
	for {
		if state != nil && state.Operation != nil && state.Operation.RequestId == id && !state.Active {
			if state.Operation.Status == "CONFIRMED" {
				return state, ""
			}
			if state.Operation.Status == "OUTCOME_UNCONFIRMED" || state.Operation.Status == "UNCONFIRMED" || state.Operation.Status == "WRITE_UNCONFIRMED" {
				return state, "operation_unconfirmed_no_retry"
			}
			return state, "operation_blocked_" + state.Operation.Reason
		}
		select {
		case <-ctx.Done():
			return state, "operation_unconfirmed_no_retry"
		case <-time.After(100 * time.Millisecond):
		}
		state, e = b.service.GetState(ctx, &emptypb.Empty{})
		if e != nil {
			return state, "operation_unconfirmed_no_retry"
		}
	}
}
func (b *addonBridge) process(c *addonCommand) {
	ctx, cancel := context.WithTimeout(b.lifetime, 180*time.Second)
	defer cancel()
	state, cached, e := b.bound(ctx, c)
	if e != nil {
		b.finish(c, "BLOCKED", e.Error())
		return
	}
	if c.kind == "J" {
		b.sellBatch(ctx, c, state)
		return
	}
	current, old := addonSlot(state, c.sourceBag, c.sourceSlot), addonSlot(cached, c.sourceBag, c.sourceSlot)
	if !addonSameItem(current, old) || current.ItemGuid != c.itemGUID {
		b.finish(c, "BLOCKED", "selected_item_changed")
		return
	}
	id := "addon_" + strings.ToLower(c.kind) + "_" + c.nonce
	var result *inventoryv1.State
	switch c.kind {
	case "M":
		d := addonSlot(state, c.destinationBag, c.destinationSlot)
		if d == nil || d.ItemGuid != 0 {
			b.finish(c, "BLOCKED", "destination_not_known_empty")
			return
		}
		result, e = b.service.Move(ctx, &inventoryv1.MoveRequest{SessionId: state.SessionId, RequestId: id, ExpectedRevision: state.Revision, ExpectedInventoryRevision: state.InventoryRevision, Source: addonRef(current), DestinationBag: c.destinationBag, DestinationSlot: c.destinationSlot})
	case "E":
		result, e = b.service.Equip(ctx, &inventoryv1.EquipRequest{SessionId: state.SessionId, RequestId: id, ExpectedRevision: state.Revision, ExpectedInventoryRevision: state.InventoryRevision, Source: addonRef(current), EquipmentSlot: 18 + c.destinationBag})
	case "G":
		b.give(ctx, c, state, current)
		return
	default:
		b.finish(c, "BLOCKED", "unsupported_inventory_command")
		return
	}
	_, why := b.wait(ctx, id, result, e)
	if why != "" {
		status := "BLOCKED"
		if strings.Contains(why, "unconfirmed") {
			status = "UNCONFIRMED"
		}
		b.finish(c, status, why)
		return
	}
	b.finish(c, "CONFIRMED", "server_inventory_change_verified")
}
func (b *addonBridge) sellBatch(ctx context.Context, c *addonCommand, state *inventoryv1.State) {
	discovered, e := b.service.Discover(ctx, &inventoryv1.DiscoverRequest{})
	if e != nil || discovered == nil {
		b.finish(c, "BLOCKED", "vendor_observations_unavailable")
		return
	}
	var vendor uint64
	for _, v := range discovered.Nearby {
		if v != nil && v.Interactable && v.Creature != nil {
			vendor = v.Creature.Guid
			break
		}
	}
	if vendor == 0 {
		b.finish(c, "BLOCKED", "no_verified_vendor_in_range")
		return
	}
	// Freeze the original eligible GUIDs. A batch never starts selling newly
	// acquired items, never travels, and stops after at most16 whole stacks.
	var eligible []*inventoryv1.ItemRef
	for _, i := range state.Items {
		if i != nil && i.SafeJunkSaleEligible && i.UnitSellPriceCopper > 0 && i.Item != nil {
			eligible = append(eligible, proto.Clone(i.Item).(*inventoryv1.ItemRef))
			if len(eligible) == 16 {
				break
			}
		}
	}
	if len(eligible) == 0 {
		b.finish(c, "BLOCKED", "no_verified_safe_gray_junk")
		return
	}
	expected := state.InventoryRevision
	for n, item := range eligible {
		state, e = b.service.GetState(ctx, &emptypb.Empty{})
		if e != nil || state == nil || state.Active || state.InventoryRevision != expected {
			b.finish(c, "UNCONFIRMED", "batch_inventory_changed_stop")
			return
		}
		var price uint32
		for _, i := range state.Items {
			if i != nil && i.SafeJunkSaleEligible && i.Item != nil && proto.Equal(i.Item, item) {
				price = i.UnitSellPriceCopper
				break
			}
		}
		if price == 0 {
			b.finish(c, "BLOCKED", "batch_item_eligibility_changed")
			return
		}
		id := fmt.Sprintf("addon_j_%s_%d", c.nonce, n)
		result, er := b.service.Sell(ctx, &inventoryv1.SellRequest{SessionId: state.SessionId, RequestId: id, ExpectedRevision: state.Revision, ExpectedInventoryRevision: state.InventoryRevision, ExpectedCopperRevision: state.CopperRevision, Source: item, VendorGuid: vendor, ExpectedUnitSellPriceCopper: price})
		result, why := b.wait(ctx, id, result, er)
		if why != "" {
			b.finish(c, "UNCONFIRMED", why)
			return
		}
		expected = result.InventoryRevision
	}
	b.finish(c, "CONFIRMED", fmt.Sprintf("sold_%d_verified_gray_stacks", len(eligible)))
}
func (b *addonBridge) tradeState(ctx context.Context) (*tradev1.State, error) {
	r := &tradev1.State{}
	e := b.service.invoke(ctx, "buddy-trade", "state", &emptypb.Empty{}, r)
	return r, e
}
func (b *addonBridge) give(ctx context.Context, c *addonCommand, state *inventoryv1.State, item *worldv1.BackpackSlot) {
	original := proto.Clone(item).(*worldv1.BackpackSlot)
	t, e := b.tradeState(ctx)
	if e != nil || t.SessionId != state.SessionId || t.Phase != "IDLE" || strings.Contains(t.Reason, "disabled_independent") {
		b.finish(c, "BLOCKED", "normal_joel_trade_unavailable")
		return
	}
	if item.Bag != 0 {
		var dest *worldv1.BackpackSlot
		for slot := uint32(0); slot < 16; slot++ {
			s := addonSlot(state, 0, slot)
			if s != nil && s.ItemGuid == 0 {
				dest = s
				break
			}
		}
		if dest == nil {
			b.finish(c, "BLOCKED", "known_empty_backpack_slot_required")
			return
		}
		id := "addon_g_stage_" + c.nonce
		moved, er := b.service.Move(ctx, &inventoryv1.MoveRequest{SessionId: state.SessionId, RequestId: id, ExpectedRevision: state.Revision, ExpectedInventoryRevision: state.InventoryRevision, Source: addonRef(item), DestinationBag: 0, DestinationSlot: dest.Slot})
		moved, why := b.wait(ctx, id, moved, er)
		if why != "" {
			b.finish(c, "UNCONFIRMED", why)
			return
		}
		item = addonSlot(moved, 0, dest.Slot)
		if item == nil || item.ItemGuid != c.itemGUID || item.Entry != original.Entry || item.StackCount != original.StackCount {
			b.finish(c, "UNCONFIRMED", "staged_item_not_verified")
			return
		}
	}
	currentInventory, inventoryErr := b.service.GetState(ctx, &emptypb.Empty{})
	if inventoryErr != nil || addonUnresolved(currentInventory) || !currentInventory.InventoryKnown || currentInventory.SessionId != state.SessionId {
		b.finish(c, "BLOCKED", "inventory_outcome_or_cleanup_unconfirmed")
		return
	}
	currentItem := addonSlot(currentInventory, 0, item.Slot)
	if !addonSameItem(currentItem, item) {
		b.finish(c, "BLOCKED", "selected_stack_changed_before_trade")
		return
	}
	t, e = b.tradeState(ctx)
	if e != nil || t.SessionId != state.SessionId || t.Phase != "IDLE" {
		b.finish(c, "BLOCKED", "normal_trade_context_changed")
		return
	}
	opened := &tradev1.State{}
	e = b.service.invoke(ctx, "buddy-trade", "open", &tradev1.BoundRequest{SessionId: t.SessionId, Revision: t.Revision}, opened)
	if e != nil {
		b.finish(c, "UNCONFIRMED", "trade_request_not_confirmed_no_retry")
		return
	}
	b.mu.Lock()
	b.queueACKLocked(c.nonce, c.snapshot, "QUEUED", "normal_trade_requested_waiting_for_joel")
	b.mu.Unlock()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		t, e = b.tradeState(ctx)
		if e != nil {
			b.finish(c, "UNCONFIRMED", "trade_state_unconfirmed_no_retry")
			return
		}
		if t.Phase == "OPEN" && t.PartnerGuid == 3 {
			break
		}
		if t.Phase != "REQUESTED" && t.Phase != "INVITED" {
			b.finish(c, "BLOCKED", "normal_trade_not_open")
			return
		}
		select {
		case <-ctx.Done():
			b.finish(c, "UNCONFIRMED", "trade_wait_canceled")
			return
		case <-deadline.C:
			b.finish(c, "BLOCKED", "normal_trade_not_open")
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !addonTradeItemBound(t, state.SessionId, item) {
		b.finish(c, "BLOCKED", "selected_stack_or_trade_session_changed")
		return
	}
	offered := &tradev1.State{}
	e = b.service.invoke(ctx, "buddy-trade", "offer_item", &tradev1.ItemRequest{SessionId: t.SessionId, Revision: t.Revision, BackpackRevision: t.BackpackRevision, BackpackSlot: item.Slot, ExpectedItemGuid: item.ItemGuid, TradeSlot: 0}, offered)
	if e != nil || !addonTradeOfferBound(offered, state.SessionId, item) {
		b.finish(c, "UNCONFIRMED", "trade_offer_unconfirmed_no_retry")
		return
	}
	// Offering is not transfer confirmation. Normal Joel trade review remains.
	b.finish(c, "QUEUED", "item_offered_review_normal_trade_window")
}
