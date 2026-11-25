package buddyloot

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type windowRequest struct {
	request *lootv1.TakeLootRequest
	receipt *lootv1.LootMutation
}

func (s *Service) observeWindowMutation() {
	if s.windowRequest == nil {
		return
	}
	r := s.windowRequest.receipt
	binding, _, _ := s.policyWindowBinding()
	if r.Terminal || r.OpenRequestId != binding {
		return
	}
	if r.Kind == "money" {
		r.MoneyCleared = s.moneyCleared
		r.Terminal = r.MoneyCleared
	} else {
		r.SlotRemoved, r.ItemPushObserved = s.removed, s.pushed
		r.Terminal = r.SlotRemoved && r.ItemPushObserved
	}
	if r.Terminal && s.state.Phase != "waiting_release" && s.cleanup.source == 0 {
		s.state.Phase = "window_ready"
	}
}
func (s *Service) windowObservation() *lootv1.LootWindow {
	binding, source, kind := s.policyWindowBinding()
	if source == 0 {
		return nil
	}
	s.observeWindowMutation()
	out := &lootv1.LootWindow{SourceGuid: source, SourceKind: kind, OpenRequestId: binding,
		Revision: s.windowRevision, Observed: s.window.guid == source, Money: s.window.gold, MoneyRequested: s.moneySent, MoneyCleared: s.moneyCleared}
	if kind == "corpse" {
		out.CorpseGuid = source
	} else {
		out.BoundQuestId = s.state.LastGather.QuestId
		out.BoundItemId = s.state.LastGather.ItemId
	}
	for _, item := range s.window.items {
		out.Slots = append(out.Slots, &lootv1.LootSlot{Slot: uint32(item.slot), ItemId: item.id, Count: item.count, SlotType: uint32(item.slotType), Removed: s.removedSlots[item.slot]})
	}
	if s.windowRequest != nil && s.windowRequest.receipt.OpenRequestId == out.OpenRequestId {
		out.Mutation = proto.Clone(s.windowRequest.receipt).(*lootv1.LootMutation)
	}
	return out
}
func (s *Service) TakeMoney(ctx context.Context, r *lootv1.TakeLootRequest) (*lootv1.LootMutation, error) {
	return s.takeWindow(ctx, r, true)
}
func (s *Service) TakeSlot(ctx context.Context, r *lootv1.TakeLootRequest) (*lootv1.LootMutation, error) {
	return s.takeWindow(ctx, r, false)
}
func (s *Service) takeWindow(ctx context.Context, r *lootv1.TakeLootRequest, money bool) (*lootv1.LootMutation, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetCorpseGuid() == 0 && r.GetSourceGuid() == 0 || r.GetCorpseGuid() != 0 && r.GetSourceGuid() != 0 || r.GetOpenRequestId() == "" || r.GetWindowRevision() == 0 || r.GetRequestId() == "" ||
		!money && (r.Slot > 255 || r.ExpectedItemId == 0 || r.ExpectedCount == 0 || r.ExpectedInventoryRevision == 0) ||
		money && (r.Slot != 0 || r.ExpectedItemId != 0 || r.ExpectedCount != 0) {
		return nil, status.Error(codes.InvalidArgument, "exact current window, source, request and selected item facts required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || r.SessionId != s.state.SessionId || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current owned session required")
	}
	kind := "slot"
	if money {
		kind = "money"
	}
	s.observeWindowMutation()
	if prior := s.windowRequests[r.RequestId]; prior != nil {
		if prior.receipt.Kind != kind || !proto.Equal(prior.request, r) {
			return nil, status.Error(codes.FailedPrecondition, "request ID binds different loot mutation")
		}
		return proto.Clone(prior.receipt).(*lootv1.LootMutation), nil
	}
	binding, source, sourceKind := s.policyWindowBinding()
	if sourceKind == "quest_container" && r.SourceGuid != source {
		return nil, status.Error(codes.FailedPrecondition, "quest-container source GUID must use source_guid")
	}
	if binding == "" || r.OpenRequestId != binding || takeWindowSource(r) != source || s.window.guid != source || s.windowRevision != r.WindowRevision ||
		s.token == "" || s.cleanup.source != 0 || s.state.Phase == "waiting_release" || s.state.Phase == "release_observed" || s.hasUnsettledLootMutation() {
		return nil, status.Error(codes.FailedPrecondition, "exact observed window with settled prior mutation required")
	}
	selected := -1
	if money {
		if s.window.gold == 0 || s.moneySent {
			return nil, status.Error(codes.FailedPrecondition, "unrequested observed money required")
		}
	} else {
		for i, item := range s.items {
			if uint32(item.slot) == r.Slot && item.id == r.ExpectedItemId && item.count == r.ExpectedCount && !s.removedSlots[item.slot] && (item.slotType == 0 || item.slotType == 4) {
				selected = i
				break
			}
		}
		if selected < 0 {
			return nil, status.Error(codes.FailedPrecondition, "exact server-permitted unremoved slot required")
		}
	}
	lease := &actionsv1.InteractionLease{}
	if err := s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "loot", Token: s.token, LeaseMs: 15000}, lease); err != nil || lease.Token != s.token || !stoppedLootLease(lease.State) {
		return nil, status.Error(codes.FailedPrecondition, "exact owned stopped loot custody required")
	}
	w := &worldv1.SnapshotResponse{}
	if err := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, w); err != nil {
		return nil, err
	}
	if w.GetSelf().GetGuid() != s.selfGUID || !money && (!w.InventoryKnown || w.InventoryRevision != r.ExpectedInventoryRevision) || ctx.Err() != nil {
		return nil, status.Error(codes.FailedPrecondition, "current body and selected inventory revision required")
	}
	if s.questGather && (!s.gatherVisitCurrent(w) || !s.boundHarvestRecipe().quest(w, s.selfGUID, false) || !s.boundHarvestRecipe().objectCurrent(w, lease.State, source)) {
		return nil, status.Error(codes.FailedPrecondition, "current exact bound quest container required")
	}
	if !money && s.unconfirmedItems[r.ExpectedItemId] {
		counts, known := completeOwnItemCounts(w, s.selfGUID)
		if !known {
			return nil, status.Error(codes.FailedPrecondition, "complete item counts required for an earlier ambiguous item identity")
		}
		s.itemCountBefore, s.itemCountKnown = counts[r.ExpectedItemId], true
	}
	receipt := &lootv1.LootMutation{SessionId: r.SessionId, SelfGuid: r.SelfGuid, CorpseGuid: r.CorpseGuid, SourceGuid: r.SourceGuid, OpenRequestId: r.OpenRequestId, WindowRevision: r.WindowRevision, RequestId: r.RequestId, Kind: kind, Slot: r.Slot, ItemId: r.ExpectedItemId, Count: r.ExpectedCount, TransportAttempted: true}
	pending := &windowRequest{request: proto.Clone(r).(*lootv1.TakeLootRequest), receipt: receipt}
	if s.windowRequests == nil {
		s.windowRequests = map[string]*windowRequest{}
	}
	s.windowRequests[r.RequestId] = pending
	s.windowRequest = pending
	code := uint32(opcode.CMSGLootMoney)
	var payload []byte
	if money {
		s.moneySent, s.moneyCleared = true, false
		s.state.Phase = "waiting_money"
	} else {
		code = opcode.CMSGAutostoreLootItem
		payload = []byte{byte(r.Slot)}
		s.index = selected
		s.removed, s.pushed = false, false
		s.itemRequested = true
		s.inventoryWriteRevision = w.InventoryRevision
		s.state.Phase = "waiting_item"
	}
	_, err := s.session.SendPacket(modcb.ForwardPolicySpell(ctx), &modv1.SendPacketRequest{Opcode: code, Payload: payload})
	if err != nil {
		if modcb.PacketWasNotDispatched(err) {
			receipt.TransportAttempted = false
			receipt.Terminal = true
			if money {
				s.moneySent = false
			} else {
				s.itemRequested = false
			}
			s.state.Phase = "window_ready"
			return proto.Clone(receipt).(*lootv1.LootMutation), err
		}
		s.state.Reason = "loot_mutation_transport_unconfirmed_no_retry"
	} else {
		receipt.TransportWritten = true
		s.state.TransportWrites++
	}
	return proto.Clone(receipt).(*lootv1.LootMutation), nil
}
