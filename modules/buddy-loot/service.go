// Package buddyloot collects server-permitted nearby corpse loot and the
// catalog-described ordinary quest containers. It never rolls, sells or destroys items.
package buddyloot

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/gamecatalog"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type sessionAPI interface {
	SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error)
	InvokeModule(context.Context, *modv1.InvokeModuleRequest, ...grpc.CallOption) (*modv1.InvokeModuleResponse, error)
}
type Service struct {
	lootv1.UnimplementedBuddyLootServer
	mu                                         sync.Mutex
	session                                    sessionAPI
	catalog                                    *gamecatalog.Catalog
	selfGUID                                   uint64
	now                                        func() time.Time
	state                                      lootv1.State
	closed                                     bool
	until, nextTry, deadline, transactionUntil time.Time
	token                                      string
	window                                     lootWindow
	items                                      []lootItem
	index                                      int
	removed, pushed, moneySent, moneyCleared   bool
	itemRequested                              bool
	unconfirmedItems                           map[uint32]bool
	removedSlots                               map[byte]bool
	itemCountBefore                            uint64
	itemCountKnown                             bool
	cleanup                                    ordinaryLootCleanup
	retired                                    map[uint64]bool
	retiredCursor                              uint64
	seen                                       map[uint64]bool
	gatherSeen                                 map[uint64]bool
	opened                                     bool
	questGather                                bool
	gatherCastCount                            byte
	gatherObjectRevision                       uint64
	pausedSource                               uint64
	pausedGather                               bool
	inventoryWriteRevision                     uint64
	rejectedInventoryRevision                  uint64
	corpseRequests                             map[string]*corpseRequest
	corpseRequest                              *corpseRequest
	gatherRequests                             map[string]*gatherRequest
	gatherRequest                              *gatherRequest
	exactCorpse                                bool
	windowRevision                             uint64
	windowRequests                             map[string]*windowRequest
	windowRequest                              *windowRequest
	lastLootWriteError                         error
	capacityResumeRequest                      *lootv1.ResumeSourceRequest
	capacityResumeReceipt                      *lootv1.ResumeSourceReceipt
	capacityEpoch                              string
}

// NewAutomaticService exposes Lua-selected loot operations from login until
// logout, with temporary suspensions for exclusive maintenance work.
func NewAutomaticService(session sessionAPI, self uint64, sessionID string) *Service {
	s := &Service{session: session, catalog: gamecatalog.Default(), selfGUID: self, now: time.Now,
		state: lootv1.State{SessionId: sessionID}, seen: map[uint64]bool{}, capacityEpoch: rand.Text()}
	s.resumeAutomatic()
	return s
}

const maintenanceLootPause = 2 * time.Minute

func (s *Service) resumeAutomatic() {
	s.state.Enabled = true
	s.until = time.Time{}
	if s.token == "" && s.cleanup.source == 0 {
		s.state.Phase = "idle"
		s.state.Reason = "automatic_nearby_ordinary_loot_enabled"
	}
}

func (s *Service) fault(reason string) {
	s.state.Reason = reason
}

func (s *Service) snapshotLocked() *lootv1.State {
	r := proto.Clone(&s.state).(*lootv1.State)
	if s.state.InventoryPaused && s.pausedGather && s.pausedSource != 0 && s.state.LastGather != nil && s.state.LastGather.ObjectGuid == s.pausedSource {
		r.CapacityPause = &lootv1.CapacityPause{SourceGuid: s.pausedSource, GatherId: s.state.LastGather.Id, InventoryRevision: s.rejectedInventoryRevision, PauseToken: s.capacityPauseToken()}
	}
	if s.state.Enabled && s.now().Before(s.until) {
		r.EnabledRemainingMs = uint64(s.until.Sub(s.now()).Milliseconds())
	}
	return r
}
func (s *Service) Enable(ctx context.Context, r *lootv1.EnableRequest) (*lootv1.State, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "enable request required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, status.Error(codes.FailedPrecondition, "session closing")
	}
	if r.Enabled {
		s.resumeAutomatic()
		return s.snapshotLocked(), nil
	}
	s.until = s.now().Add(maintenanceLootPause)
	s.state.Enabled = r.Enabled
	if s.policyWindow() {
		// Compatibility scheduling metadata applies to the remaining named
		// gather controller, never the current Lua-selected corpse operation.
		return s.snapshotLocked(), nil
	}
	if s.cleanup.source != 0 {
		s.state.Reason = "disabled_by_request"
		return s.snapshotLocked(), nil
	}
	if s.token != "" {
		switch s.state.Phase {
		case "release_observed":
			s.finishReleased(ctx)
		case "waiting_release":
			// Disabling new loot does not resend an outstanding release or
			// restart its receipt deadline. Its ACK/timeout still finishes it.
		default:
			s.state.Phase = "abort"
		}
		s.state.Reason = "disabled_by_request"
	} else {
		s.state.Phase = "disabled"
		s.state.Reason = "disabled_by_request"
	}
	return s.snapshotLocked(), nil
}
func (s *Service) GetState(ctx context.Context, _ *emptypb.Empty) (*lootv1.State, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked(), nil
}
func (s *Service) invoke(ctx context.Context, module, method string, in, out proto.Message) error {
	if s.session == nil {
		return errors.New("session unavailable")
	}
	request, err := anypb.New(in)
	if err != nil {
		return err
	}
	result, err := s.session.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: module, Method: method, Request: request})
	if err != nil {
		return err
	}
	if result == nil || result.Result == nil {
		return errors.New("missing dependency result")
	}
	return result.Result.UnmarshalTo(out)
}
func (s *Service) write(ctx context.Context, op uint32, p []byte) bool {
	s.lastLootWriteError = nil
	if s.session == nil {
		return false
	}
	gatherOpening := s.questGather && s.gatherRequest != nil && op == opcode.CMSGCastSpell
	if gatherOpening {
		s.gatherRequest.receipt.OpeningTransportAttempted = true
		// An in-range Lua RPC retains its exact cast token. Delayed finite
		// approach execution never stores or reuses an expired RPC token.
		ctx = modcb.ForwardPolicySpell(ctx)
	}
	if s.policyWindow() && (op == opcode.CMSGLoot || op == opcode.CMSGLootRelease) {
		if op == opcode.CMSGLoot && s.corpseRequest != nil {
			s.corpseRequest.receipt.OpeningTransportAttempted = true
		}
		ctx = modcb.ForwardPolicySpell(ctx)
	}
	_, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: op, Payload: p})
	s.lastLootWriteError = err
	if err != nil {
		if gatherOpening {
			if modcb.PacketWasNotDispatched(err) {
				s.gatherRequest.receipt.OpeningTransportAttempted = false
				delete(s.gatherSeen, s.pendingGUID())
				s.state.Reason = "opening_callback_rejected_before_transport"
				return false
			}
			if s.retired == nil {
				s.retired = map[uint64]bool{}
			}
			s.retired[s.pendingGUID()] = true
		}
		if s.exactCorpse && s.corpseRequest != nil && op == opcode.CMSGLoot && modcb.PacketWasNotDispatched(err) {
			s.corpseRequest.receipt.OpeningTransportAttempted = false
			s.state.Reason = "opening_callback_rejected_before_transport"
			return false
		}
		if s.policyWindow() && op == opcode.CMSGLootRelease {
			s.state.Reason = "loot_release_outcome_unconfirmed"
			return false
		}
		s.fault("transport_error_no_retry")
		return false
	}
	s.state.TransportWrites++
	if gatherOpening {
		s.gatherRequest.receipt.OpeningTransportWritten = true
	}
	if s.exactCorpse && s.corpseRequest != nil && op == opcode.CMSGLoot {
		s.corpseRequest.receipt.OpeningTransportWritten = true
	}
	return true
}
func (s *Service) releaseLease(ctx context.Context) {
	if s.token != "" {
		_ = s.invoke(ctx, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "loot", Token: s.token}, &actionsv1.State{})
		s.token = ""
	}
}
func (s *Service) retryUnopenedCorpse() {
	if !s.opened && !s.questGather {
		delete(s.seen, s.state.PendingCorpseGuid)
	}
}

func (s *Service) finish(ctx context.Context) {
	// Every exit must retain an unsettled ordinary mutation. A failed release,
	// lost lease or disable cannot turn generic cleanup into a success receipt.
	if (!s.questGather || s.policyWindow()) && s.cleanup.source == 0 && s.pendingGUID() != 0 && s.hasUnsettledLootMutation() {
		s.finishCorpseReceipt("mutation_unconfirmed", false)
		s.reserveMutationQuarantine()
		s.releaseLease(ctx)
		return
	}
	if s.exactCorpse {
		s.finishCorpseReceipt("closed_unverified", true)
	}
	s.releaseLease(ctx)
	if s.questGather {
		r := s.state.LastGather
		if r != nil {
			if r.ItemPushObserved && r.SlotRemoved && r.ReleaseObserved {
				r.Status = lootv1.GatherStatus_GATHER_CONFIRMED
				r.Reason = "server_item_slot_and_release_confirmed"
			} else if r.Status != lootv1.GatherStatus_GATHER_SERVER_REJECTED {
				r.Status = lootv1.GatherStatus_GATHER_UNCONFIRMED
				r.Reason = s.state.Reason
			}
		}
		s.finishGatherRequest()
	} else if s.cleanup.source == 0 {
		s.state.LastCorpseGuid = s.state.PendingCorpseGuid
	}
	s.state.PendingCorpseGuid = 0
	s.state.PendingGameObjectGuid = 0
	s.questGather = false
	s.gatherObjectRevision = 0
	s.items = nil
	s.removedSlots = nil
	s.itemCountKnown = false
	s.window = lootWindow{}
	s.index = 0
	s.itemRequested = false
	s.opened = false
	s.exactCorpse = false
	if s.state.Enabled {
		s.state.Phase = "idle"
	} else {
		s.state.Phase = "disabled"
	}
	s.nextTry = s.now().Add(time.Second)
}
func (s *Service) finishReleased(ctx context.Context) {
	if s.questGather {
		w := &worldv1.SnapshotResponse{}
		if s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, w) == nil {
			s.observeGatherQuest(w)
		}
	}
	s.finish(ctx)
}
func (s *Service) beginRelease(ctx context.Context) {
	previousPhase := s.state.Phase
	if s.questGather && s.opened && (s.state.Phase == "waiting_open" || s.state.Phase == "abort") && !s.state.LastGather.LootWindowObserved {
		packet := []byte{s.gatherCastCount}
		packet = binary.LittleEndian.AppendUint32(packet, s.boundHarvestRecipe().openSpell)
		if !s.write(ctx, opcode.CMSGCancelCast, packet) {
			s.finish(ctx)
			return
		}
	}
	if !s.opened {
		s.finish(ctx)
		return
	}
	s.state.Phase = "waiting_release"
	s.deadline = s.now().Add(2 * time.Second)
	if !s.write(ctx, opcode.CMSGLootRelease, guidPacket(s.pendingGUID())) {
		if s.policyWindow() {
			if modcb.PacketWasNotDispatched(s.lastLootWriteError) {
				s.state.Phase = previousPhase
				return
			}
			s.beginOrdinaryTimeout(ctx, "release_transport_unconfirmed")
			return
		}
		s.finish(ctx)
	}
}
func (s *Service) BeforeLogout(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.state.Enabled = false
	if s.cleanup.source != 0 {
		// Cleanup has already reserved its one release; logout never resends it.
		s.releaseLease(ctx)
		s.state.Reason = "session_closing"
		return &emptypb.Empty{}, nil
	}
	if s.token != "" && s.opened && s.pendingGUID() != 0 {
		s.beginRelease(ctx)
	}
	s.finish(ctx)
	s.state.Reason = "session_closing"
	return &emptypb.Empty{}, nil
}
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			call, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
			s.Tick(call)
			cancel()
		}
	}
}

// Completed ordinary spline endpoints are explicitly estimated; active paths
// and transport-relative coordinates cannot establish a corpse world position.
func corpsePosition(c *worldv1.CreatureState) (float32, float32, float32, bool) {
	if c == nil {
		return 0, 0, 0, false
	}
	x, y, z, known := c.X, c.Y, c.Z, c.PositionKnown
	if t := c.Trajectory; t != nil && t.Known {
		if t.TransportRelative {
			return 0, 0, 0, false
		}
		if !t.Stopped {
			if t.Cyclic || !t.DestinationKnown || t.AgeMs < uint64(t.DurationMs) {
				return 0, 0, 0, false
			}
			x, y, z, known = t.DestinationX, t.DestinationY, t.DestinationZ, true
		}
	} else if c.MovementFlags&0x08000000 != 0 || c.MovementFlags&0xff != 0 && c.PositionAgeMs > 1500 {
		return 0, 0, 0, false
	}
	for _, v := range []float32{x, y, z} {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || math.Abs(float64(v)) > 40000 {
			return 0, 0, 0, false
		}
	}
	return x, y, z, known
}

func corpseWithin(self *worldv1.PlayerState, p *actionsv1.ClientPosition, c *worldv1.CreatureState, maxRange, maxHeight float64, allowCombat bool) bool {
	blocked := uint32(0x1 | 0x4 | 0x40000 | 0x80000 | 0x100000 | 0x400000 | 0x800000 | 0x1000000 | 0x8000000)
	if allowCombat {
		blocked &^= 0x80000
	}
	if self == nil || p == nil || c == nil || !self.Present || !self.AliveKnown || !self.Alive || !self.UnitFlagsKnown || self.UnitFlags&blocked != 0 {
		return false
	}
	if !p.Known || !p.MapKnown || !c.Present || !c.MapKnown || p.MapId != c.MapId || !c.HealthKnown || c.Health != 0 || !c.AliveKnown || c.Alive || c.DynamicFlags&1 == 0 {
		return false
	}
	if kind := c.Guid >> 48; kind != 0xf130 && kind != 0xf150 {
		return false
	}
	x, y, z, known := corpsePosition(c)
	if !known {
		return false
	}
	for _, v := range []float32{p.X, p.Y, p.Z} {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || math.Abs(float64(v)) > 40000 {
			return false
		}
	}
	dx, dy, dz := float64(p.X-x), float64(p.Y-y), float64(p.Z-z)
	return dx*dx+dy*dy+dz*dz <= maxRange*maxRange && math.Abs(dz) <= maxHeight
}

func (s *Service) openLocked(ctx context.Context) {
	world := s.openingCapacity(ctx)
	if world == nil {
		return
	}
	s.state.Phase = "waiting_open"
	s.state.Reason = "loot_open_transport_pending"
	s.deadline = s.now().Add(2 * time.Second)
	if s.questGather {
		s.deadline = s.now().Add(3 * time.Second)
	}
	s.opened = true // Reserve before an ambiguous transport write.
	s.inventoryWriteRevision = world.InventoryRevision
	if s.questGather {
		s.gatherCastCount++
		if s.write(ctx, opcode.CMSGCastSpell, gatherOpenPacket(s.gatherCastCount, s.pendingGUID(), s.boundHarvestRecipe().openSpell)) {
			s.state.LastGather.OpeningTransportWritten = true
		} else {
			s.finish(ctx)
		}
	} else if !s.write(ctx, opcode.CMSGLoot, guidPacket(s.pendingGUID())) {
		s.finish(ctx)
	}
}

func (s *Service) Tick(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	now := s.now()
	if !s.state.Enabled && !s.until.IsZero() && !now.Before(s.until) {
		s.resumeAutomatic()
	}
	if s.cleanup.source != 0 {
		s.tickOrdinaryCleanup(ctx)
		return
	}
	if s.policyWindow() {
		// Observe settlement only. Lua owns window choice, ordering, timeout,
		// retry and release; a native timer must never take another item.
		s.observeWindowMutation()
		if s.state.Phase == "release_observed" {
			s.finishReleased(ctx)
		}
		return
	}
	if s.token != "" {
		var observed *worldv1.SnapshotResponse
		var currentActions *actionsv1.State
		if s.state.Phase == "release_observed" {
			s.finishReleased(ctx)
			return
		}
		if s.state.Phase != "waiting_release" && !now.Before(s.transactionUntil) {
			if !s.questGather {
				s.beginOrdinaryTimeout(ctx, "loot_transaction_timeout")
				return
			}
			s.state.Reason = "loot_transaction_timeout_no_retry"
			s.beginRelease(ctx)
			return
		}
		if s.state.Phase == "abort" {
			s.beginRelease(ctx)
			return
		}
		if s.state.Phase != "waiting_release" {
			world := &worldv1.SnapshotResponse{}
			if s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, world) != nil || world.Self == nil || !world.Self.AliveKnown || !world.Self.Alive || !s.exactCorpse && world.Self.UnitFlags&0x80000 != 0 {
				s.retryUnopenedCorpse()
				s.state.Reason = "loot_preempted_by_combat_or_unknown_state"
				s.beginRelease(ctx)
				return
			}
			observed = world
			active := &actionsv1.State{}
			if s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, active) != nil || active.InteractionOwner != "loot" || active.InteractionLeaseRemainingMs < 1000 {
				s.retryUnopenedCorpse()
				s.fault("interaction_lease_lost_no_further_loot_writes")
				s.finish(ctx)
				return
			}
			currentActions = active
			if s.questGather {
				s.observeGatherQuest(world)
				if active.Moving && s.state.Phase != "gather_approaching" || !s.boundHarvestRecipe().context(world, active, s.selfGUID, false) {
					s.state.Reason = "gather_character_or_quest_became_unsafe"
					s.beginRelease(ctx)
					return
				}
			}
		}
		if (s.state.Phase == "waiting_open" || s.state.Phase == "waiting_item" || s.state.Phase == "waiting_money" || s.state.Phase == "waiting_release") && !now.Before(s.deadline) {
			if !s.questGather && (s.exactCorpse || s.state.Enabled || s.hasUnsettledLootMutation()) {
				s.beginOrdinaryTimeout(ctx, "loot_response_timeout")
				return
			}
			s.state.Reason = "loot_response_timeout_no_retry"
			if s.state.Phase == "waiting_release" {
				s.finish(ctx)
			} else {
				s.beginRelease(ctx)
			}
			return
		}
		switch s.state.Phase {
		case "gather_approaching":
			s.gatherApproachLocked(ctx, observed, currentActions)
		}
		return
	}
	if !s.state.Enabled {
		return
	}
	if now.Before(s.nextTry) {
		return
	}
	s.nextTry = now.Add(time.Second)
	world := &worldv1.SnapshotResponse{}
	if err := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, world); err != nil {
		s.state.Reason = "world_observations_unavailable"
		return
	}
	if s.refreshRetiredSource(ctx) {
		s.state.Reason = "loot_source_new_life_observed_waiting_fresh_corpse"
		return
	}
	if s.state.InventoryPaused {
		if s.pausedGather && s.pausedSource != 0 {
			s.state.Reason = "container_capacity_resume_owned_by_lua"
			return
		}
		if !ownKnownFreeSlot(world, s.selfGUID) || world.InventoryRevision <= s.rejectedInventoryRevision {
			s.state.Reason = "inventory_full_paused_until_known_free_slot"
			return
		}
		s.state.InventoryPaused = false
		s.rejectedInventoryRevision = 0
		delete(s.seen, s.pausedSource)
		s.pausedSource = 0
		s.pausedGather = false
		s.state.Reason = "inventory_space_observed_loot_resumed"
	}
	if !ownKnownFreeSlot(world, s.selfGUID) {
		s.pauseForCapacity()
		return
	}
	currentObjects := map[uint64]bool{}
	for _, g := range world.QuestObjects {
		if g != nil {
			currentObjects[g.Guid] = true
		}
	}
	for _, c := range world.LootableCorpses {
		if c != nil {
			currentObjects[c.Guid] = true
		}
	}
	for guid := range s.seen {
		if !currentObjects[guid] {
			delete(s.seen, guid)
		}
	}
	s.state.Reason = "ordinary_corpse_selection_owned_by_lua"
}

func (s *Service) OnPacket(_ context.Context, r *modv1.WorldPacket) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r == nil {
		return &emptypb.Empty{}, nil
	}
	p := r.Payload
	bad := false
	switch r.Opcode {
	case opcode.SMSGCastFailed:
		if len(p) < 6 || len(p) > 64 {
			bad = true
			break
		}
		if s.questGather && s.token != "" && s.state.Phase == "waiting_open" && p[0] == s.gatherCastCount && binary.LittleEndian.Uint32(p[1:]) == s.boundHarvestRecipe().openSpell {
			s.state.LastGather.Status = lootv1.GatherStatus_GATHER_SERVER_REJECTED
			s.state.LastGather.Reason = "server_opening_cast_rejected"
			s.state.Reason = "server_opening_cast_rejected"
			s.state.Phase = "abort"
		}
	case opcode.SMSGLootResponse:
		window, err := decodeWindow(p)
		if s.questGather {
			window, err = decodeGatherWindow(p)
		}
		if err != nil {
			bad = true
			break
		}
		if s.token == "" || window.guid != s.pendingGUID() {
			break
		}
		if window.failed {
			s.state.LastLootError = uint32(window.errorCode)
			s.state.Reason = "server_loot_error"
			s.state.Phase = "abort"
			if s.questGather {
				s.state.LastGather.Status = lootv1.GatherStatus_GATHER_SERVER_REJECTED
				s.state.LastGather.Reason = "server_loot_error"
			}
			break
		}
		if s.state.Phase != "waiting_open" {
			break
		}
		s.window = window
		s.windowRevision++
		s.removedSlots = map[byte]bool{}
		s.items = nil
		s.index = 0
		s.state.LootWindowsObserved++
		if s.exactCorpse && s.corpseRequest != nil {
			s.corpseRequest.receipt.WindowObserved = true
		}
		if s.questGather {
			s.state.LastGather.LootWindowObserved = true
		}
		s.items = append(s.items, window.items...)
		if s.policyWindow() {
			s.state.Phase = "window_ready"
		} else {
			s.state.Reason = "unbound_loot_window"
			s.state.Phase = "abort"
		}
	case opcode.SMSGLootRemoved:
		if len(p) != 1 {
			bad = true
			break
		}
		if s.token != "" && s.cleanup.source == 0 && s.window.guid == s.pendingGUID() {
			for _, item := range s.window.items {
				if item.slot == p[0] {
					if s.removedSlots == nil {
						s.removedSlots = map[byte]bool{}
					}
					s.removedSlots[p[0]] = true
					s.windowRevision++
					break
				}
			}
		}
		if s.token != "" && s.state.Phase == "waiting_item" && s.index < len(s.items) && p[0] == s.items[s.index].slot && !s.removed {
			s.removed = true
			s.state.LootSlotsRemoved++
			if s.questGather && s.items[s.index].id == s.state.LastGather.ItemId {
				s.state.LastGather.SlotRemoved = true
			}
		}
	case opcode.SMSGItemPushResult:
		push, err := decodePush(p)
		if err != nil {
			bad = true
			break
		}
		if s.token != "" && s.state.Phase == "waiting_item" && s.index < len(s.items) && !s.pushed && !s.unconfirmedItems[push.id] && push.player == s.selfGUID && push.received == 0 && push.created == 0 && push.id == s.items[s.index].id && push.count == s.items[s.index].count {
			s.pushed = true
			s.state.ItemsStoredConfirmed += uint64(push.count)
			if s.questGather && push.id == s.state.LastGather.ItemId {
				s.state.LastGather.ItemPushObserved = true
				s.state.LastGather.Count = push.count
				s.state.LastGather.Status = lootv1.GatherStatus_GATHER_ITEM_STORED
				s.state.LastGather.Reason = "server_inventory_item_push_observed"
			}
			s.state.RecentItems = append(s.state.RecentItems, &lootv1.ItemReceipt{ItemId: push.id, Count: push.count, LootSlot: uint32(s.items[s.index].slot)})
			if len(s.state.RecentItems) > 16 {
				s.state.RecentItems = s.state.RecentItems[len(s.state.RecentItems)-16:]
			}
		}
	case opcode.SMSGLootClearMoney:
		if len(p) != 0 {
			bad = true
		} else if s.token != "" && s.moneySent {
			s.moneyCleared = true
			s.windowRevision++
		}
	case opcode.SMSGLootMoneyNotify:
		if len(p) != 5 || p[4] > 1 {
			bad = true
		} else if s.token != "" && s.moneySent {
			s.state.MoneyAwardedNotified += uint64(binary.LittleEndian.Uint32(p))
		}
	case opcode.SMSGLootReleaseResponse:
		if len(p) != 9 || p[8] > 1 {
			bad = true
			break
		}
		if s.cleanup.source != 0 {
			if binary.LittleEndian.Uint64(p) == s.cleanup.source && s.cleanup.releaseRequested {
				if p[8] == 1 && !s.cleanup.releaseObserved {
					s.cleanup.releaseObserved = true
					s.state.ReleasesObserved++
					if s.questGather && s.state.LastGather != nil && s.state.LastGather.ObjectGuid == s.cleanup.source {
						s.state.LastGather.ReleaseObserved = true
					}
					if s.exactCorpse && s.corpseRequest != nil {
						s.corpseRequest.receipt.ReleaseObserved = true
					}
				} else if p[8] == 0 && !s.cleanup.releaseObserved {
					s.cleanup.releaseRejected = true
				}
			}
			break
		}
		if s.token != "" && binary.LittleEndian.Uint64(p) == s.pendingGUID() {
			if p[8] == 1 {
				if s.exactCorpse && s.corpseRequest != nil {
					s.corpseRequest.receipt.ReleaseObserved = true
				}
				s.state.ReleasesObserved++
				if s.questGather {
					s.state.LastGather.ReleaseObserved = true
				}
				if (!s.questGather || s.policyWindow()) && s.hasUnsettledLootMutation() {
					s.quarantineReleasedMutation(true)
					break
				}
				if s.questGather {
					s.state.LastGather.ReleaseObserved = true
				}
				s.state.Phase = "release_observed"
			} else {
				if s.policyWindow() {
					s.cleanup = ordinaryLootCleanup{source: s.pendingGUID(), timedOutPhase: s.state.Phase, safeToContinue: !s.hasUnsettledLootMutation(), releaseRequested: s.state.Phase == "waiting_release", releaseRejected: true}
					if s.retired == nil {
						s.retired = map[uint64]bool{}
					}
					s.retired[s.pendingGUID()] = true
					s.state.Phase = "loot_cleanup_unconfirmed"
					s.state.Reason = "server_release_rejected_no_retry"
					break
				}
				s.fault("server_release_unconfirmed")
				if (!s.questGather || s.policyWindow()) && s.hasUnsettledLootMutation() {
					s.quarantineReleasedMutation(false)
					break
				}
				s.state.Phase = "release_observed"
			}
		}
	case opcode.SMSGInventoryChangeFailure:
		if len(p) < 1 || (p[0] == 0 && len(p) != 1) || (p[0] != 0 && len(p) != 18 && len(p) != 22 && len(p) != 38) {
			bad = true
			break
		}
		if p[0] != 0 && s.token != "" {
			s.state.LastInventoryError = uint32(p[0])
			// A real inventory rejection settles this request as rejected, not
			// as an ambiguous pickup. Keep its existing pause/disable policy.
			if s.policyWindow() && (s.windowRequest == nil || s.windowRequest.receipt.Kind != "slot" || s.windowRequest.receipt.Terminal || s.state.Phase != "waiting_item") {
				break
			}
			s.itemRequested = false
			if s.policyWindow() && s.windowRequest != nil {
				s.windowRequest.receipt.ServerRejected = true
				s.windowRequest.receipt.InventoryError = uint32(p[0])
				s.windowRequest.receipt.Terminal = true
				if s.questGather {
					s.state.LastGather.Status = lootv1.GatherStatus_GATHER_SERVER_REJECTED
					s.state.LastGather.Reason = "server_inventory_rejected"
					if p[0] == 50 {
						// Preserve the observed rejection/source revision for the
						// retained pre-opening gather workflow. Lua still decides
						// whether/when to close this current window.
						s.state.InventoryPaused = true
						s.rejectedInventoryRevision = max(s.rejectedInventoryRevision, s.inventoryWriteRevision)
						s.pausedSource, s.pausedGather = s.pendingGUID(), true
					}
				}
				s.windowRevision++
				s.state.Phase = "window_ready"
				break
			}
			if p[0] == 50 && s.state.Phase == "waiting_item" {
				s.state.InventoryPaused = true
				if s.inventoryWriteRevision > s.rejectedInventoryRevision {
					s.rejectedInventoryRevision = s.inventoryWriteRevision
				}
				s.pausedSource, s.pausedGather = s.pendingGUID(), s.questGather
				s.state.Reason = "inventory_full_paused_until_known_free_slot"
			} else {
				s.state.Reason = "server_rejected_loot_item"
			}
			if s.questGather {
				s.state.LastGather.Status = lootv1.GatherStatus_GATHER_SERVER_REJECTED
				s.state.LastGather.Reason = "server_inventory_rejected"
			}
			s.state.Phase = "abort"
		}
	}
	if bad {
		s.state.MalformedPackets++
	}
	s.observeWindowMutation()
	return &emptypb.Empty{}, nil
}
