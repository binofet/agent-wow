package buddytrade

import (
	"context"
	"math"
	"sync"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

const ownerGUID = 3

type sessionAPI interface {
	SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error)
	InvokeModule(context.Context, *modv1.InvokeModuleRequest, ...grpc.CallOption) (*modv1.InvokeModuleResponse, error)
}
type Service struct {
	tradev1.UnimplementedBuddyTradeServer
	session                     sessionAPI
	selfGUID                    uint64
	independent                 bool
	opMu                        sync.Mutex
	mu                          sync.Mutex
	state                       *tradev1.State
	now                         func() time.Time
	deadline, renewAt           time.Time
	token                       string // protected by opMu, never returned through status
	pendingBegin, pendingCancel bool
	spell                       uint32
	names                       map[uint32]string
	queried                     map[uint32]bool
	metadata                    map[uint32]itemMetadata
	lookup                      itemLookupRuntime
	junkPending                 *tradev1.JunkDisposal
	junkUntil, junkNext         time.Time
}

func NewService(session sessionAPI, sessionID string, selfGUID uint64) *Service {
	return &Service{session: session, selfGUID: selfGUID, now: time.Now, names: map[uint32]string{}, queried: map[uint32]bool{}, metadata: map[uint32]itemMetadata{}, state: &tradev1.State{SessionId: sessionID, Phase: "IDLE", Limitation: "Joel only; whole backpack stacks; no equipment, bag contents, splitting or enchantments. Own requested offers are transport intentions. Completion requires server confirmation. Opt-in junk disposal removes one full gray miscellaneous junk stack only when all sixteen backpack slots are occupied."}}
}

// NewIndependentService retains own inventory operations without owner trades.
func NewIndependentService(session sessionAPI, sessionID string, selfGUID uint64) *Service {
	s := NewService(session, sessionID, selfGUID)
	s.independent = true
	s.state.Reason = "trading_disabled_independent"
	s.state.Limitation = "Independent mode disables trades. Own wallet, backpack observations and opt-in junk disposal remain available; disposal requires server inventory confirmation."
	return s
}
func (s *Service) invoke(ctx context.Context, module, method string, input, output proto.Message) error {
	if s.session == nil {
		return status.Error(codes.FailedPrecondition, "session unavailable")
	}
	request, err := anypb.New(input)
	if err != nil {
		return err
	}
	response, err := s.session.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: module, Method: method, Request: request})
	if err != nil {
		return err
	}
	if response == nil || response.Result == nil {
		return status.Error(codes.FailedPrecondition, "dependency returned no result")
	}
	return response.Result.UnmarshalTo(output)
}
func (s *Service) send(ctx context.Context, op uint32, payload []byte) error {
	if s.independent {
		switch op {
		case opcode.CMSGInitiateTrade, opcode.CMSGBeginTrade, opcode.CMSGBusyTrade, opcode.CMSGIgnoreTrade, opcode.CMSGAcceptTrade, opcode.CMSGUnacceptTrade, opcode.CMSGSetTradeItem, opcode.CMSGClearTradeItem, opcode.CMSGSetTradeGold:
			return status.Error(codes.FailedPrecondition, "trading is disabled in independent mode")
		}
	}
	if s.session == nil {
		return status.Error(codes.FailedPrecondition, "session unavailable")
	}
	_, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: uint32(op), Payload: payload})
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.state.Reason = "transport_unconfirmed_no_retry"
		return err
	}
	s.state.TransportWrites++
	return nil
}
func (s *Service) snapshot() *tradev1.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.Clone(s.state).(*tradev1.State)
}
func (s *Service) boundLocked(session string, revision uint64) error {
	if session != s.state.SessionId || revision != s.state.Revision {
		return status.Error(codes.FailedPrecondition, "trade session or revision changed")
	}
	return nil
}
func active(phase string) bool {
	return phase == "REQUESTED" || phase == "INVITED" || phase == "OPEN" || phase == "ACCEPT_SENT"
}
func (s *Service) world(ctx context.Context) (*worldv1.SnapshotResponse, error) {
	w := &worldv1.SnapshotResponse{}
	err := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, w)
	if err != nil {
		return nil, err
	}
	if w.Self == nil || w.Self.Guid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "wrong character observation")
	}
	return w, nil
}
func (s *Service) inventory(ctx context.Context) (*worldv1.SnapshotResponse, error) {
	w, err := s.world(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if w.SelfCopperRevision >= s.state.CopperRevision {
		s.state.CopperKnown = w.SelfCopperKnown
		s.state.Copper = w.SelfCopper
		s.state.CopperRevision = w.SelfCopperRevision
	}
	s.state.BackpackKnown = w.BackpackKnown && len(w.Backpack) == 16
	s.state.BackpackRevision = w.BackpackRevision
	s.state.Backpack = nil
	query := []uint32{}
	if s.state.BackpackKnown {
		for _, item := range w.Backpack {
			if item == nil || item.Slot >= 16 || item.Bag != 0 {
				s.state.BackpackKnown = false
				s.state.Backpack = nil
				break
			}
			if item.ItemGuid == 0 {
				continue
			}
			s.state.Backpack = append(s.state.Backpack, &tradev1.Item{Slot: item.Slot, Entry: item.Entry, Count: item.StackCount, ItemGuid: item.ItemGuid, MetadataKnown: item.MetadataKnown, Name: s.names[item.Entry]})
			if item.MetadataKnown && item.Entry != 0 && !s.queried[item.Entry] && len(s.queried) < 128 {
				s.queried[item.Entry] = true
				query = append(query, item.Entry)
			}
		}
	}
	s.mu.Unlock()
	for _, entry := range query {
		if err := s.send(ctx, opcode.CMSGItemQuerySingle, u32(entry)); err != nil {
			break
		}
	}
	return w, nil
}
func (s *Service) GetState(ctx context.Context, _ *emptypb.Empty) (*tradev1.State, error) {
	read, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if _, err := s.inventory(read); err != nil {
		return nil, err
	}
	return s.snapshot(), nil
}
func (s *Service) acquire(ctx context.Context) error {
	lease := &actionsv1.InteractionLease{}
	if err := s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "trade", Token: s.token, LeaseMs: 30000}, lease); err != nil {
		return err
	}
	if lease.Token == "" {
		return status.Error(codes.FailedPrecondition, "trade pause missing token")
	}
	s.token = lease.Token
	s.renewAt = s.now().Add(10 * time.Second)
	return nil
}
func (s *Service) release(ctx context.Context) {
	if s.token == "" {
		return
	}
	token := s.token
	s.token = ""
	_ = s.invoke(ctx, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "trade", Token: token}, &actionsv1.State{})
}
func (s *Service) nearby(ctx context.Context) error {
	w, err := s.world(ctx)
	if err != nil {
		return err
	}
	a := &actionsv1.State{}
	if err := s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, a); err != nil {
		return err
	}
	o, p := w.Owner, a.Position
	if !w.Self.Present || !w.Self.MapKnown || !w.Self.AliveKnown || !w.Self.Alive || o == nil || o.Guid != ownerGUID || !o.Present || !o.AliveKnown || !o.Alive || !o.MapKnown || !o.PositionKnown || p == nil || !p.Known || !p.MapKnown || p.MapId != o.MapId || p.MapId != w.Self.MapId || a.Moving || a.MotionUnconfirmed || a.CastPending {
		return status.Error(codes.FailedPrecondition, "Joel must be alive nearby and Arlen stopped")
	}
	dx, dy, dz := float64(p.X-o.X), float64(p.Y-o.Y), float64(p.Z-o.Z)
	distance := math.Sqrt(dx*dx + dy*dy + dz*dz)
	if math.IsNaN(distance) || math.IsInf(distance, 0) || distance > 10 || (o.MovementFlags&0xff != 0 && o.PositionAgeMs > 1500) {
		return status.Error(codes.FailedPrecondition, "Joel is outside conservative trade range")
	}
	return nil
}
func (s *Service) resetLocked(phase string) {
	s.state.Phase = phase
	s.state.PartnerGuid = ownerGUID
	s.state.PartnerAccepted = false
	s.state.AcceptTransportWritten = false
	s.state.OwnRequestedItems = nil
	s.state.OwnRequestedCopper = 0
	s.state.PartnerItems = nil
	s.state.PartnerCopper = 0
	s.state.Reason = ""
	s.spell = 0
	s.state.Revision++
}
func (s *Service) Open(ctx context.Context, r *tradev1.BoundRequest) (*tradev1.State, error) {
	if s.independent {
		return nil, status.Error(codes.FailedPrecondition, "trading is disabled in independent mode")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	s.mu.Lock()
	err := s.boundLocked(r.SessionId, r.Revision)
	busy := active(s.state.Phase) || s.junkPending != nil
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if busy {
		return nil, status.Error(codes.FailedPrecondition, "trade already active")
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.acquire(call); err != nil {
		return nil, err
	}
	if err := s.nearby(call); err != nil {
		s.release(call)
		return nil, err
	}
	s.mu.Lock()
	s.resetLocked("REQUESTED")
	s.deadline = s.now().Add(20 * time.Second)
	s.mu.Unlock()
	if err := s.send(call, opcode.CMSGInitiateTrade, u64(ownerGUID)); err != nil {
		s.fail("trade_request_write_unconfirmed")
	}
	return s.snapshot(), nil
}
func (s *Service) fail(reason string) {
	s.mu.Lock()
	s.state.Reason = reason
	s.pendingCancel = true
	s.state.Revision++
	s.mu.Unlock()
}
func (s *Service) offerGate(session string, revision uint64) error {
	if s.independent {
		return status.Error(codes.FailedPrecondition, "trading is disabled in independent mode")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.boundLocked(session, revision); err != nil {
		return err
	}
	if s.state.Phase != "OPEN" || s.state.PartnerGuid != ownerGUID || s.state.AcceptTransportWritten {
		return status.Error(codes.FailedPrecondition, "trade is not open for changes")
	}
	return nil
}
func (s *Service) OfferItem(ctx context.Context, r *tradev1.ItemRequest) (*tradev1.State, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if r == nil || r.BackpackSlot >= 16 || r.TradeSlot >= 6 || r.ExpectedItemGuid == 0 {
		return nil, status.Error(codes.InvalidArgument, "one whole backpack stack and transfer slot required")
	}
	if err := s.offerGate(r.SessionId, r.Revision); err != nil {
		return nil, err
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.acquire(call); err != nil {
		return nil, err
	}
	if err := s.nearby(call); err != nil {
		return nil, err
	}
	w, err := s.inventory(call)
	if err != nil {
		return nil, err
	}
	if !w.BackpackKnown || w.BackpackRevision != r.BackpackRevision {
		return nil, status.Error(codes.FailedPrecondition, "backpack changed")
	}
	var selected *worldv1.BackpackSlot
	for _, item := range w.Backpack {
		if item != nil && item.Slot == r.BackpackSlot && item.Bag == 0 && item.ItemGuid == r.ExpectedItemGuid && item.MetadataKnown && item.Entry != 0 && item.StackCount > 0 {
			selected = item
		}
	}
	if selected == nil {
		return nil, status.Error(codes.FailedPrecondition, "stack is not confirmed in this backpack slot")
	}
	s.mu.Lock()
	if err := s.boundLocked(r.SessionId, r.Revision); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	for _, item := range s.state.OwnRequestedItems {
		if item.ItemGuid == selected.ItemGuid || item.Slot == r.TradeSlot {
			s.mu.Unlock()
			return nil, status.Error(codes.FailedPrecondition, "item or trade slot already offered")
		}
	}
	s.state.PartnerAccepted = false
	s.state.Revision++
	s.state.OwnRequestedItems = append(s.state.OwnRequestedItems, &tradev1.Item{Slot: r.TradeSlot, ItemGuid: selected.ItemGuid, Entry: selected.Entry, Count: selected.StackCount, Name: s.names[selected.Entry], MetadataKnown: true})
	s.mu.Unlock()
	if err := s.send(call, opcode.CMSGSetTradeItem, []byte{byte(r.TradeSlot), 255, byte(23 + r.BackpackSlot)}); err != nil {
		s.fail("item_offer_write_unconfirmed")
	}
	return s.snapshot(), nil
}
func (s *Service) OfferGold(ctx context.Context, r *tradev1.GoldRequest) (*tradev1.State, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if r == nil || r.Copper > 2147483647 {
		return nil, status.Error(codes.InvalidArgument, "exact legal copper amount required")
	}
	if err := s.offerGate(r.SessionId, r.Revision); err != nil {
		return nil, err
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.acquire(call); err != nil {
		return nil, err
	}
	if err := s.nearby(call); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if err := s.boundLocked(r.SessionId, r.Revision); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.state.OwnRequestedCopper = r.Copper
	s.state.PartnerAccepted = false
	s.state.Revision++
	s.mu.Unlock()
	if err := s.send(call, opcode.CMSGSetTradeGold, u32(r.Copper)); err != nil {
		s.fail("gold_offer_write_unconfirmed")
	}
	return s.snapshot(), nil
}
func (s *Service) cancelTrade(ctx context.Context) error {
	err := s.send(ctx, opcode.CMSGCancelTrade, nil)
	s.mu.Lock()
	s.state.Phase = "CANCEL_SENT"
	s.state.PartnerAccepted = false
	s.state.Revision++
	s.pendingBegin = false
	s.pendingCancel = false
	s.mu.Unlock()
	s.release(ctx)
	return err
}
func (s *Service) Cancel(ctx context.Context, r *tradev1.BoundRequest) (*tradev1.State, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	s.mu.Lock()
	err := s.boundLocked(r.SessionId, r.Revision)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = s.cancelTrade(call)
	return s.snapshot(), nil
}
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Tick(ctx)
		}
	}
}
func (s *Service) Tick(ctx context.Context) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	s.mu.Lock()
	phase, pendingBegin, pendingCancel, accepted, deadline := s.state.Phase, s.pendingBegin, s.pendingCancel, s.state.PartnerAccepted, s.deadline
	s.mu.Unlock()
	if pendingCancel {
		_ = s.cancelTrade(call)
		return
	}
	if !active(phase) {
		s.junkTick(call)
		return
	}
	if s.independent {
		s.fail("trading_disabled_independent")
		_ = s.cancelTrade(call)
		return
	}
	if !deadline.IsZero() && !s.now().Before(deadline) {
		s.fail("trade_timed_out")
		_ = s.cancelTrade(call)
		return
	}
	if pendingBegin || s.token == "" || !s.now().Before(s.renewAt) {
		if err := s.acquire(call); err != nil {
			s.fail("trade_pause_unavailable")
			_ = s.cancelTrade(call)
			return
		}
	}
	// A trade must yield promptly if an enemy engages either companion. The
	// combat/healing controller resumes as soon as this lease is released.
	wCombat, err := s.world(call)
	if err != nil || !wCombat.Self.AliveKnown || !wCombat.Self.Alive || wCombat.Self.UnitFlags&0x80000 != 0 || (wCombat.Owner != nil && wCombat.Owner.UnitFlags&0x80000 != 0) {
		s.fail("trade_preempted_by_combat_or_unknown_state")
		_ = s.cancelTrade(call)
		return
	}
	if pendingBegin {
		if err := s.nearby(call); err != nil {
			s.fail("trade_partner_not_nearby")
			_ = s.cancelTrade(call)
			return
		}
		s.mu.Lock()
		s.pendingBegin = false
		s.mu.Unlock()
		if err := s.send(call, opcode.CMSGBeginTrade, nil); err != nil {
			s.fail("begin_trade_write_unconfirmed")
		}
		return
	}
	if phase != "OPEN" || !accepted {
		return
	}
	if err := s.nearby(call); err != nil {
		s.fail("trade_partner_not_nearby")
		return
	}
	// Check every offered GUID and entire stack again immediately before consent.
	w, err := s.inventory(call)
	if err != nil {
		s.fail("inventory_unavailable_at_accept")
		return
	}
	s.mu.Lock()
	if s.state.Phase != "OPEN" || !s.state.PartnerAccepted || s.spell != 0 {
		s.mu.Unlock()
		return
	}
	for _, offered := range s.state.OwnRequestedItems {
		found := false
		for _, item := range w.Backpack {
			if item != nil && item.MetadataKnown && item.ItemGuid == offered.ItemGuid && item.Entry == offered.Entry && item.StackCount == offered.Count {
				found = true
			}
		}
		if !w.BackpackKnown || !found {
			s.mu.Unlock()
			s.fail("offered_stack_changed")
			return
		}
	}
	s.state.Phase = "ACCEPT_SENT"
	s.state.PartnerAccepted = false
	s.state.Revision++
	s.mu.Unlock()
	if err := s.send(call, opcode.CMSGAcceptTrade, nil); err != nil {
		s.fail("accept_trade_write_unconfirmed")
		return
	}
	s.mu.Lock()
	s.state.AcceptTransportWritten = true
	s.mu.Unlock()
}
func (s *Service) OnPacket(_ context.Context, p *modv1.WorldPacket) (*emptypb.Empty, error) {
	if p == nil {
		return &emptypb.Empty{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch p.Opcode {
	case uint32(opcode.SMSGTradeStatus):
		code, guid, err := parseStatus(p.Payload)
		if err != nil {
			s.state.MalformedPackets++
			break
		}
		s.state.LastServerStatus = code
		s.state.Revision++
		if s.independent {
			s.state.Phase = "CLOSED"
			s.state.PartnerGuid = 0
			s.state.PartnerAccepted = false
			s.state.AcceptTransportWritten = false
			s.state.Reason = "trading_disabled_independent"
			s.pendingBegin = false
			// Refuse any server-allocated trade without opening or consenting.
			if code == 1 || code == 2 || code == 4 || code == 7 {
				s.pendingCancel = true
			}
			break
		}
		switch code {
		case 1:
			if s.junkPending != nil {
				s.pendingCancel = true
				s.state.Reason = "inventory_mutation_awaits_confirmation"
				break
			}
			if guid != ownerGUID {
				s.pendingCancel = true
				s.state.Reason = "only_Joel_may_trade"
				break
			}
			s.resetLocked("INVITED")
			s.pendingBegin = true
			s.deadline = s.now().Add(20 * time.Second)
		case 2:
			if s.state.PartnerGuid != ownerGUID || !active(s.state.Phase) {
				s.pendingCancel = true
				break
			}
			s.state.Phase = "OPEN"
			s.deadline = s.now().Add(5 * time.Minute)
		case 4:
			if s.state.Phase == "OPEN" && s.state.PartnerGuid == ownerGUID {
				s.state.PartnerAccepted = true
			}
		case 7:
			if active(s.state.Phase) {
				s.state.Phase = "OPEN"
				s.state.PartnerAccepted = false
				s.state.AcceptTransportWritten = false
			}
		case 8:
			if s.state.PartnerGuid == ownerGUID && s.state.Phase == "ACCEPT_SENT" {
				s.state.Phase = "COMPLETE"
				s.state.CompletedTrades++
				s.state.Reason = "server_confirmed_trade_complete"
			} else {
				s.state.Reason = "unexpected_completion_status"
			}
		default:
			// Several server rejection paths only tell the normal client to
			// close its window; they leave TradeData allocated until the client
			// cancels. Send that cleanup once so another trade can start.
			if active(s.state.Phase) {
				s.pendingCancel = true
			}
			s.state.Phase = "CLOSED"
			s.state.PartnerAccepted = false
			s.state.Reason = "server_closed_or_rejected_trade"
			s.pendingBegin = false
		}
	case uint32(opcode.SMSGTradeStatusExtended):
		partner, copper, spell, items, err := parseExtended(p.Payload)
		if err != nil {
			s.state.MalformedPackets++
			break
		}
		if s.independent {
			s.pendingCancel = true
			s.pendingBegin = false
			s.state.Revision++
			s.state.Phase = "CLOSED"
			s.state.PartnerGuid = 0
			s.state.PartnerAccepted = false
			s.state.AcceptTransportWritten = false
			s.state.Reason = "trading_disabled_independent"
			break
		}
		if !active(s.state.Phase) || s.state.PartnerGuid != ownerGUID {
			break
		}
		s.state.Revision++
		s.state.PartnerAccepted = false
		s.state.AcceptTransportWritten = false
		s.state.Phase = "OPEN"
		if partner {
			s.state.PartnerCopper = copper
			s.state.PartnerItems = items
			s.spell = spell
			for _, item := range items {
				if item.Slot == 6 {
					s.spell = 1
				}
			}
		} else {
			// Some server paths echo our side too. If present, that view must
			// agree with the explicit requests before automatic consent.
			matches := copper == s.state.OwnRequestedCopper && spell == 0 && len(items) == len(s.state.OwnRequestedItems)
			for _, item := range items {
				found := false
				for _, requested := range s.state.OwnRequestedItems {
					if item.Slot == requested.Slot && item.Entry == requested.Entry && item.Count == requested.Count {
						found = true
					}
				}
				matches = matches && found
			}
			if !matches {
				s.state.Reason = "unexpected_own_trade_offer"
				s.pendingCancel = true
			}
		}
	case uint32(opcode.SMSGItemQuerySingleResponse):
		s.observeItemLookupLocked(p.Payload)
		entry, name, err := parseItemName(p.Payload)
		if err != nil {
			s.state.MalformedPackets++
			break
		}
		if s.queried[entry] && len(s.names) < 128 {
			s.names[entry] = name
			if metadata, metadataErr := parseItemMetadata(p.Payload); metadataErr == nil && metadata.known {
				s.metadata[entry] = metadata
			}
			for _, item := range s.state.Backpack {
				if item.Entry == entry {
					item.Name = name
				}
			}
		}
	}
	return &emptypb.Empty{}, nil
}
func (s *Service) BeforeLogout(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if active(s.snapshot().Phase) {
		_ = s.cancelTrade(ctx)
	} else {
		s.release(ctx)
	}
	return &emptypb.Empty{}, nil
}
