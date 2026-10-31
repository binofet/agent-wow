package buddyinventory

import (
	"context"
	"encoding/binary"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	"math"
	"regexp"
	"sort"
	"sync"
	"time"
)

const limitation = "Exact owned ordinary cloth/leather/mail/plate armor, mainhand daggers and two-handed staves with an empty offhand; replacement retains the old item in its source slot. Conservative unenhanced armor/stat upgrades; weapon upgrades additionally require strictly higher physical DPS without lower stats or resistances. Empty-slot carried moves and bag41599 equipment; safe-gray-junk vendor sales. No other weapons, offhand equipment, jewelry, special/scaling gear, bank, destruction or ambiguous retries. Equipment requires actual class/race/level/proficiency and newer exact equipment plus carried-inventory receipts; sales require wallet credit."

var requestPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type sessionAPI interface {
	SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error)
	InvokeModule(context.Context, *modv1.InvokeModuleRequest, ...grpc.CallOption) (*modv1.InvokeModuleResponse, error)
}
type operation struct {
	request   proto.Message
	receipt   *inventoryv1.Operation
	baseline  view
	expected  view
	token     string
	ctx       context.Context
	cancel    context.CancelFunc
	attempted bool
}
type Service struct {
	inventoryv1.UnimplementedBuddyInventoryServer
	mu           sync.Mutex
	sendMu       sync.Mutex
	observeMu    sync.Mutex
	session      sessionAPI
	selfGUID     uint64
	now          func() time.Time
	state        *inventoryv1.State
	view         view
	known        bool
	actions      *actionsv1.State
	trade        *tradev1.State
	loot         *lootv1.State
	prof         *professionsv1.State
	metadata     map[uint32]itemMetadata
	queried      map[uint32]bool
	queryAfter   time.Time
	requests     map[string]*operation
	active, last *operation
	closed       bool
	addon        *addonBridge
}

func NewService(session sessionAPI, sessionID string, self uint64) *Service {
	s := &Service{session: session, selfGUID: self, now: time.Now, state: &inventoryv1.State{SessionId: sessionID, SelfGuid: self, Revision: 1, Limitation: limitation}, metadata: map[uint32]itemMetadata{}, queried: map[uint32]bool{}, requests: map[string]*operation{}}
	s.addon = newAddonBridge(s)
	return s
}
func fail(reason string) error { return status.Error(codes.FailedPrecondition, reason) }
func (s *Service) invoke(ctx context.Context, module, method string, in, out proto.Message) error {
	if s.session == nil {
		return status.Error(codes.Unavailable, "session_unavailable")
	}
	a, e := anypb.New(in)
	if e != nil {
		return e
	}
	r, e := s.session.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: module, Method: method, Request: a})
	if e != nil {
		return e
	}
	if r == nil || r.Result == nil {
		return fail("dependency_result_missing")
	}
	return r.Result.UnmarshalTo(out)
}
func (s *Service) snapshotLocked() *inventoryv1.State {
	return proto.Clone(s.state).(*inventoryv1.State)
}
func (s *Service) snapshot() *inventoryv1.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}
func (s *Service) observe(ctx context.Context, query *inventoryv1.DiscoverRequest) error {
	s.observeMu.Lock()
	defer s.observeMu.Unlock()
	w := &worldv1.SnapshotResponse{}
	request := &worldv1.SnapshotRequest{}
	if query != nil {
		request.CreatureGuid = query.VendorGuid
		request.CreatureEntry = query.VendorEntry
	}
	e := s.invoke(ctx, "buddy-world", "snapshot", request, w)
	v := view{}
	if e == nil {
		v, e = validateWorld(w, s.selfGUID)
	}
	a, t, l, p := &actionsv1.State{}, &tradev1.State{}, &lootv1.State{}, &professionsv1.State{}
	for _, d := range []struct {
		module, method string
		out            proto.Message
	}{{"buddy-actions", "status", a}, {"buddy-trade", "state", t}, {"buddy-loot", "state", l}, {"buddy-professions", "state", p}} {
		if e == nil {
			e = s.invoke(ctx, d.module, d.method, &emptypb.Empty{}, d.out)
		}
	}
	s.mu.Lock()
	before := proto.Clone(s.state).(*inventoryv1.State)
	if e == nil && (t.SessionId != s.state.SessionId || l.SessionId != s.state.SessionId || p.SessionId != s.state.SessionId || p.SelfGuid != s.selfGUID) {
		e = fail("dependency_session_or_actor_changed")
	}
	if e == nil && (w.InventoryRevision < s.state.InventoryRevision || w.SelfCopperRevision < s.state.CopperRevision) {
		e = fail("inventory_or_wallet_revision_regressed")
	}
	if e == nil && w.SelfEquipmentKnown && w.SelfEquipmentRevision < s.state.EquipmentRevision {
		e = fail("equipment_revision_regressed")
	}
	s.known = e == nil
	s.state.BlockedReason = ""
	if e != nil {
		s.state.InventoryKnown = false
		s.state.CopperKnown = false
		s.state.EquipmentKnown = false
		s.state.BlockedReason = "coherent_current_observations_unavailable"
	} else {
		s.view, s.actions, s.trade, s.loot, s.prof = v, a, t, l, p
		s.state.InventoryKnown = true
		s.state.InventoryRevision = w.InventoryRevision
		s.state.InventoryBags = w.InventoryBags
		s.state.CopperKnown = w.SelfCopperKnown
		s.state.Copper = w.SelfCopper
		s.state.CopperRevision = w.SelfCopperRevision
		s.equipmentStateLocked(v)
		s.evidenceLocked(v)
	}
	s.eligibilityLocked()
	if !proto.Equal(before, s.state) {
		s.state.Revision++
	}
	ids := []uint32{}
	if e == nil && !s.closed && s.active == nil && !s.now().Before(s.queryAfter) {
		owned := map[uint32]bool{}
		for _, i := range v.slots {
			if i.ItemGuid != 0 && i.MetadataKnown {
				owned[i.Entry] = true
			}
		}
		for _, b := range v.bags {
			if b.ContainerGuid != 0 && b.MetadataKnown {
				owned[b.Entry] = true
			}
		}
		if equipment, known := equipmentView(v); known {
			for _, i := range equipment {
				if i.ItemGuid != 0 && i.MetadataKnown {
					owned[i.Entry] = true
				}
			}
		}
		for id := range owned {
			if !s.queried[id] {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		if len(ids) > 8 {
			ids = ids[:8]
		}
		if len(s.queried)+len(ids) > 256 {
			ids = nil
		}
		for _, id := range ids {
			s.queried[id] = true
		}
		if len(ids) > 0 {
			s.queryAfter = s.now().Add(time.Second)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.sendMu.Lock()
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if !closed {
			_, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: opcode.CMSGItemQuerySingle, Payload: binary.LittleEndian.AppendUint32(nil, id)})
			if err == nil {
				s.mu.Lock()
				s.state.TransportWrites++
				s.mu.Unlock()
			}
		}
		s.sendMu.Unlock()
	}
	return e
}
func (s *Service) eligibilityLocked() {
	s.state.Items = nil
	if !s.known {
		return
	}
	keys := []slotKey{}
	for k, i := range s.view.slots {
		if i.ItemGuid != 0 {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].bag != keys[j].bag {
			return keys[i].bag < keys[j].bag
		}
		return keys[i].slot < keys[j].slot
	})
	for _, k := range keys {
		i := s.view.slots[k]
		m := s.metadata[i.Entry]
		r := &inventoryv1.ItemEligibility{Item: ref(i), MetadataKnown: m.known && i.MetadataKnown, SafeJunkSaleEligible: i.MetadataKnown && m.disposable() && m.sellPrice > 0, UnitSellPriceCopper: m.sellPrice, NormalBag: i.MetadataKnown && normalBag(m), ContainerSlots: m.containerSlots, Name: m.name}
		r.GearEquipmentSlot, r.ExpectedEquippedItemGuid, r.ConservativeUpgrade, r.GearBlockedReason = s.gearEligibilityLocked(i)
		r.GearEquipEligible = r.GearBlockedReason == "" && s.ordinaryBagLocked(i.Bag)
		if !i.MetadataKnown || !m.known {
			r.BlockedReason = "complete_owned_item_metadata_pending"
		} else if !r.SafeJunkSaleEligible && !r.NormalBag && !r.GearEquipEligible {
			r.BlockedReason = "not_supported_sale_bag_or_armor"
		}
		s.state.Items = append(s.state.Items, r)
	}
}
func (s *Service) GetState(ctx context.Context, _ *emptypb.Empty) (*inventoryv1.State, error) {
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = s.observe(call, nil)
	return s.snapshot(), nil
}
func (s *Service) idleLocked(leased bool) string {
	if !s.known || s.closed {
		return "current_owned_observations_required"
	}
	p := s.view.w.Self
	a := s.actions
	if owner := s.view.w.Owner; owner != nil && owner.Present && owner.UnitFlagsKnown && owner.UnitFlags&0x80000 != 0 {
		return "observed_owner_combat_has_priority"
	}
	const unsafe = uint32(1 | 4 | 0x40000 | 0x80000 | 0x100000 | 0x400000 | 0x800000 | 0x1000000 | 0x2000000 | 0x8000000)
	if !p.AliveKnown || !p.Alive || !p.HealthKnown || p.Health == 0 || !p.UnitFlagsKnown || p.UnitFlags&unsafe != 0 || !p.MapKnown || !p.PositionKnown || !finite(p.X) || !finite(p.Y) || !finite(p.Z) || !p.MovementFlagsKnown || !movementflags.GroundCompatible(p.MovementFlags, p.ExtraMovementFlags) || !p.ChannelObjectKnown || p.ChannelObjectGuid != 0 || !p.ChannelSpellKnown || p.ChannelSpellId != 0 {
		return "alive_safe_ground_nonchanneling_character_required"
	}
	if a == nil || a.Position == nil || !a.Position.Known || !a.Position.MapKnown || a.Position.MapId != p.MapId || !finite(a.Position.X) || !finite(a.Position.Y) || !finite(a.Position.Z) || a.Moving || a.Traveling || a.MotionUnconfirmed || a.CastPending || a.AutoAttacking || a.AutoAttackRequested || a.AutoAttackUnconfirmed || a.Following || a.Healing || a.Assisting || a.Practicing {
		return "controller_must_be_confirmed_idle"
	}
	if !leased && a.InteractionOwner != "" || leased && (a.InteractionOwner != "trade" || a.InteractionLeaseRemainingMs < 11000) {
		return "exclusive_trade_lease_required"
	}
	if !settledTrade(s.trade) {
		return "trade_or_junk_operation_busy"
	}
	if s.loot == nil || s.loot.Enabled || s.loot.PendingCorpseGuid != 0 || s.loot.PendingGameObjectGuid != 0 || (s.loot.Phase != "disabled" && s.loot.Phase != "idle") || s.prof == nil || s.prof.Active || s.prof.Closing || s.prof.Operation != nil && (s.prof.Operation.CleanupUnconfirmed || s.prof.Operation.Status == professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED) {
		return "loot_or_profession_operation_busy"
	}
	for _, i := range s.view.slots {
		if i.ItemGuid != 0 && (!i.MetadataKnown || i.Entry == 0 || i.StackCount == 0) {
			return "full_inventory_item_metadata_required"
		}
	}
	return ""
}

// A completed trade retains its offers for the receipt. Those historical fields
// do not represent a live window after the server positively confirmed status8.
// Disposal enabled is also an intent; its actual unresolved receipt still blocks.
func settledTrade(t *tradev1.State) bool {
	if t == nil || t.LastJunkDisposal != nil && t.LastJunkDisposal.Status != "SERVER_SLOT_REMOVAL_CONFIRMED" {
		return false
	}
	if t.Phase == "COMPLETE" {
		return t.PartnerGuid == 3 && t.LastServerStatus == 8 && t.Reason == "server_confirmed_trade_complete" && t.CompletedTrades > 0
	}
	return t.Phase == "IDLE" && t.PartnerGuid == 0 && !t.PartnerAccepted && !t.AcceptTransportWritten && len(t.OwnRequestedItems) == 0 && len(t.PartnerItems) == 0 && t.OwnRequestedCopper == 0 && t.PartnerCopper == 0
}
func (s *Service) ordinaryBagLocked(n uint32) bool {
	if n == 0 {
		return true
	}
	if n >= 5 {
		return false
	}
	b := s.view.bags[n]
	m := s.metadata[b.Entry]
	return b.ContainerGuid != 0 && normalBag(m) && b.SlotCount == m.containerSlots
}
func (s *Service) vendorLocked(guid uint64) (*worldv1.CreatureState, string, float64) {
	if !creatureGUID(guid) || !s.known || s.actions == nil || s.actions.Position == nil {
		return nil, "exact_observed_vendor_required", 0
	}
	var found *worldv1.CreatureState
	w := s.view.w
	for _, group := range [][]*worldv1.CreatureState{w.NearbyCreatures, w.QuestGivers, w.RequestedCreatures, {w.RequestedCreature}} {
		if len(group) > 64 {
			return nil, "vendor_observation_bound_exceeded", 0
		}
		for _, c := range group {
			if c == nil || c.Guid != guid {
				continue
			}
			p := s.actions.Position
			if c.Entry != uint32(guid>>24&0xffffff) || !c.Present || !c.MapKnown || c.MapId != p.MapId || !c.PositionKnown || !finite(c.X) || !finite(c.Y) || !finite(c.Z) || !c.AliveKnown || !c.Alive || !c.HealthKnown || c.Health == 0 || c.NpcFlags&0x80 == 0 || c.UnitFlags&0x80000 != 0 || c.MovementFlags&0x080000ff != 0 || c.Trajectory != nil && c.Trajectory.Known && (!c.Trajectory.Stopped || c.Trajectory.TransportRelative) {
				return c, "stationary_alive_same_map_vendor_required", 0
			}
			if found != nil && (found.Entry != c.Entry || found.X != c.X || found.Y != c.Y || found.Z != c.Z || found.PositionRevision != c.PositionRevision || found.Present != c.Present || found.NpcFlags != c.NpcFlags) {
				return nil, "vendor_views_disagree", 0
			}
			found = c
		}
	}
	if found == nil {
		return nil, "vendor_not_observed", 0
	}
	c := found
	p := s.actions.Position
	if c.Entry != uint32(guid>>24&0xffffff) || !c.Present || !c.MapKnown || c.MapId != p.MapId || !c.PositionKnown || !finite(c.X) || !finite(c.Y) || !finite(c.Z) || !c.AliveKnown || !c.Alive || !c.HealthKnown || c.Health == 0 || c.NpcFlags&0x80 == 0 || c.UnitFlags&0x80000 != 0 || c.MovementFlags&0x080000ff != 0 || c.Trajectory != nil && c.Trajectory.Known && (!c.Trajectory.Stopped || c.Trajectory.TransportRelative) {
		return c, "stationary_alive_same_map_vendor_required", 0
	}
	d := math.Sqrt(math.Pow(float64(c.X-p.X), 2) + math.Pow(float64(c.Y-p.Y), 2) + math.Pow(float64(c.Z-p.Z), 2))
	if d > 4.5 {
		return c, "vendor_outside_4_5_yards", d
	}
	return c, "", d
}
func (s *Service) Discover(ctx context.Context, r *inventoryv1.DiscoverRequest) (*inventoryv1.Discovery, error) {
	if r == nil {
		r = &inventoryv1.DiscoverRequest{}
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = s.observe(call, r)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &inventoryv1.Discovery{State: s.snapshotLocked()}
	if !s.known {
		return out, nil
	}
	ids := map[uint64]bool{}
	for _, g := range [][]*worldv1.CreatureState{s.view.w.NearbyCreatures, s.view.w.QuestGivers, s.view.w.RequestedCreatures, {s.view.w.RequestedCreature}} {
		for _, c := range g {
			if c != nil && c.NpcFlags&0x80 != 0 && (r.VendorGuid == 0 || c.Guid == r.VendorGuid) && (r.VendorEntry == 0 || c.Entry == r.VendorEntry) {
				ids[c.Guid] = true
			}
		}
	}
	sorted := []uint64{}
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for _, id := range sorted {
		c, reason, d := s.vendorLocked(id)
		if c != nil {
			if reason == "" {
				reason = s.idleLocked(false)
			}
			out.Nearby = append(out.Nearby, &inventoryv1.Vendor{Creature: proto.Clone(c).(*worldv1.CreatureState), Interactable: reason == "", BlockedReason: reason, Distance: d})
		}
		if len(out.Nearby) >= 32 {
			break
		}
	}
	return out, nil
}
func (s *Service) gateLocked(op *operation, leased bool) string {
	if reason := s.idleLocked(leased); reason != "" {
		return reason
	}
	r := op.receipt
	if s.view.w.InventoryRevision != r.BaselineInventoryRevision {
		return "inventory_revision_changed"
	}
	source, ok := s.view.source(r.Source)
	if !ok || !s.ordinaryBagLocked(source.Bag) {
		return "exact_owned_source_changed_or_unsupported_bag"
	}
	m := s.metadata[source.Entry]
	if !m.known {
		return "complete_normal_item_query_pending"
	}
	switch r.Kind {
	case "move":
		d := s.view.slots[slotKey{r.DestinationBag, r.DestinationSlot}]
		if m.class == 1 || m.containerSlots != 0 {
			return "container_items_cannot_be_moved"
		}
		if d == nil || d.ItemGuid != 0 || !s.ordinaryBagLocked(r.DestinationBag) || source.Bag == d.Bag && source.Slot == d.Slot {
			return "explicit_empty_normal_destination_required"
		}
	case "equip":
		if r.EquipmentSlot < 19 || r.EquipmentSlot > 22 || !normalBag(m) || source.StackCount != 1 || s.view.bags[r.EquipmentSlot-18].ContainerGuid != 0 {
			return "only_41599_single_bag_to_known_empty_equipment_slot"
		}
	case "equip_gear":
		if s.view.w.SelfEquipmentRevision != r.BaselineEquipmentRevision {
			return "equipment_revision_changed"
		}
		slot, displaced, upgrade, reason := s.gearEligibilityLocked(source)
		if reason != "" {
			return reason
		}
		if slot != r.EquipmentSlot || displaced != r.DisplacedItemGuid {
			return "exact_equipment_destination_changed"
		}
		request, ok := op.request.(*inventoryv1.EquipGearRequest)
		if !ok || request.RequireConservativeUpgrade && !upgrade {
			return "conservative_upgrade_not_proven"
		}
	case "sell":
		if !m.disposable() || m.sellPrice == 0 || m.sellPrice != r.UnitSellPriceCopper {
			return "exact_safe_gray_junk_positive_price_required"
		}
		if !s.view.w.SelfCopperKnown || s.view.w.SelfCopperRevision != r.BaselineCopperRevision {
			return "exact_known_wallet_revision_required"
		}
		credit := uint64(m.sellPrice) * uint64(source.StackCount)
		if credit == 0 || credit > math.MaxUint32 || credit+uint64(s.view.w.SelfCopper) > math.MaxUint32 {
			return "sale_credit_overflow"
		}
		if _, reason, _ := s.vendorLocked(r.VendorGuid); reason != "" {
			return reason
		}
	default:
		return "unknown_inventory_operation"
	}
	return ""
}
func (s *Service) submit(ctx context.Context, kind, id, session string, revision, inventoryRevision uint64, request proto.Message, r *inventoryv1.Operation) (*inventoryv1.State, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !requestPattern.MatchString(id) {
		return nil, status.Error(codes.InvalidArgument, "bounded_request_id_required")
	}
	s.mu.Lock()
	if old := s.requests[id]; old != nil {
		if !proto.Equal(old.request, request) || old.receipt.Kind != kind {
			s.mu.Unlock()
			return nil, status.Error(codes.AlreadyExists, "request_id_payload_changed")
		}
		out := s.snapshotLocked()
		out.Operation = proto.Clone(old.receipt).(*inventoryv1.Operation)
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()
	_ = s.observe(ctx, &inventoryv1.DiscoverRequest{VendorGuid: r.VendorGuid})
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.requests[id]; old != nil {
		if !proto.Equal(old.request, request) {
			return nil, status.Error(codes.AlreadyExists, "request_id_payload_changed")
		}
		return s.snapshotLocked(), nil
	}
	if s.closed || session != s.state.SessionId || revision != s.state.Revision {
		return nil, fail("session_or_state_revision_changed")
	}
	if s.active != nil {
		return nil, fail("inventory_operation_active")
	}
	for _, old := range s.requests {
		if old.receipt.Status == "OUTCOME_UNCONFIRMED" || old.receipt.Status == "REQUESTED" || !old.receipt.LeaseReleased {
			return nil, fail("prior_inventory_outcome_or_cleanup_unconfirmed")
		}
	}
	if len(s.requests) >= 256 {
		return nil, status.Error(codes.ResourceExhausted, "inventory_request_history_full")
	}
	r.RequestId = id
	r.Kind = kind
	r.Status = "REQUESTED"
	r.Reason = "reserved_before_packet"
	r.BaselineInventoryRevision = inventoryRevision
	opCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	op := &operation{request: proto.Clone(request), receipt: r, ctx: opCtx, cancel: cancel}
	s.requests[id] = op
	s.active = op
	s.last = op
	s.state.Active = true
	s.state.Operation = r
	s.state.Revision++
	if reason := s.gateLocked(op, false); reason != "" {
		r.Status = "BLOCKED"
		r.Reason = reason
		r.LeaseReleased = true
		s.active = nil
		s.state.Active = false
		cancel()
		return s.snapshotLocked(), nil
	}
	go s.run(op)
	return s.snapshotLocked(), nil
}
func (s *Service) Move(ctx context.Context, r *inventoryv1.MoveRequest) (*inventoryv1.State, error) {
	if r == nil || r.Source == nil || r.ExpectedDestinationItemGuid != 0 {
		return nil, status.Error(codes.InvalidArgument, "exact_source_and_empty_destination_required")
	}
	return s.submit(ctx, "move", r.RequestId, r.SessionId, r.ExpectedRevision, r.ExpectedInventoryRevision, r, &inventoryv1.Operation{Source: proto.Clone(r.Source).(*inventoryv1.ItemRef), DestinationBag: r.DestinationBag, DestinationSlot: r.DestinationSlot})
}
func (s *Service) Equip(ctx context.Context, r *inventoryv1.EquipRequest) (*inventoryv1.State, error) {
	if r == nil || r.Source == nil || r.ExpectedContainerGuid != 0 {
		return nil, status.Error(codes.InvalidArgument, "exact_source_and_empty_bag_equipment_required")
	}
	return s.submit(ctx, "equip", r.RequestId, r.SessionId, r.ExpectedRevision, r.ExpectedInventoryRevision, r, &inventoryv1.Operation{Source: proto.Clone(r.Source).(*inventoryv1.ItemRef), EquipmentSlot: r.EquipmentSlot})
}
func (s *Service) Sell(ctx context.Context, r *inventoryv1.SellRequest) (*inventoryv1.State, error) {
	if r == nil || r.Source == nil {
		return nil, status.Error(codes.InvalidArgument, "exact_sale_request_required")
	}
	credit := uint64(r.ExpectedUnitSellPriceCopper) * uint64(r.Source.StackCount)
	if credit > math.MaxUint32 {
		return nil, status.Error(codes.InvalidArgument, "sale_credit_overflow")
	}
	return s.submit(ctx, "sell", r.RequestId, r.SessionId, r.ExpectedRevision, r.ExpectedInventoryRevision, r, &inventoryv1.Operation{Source: proto.Clone(r.Source).(*inventoryv1.ItemRef), VendorGuid: r.VendorGuid, UnitSellPriceCopper: r.ExpectedUnitSellPriceCopper, ExpectedCreditCopper: uint32(credit), BaselineCopperRevision: r.ExpectedCopperRevision})
}
func (s *Service) finish(op *operation, status, reason string) {
	s.mu.Lock()
	if op.receipt.Status != "CONFIRMED" && op.receipt.Status != "SERVER_REJECTED" {
		op.receipt.Status = status
		op.receipt.Reason = reason
	}
	s.mu.Unlock()
	released := op.token == ""
	if op.token != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		out := &actionsv1.State{}
		released = s.invoke(ctx, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "trade", Token: op.token}, out) == nil
		cancel()
	}
	s.mu.Lock()
	op.receipt.LeaseReleased = released
	if s.active == op {
		s.active = nil
		s.state.Active = false
	}
	s.state.Revision++
	s.mu.Unlock()
	op.cancel()
}
func (s *Service) run(op *operation) {
	lease := &actionsv1.InteractionLease{}
	if e := s.invoke(op.ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "trade", LeaseMs: 15000}, lease); e != nil {
		s.finish(op, "BLOCKED", "fresh_trade_lease_unavailable")
		return
	}
	s.mu.Lock()
	op.token = lease.Token
	s.mu.Unlock()
	if lease.Token == "" || !safeLeaseState(lease.State) {
		s.finish(op, "BLOCKED", "trade_lease_stop_unconfirmed")
		return
	}
	if e := s.observe(op.ctx, &inventoryv1.DiscoverRequest{VendorGuid: op.receipt.VendorGuid}); e != nil {
		s.finish(op, "BLOCKED", "fresh_inventory_unavailable_after_lease")
		return
	}
	// Keep observation and write ordering consistent with observe(), including
	// its metadata sends. No dependency RPC may follow the final world read.
	s.observeMu.Lock()
	s.sendMu.Lock()
	unlockWrite := func() {
		s.sendMu.Unlock()
		s.observeMu.Unlock()
	}
	s.mu.Lock()
	preRenewSelf := s.view.w.Self
	s.mu.Unlock()
	// Owner labels alone cannot prove that our exact token still owns the
	// controller. A replacement trade lease must not authorize this old write.
	renewed := &actionsv1.InteractionLease{}
	if e := s.invoke(op.ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "trade", Token: op.token, LeaseMs: 15000}, renewed); e != nil || renewed.Token != op.token || !safeLeaseState(renewed.State) {
		unlockWrite()
		s.finish(op, "BLOCKED", "exact_trade_lease_renewal_unconfirmed")
		return
	}
	w := &worldv1.SnapshotResponse{}
	e := s.invoke(op.ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{CreatureGuid: op.receipt.VendorGuid}, w)
	v := view{}
	if e == nil {
		v, e = validateWorld(w, s.selfGUID)
	}
	s.mu.Lock()
	if e != nil || w.InventoryRevision < s.state.InventoryRevision || w.SelfCopperRevision < s.state.CopperRevision || w.SelfEquipmentKnown && w.SelfEquipmentRevision < s.state.EquipmentRevision {
		s.known = false
		s.state.InventoryKnown = false
		s.state.CopperKnown = false
		s.state.EquipmentKnown = false
		s.mu.Unlock()
		unlockWrite()
		s.finish(op, "BLOCKED", "final_world_observation_unavailable_or_regressed")
		return
	}
	s.view, s.known = v, true
	s.state.InventoryKnown = true
	s.state.InventoryRevision = w.InventoryRevision
	s.state.InventoryBags = w.InventoryBags
	s.state.CopperKnown = w.SelfCopperKnown
	s.state.Copper = w.SelfCopper
	s.state.CopperRevision = w.SelfCopperRevision
	s.equipmentStateLocked(v)
	s.actions = proto.Clone(renewed.State).(*actionsv1.State)
	reason := s.gateLocked(op, true)
	if preRenewSelf == nil || w.Self.PositionRevision != preRenewSelf.PositionRevision || w.Self.MapKnown != preRenewSelf.MapKnown || w.Self.MapId != preRenewSelf.MapId {
		reason = "authoritative_pose_changed_during_lease_renewal"
	}
	if op.ctx.Err() != nil {
		reason = "operation_canceled_before_write"
	}
	if reason != "" {
		s.mu.Unlock()
		unlockWrite()
		s.finish(op, "BLOCKED", reason)
		return
	}
	op.baseline = s.view
	expected, e := expectedAfter(op.baseline, op.receipt)
	if e != nil {
		s.mu.Unlock()
		unlockWrite()
		s.finish(op, "BLOCKED", "expected_inventory_vector_invalid")
		return
	}
	op.expected = expected
	op.receipt.BaselineCopper = op.baseline.w.SelfCopper
	code := uint32(opcode.CMSGSwapItem)
	var payload []byte
	r := op.receipt
	switch r.Kind {
	case "move":
		payload = movePacket(r.Source.Bag, r.Source.Slot, r.DestinationBag, r.DestinationSlot)
	case "equip":
		code = opcode.CMSGAutoequipItemSlot
		payload = equipPacket(r.Source.ItemGuid, r.EquipmentSlot)
	case "equip_gear":
		code = opcode.CMSGAutoequipItemSlot
		payload = equipGearPacket(r.Source.ItemGuid, r.EquipmentSlot)
	case "sell":
		code = opcode.CMSGSellItem
		payload = sellPacket(r.VendorGuid, r.Source.ItemGuid, r.Source.StackCount)
	}
	if len(payload) == 0 {
		s.mu.Unlock()
		unlockWrite()
		s.finish(op, "BLOCKED", "packet_binding_invalid")
		return
	}
	op.attempted = true
	r.TransportAttempted = true
	r.Reason = "awaiting_actual_server_inventory_evidence"
	s.state.Revision++
	s.mu.Unlock()
	_, sendErr := s.session.SendPacket(op.ctx, &modv1.SendPacketRequest{Opcode: code, Payload: payload})
	s.mu.Lock()
	r.TransportWritten = sendErr == nil
	if sendErr == nil {
		s.state.TransportWrites++
	}
	s.mu.Unlock()
	unlockWrite()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		_ = s.observe(op.ctx, nil)
		s.mu.Lock()
		done := r.Status == "CONFIRMED" || r.Status == "SERVER_REJECTED"
		status, reason := r.Status, r.Reason
		s.mu.Unlock()
		if done {
			s.finish(op, status, reason)
			return
		}
		select {
		case <-op.ctx.Done():
			s.finish(op, "OUTCOME_UNCONFIRMED", "no_complete_inventory_receipt_no_retry")
			return
		case <-ticker.C:
		}
	}
}

func safeLeaseState(a *actionsv1.State) bool {
	return a != nil && a.InteractionOwner == "trade" && a.InteractionLeaseRemainingMs >= 11000 && !a.Moving && !a.Traveling && !a.MotionUnconfirmed && !a.CastPending && !a.AutoAttacking && !a.AutoAttackRequested && !a.AutoAttackUnconfirmed && !a.Following && !a.Healing && !a.Assisting && !a.Practicing
}
func (s *Service) evidenceLocked(v view) {
	op := s.last
	if op == nil || !op.attempted || op.receipt.Status == "CONFIRMED" || op.receipt.Status == "SERVER_REJECTED" {
		return
	}
	r := op.receipt
	r.ObservedInventoryRevision = v.w.InventoryRevision
	r.ObservedEquipmentRevision = v.w.SelfEquipmentRevision
	r.ObservedCopperRevision = v.w.SelfCopperRevision
	r.ObservedCopper = v.w.SelfCopper
	if v.w.InventoryRevision <= r.BaselineInventoryRevision {
		return
	}
	r.InventoryConfirmed = exactVector(op.expected, v)
	if r.Kind == "equip" {
		r.EquipmentConfirmed = r.InventoryConfirmed
	}
	if r.Kind == "equip_gear" {
		r.EquipmentConfirmed = v.w.SelfEquipmentRevision > r.BaselineEquipmentRevision && exactEquipmentVector(op.expected, v)
	}
	if r.Kind == "sell" {
		r.CopperConfirmed = v.w.SelfCopperKnown && v.w.SelfCopperRevision > r.BaselineCopperRevision && uint64(v.w.SelfCopper) == uint64(r.BaselineCopper)+uint64(r.ExpectedCreditCopper)
	}
	if r.InventoryConfirmed && (r.Kind != "sell" || r.CopperConfirmed) && (r.Kind != "equip_gear" || r.EquipmentConfirmed) {
		r.Status = "CONFIRMED"
		r.Reason = "newer_exact_server_inventory_vector_confirmed"
		if r.Kind == "sell" {
			r.Reason = "newer_exact_stack_removal_and_wallet_credit_confirmed"
		}
		if r.Kind == "equip_gear" {
			r.Reason = "newer_exact_equipment_and_displaced_item_inventory_confirmed"
		}
	}
}
func (s *Service) OnPacket(_ context.Context, p *modv1.WorldPacket) (*emptypb.Empty, error) {
	if p == nil {
		return &emptypb.Empty{}, nil
	}
	if p.Opcode == opcode.SMSGMessagechat {
		if s.addon != nil {
			s.addon.receive(p.Payload)
		}
		return &emptypb.Empty{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Opcode == opcode.SMSGItemQuerySingleResponse {
		m, e := parseItemMetadata(p.Payload)
		if e != nil {
			s.state.MalformedPackets++
		} else if s.queried[m.entry] && m.known {
			s.metadata[m.entry] = m
			s.eligibilityLocked()
			s.state.Revision++
		}
		return &emptypb.Empty{}, nil
	}
	op := s.active
	if op == nil || !op.attempted {
		return &emptypb.Empty{}, nil
	}
	r := op.receipt
	if p.Opcode == opcode.SMSGInventoryChangeFailure {
		f, e := parseInventoryChangeFailure(p.Payload)
		if e != nil {
			s.state.MalformedPackets++
		} else if f.code != 0 && (f.itemGUID1 == r.Source.ItemGuid || f.itemGUID2 == r.Source.ItemGuid) {
			r.ServerErrorObserved = true
			r.ServerError = uint32(f.code)
			r.Status = "SERVER_REJECTED"
			r.Reason = "matching_server_inventory_error"
		}
	}
	if p.Opcode == opcode.SMSGSellItem && r.Kind == "sell" {
		vendor, item, code, e := sellFailure(p.Payload)
		if e != nil {
			s.state.MalformedPackets++
		} else if vendor == r.VendorGuid && item == r.Source.ItemGuid {
			r.ServerErrorObserved = true
			r.ServerError = uint32(code)
			r.Status = "SERVER_REJECTED"
			r.Reason = "matching_server_sale_error"
		}
	}
	return &emptypb.Empty{}, nil
}
func (s *Service) Tick(ctx context.Context) {
	if s.addon != nil {
		s.addon.tick(ctx)
	}
}
func (s *Service) BeforeLogout(_ context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	if s.addon != nil {
		s.addon.close()
	}
	s.sendMu.Lock()
	s.mu.Lock()
	s.closed = true
	s.state.Closing = true
	op := s.active
	if op != nil {
		op.cancel()
	}
	s.mu.Unlock()
	s.sendMu.Unlock()
	return &emptypb.Empty{}, nil
}
