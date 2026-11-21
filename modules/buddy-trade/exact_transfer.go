package buddytrade

import (
	"context"
	"fmt"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ExactTransfer never chooses a partner, terms, response, retry or acceptance.
// Tick only expires the caller's finite custody; Lua issues each normal input.
func transferTerms(items []*tradev1.Item) bool {
	seen := map[uint32]bool{}
	for _, i := range items {
		if i == nil || i.Slot >= 6 || seen[i.Slot] || i.Entry == 0 || i.Count == 0 {
			return false
		}
		seen[i.Slot] = true
	}
	return len(items) <= 6
}
func sameTransferTerms(a, b []*tradev1.Item) bool {
	if len(a) != len(b) {
		return false
	}
	for _, i := range a {
		found := false
		for _, j := range b {
			if i.Slot == j.Slot && i.Entry == j.Entry && i.Count == j.Count {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func (s *Service) nearbyTransfer(ctx context.Context, partner uint64) error {
	w, err := s.world(ctx)
	if err != nil {
		return err
	}
	a := &actionsv1.State{}
	if err = s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, a); err != nil {
		return err
	}
	var peer *worldv1.PlayerState
	for _, p := range append(append([]*worldv1.PlayerState{w.Owner}, w.NearbyPlayers...), w.Self) {
		if p != nil && p.Guid == partner {
			peer = p
		}
	}
	self, p := w.Self, a.Position
	if a.SessionId != s.state.SessionId || a.SelfGuid != s.selfGUID || self == nil || !self.Present || !self.AliveKnown || !self.Alive || !self.MapKnown || peer == nil || !peer.Present || !peer.AliveKnown || !peer.Alive || !peer.MapKnown || !peer.PositionKnown || p == nil || !p.Known || !p.MapKnown || p.MapId != self.MapId || p.MapId != peer.MapId || a.Moving || a.MotionUnconfirmed || a.CastPending {
		return status.Error(codes.FailedPrecondition, "exact living observed nearby partner and stationary body required")
	}
	dx, dy, dz := float64(p.X-peer.X), float64(p.Y-peer.Y), float64(p.Z-peer.Z)
	d := math.Sqrt(dx*dx + dy*dy + dz*dz)
	if math.IsNaN(d) || math.IsInf(d, 0) || d > 10 {
		return status.Error(codes.FailedPrecondition, "exact partner outside normal trade range")
	}
	return nil
}
func transferSource(w *worldv1.SnapshotResponse, r *tradev1.TransferSource) *worldv1.BackpackSlot {
	if r == nil || r.ItemGuid>>48 != 0x4000 || r.Entry == 0 || r.Count == 0 || r.Bag > 4 || r.Slot >= 36 {
		return nil
	}
	var rows []*worldv1.BackpackSlot
	if r.BackpackRevision != 0 && r.InventoryRevision == 0 && r.Bag == 0 && w.BackpackKnown && w.BackpackRevision == r.BackpackRevision {
		rows = w.Backpack
	} else if r.BackpackRevision == 0 && r.InventoryRevision != 0 && w.InventoryKnown && w.InventoryRevision == r.InventoryRevision {
		for _, b := range w.InventoryBags {
			if b != nil && b.Bag == r.Bag && b.ContentsKnown {
				rows = b.Slots
			}
		}
	}
	for _, i := range rows {
		if i != nil && i.Bag == r.Bag && i.Slot == r.Slot && i.ItemGuid == r.ItemGuid && i.Entry == r.Entry && i.StackCount == r.Count && i.MetadataKnown {
			return i
		}
	}
	return nil
}
func (s *Service) TransferStep(ctx context.Context, r *tradev1.TransferStepRequest) (*tradev1.State, error) {
	if r == nil || r.SelfGuid != s.selfGUID || r.PartnerGuid == 0 || r.PartnerGuid == s.selfGUID || r.PartnerGuid>>48 != 0 || r.TransactionId == "" || len(r.TransactionId) > 128 || r.RequestId == "" || len(r.RequestId) > 128 || !transferTerms(r.OwnItems) || !transferTerms(r.PartnerItems) {
		return nil, status.Error(codes.InvalidArgument, "exact body, partner, transaction, request and legal terms required")
	}
	switch r.Mode {
	case "reserve", "open", "begin", "offer", "accept", "renew", "cancel":
	default:
		return nil, status.Error(codes.InvalidArgument, "unknown exact trade input")
	}
	r = proto.Clone(r).(*tradev1.TransferStepRequest)
	s.opMu.Lock()
	defer s.opMu.Unlock()
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	s.mu.Lock()
	if old := s.transferRequests[r.RequestId]; old != nil {
		same := proto.Equal(old, r)
		s.mu.Unlock()
		if !same {
			return nil, status.Error(codes.FailedPrecondition, "request ID already bound")
		}
		return s.snapshot(), nil
	}
	err := s.boundLocked(r.SessionId, r.Revision)
	x := s.state.ExactTransfer
	start := r.Mode == "reserve" || r.Mode == "open"
	if err == nil {
		if start {
			if x != nil || active(s.state.Phase) || s.state.Phase == "CANCEL_UNCONFIRMED" || s.pendingCancel || s.junkPending != nil {
				err = status.Error(codes.FailedPrecondition, "another trade is active")
			}
		} else if x == nil || x.TransactionId != r.TransactionId || x.PartnerGuid != r.PartnerGuid || !s.now().Before(s.transferUntil) {
			err = status.Error(codes.FailedPrecondition, "current exact transfer required")
		}
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err = call.Err(); err != nil {
		return nil, err
	}
	if r.Mode != "cancel" {
		if err = s.nearbyTransfer(call, r.PartnerGuid); err != nil {
			return nil, err
		}
		if r.LeaseMs == 0 {
			return nil, status.Error(codes.InvalidArgument, "explicit finite lease required")
		}
	}
	var source *worldv1.BackpackSlot
	if r.Mode == "offer" || start && r.Source != nil {
		w, e := s.inventory(call)
		if e != nil {
			return nil, e
		}
		source = transferSource(w, r.Source)
		if source == nil || len(r.OwnItems) != 1 || r.OwnItems[0].Slot != 0 || r.OwnItems[0].Entry != source.Entry || r.OwnItems[0].Count != source.StackCount {
			return nil, status.Error(codes.FailedPrecondition, "exact whole carried stack and terms required")
		}
	}
	s.mu.Lock()
	if err = s.boundLocked(r.SessionId, r.Revision); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if !start {
		if !sameTransferTerms(x.OwnItems, r.OwnItems) || !sameTransferTerms(x.PartnerItems, r.PartnerItems) {
			s.mu.Unlock()
			return nil, status.Error(codes.FailedPrecondition, "reserved terms changed")
		}
	}
	phase := s.state.Phase
	if r.Mode == "begin" && phase != "INVITED" || r.Mode == "offer" && (phase != "OPEN" || len(s.state.OwnRequestedItems) != 0) || r.Mode == "accept" && (phase != "OPEN" || !x.PartnerViewKnown || !sameTransferTerms(s.state.OwnRequestedItems, r.OwnItems) || !sameTransferTerms(s.state.PartnerItems, r.PartnerItems) || s.state.PartnerCopper != 0 || s.state.OwnRequestedCopper != 0 || s.partnerSpell != 0 || s.serviceItem.entry != 0) {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "current observed trade phase and exact terms required")
	}
	if r.Mode == "offer" && !proto.Equal(x.Source, r.Source) {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "reserved source changed")
	}
	s.mu.Unlock()
	if r.Mode == "accept" && x.Source != nil {
		w, e := s.inventory(call)
		if e != nil {
			return nil, e
		}
		if transferSource(w, x.Source) == nil {
			return nil, status.Error(codes.FailedPrecondition, "offered whole stack changed")
		}
	}
	admittedUntil := s.now().Add(time.Duration(r.LeaseMs) * time.Millisecond)
	if r.Mode != "cancel" {
		lease := &actionsv1.InteractionLease{}
		if err = s.invoke(call, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "trade", Token: s.token, LeaseMs: r.LeaseMs}, lease); err != nil {
			return nil, err
		}
		if lease.Token == "" {
			return nil, status.Error(codes.FailedPrecondition, "finite trade custody unavailable")
		}
		s.token = lease.Token
		if err = s.nearbyTransfer(call, r.PartnerGuid); err != nil {
			if start {
				s.release(call)
			}
			return nil, err
		}
		if r.Source != nil {
			w, readErr := s.inventory(call)
			if readErr != nil || transferSource(w, r.Source) == nil {
				if start {
					s.release(call)
				}
				return nil, status.Error(codes.FailedPrecondition, "exact offered stack changed during custody admission")
			}
		}
		if !s.now().Before(admittedUntil) {
			if start {
				s.release(call)
			}
			return nil, status.Error(codes.FailedPrecondition, "finite trade custody expired before input")
		}
	}
	if err = call.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	// Observation callbacks may arrive while custody is acquired. Exact revision
	// fences consent against changed terms, before reserving any packet write.
	if err = s.boundLocked(r.SessionId, r.Revision); err != nil {
		s.mu.Unlock()
		if start {
			s.release(call)
		}
		return nil, err
	}
	if start {
		s.resetLocked("RESERVED")
		s.pendingBegin = false
		s.state.PartnerGuid = r.PartnerGuid
		s.state.ExactTransfer = &tradev1.ExactTransfer{TransactionId: r.TransactionId, PartnerGuid: r.PartnerGuid, Source: r.Source, OwnItems: r.OwnItems, PartnerItems: r.PartnerItems}
		x = s.state.ExactTransfer
	}
	s.transferUntil = admittedUntil
	if s.transferRequests == nil {
		s.transferRequests = map[string]*tradev1.TransferStepRequest{}
	}
	s.transferRequests[r.RequestId] = proto.Clone(r).(*tradev1.TransferStepRequest)
	receipt := &tradev1.TransferStepReceipt{TransactionId: r.TransactionId, RequestId: r.RequestId, Mode: r.Mode, Phase: "reserved"}
	s.state.LastTransferStep = receipt
	var op uint32
	var payload []byte
	switch r.Mode {
	case "open":
		s.state.Phase = "REQUESTED"
		op = opcode.CMSGInitiateTrade
		payload = u64(r.PartnerGuid)
	case "begin":
		op = opcode.CMSGBeginTrade
		s.state.Phase = "BEGIN_SENT"
	case "offer":
		op = opcode.CMSGSetTradeItem
		bag, slot := byte(255), byte(23+r.Source.Slot)
		if r.Source.Bag != 0 {
			bag = byte(18 + r.Source.Bag)
			slot = byte(r.Source.Slot)
		}
		payload = []byte{0, bag, slot}
		s.state.OwnRequestedItems = []*tradev1.Item{{Slot: 0, Entry: source.Entry, Count: source.StackCount, ItemGuid: source.ItemGuid, MetadataKnown: true}}
	case "accept":
		op = opcode.CMSGAcceptTrade
		s.state.Phase = "ACCEPT_SENT"
	case "cancel":
		op = opcode.CMSGCancelTrade
		s.state.Phase = "CANCEL_SENT"
	}
	s.state.Revision++
	if op != 0 {
		receipt.TransportAttempted = true
		receipt.Phase = "transport_unconfirmed"
	}
	s.mu.Unlock()
	if op != 0 {
		err = s.sendTradePacket(call, op, payload, true)
		s.mu.Lock()
		if err != nil {
			receipt.Error = err.Error()
		} else {
			receipt.TransportWritten = true
			receipt.Phase = "transport_written"
			if r.Mode == "accept" {
				s.state.AcceptTransportWritten = true
			}
		}
		s.mu.Unlock()
	}
	if r.Mode == "cancel" {
		s.release(call)
		s.mu.Lock()
		if err != nil {
			s.state.Phase = "CANCEL_UNCONFIRMED"
		}
		s.state.ExactTransfer = nil
		s.state.WaterGiftArmed = false
		s.mu.Unlock()
	}
	return s.snapshot(), nil
}

func (s *Service) exactTransferTick(ctx context.Context) bool {
	s.mu.Lock()
	x := s.state.ExactTransfer
	if x == nil {
		s.mu.Unlock()
		return false
	}
	expired := !s.now().Before(s.transferUntil)
	closed := s.state.Phase == "COMPLETE" || s.state.Phase == "CLOSED"
	bad := s.pendingCancel
	if expired || closed || bad {
		s.state.WaterGiftArmed = false
		s.state.ExactTransfer = nil
		s.pendingCancel = false
		s.state.Revision++
		if expired {
			s.state.Reason = "exact_transfer_lease_expired"
		}
	}
	s.mu.Unlock()
	if expired || bad {
		if err := s.cancelTradeInput(ctx, true); err != nil {
			s.mu.Lock()
			s.state.Phase = "CANCEL_UNCONFIRMED"
			s.mu.Unlock()
		}
	} else if closed {
		s.release(ctx)
	}
	return true
}
func (s *Service) exactTransferPacketLocked(p *modv1.WorldPacket) bool {
	x := s.state.ExactTransfer
	if x == nil {
		return false
	}
	switch p.Opcode {
	case opcode.SMSGTradeStatus:
		code, guid, err := parseStatus(p.Payload)
		if err != nil {
			s.state.MalformedPackets++
			return true
		}
		s.state.LastServerStatus = code
		s.state.Revision++
		switch code {
		case 1:
			if guid != x.PartnerGuid {
				s.pendingCancel = true
				s.state.Reason = "unexpected_exact_trade_partner"
			} else {
				s.state.Phase = "INVITED"
			}
		case 2:
			if s.state.Phase == "REQUESTED" || s.state.Phase == "BEGIN_SENT" || s.state.Phase == "INVITED" {
				s.state.Phase = "OPEN"
			}
		case 4:
			s.state.PartnerAccepted = true
		case 7:
			s.state.Phase = "OPEN"
			s.state.PartnerAccepted = false
			s.state.AcceptTransportWritten = false
		case 8:
			if s.state.Phase == "ACCEPT_SENT" {
				s.state.Phase = "COMPLETE"
				s.state.CompletedTrades++
				s.state.Reason = "server_confirmed_trade_complete"
				s.state.WaterGiftArmed = false
			} else {
				s.state.Reason = "unexpected_completion_status"
			}
		default:
			s.state.Phase = "CLOSED"
			s.state.Reason = "server_closed_or_rejected_trade"
		}
		return true
	case opcode.SMSGTradeStatusExtended:
		v, err := parseExtended(p.Payload)
		if err != nil {
			s.state.MalformedPackets++
			return true
		}
		if s.state.Phase != "OPEN" && s.state.Phase != "ACCEPT_SENT" {
			return true
		}
		s.state.Revision++
		s.state.PartnerAccepted = false
		s.state.AcceptTransportWritten = false
		s.state.Phase = "OPEN"
		if v.partner {
			s.state.PartnerItems = v.items
			s.state.PartnerCopper = v.copper
			s.partnerSpell = v.spell
			s.serviceItem = v.serviceItem
			x.PartnerViewKnown = true
		} else if v.copper != 0 || v.spell != 0 || !sameTransferTerms(v.items, s.state.OwnRequestedItems) {
			s.pendingCancel = true
			s.state.Reason = "unexpected_own_exact_trade_offer"
		}
		return true
	}
	return false
}

func (s *Service) WaterGift(ctx context.Context, r *tradev1.WaterGiftRequest) (*tradev1.State, error) {
	if r == nil || r.ItemEntry == 0 || r.ItemCount == 0 {
		return nil, status.Error(codes.InvalidArgument, "explicit legacy gift terms required")
	}
	id := fmt.Sprintf("legacy:%s:%d", r.SessionId, r.Revision)
	step := &tradev1.TransferStepRequest{SessionId: r.SessionId, SelfGuid: s.selfGUID, Revision: r.Revision, PartnerGuid: r.PartnerGuid, TransactionId: id, RequestId: id, Mode: "reserve", LeaseMs: 20000}
	item := &tradev1.Item{Slot: 0, Entry: r.ItemEntry, Count: r.ItemCount}
	if r.ExpectedItemGuid != 0 {
		step.Mode = "open"
		step.OwnItems = []*tradev1.Item{item}
		slot := r.SourceSlot
		if r.BackpackRevision != 0 {
			slot = r.BackpackSlot
		}
		step.Source = &tradev1.TransferSource{Bag: r.SourceBag, Slot: slot, ItemGuid: r.ExpectedItemGuid, Entry: r.ItemEntry, Count: r.ItemCount, BackpackRevision: r.BackpackRevision, InventoryRevision: r.InventoryRevision}
	} else {
		step.PartnerItems = []*tradev1.Item{item}
	}
	out, err := s.TransferStep(ctx, step)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.state.ExactTransfer != nil {
		s.state.WaterGiftArmed = true
		s.state.Reason = "legacy_gift_reserved_requires_current_Lua_inputs"
	}
	s.mu.Unlock()
	out = s.snapshot()
	return out, nil
}
