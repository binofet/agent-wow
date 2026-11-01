package buddysupplies

import (
	"context"
	"math"
	"regexp"
	"sort"
	"sync"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	suppliesv1 "github.com/agent-wow/agent-wow/modules/buddy-supplies/api"
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

const limitation = "Finite explicit starter purchases and exact owned fishing-pole swaps only. Purchase confirmation requires server acknowledgement, exact inventory increase and exact copper debit. Tools are checked in the backpack, main hand and this module's confirmed purchase history; other bags and banks are unobserved. No grants, selling, automatic restoration or retry after an ambiguous write. Vendor distance uses the stopped client position, which may be an estimate. A new module cannot reconstruct forgotten equipment history."

var requestPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type sessionAPI interface {
	SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error)
	InvokeModule(context.Context, *modv1.InvokeModuleRequest, ...grpc.CallOption) (*modv1.InvokeModuleResponse, error)
}
type operation struct {
	request                  proto.Message
	receipt                  *suppliesv1.Operation
	baseline                 worldView
	token                    string
	ctx                      context.Context
	cancel                   context.CancelFunc
	written, releaseReserved bool
	slot                     uint32
}
type Service struct {
	suppliesv1.UnimplementedBuddySuppliesServer
	mu             sync.Mutex
	sendMu         sync.Mutex
	observeGate    chan struct{}
	session        sessionAPI
	selfGUID       uint64
	now            func() time.Time
	state          *suppliesv1.State
	view           worldView
	viewKnown      bool
	combatObserved bool
	actions        *actionsv1.State
	loot           *lootv1.State
	trade          *tradev1.State
	professions    *professionsv1.State
	metadata       map[uint32]*tradev1.ItemDetails
	requests       map[string]*operation
	active, last   *operation
	offerUntil     time.Time
	queryNPC       uint64
	queryWait      chan struct{}
	queryReserved  bool
	closed         bool
}

func NewService(session sessionAPI, sessionID string, selfGUID uint64) *Service {
	return &Service{session: session, selfGUID: selfGUID, now: time.Now, observeGate: make(chan struct{}, 1), metadata: map[uint32]*tradev1.ItemDetails{}, requests: map[string]*operation{}, state: &suppliesv1.State{SessionId: sessionID, SelfGuid: selfGUID, Revision: 1, Limitation: limitation}}
}
func fail(reason string) error { return status.Error(codes.FailedPrecondition, reason) }
func (s *Service) invoke(ctx context.Context, module, method string, input, output proto.Message) error {
	if s.session == nil {
		return status.Error(codes.Unavailable, "session_callback_unavailable")
	}
	raw, e := anypb.New(input)
	if e != nil {
		return e
	}
	r, e := s.session.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: module, Method: method, Request: raw})
	if e != nil {
		return e
	}
	if r == nil || r.Result == nil {
		return status.Error(codes.Unavailable, "module_result_missing")
	}
	return r.Result.UnmarshalTo(output)
}
func (s *Service) snapshotLocked() *suppliesv1.State {
	r := proto.Clone(s.state).(*suppliesv1.State)
	if r.Offer != nil {
		r.Offer.ExpiresInMs = 0
		if d := s.offerUntil.Sub(s.now()); d > 0 {
			r.Offer.ExpiresInMs = uint64(d.Milliseconds())
		}
	}
	return r
}
func (s *Service) snapshot() *suppliesv1.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}
func (s *Service) observe(ctx context.Context) error {
	select {
	case s.observeGate <- struct{}{}:
		defer func() { <-s.observeGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	w := &worldv1.SnapshotResponse{}
	e := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, w)
	combat := e == nil && w.Self != nil && w.Self.Guid == s.selfGUID && ((w.Self.UnitFlagsKnown && w.Self.UnitFlags&0x80000 != 0) || (w.Owner != nil && w.Owner.Present && w.Owner.UnitFlagsKnown && w.Owner.UnitFlags&0x80000 != 0))
	v := worldView{}
	if e == nil {
		v, e = validateWorld(w, s.selfGUID)
	}
	a, l, t, p := &actionsv1.State{}, &lootv1.State{}, &tradev1.State{}, &professionsv1.State{}
	for _, dep := range []struct {
		module, method string
		output         proto.Message
	}{{"buddy-actions", "status", a}, {"buddy-loot", "state", l}, {"buddy-trade", "state", t}, {"buddy-professions", "state", p}} {
		if e == nil {
			e = s.invoke(ctx, dep.module, dep.method, &emptypb.Empty{}, dep.output)
		}
	}
	s.mu.Lock()
	if e == nil && (v.backpackRevision < s.state.BackpackRevision || v.skillsRevision < s.state.SkillsRevision || v.copperRevision < s.state.CopperRevision) {
		e = fail("actual_world_revision_regressed")
	}
	if e == nil && (t.SessionId != s.state.SessionId || p.SessionId != s.state.SessionId || p.SelfGuid != s.selfGUID || l.SessionId != s.state.SessionId) {
		e = fail("dependency_body_identity_mismatch")
	}
	old := proto.Clone(s.state).(*suppliesv1.State)
	s.viewKnown = e == nil
	s.combatObserved = combat
	s.state.BlockedReason = ""
	if e != nil {
		s.state.BlockedReason = "observations_unavailable"
		s.state.BackpackKnown = false
		s.state.CopperKnown = false
		s.state.SkillsKnown = false
	} else {
		s.view, s.actions, s.loot, s.trade, s.professions = v, a, l, t, p
		s.state.BackpackKnown = true
		s.state.BackpackRevision = v.backpackRevision
		s.state.Backpack = w.Backpack
		s.state.CopperKnown = true
		s.state.Copper = v.copper
		s.state.CopperRevision = v.copperRevision
		s.state.SkillsKnown = true
		s.state.SkillsRevision = v.skillsRevision
		s.state.Mainhand = w.Self.Mainhand
		s.evidenceLocked(v)
	}
	if !proto.Equal(old, s.state) {
		s.state.Revision++
	}
	ids := map[uint32]bool{}
	if e == nil {
		for _, b := range v.bag {
			if b.ItemGuid != 0 {
				ids[b.Entry] = true
			}
		}
		if v.self.Mainhand.ItemGuid != 0 {
			ids[v.self.Mainhand.Entry] = true
		}
		if s.state.Offer != nil {
			for _, i := range s.state.Offer.Items {
				if _, ok := catalogLookup(i.Entry); ok {
					ids[i.Entry] = true
				}
			}
		}
	}
	query := []uint32{}
	for id := range ids {
		if s.metadata[id] == nil {
			query = append(query, id)
		}
	}
	sort.Slice(query, func(i, j int) bool { return query[i] < query[j] })
	s.mu.Unlock()
	if e != nil {
		return e
	}
	for len(query) > 0 {
		n := len(query)
		if n > 6 {
			n = 6
		}
		batch := query[:n]
		query = query[n:]
		r := &tradev1.LookupItemsResponse{}
		if er := s.invoke(ctx, "buddy-trade", "lookup_items", &tradev1.LookupItemsRequest{ItemIds: batch}, r); er != nil {
			return er
		}
		if r.SessionId != s.state.SessionId || len(r.Items) > len(batch) {
			return fail("metadata_body_or_result_bound_mismatch")
		}
		allowed := map[uint32]bool{}
		for _, id := range batch {
			allowed[id] = true
		}
		seen := map[uint32]bool{}
		for _, i := range r.Items {
			if i == nil || !allowed[i.ItemId] || seen[i.ItemId] {
				return fail("metadata_result_identity_mismatch")
			}
			seen[i.ItemId] = true
		}
		s.mu.Lock()
		for _, i := range r.Items {
			if i.Available && !i.Pending {
				s.metadata[i.ItemId] = proto.Clone(i).(*tradev1.ItemDetails)
				s.state.Revision++
			}
		}
		s.mu.Unlock()
	}
	return nil
}
func (s *Service) GetState(ctx context.Context, _ *emptypb.Empty) (*suppliesv1.State, error) {
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = s.observe(call)
	return s.snapshot(), nil
}
func vendors(v worldView) ([]*worldv1.CreatureState, error) {
	if len(v.raw.NearbyCreatures) > 32 || len(v.raw.QuestGivers) > 64 {
		return nil, fail("vendor_observation_bound_exceeded")
	}
	rows := map[uint64]*worldv1.CreatureState{}
	for _, group := range [][]*worldv1.CreatureState{v.raw.NearbyCreatures, v.raw.QuestGivers, {v.raw.RequestedCreature}} {
		for _, c := range group {
			if c == nil || c.NpcFlags&0x80 == 0 {
				continue
			}
			if old := rows[c.Guid]; old != nil && !proto.Equal(old, c) {
				return nil, fail("vendor_observations_disagree")
			}
			rows[c.Guid] = c
		}
	}
	out := []*worldv1.CreatureState{}
	for _, c := range rows {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Guid < out[j].Guid })
	return out, nil
}
func (s *Service) Discover(ctx context.Context, _ *emptypb.Empty) (*suppliesv1.Discovery, error) {
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	e := s.observe(call)
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &suppliesv1.Discovery{State: s.snapshotLocked()}
	if e != nil {
		return r, nil
	}
	rows, e := vendors(s.view)
	if e != nil {
		return nil, e
	}
	for _, c := range rows {
		reason := idleGate(s.view, s.actions, s.loot, s.trade, s.professions, s.state.SessionId, false)
		if reason == "" {
			reason = vendorGate(s.view, s.actions, c)
		}
		d := float64(0)
		if s.actions != nil && s.actions.Position != nil {
			dx, dy, dz := float64(c.X-s.actions.Position.X), float64(c.Y-s.actions.Position.Y), float64(c.Z-s.actions.Position.Z)
			d = distance(dx, dy, dz)
		}
		r.Nearby = append(r.Nearby, &suppliesv1.Vendor{Creature: proto.Clone(c).(*worldv1.CreatureState), Interactable: reason == "", BlockedReason: reason, Distance: d})
	}
	return r, nil
}
func (s *Service) vendorLocked(guid uint64) string {
	rows, e := vendors(s.view)
	if e != nil {
		return "vendor_observations_disagree"
	}
	for _, c := range rows {
		if c.Guid == guid {
			return vendorGate(s.view, s.actions, c)
		}
	}
	return "exact_vendor_not_observed"
}
func (s *Service) acquire(ctx context.Context) (string, error) {
	r := &actionsv1.InteractionLease{}
	e := s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "trade", LeaseMs: 20000}, r)
	if e != nil {
		return "", e
	}
	if r.Token == "" {
		return "", fail("fresh_trade_lease_token_missing")
	}
	return r.Token, nil
}
func (s *Service) release(token string) bool {
	if token == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := &actionsv1.State{}
	return s.invoke(ctx, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "trade", Token: token}, r) == nil
}
func (s *Service) List(ctx context.Context, r *suppliesv1.ListRequest) (result *suppliesv1.State, err error) {
	if r == nil || r.NpcGuid == 0 {
		return nil, status.Error(codes.InvalidArgument, "exact_vendor_required")
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e := s.observe(call); e != nil {
		return nil, e
	}
	s.mu.Lock()
	if r.SessionId != s.state.SessionId || s.closed || s.state.Active || s.unresolvedLocked() {
		s.mu.Unlock()
		return nil, fail("body_inactive_or_operation_unresolved")
	}
	reason := idleGate(s.view, s.actions, s.loot, s.trade, s.professions, s.state.SessionId, false)
	if reason == "" {
		reason = s.vendorLocked(r.NpcGuid)
	}
	if reason != "" {
		s.mu.Unlock()
		return nil, fail(reason)
	}
	s.state.Active = true
	s.queryNPC = r.NpcGuid
	s.queryWait = make(chan struct{}, 1)
	wait := s.queryWait
	s.state.Revision++
	s.mu.Unlock()
	token, e := s.acquire(call)
	defer func() {
		released := s.release(token)
		s.mu.Lock()
		s.queryNPC = 0
		s.queryReserved = false
		s.queryWait = nil
		s.state.Active = false
		if !released && token != "" {
			s.state.BlockedReason = "list_lease_release_unconfirmed"
		}
		s.state.Revision++
		if result != nil {
			result = s.snapshotLocked()
		}
		s.mu.Unlock()
	}()
	if e != nil {
		return nil, e
	}
	if e = s.observe(call); e != nil {
		return nil, e
	}
	s.mu.Lock()
	reason = idleGate(s.view, s.actions, s.loot, s.trade, s.professions, s.state.SessionId, true)
	if reason == "" {
		reason = s.vendorLocked(r.NpcGuid)
	}
	if s.closed {
		reason = "module_closing"
	}
	if reason == "" {
		s.queryReserved = true
		s.state.Offer = nil
		s.offerUntil = time.Time{}
	}
	s.mu.Unlock()
	if reason != "" {
		return nil, fail(reason)
	}
	s.sendMu.Lock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		s.sendMu.Unlock()
		return nil, fail("module_closing")
	}
	_, e = s.session.SendPacket(call, &modv1.SendPacketRequest{Opcode: opcode.CMSGListInventory, Payload: listInventoryPacket(r.NpcGuid)})
	s.sendMu.Unlock()
	if e != nil {
		return nil, status.Error(codes.Unavailable, "vendor_list_transport_unconfirmed")
	}
	s.mu.Lock()
	s.state.TransportWrites++
	s.mu.Unlock()
	select {
	case <-wait:
	case <-call.Done():
		return s.snapshot(), nil
	}
	_ = s.observe(call)
	return s.snapshot(), nil
}
func (s *Service) unresolvedLocked() bool {
	return s.last != nil && (s.last.receipt.Status == "OUTCOME_UNCONFIRMED" || !s.last.receipt.LeaseReleased && s.last.token != "")
}
func (s *Service) reserve(id, kind, session string, revision uint64, request proto.Message) (*operation, *suppliesv1.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.requests[id]; old != nil {
		if old.receipt.Kind != kind || !proto.Equal(old.request, request) {
			return nil, nil, status.Error(codes.AlreadyExists, "request_id_was_used_for_another_operation")
		}
		r := s.snapshotLocked()
		r.Operation = proto.Clone(old.receipt).(*suppliesv1.Operation)
		return nil, r, nil
	}
	if !requestPattern.MatchString(id) {
		return nil, nil, status.Error(codes.InvalidArgument, "bounded_request_id_required")
	}
	if s.closed || session != s.state.SessionId || revision != s.state.Revision || s.state.Active || s.unresolvedLocked() {
		return nil, nil, fail("body_revision_or_idle_state_changed")
	}
	if len(s.requests) >= 128 {
		return nil, nil, status.Error(codes.ResourceExhausted, "request_history_limit_reached")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	op := &operation{request: proto.Clone(request), ctx: ctx, cancel: cancel, receipt: &suppliesv1.Operation{RequestId: id, Kind: kind, Status: "REQUESTED", Reason: "reserved_before_packet"}}
	// These fields bind the durable request even when a preflight blocks. They
	// describe intent until the independent receipt flags prove a server change.
	switch r := request.(type) {
	case *suppliesv1.BuyRequest:
		op.receipt.NpcGuid = r.NpcGuid
		op.receipt.ItemId = r.ItemId
		op.receipt.Count = r.Count
		cost := uint64(r.ExpectedPriceCopper) * uint64(r.Count)
		if cost <= math.MaxUint32 {
			op.receipt.CostCopper = uint32(cost)
		}
		op.receipt.BaselineBackpackRevision = r.ExpectedBackpackRevision
		op.receipt.BaselineCopperRevision = r.ExpectedCopperRevision
	case *suppliesv1.EquipmentRequest:
		op.receipt.ItemGuid = r.ItemGuid
		op.receipt.OriginalWeaponGuid = r.OriginalWeaponGuid
		op.receipt.Count = 1
		op.receipt.BaselineBackpackRevision = r.ExpectedBackpackRevision
		if kind == "restore" {
			op.receipt.ItemId = s.state.OriginalWeaponEntry
		}
		op.receipt.OriginalWeaponEntry = s.state.OriginalWeaponEntry
	}
	s.requests[id] = op
	s.active = op
	s.last = op
	s.state.Active = true
	s.state.Operation = op.receipt
	s.state.Revision++
	return op, nil, nil
}
func (s *Service) finish(op *operation, state, reason string) {
	s.mu.Lock()
	if s.active != op {
		s.mu.Unlock()
		return
	}
	if op.receipt.Status != "CONFIRMED" && op.receipt.Status != "SERVER_REJECTED" {
		op.receipt.Status = state
		op.receipt.Reason = reason
	}
	if op.releaseReserved {
		s.mu.Unlock()
		return
	}
	op.releaseReserved = true
	token := op.token
	s.mu.Unlock()
	released := token == "" || s.release(token)
	s.mu.Lock()
	op.receipt.LeaseReleased = released
	s.state.Active = false
	s.active = nil
	s.state.Revision++
	s.mu.Unlock()
	op.cancel()
}
func (s *Service) Buy(ctx context.Context, r *suppliesv1.BuyRequest) (*suppliesv1.State, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "buy_request_required")
	}
	op, old, e := s.reserve(r.RequestId, "buy", r.SessionId, r.ExpectedRevision, r)
	if e != nil || old != nil {
		return old, e
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if e = s.observe(call); e != nil {
		s.finish(op, "BLOCKED", "observations_unavailable")
		return s.snapshot(), nil
	}
	s.mu.Lock()
	reason := s.buyGateLocked(r, false)
	if reason == "" {
		op.baseline = s.view
		op.receipt.NpcGuid = r.NpcGuid
		op.receipt.ItemId = r.ItemId
		op.receipt.Count = r.Count
		op.receipt.CostCopper = r.ExpectedPriceCopper * r.Count
		op.receipt.BaselineBackpackRevision = s.view.backpackRevision
		op.receipt.BaselineCopperRevision = s.view.copperRevision
		op.slot = r.VendorSlot
	}
	s.mu.Unlock()
	if reason != "" {
		s.finish(op, "BLOCKED", reason)
		return s.snapshot(), nil
	}
	if !s.prepareWrite(call, op, func() string { return s.buyGateLocked(r, true) }) {
		return s.snapshot(), nil
	}
	go s.run(op, opcode.CMSGBuyItem, buyItemPacket(r.NpcGuid, r.VendorSlot, r.ItemId, r.Count))
	return s.snapshot(), nil
}
func (s *Service) buyGateLocked(r *suppliesv1.BuyRequest, leased bool) string {
	if !s.viewKnown {
		return "actual_observations_required"
	}
	if reason := idleGate(s.view, s.actions, s.loot, s.trade, s.professions, s.state.SessionId, leased); reason != "" {
		return reason
	}
	if reason := s.vendorLocked(r.NpcGuid); reason != "" {
		return reason
	}
	if r.ExpectedBackpackRevision != s.view.backpackRevision || r.ExpectedCopperRevision != s.view.copperRevision || r.ExpectedSkillsRevision != s.view.skillsRevision {
		return "backpack_wallet_or_skills_changed"
	}
	spec, ok := catalogLookup(r.ItemId)
	if !ok {
		return "item_outside_fixed_catalog"
	}
	if r.Count != 1 || r.MaxSpendCopper == 0 || r.MaxSpendCopper > 10000 {
		return "bounded_units_and_budget_required"
	}
	if spec.oncePerCharacter && (r.Count != 1 || s.view.counts[r.ItemId] != 0 || s.view.self.Mainhand.Entry == r.ItemId) {
		return "tool_is_already_owned_or_count_is_not_one"
	}
	if spec.oncePerCharacter {
		for _, old := range s.requests {
			if old.receipt.Kind == "buy" && old.receipt.ItemId == r.ItemId && old.receipt.Status == "CONFIRMED" {
				return "tool_purchase_already_confirmed_in_this_body"
			}
		}
	}
	if s.view.free == 0 {
		return "known_empty_backpack_slot_required"
	}
	i := s.metadata[r.ItemId]
	if reason := validateCatalogMetadata(i, spec); reason != "" {
		return reason
	}
	if reason := itemRequirements(s.view, i); reason != "" {
		return reason
	}
	o := s.state.Offer
	if o == nil || o.Revision != r.OfferRevision || o.NpcGuid != r.NpcGuid || !s.now().Before(s.offerUntil) {
		return "fresh_exact_vendor_offer_required"
	}
	for _, v := range o.Items {
		if v.Slot != r.VendorSlot {
			continue
		}
		if v.Entry != r.ItemId || v.BuyCount != 1 || v.ExtendedCost != 0 || v.PriceCopper != r.ExpectedPriceCopper {
			return "vendor_item_or_price_changed"
		}
		if v.PriceCopper == 0 {
			return "zero_price_purchase_is_outside_exact_debit_proof"
		}
		cost := uint64(v.PriceCopper) * uint64(r.Count)
		if cost > 10000 || cost > uint64(r.MaxSpendCopper) || cost > uint64(s.view.copper) {
			return "purchase_exceeds_actual_wallet_or_budget"
		}
		if v.AvailableCount >= 0 && uint32(v.AvailableCount) < r.Count {
			return "vendor_stock_insufficient"
		}
		return ""
	}
	return "vendor_slot_missing"
}
func (s *Service) Equip(ctx context.Context, r *suppliesv1.EquipmentRequest) (*suppliesv1.State, error) {
	return s.equipment(ctx, r, false)
}
func (s *Service) Restore(ctx context.Context, r *suppliesv1.EquipmentRequest) (*suppliesv1.State, error) {
	return s.equipment(ctx, r, true)
}
func (s *Service) equipment(ctx context.Context, r *suppliesv1.EquipmentRequest, restore bool) (*suppliesv1.State, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "equipment_request_required")
	}
	kind := "equip"
	if restore {
		kind = "restore"
	}
	op, old, e := s.reserve(r.RequestId, kind, r.SessionId, r.ExpectedRevision, r)
	if e != nil || old != nil {
		return old, e
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if e = s.observe(call); e != nil {
		s.finish(op, "BLOCKED", "observations_unavailable")
		return s.snapshot(), nil
	}
	s.mu.Lock()
	reason := s.equipmentGateLocked(r, restore, false)
	if reason == "" {
		op.baseline = s.view
		op.slot = r.BackpackSlot
		op.receipt.ItemGuid = r.ItemGuid
		op.receipt.ItemId = s.view.bag[r.BackpackSlot].Entry
		op.receipt.Count = 1
		op.receipt.OriginalWeaponGuid = r.OriginalWeaponGuid
		op.receipt.OriginalWeaponEntry = s.view.self.Mainhand.Entry
		if restore || s.state.RestoreRequired {
			op.receipt.OriginalWeaponEntry = s.state.OriginalWeaponEntry
		}
		op.receipt.BaselineBackpackRevision = s.view.backpackRevision
	}
	s.mu.Unlock()
	if reason != "" {
		s.finish(op, "BLOCKED", reason)
		return s.snapshot(), nil
	}
	if !s.prepareWrite(call, op, func() string { return s.equipmentGateLocked(r, restore, true) }) {
		return s.snapshot(), nil
	}
	go s.run(op, opcode.CMSGAutoequipItemSlot, autoEquipItemSlotPacket(r.ItemGuid, 15))
	return s.snapshot(), nil
}
func (s *Service) equipmentGateLocked(r *suppliesv1.EquipmentRequest, restore, leased bool) string {
	if !s.viewKnown {
		return "actual_observations_required"
	}
	if reason := idleGate(s.view, s.actions, s.loot, s.trade, s.professions, s.state.SessionId, leased); reason != "" {
		return reason
	}
	if r.ExpectedBackpackRevision != s.view.backpackRevision || r.ExpectedSkillsRevision != s.view.skillsRevision {
		return "backpack_or_skills_changed"
	}
	if r.BackpackSlot >= 16 {
		return "exact_backpack_slot_required"
	}
	b, h := s.view.bag[r.BackpackSlot], s.view.self.Mainhand
	off := observedOffhand(s.view.self)
	if off == nil || !off.SlotKnown || off.ItemGuid != 0 || off.Entry != 0 || off.MetadataKnown {
		return "actual_empty_offhand_required"
	}
	if b.ItemGuid != r.ItemGuid || !validItemGUID(r.ItemGuid) || b.StackCount != 1 || h.ItemGuid == 0 || !h.MetadataKnown {
		return "exact_owned_item_and_original_mainhand_required"
	}
	if restore {
		if !s.state.RestoreRequired || s.state.OriginalWeaponGuid == 0 || s.state.EquippedPoleGuid == 0 || h.ItemGuid != s.state.EquippedPoleGuid || !fishingPole(s.metadata[h.Entry]) || r.OriginalWeaponGuid != s.state.OriginalWeaponGuid || r.ItemGuid != s.state.OriginalWeaponGuid || b.Entry != s.state.OriginalWeaponEntry {
			return "remembered_original_weapon_and_current_pole_required"
		}
	} else {
		if !fishingPole(s.metadata[b.Entry]) {
			return "complete_actual_fishing_pole_metadata_required"
		}
		if s.state.RestoreRequired {
			// A pole upgrade displaces only the old pole; preserve the exact
			// original weapon and require it still actually owned in the backpack.
			originalOwned := false
			for _, item := range s.view.bag {
				if item.ItemGuid == s.state.OriginalWeaponGuid && item.Entry == s.state.OriginalWeaponEntry && item.MetadataKnown && item.StackCount == 1 {
					originalOwned = true
				}
			}
			if !originalOwned || s.state.OriginalWeaponGuid == 0 || h.ItemGuid != s.state.EquippedPoleGuid || !fishingPole(s.metadata[h.Entry]) || r.OriginalWeaponGuid != s.state.OriginalWeaponGuid {
				return "remembered_original_weapon_and_current_pole_required"
			}
		} else if fishingPole(s.metadata[h.Entry]) || r.OriginalWeaponGuid != h.ItemGuid {
			return "fresh_owned_pole_and_exact_original_required"
		}
	}
	for _, entry := range []uint32{b.Entry, h.Entry} {
		// Restoring removes the current pole; its former skill/level
		// requirements cannot prevent equipping an eligible original weapon.
		if restore && entry == h.Entry && entry != b.Entry {
			continue
		}
		if reason := itemRequirements(s.view, s.metadata[entry]); reason != "" {
			return reason
		}
	}
	return ""
}
func (s *Service) prepareWrite(ctx context.Context, op *operation, gate func() string) bool {
	token, e := s.acquire(ctx)
	s.mu.Lock()
	op.token = token
	closing := s.closed || s.active != op || op.releaseReserved
	s.mu.Unlock()
	if closing {
		released := token == "" || s.release(token)
		s.mu.Lock()
		op.receipt.LeaseReleased = released
		s.mu.Unlock()
		return false
	}
	if e != nil {
		s.finish(op, "BLOCKED", "fresh_trade_lease_unavailable")
		return false
	}
	if e = s.observe(ctx); e != nil {
		s.finish(op, "BLOCKED", "reobservation_failed")
		return false
	}
	s.mu.Lock()
	reason := gate()
	if s.closed || s.active != op || op.ctx.Err() != nil {
		reason = "operation_cancelled_before_write"
	}
	if reason == "" && (s.view.backpackRevision != op.baseline.backpackRevision || s.view.copperRevision != op.baseline.copperRevision || s.view.skillsRevision != op.baseline.skillsRevision || !proto.Equal(s.view.self.Mainhand, op.baseline.self.Mainhand)) {
		reason = "baseline_changed_after_lease"
	}
	s.mu.Unlock()
	if reason != "" {
		s.finish(op, "BLOCKED", reason)
		return false
	}
	return true
}
func (s *Service) run(op *operation, code uint32, payload []byte) {
	if len(payload) == 0 {
		s.finish(op, "BLOCKED", "invalid_packet_arguments")
		return
	}
	if e := s.observe(op.ctx); e != nil {
		s.finish(op, "BLOCKED", "final_reobservation_failed")
		return
	}
	s.sendMu.Lock()
	s.mu.Lock()
	reason := ""
	switch r := op.request.(type) {
	case *suppliesv1.BuyRequest:
		reason = s.buyGateLocked(r, true)
	case *suppliesv1.EquipmentRequest:
		reason = s.equipmentGateLocked(r, op.receipt.Kind == "restore", true)
	default:
		reason = "operation_request_missing"
	}
	if reason == "" && (s.view.backpackRevision != op.baseline.backpackRevision || s.view.copperRevision != op.baseline.copperRevision || s.view.skillsRevision != op.baseline.skillsRevision || !proto.Equal(s.view.self.Mainhand, op.baseline.self.Mainhand)) {
		reason = "baseline_changed_before_packet"
	}
	if s.closed || s.active != op || op.ctx.Err() != nil || reason != "" {
		s.mu.Unlock()
		s.sendMu.Unlock()
		if reason == "" {
			reason = "cancelled_before_packet"
		}
		s.finish(op, "BLOCKED", reason)
		return
	}
	op.written = true
	op.receipt.Reason = "packet_reserved_once"
	s.state.Revision++
	s.mu.Unlock()
	_, e := s.session.SendPacket(op.ctx, &modv1.SendPacketRequest{Opcode: code, Payload: payload})
	s.mu.Lock()
	if e == nil {
		op.receipt.TransportWritten = true
		s.state.TransportWrites++
	} else {
		op.receipt.Reason = "transport_outcome_unconfirmed"
	}
	s.state.Revision++
	s.mu.Unlock()
	s.sendMu.Unlock()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		_ = s.observe(op.ctx)
		s.mu.Lock()
		done := op.receipt.Status == "CONFIRMED" || op.receipt.Status == "SERVER_REJECTED"
		combat := s.combatObserved
		s.mu.Unlock()
		if done {
			s.finish(op, "OUTCOME_UNCONFIRMED", "receipt_window_ended_without_complete_proof")
			return
		}
		if combat {
			s.finish(op, "OUTCOME_UNCONFIRMED", "combat_priority_released_lease_without_retry")
			return
		}
		select {
		case <-op.ctx.Done():
			s.finish(op, "OUTCOME_UNCONFIRMED", "receipt_window_ended_without_complete_proof")
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) evidenceLocked(v worldView) {
	op := s.last
	if op == nil || !op.written || op.receipt.Status == "BLOCKED" || op.receipt.Status == "SERVER_REJECTED" || op.receipt.Status == "CONFIRMED" {
		return
	}
	r := op.receipt
	if r.Kind == "buy" {
		r.InventoryConfirmed = exactPurchaseIncrease(op.baseline, v, r.ItemId, r.Count)
		r.CopperConfirmed = v.copperRevision > op.baseline.copperRevision && r.CostCopper <= op.baseline.copper && v.copper == op.baseline.copper-r.CostCopper
		if r.ServerSuccessObserved && r.InventoryConfirmed && r.CopperConfirmed {
			r.Status = "CONFIRMED"
			r.Reason = "server_purchase_inventory_and_exact_copper_confirmed"
		}
	} else {
		beforeHand := op.baseline.self.Mainhand
		r.EquipmentConfirmed = exactEquipmentSwap(op.baseline, v, op.slot, r.ItemGuid, beforeHand.ItemGuid, r.ItemId, beforeHand.Entry)
		if r.EquipmentConfirmed {
			r.Status = "CONFIRMED"
			r.Reason = "actual_mainhand_and_exact_backpack_swap_confirmed"
			if r.Kind == "equip" {
				if !s.state.RestoreRequired {
					s.state.OriginalWeaponGuid = beforeHand.ItemGuid
					s.state.OriginalWeaponEntry = beforeHand.Entry
				}
				s.state.EquippedPoleGuid = r.ItemGuid
				s.state.RestoreRequired = true
			} else {
				s.state.RestoreRequired = false
				s.state.EquippedPoleGuid = 0
			}
		}
	}
}
func (s *Service) OnPacket(_ context.Context, p *modv1.WorldPacket) (*emptypb.Empty, error) {
	if p == nil {
		return &emptypb.Empty{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	malformed := func() { s.state.MalformedPackets++; s.state.Revision++ }
	switch p.Opcode {
	case opcode.SMSGListInventory:
		npc, items, e := parseVendorList(p.Payload)
		if e != nil {
			malformed()
			break
		}
		if !s.closed && s.queryReserved && npc == s.queryNPC {
			s.state.Revision++
			s.state.Offer = &suppliesv1.Offer{Revision: s.state.Revision, NpcGuid: npc, Items: items}
			s.offerUntil = s.now().Add(15 * time.Second)
			if s.queryWait != nil {
				select {
				case s.queryWait <- struct{}{}:
				default:
				}
			}
		}
	case opcode.SMSGBuyItem, opcode.SMSGBuyFailed, opcode.SMSGInventoryChangeFailure:
		var r purchaseReceipt
		var e error
		if p.Opcode == opcode.SMSGBuyItem {
			r, e = parseBuyItem(p.Payload)
		} else if p.Opcode == opcode.SMSGBuyFailed {
			r, e = parseBuyFailed(p.Payload)
		} else {
			r, e = parseInventoryChangeFailure(p.Payload)
		}
		if e != nil {
			malformed()
			break
		}
		op := s.last
		if op == nil || !op.written || op.receipt.Status == "CONFIRMED" || op.receipt.Status == "SERVER_REJECTED" {
			break
		}
		o := op.receipt
		if p.Opcode == opcode.SMSGBuyItem && o.Kind == "buy" && r.npc == o.NpcGuid && r.slot == op.slot && r.count == o.Count {
			o.ServerSuccessObserved = true
			s.evidenceLocked(s.view)
			s.state.Revision++
		}
		if p.Opcode == opcode.SMSGBuyFailed && o.Kind == "buy" && r.npc == o.NpcGuid && r.item == o.ItemId {
			o.ServerFailure = uint32(r.code)
			o.Status = "SERVER_REJECTED"
			o.Reason = "matching_server_buy_failure"
			s.state.Revision++
		}
		if p.Opcode == opcode.SMSGInventoryChangeFailure && r.code != 0 && (r.itemGUID1 == o.ItemGuid || r.itemGUID2 == o.ItemGuid) && o.ItemGuid != 0 {
			o.ServerFailure = uint32(r.code)
			o.Status = "SERVER_REJECTED"
			o.Reason = "matching_server_inventory_failure"
			s.state.Revision++
		}
	}
	return &emptypb.Empty{}, nil
}
func (s *Service) BeforeLogout(_ context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	s.sendMu.Lock()
	s.mu.Lock()
	s.closed = true
	if s.queryWait != nil {
		select {
		case s.queryWait <- struct{}{}:
		default:
		}
	}
	op := s.active
	if op != nil {
		op.cancel()
	}
	s.state.Revision++
	s.mu.Unlock()
	s.sendMu.Unlock()
	if op != nil {
		s.finish(op, "OUTCOME_UNCONFIRMED", "logout_preserved_unconfirmed_receipt_without_restore")
	}
	return &emptypb.Empty{}, nil
}

func distance(x, y, z float64) float64 { return math.Sqrt(x*x + y*y + z*z) }
