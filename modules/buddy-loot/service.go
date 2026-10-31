// Package buddyloot collects server-permitted nearby corpse loot and the
// fixed Milly harvest quest object. It never rolls, sells or destroys items.
package buddyloot

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
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

type sessionAPI interface {
	SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error)
	InvokeModule(context.Context, *modv1.InvokeModuleRequest, ...grpc.CallOption) (*modv1.InvokeModuleResponse, error)
}
type Service struct {
	lootv1.UnimplementedBuddyLootServer
	mu                                         sync.Mutex
	session                                    sessionAPI
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
	questWolf                                  bool
	questColdridge                             bool
	questFargodeep                             bool
	coldridgeGoal                              coldridgeLootGoal
	questGather                                bool
	gatherCastCount                            byte
	gatherObjectRevision                       uint64
	pausedSource                               uint64
	pausedGather                               bool
	inventoryWriteRevision                     uint64
	rejectedInventoryRevision                  uint64
}

func NewService(session sessionAPI, self uint64, sessionID ...string) *Service {
	s := &Service{session: session, selfGUID: self, now: time.Now, state: lootv1.State{Phase: "disabled", Reason: "disabled"}, seen: map[uint64]bool{}}
	if len(sessionID) > 0 {
		s.state.SessionId = sessionID[0]
	}
	return s
}
func (s *Service) snapshotLocked() *lootv1.State {
	r := proto.Clone(&s.state).(*lootv1.State)
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
	s.state.Enabled = r.Enabled
	if s.cleanup.source != 0 {
		if r.Enabled {
			s.until = s.now().Add(4 * time.Hour)
		} else {
			s.state.Reason = "disabled_by_request"
		}
		return s.snapshotLocked(), nil
	}
	if r.Enabled {
		s.until = s.now().Add(4 * time.Hour)
		s.seen = map[uint64]bool{}
		s.gatherSeen = map[uint64]bool{}
		if s.token == "" {
			s.state.Phase = "idle"
		}
		s.state.Reason = "automatic_nearby_ordinary_loot_enabled"
	} else if s.token != "" {
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
	if s.session == nil {
		return false
	}
	_, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: op, Payload: p})
	if err != nil {
		s.state.Enabled = false
		s.state.Reason = "transport_error_no_retry"
		return false
	}
	s.state.TransportWrites++
	return true
}
func (s *Service) releaseLease(ctx context.Context) {
	if s.token != "" {
		_ = s.invoke(ctx, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "loot", Token: s.token}, &actionsv1.State{})
		s.token = ""
		s.questWolf = false
		s.questColdridge = false
		s.questFargodeep = false
		s.coldridgeGoal = coldridgeLootGoal{}
	}
}
func (s *Service) finish(ctx context.Context) {
	// Every exit must retain an unsettled ordinary mutation. A failed release,
	// lost lease or disable cannot turn generic cleanup into a success receipt.
	if !s.questGather && s.cleanup.source == 0 && s.pendingGUID() != 0 && s.hasUnsettledLootMutation() {
		s.reserveMutationQuarantine()
		s.releaseLease(ctx)
		return
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
	s.questWolf = false
	s.questColdridge = false
	s.questFargodeep = false
	s.coldridgeGoal = coldridgeLootGoal{}
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
	if s.questGather && s.opened && (s.state.Phase == "waiting_open" || s.state.Phase == "abort") && !s.state.LastGather.LootWindowObserved {
		packet := []byte{s.gatherCastCount}
		packet = binary.LittleEndian.AppendUint32(packet, harvestOpenSpell)
		if !s.write(ctx, opcode.CMSGCancelCast, packet) {
			s.finish(ctx)
			return
		}
	}
	if !s.opened {
		s.finish(ctx)
		return
	}
	if s.questColdridge {
		if _, owned := s.renewColdridgeLease(ctx); !owned {
			s.state.Enabled = false
			s.state.Reason = "coldridge_interaction_token_lost_no_further_loot_writes"
			s.finish(ctx)
			return
		}
	}
	s.state.Phase = "waiting_release"
	s.deadline = s.now().Add(2 * time.Second)
	if !s.write(ctx, opcode.CMSGLootRelease, guidPacket(s.pendingGUID())) {
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

func ownerReady(p *worldv1.PlayerState) bool {
	return p != nil && p.Guid == 3 && p.Present && p.AliveKnown && p.Alive && p.PlayerFlags&2 == 0 && p.UnitFlagsKnown
}

// This exception belongs to the acting character's observed quest log. Party
// members' quest IDs, backpack guesses, and an enable request cannot grant it.
func ownWolfQuest(world *worldv1.SnapshotResponse, selfGUID uint64) bool {
	if world == nil || world.Self == nil || world.Self.Guid != selfGUID || !world.SelfQuestLogKnown || len(world.SelfQuestLog) != 25 {
		return false
	}
	var slots [25]bool
	found := false
	for _, entry := range world.SelfQuestLog {
		if entry == nil || entry.Slot >= 25 || slots[entry.Slot] {
			return false
		}
		slots[entry.Slot] = true
		if entry.QuestId == 33 && !entry.Failed && entry.State&2 == 0 {
			found = true
		}
	}
	return found
}

func wolfQuestContext(world *worldv1.SnapshotResponse, p *actionsv1.ClientPosition, selfGUID uint64) bool {
	if !ownWolfQuest(world, selfGUID) || p == nil {
		return false
	}
	return safeOwnQuestLootContext(world, p)
}

func safeOwnQuestLootContext(world *worldv1.SnapshotResponse, p *actionsv1.ClientPosition) bool {
	if world == nil || world.Self == nil || p == nil {
		return false
	}
	self := world.Self
	if !self.Present || !self.AliveKnown || !self.Alive || !self.HealthKnown || self.Health == 0 || !self.MapKnown || self.MapId != 0 || !self.PositionKnown || !self.UnitFlagsKnown || self.UnitFlags&(0x1|0x4|0x40000|0x80000|0x100000|0x400000|0x800000|0x1000000|0x2000000|0x8000000) != 0 || !self.MovementFlagsKnown || self.MovementFlags&^uint32(0x1ff) != 0 || self.ExtraMovementFlags != 0 || !self.SpeedsKnown || math.IsNaN(float64(self.RunSpeed)) || math.IsInf(float64(self.RunSpeed), 0) || math.Abs(float64(self.RunSpeed)-7) > 0.01 {
		return false
	}
	if !p.Known || !p.MapKnown || p.MapId != 0 {
		return false
	}
	for _, v := range []float32{p.X, p.Y, p.Z} {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || math.Abs(float64(v)) > 40000 {
			return false
		}
	}
	return true
}

func activeOwnerReady(world *worldv1.SnapshotResponse) bool {
	return world != nil && ownerReady(world.Owner) && world.Owner.UnitFlags&0x80000 == 0
}

func eligible(self *worldv1.PlayerState, p *actionsv1.ClientPosition, c *worldv1.CreatureState) bool {
	return eligibleWithin(self, p, c, 4.5, 2)
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

func eligibleWithin(self *worldv1.PlayerState, p *actionsv1.ClientPosition, c *worldv1.CreatureState, maxRange, maxHeight float64) bool {
	if self == nil || p == nil || c == nil || !self.Present || !self.AliveKnown || !self.Alive || !self.UnitFlagsKnown || self.UnitFlags&(0x1|0x4|0x40000|0x80000|0x100000|0x400000|0x800000|0x1000000|0x8000000) != 0 {
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

// Renew the retained token; an owner label cannot identify this operation.
func (s *Service) renewColdridgeLease(ctx context.Context) (*actionsv1.State, bool) {
	lease := &actionsv1.InteractionLease{}
	if s.token == "" || s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "loot", Token: s.token, LeaseMs: 15000}, lease) != nil || lease.Token != s.token || lease.State == nil || lease.State.InteractionOwner != "loot" || lease.State.InteractionLeaseRemainingMs < 1000 {
		return nil, false
	}
	return lease.State, true
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
		if s.write(ctx, opcode.CMSGCastSpell, gatherOpenPacket(s.gatherCastCount, s.pendingGUID())) {
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
	if s.cleanup.source != 0 {
		s.tickOrdinaryCleanup(ctx)
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
			if !s.gatherOpeningTimeoutRecoverable() {
				s.state.Enabled = false
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
			if s.invoke(ctx, "buddy-world", "snapshot", s.lootWorldRequest(), world) != nil || world.Self == nil || !world.Self.AliveKnown || !world.Self.Alive || world.Self.UnitFlags&0x80000 != 0 {
				s.state.Reason = "loot_preempted_by_combat_or_unknown_state"
				s.beginRelease(ctx)
				return
			}
			observed = world
			active := &actionsv1.State{}
			if s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, active) != nil || active.InteractionOwner != "loot" || active.InteractionLeaseRemainingMs < 1000 {
				s.state.Enabled = false
				s.state.Reason = "interaction_lease_lost_no_further_loot_writes"
				s.finish(ctx)
				return
			}
			currentActions = active
			if s.questFargodeep && (!s.fargodeepPartyCurrent(ctx) || !fargodeepLootContext(world, active, s.selfGUID, s.state.PendingCorpseGuid)) {
				s.state.Reason = "fargodeep_loot_scope_became_unsafe"
				s.beginRelease(ctx)
				return
			}
			if s.questColdridge {
				if active.MotionUnconfirmed || !s.coldridgeGoal.context(world, active.Position, s.selfGUID, s.state.PendingCorpseGuid) {
					s.state.Reason = "coldridge_loot_scope_became_unsafe"
					s.beginRelease(ctx)
					return
				}
				renewed, owned := s.renewColdridgeLease(ctx)
				if !owned {
					s.state.Enabled = false
					s.state.Reason = "coldridge_interaction_token_lost_no_further_loot_writes"
					s.finish(ctx)
					return
				}
				latest := &worldv1.SnapshotResponse{}
				if s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, latest) != nil || renewed.MotionUnconfirmed || renewed.Moving && s.state.Phase != "approaching" || !s.coldridgeGoal.context(latest, renewed.Position, s.selfGUID, s.state.PendingCorpseGuid) {
					s.state.Reason = "coldridge_loot_scope_became_unsafe"
					s.beginRelease(ctx)
					return
				}
				world, observed, active, currentActions = latest, latest, renewed, renewed
			}
			if s.questGather {
				s.observeGatherQuest(world)
				if active.Moving && s.state.Phase != "gather_approaching" || !s.boundHarvestRecipe().context(world, active, s.selfGUID, false) {
					s.state.Reason = "gather_character_or_quest_became_unsafe"
					s.beginRelease(ctx)
					return
				}
			}
			if !activeOwnerReady(world) && !(s.questWolf && !active.MotionUnconfirmed && wolfQuestContext(world, active.Position, s.selfGUID)) && !(s.questFargodeep && fargodeepLootContext(world, active, s.selfGUID, s.state.PendingCorpseGuid)) && !(s.questColdridge && !active.MotionUnconfirmed && s.coldridgeGoal.context(world, active.Position, s.selfGUID, s.state.PendingCorpseGuid)) && !(s.questGather && (!active.Moving || s.state.Phase == "gather_approaching") && s.boundHarvestRecipe().context(world, active, s.selfGUID, false)) {
				s.state.Reason = "loot_preempted_by_combat_or_unknown_state"
				s.beginRelease(ctx)
				return
			}
		}
		if (s.state.Phase == "waiting_open" || s.state.Phase == "waiting_item" || s.state.Phase == "waiting_money" || s.state.Phase == "waiting_release") && !now.Before(s.deadline) {
			if !s.questGather && (s.state.Enabled || s.hasUnsettledLootMutation()) {
				s.beginOrdinaryTimeout(ctx, "loot_response_timeout")
				return
			}
			if !s.gatherOpeningTimeoutRecoverable() {
				s.state.Enabled = false
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
		case "approaching":
			approach := &actionsv1.State{}
			if s.invoke(ctx, "buddy-actions", "approach_loot", &actionsv1.ApproachLootRequest{Token: s.token, CorpseGuid: s.state.PendingCorpseGuid}, approach) != nil {
				s.state.Reason = "loot_approach_unavailable_no_retry"
				s.finish(ctx)
				return
			}
			if approach.Moving {
				s.state.Reason = "approaching_nearby_corpse"
				return
			}
			var corpse *worldv1.CreatureState
			for _, c := range observed.LootableCorpses {
				if c != nil && c.Guid == s.state.PendingCorpseGuid {
					corpse = c
					break
				}
			}
			if approach.Reason != "loot_approach_within_distance" || !eligible(observed.Self, approach.Position, corpse) {
				s.state.Reason = "loot_approach_stopped_unsafe_or_out_of_range"
				s.finish(ctx)
				return
			}
			s.openLocked(ctx)

		case "ready_money":
			s.moneySent = true
			s.moneyCleared = false
			s.state.Phase = "waiting_money"
			s.deadline = now.Add(2 * time.Second)
			if !s.write(ctx, opcode.CMSGLootMoney, nil) {
				s.state.Phase = "abort"
			}
		case "waiting_money":
			if s.moneyCleared {
				s.state.Phase = "ready_item"
			}
		case "ready_item":
			if s.questGather && s.index < len(s.items) && !s.boundHarvestRecipe().objectCurrent(observed, currentActions, s.pendingGUID()) {
				s.state.Reason = "harvest_object_no_longer_in_range"
				s.beginRelease(ctx)
				return
			}
			if s.questGather && !s.boundHarvestRecipe().quest(observed, s.selfGUID, true) {
				s.state.Reason = "harvest_quest_no_longer_incomplete"
				s.beginRelease(ctx)
				return
			}
			// Shared loot may be removed by another party member before our turn.
			// Skip only slots actually removed from this bound open window.
			for s.index < len(s.items) && s.removedSlots[s.items[s.index].slot] {
				s.index++
			}
			if s.index >= len(s.items) {
				s.state.Reason = "ordinary_loot_cycle_released"
				s.beginRelease(ctx)
				return
			}
			item := s.items[s.index]
			if !ownKnownFreeSlot(observed, s.selfGUID) {
				s.pauseForCapacity()
				s.beginRelease(ctx)
				return
			}
			s.itemCountBefore, s.itemCountKnown = 0, false
			if s.unconfirmedItems[item.id] {
				counts, known := completeOwnItemCounts(observed, s.selfGUID)
				if !known {
					s.state.Reason = "complete_owned_inventory_required_for_unconfirmed_item"
					s.beginRelease(ctx)
					return
				}
				s.itemCountBefore, s.itemCountKnown = counts[item.id], true
			}
			s.removed = false
			s.pushed = false
			s.itemRequested = true
			s.state.Phase = "waiting_item"
			s.deadline = now.Add(2 * time.Second)
			s.inventoryWriteRevision = observed.InventoryRevision
			if !s.write(ctx, opcode.CMSGAutostoreLootItem, []byte{item.slot}) {
				s.state.Phase = "abort"
			}
		case "waiting_item":
			if s.removed && s.pushed || s.unconfirmedItemDelta(observed) {
				s.itemRequested = false
				s.index++
				s.state.Phase = "ready_item"
			}
		}
		return
	}
	if !s.state.Enabled {
		return
	}
	if !now.Before(s.until) {
		s.state.Enabled = false
		s.state.Phase = "disabled"
		s.state.Reason = "automatic_loot_enable_expired"
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
		if !ownKnownFreeSlot(world, s.selfGUID) || world.InventoryRevision <= s.rejectedInventoryRevision {
			s.state.Reason = "inventory_full_paused_until_known_free_slot"
			return
		}
		s.state.InventoryPaused = false
		s.rejectedInventoryRevision = 0
		if s.pausedGather {
			delete(s.gatherSeen, s.pausedSource)
		} else {
			delete(s.seen, s.pausedSource)
		}
		s.pausedSource = 0
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
	if s.tryGatherLocked(ctx, world) {
		return
	}
	if len(s.retired) >= maxRetiredLootSources {
		s.state.Reason = "loot_retired_source_bound_waiting_for_actual_new_life"
		return
	}
	if !ownerReady(world.Owner) && !ownWolfQuest(world, s.selfGUID) && !ownColdridgeQuest(world, s.selfGUID) && !ownFargodeepQuest(world, s.selfGUID) {
		s.state.Reason = "loot_deferred_owner_absent_afk_or_unsafe"
		return
	}
	if world.Owner != nil && world.Owner.UnitFlags&0x80000 != 0 && !ownWolfQuest(world, s.selfGUID) && !ownColdridgeQuest(world, s.selfGUID) && !ownFargodeepQuest(world, s.selfGUID) {
		s.state.Reason = "loot_deferred_owner_combat"
		return
	}
	current := currentObjects
	for _, c := range world.LootableCorpses {
		if c != nil {
			current[c.Guid] = true
		}
	}
	for guid := range s.seen {
		if !current[guid] {
			delete(s.seen, guid)
		}
	}
	if len(world.LootableCorpses) == 0 {
		return
	}
	if len(world.LootableCorpses) > 64 {
		s.state.Reason = "invalid_corpse_observation_count"
		return
	}
	actions := &actionsv1.State{}
	if err := s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, actions); err != nil {
		s.state.Reason = "client_position_unavailable"
		return
	}
	ownerNearby := activeOwnerReady(world) && actions.Position != nil && actions.Position.Known && actions.Position.MapKnown && world.Owner.MapKnown && world.Owner.MapId == actions.Position.MapId && world.Owner.PositionKnown && math.Pow(float64(actions.Position.X-world.Owner.X), 2)+math.Pow(float64(actions.Position.Y-world.Owner.Y), 2)+math.Pow(float64(actions.Position.Z-world.Owner.Z), 2) <= 30*30
	wolfScope := !actions.MotionUnconfirmed && wolfQuestContext(world, actions.Position, s.selfGUID)
	coldridgeScope := !actions.MotionUnconfirmed && coldridgeQuestContext(world, actions.Position, s.selfGUID)
	fargodeepScope := ownFargodeepQuest(world, s.selfGUID) && s.fargodeepPartyCurrent(ctx)
	if !ownerNearby && !wolfScope && !coldridgeScope && !fargodeepScope {
		s.state.Reason = "loot_deferred_owner_not_nearby"
		return
	}
	var selected *worldv1.CreatureState
	var selectedColdridgeGoal coldridgeLootGoal
	selectedFargodeep := false
	best := math.Inf(1)
	for _, c := range world.LootableCorpses {
		if c == nil {
			continue
		}
		goal, matchesColdridge := selectColdridgeLootGoal(world, s.selfGUID, c)
		matchesFargodeep := fargodeepScope && fargodeepLootSelection(world, actions, s.selfGUID, c.Guid)
		if !ownerNearby && !(wolfScope && c.Entry == 299) && !(coldridgeScope && matchesColdridge) && !matchesFargodeep {
			continue
		}
		if s.seen[c.Guid] || s.retired[c.Guid] || !eligibleWithin(world.Self, actions.Position, c, 12, 1.5) {
			continue
		}
		x, y, z, _ := corpsePosition(c)
		d := math.Pow(float64(actions.Position.X-x), 2) + math.Pow(float64(actions.Position.Y-y), 2) + math.Pow(float64(actions.Position.Z-z), 2)
		if d < best {
			best = d
			selected = c
			selectedFargodeep = matchesFargodeep
			selectedColdridgeGoal = coldridgeLootGoal{}
			if coldridgeScope && matchesColdridge {
				selectedColdridgeGoal = goal
			}
		}
	}
	if selected == nil {
		return
	}
	lease := &actionsv1.InteractionLease{}
	if err := s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "loot", LeaseMs: 15000}, lease); err != nil {
		s.state.Reason = "interaction_busy_or_unavailable"
		return
	}
	if lease.Token == "" || lease.State == nil || lease.State.InteractionOwner != "loot" {
		s.state.Reason = "invalid_interaction_lease"
		return
	}
	s.token = lease.Token
	s.questWolf = selected.Entry == 299 && wolfScope
	s.coldridgeGoal = selectedColdridgeGoal
	s.questColdridge = selectedColdridgeGoal.questID != 0
	s.questFargodeep = selectedFargodeep
	if s.questWolf || s.questColdridge || s.questFargodeep {
		selectedGUID := selected.Guid
		fresh := &worldv1.SnapshotResponse{}
		freshErr := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{CreatureGuid: selectedGUID}, fresh)
		// Independent Coldridge looting cannot rely on the position returned
		// before the subsequent world observation completed.
		if freshErr == nil && (s.questColdridge || s.questFargodeep) {
			freshActions := &actionsv1.State{}
			freshErr = s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, freshActions)
			if freshErr == nil && (freshActions.InteractionOwner != "loot" || freshActions.InteractionLeaseRemainingMs < 1000) {
				freshErr = errors.New("owned loot lease changed")
			}
			lease.State = freshActions
		}
		if freshErr != nil || lease.State.MotionUnconfirmed || s.questWolf && !wolfQuestContext(fresh, lease.State.Position, s.selfGUID) || s.questColdridge && !s.coldridgeGoal.context(fresh, lease.State.Position, s.selfGUID, selectedGUID) || s.questFargodeep && (!s.fargodeepPartyCurrent(ctx) || !fargodeepLootSelection(fresh, lease.State, s.selfGUID, selectedGUID)) {
			s.releaseLease(ctx)
			s.questWolf = false
			s.questColdridge = false
			s.state.Reason = "quest_wolf_scope_changed_after_stop"
			return
		}
		world = fresh
		selected = nil
		if len(fresh.LootableCorpses) <= 64 {
			for _, c := range fresh.LootableCorpses {
				if c != nil && c.Guid == selectedGUID && (s.questWolf && c.Entry == 299 || s.questColdridge && s.coldridgeGoal.matchesCorpse(c) || s.questFargodeep && fargodeepLootSelection(fresh, lease.State, s.selfGUID, selectedGUID)) {
					selected = c
					break
				}
			}
		}
	}
	if !eligibleWithin(world.Self, lease.State.Position, selected, 12, 1.5) {
		s.releaseLease(ctx)
		s.state.Reason = "corpse_out_of_range_after_stop"
		return
	}
	s.seen[selected.Guid] = true
	s.state.PendingCorpseGuid = selected.Guid
	s.transactionUntil = now.Add(12 * time.Second)
	s.moneySent = false
	s.moneyCleared = false
	s.itemRequested = false
	if eligible(world.Self, lease.State.Position, selected) {
		s.openLocked(ctx)
	} else {
		s.state.Phase = "approaching"
		s.state.Reason = "nearby_corpse_approach_pending"
	}
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
		if s.questGather && s.token != "" && s.state.Phase == "waiting_open" && p[0] == s.gatherCastCount && binary.LittleEndian.Uint32(p[1:]) == harvestOpenSpell {
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
		s.removedSlots = map[byte]bool{}
		s.items = nil
		s.index = 0
		s.state.LootWindowsObserved++
		if s.questGather {
			s.state.LastGather.LootWindowObserved = true
		}
		for _, item := range window.items {
			if s.questGather && (item.id != s.boundHarvestRecipe().itemID || item.count > 8) {
				continue
			}
			if item.slotType == 0 || item.slotType == 4 {
				s.items = append(s.items, item)
			}
		}
		if s.questGather && len(s.items) != 1 {
			s.state.Reason = "harvest_window_missing_unique_required_item"
			s.state.Phase = "abort"
			break
		}
		if window.gold > 0 && !s.questGather {
			s.state.Phase = "ready_money"
		} else {
			s.state.Phase = "ready_item"
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
					break
				}
			}
		}
		if s.token != "" && s.state.Phase == "waiting_item" && s.index < len(s.items) && p[0] == s.items[s.index].slot && !s.removed {
			s.removed = true
			s.state.LootSlotsRemoved++
			if s.questGather {
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
			if s.questGather {
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
				} else if p[8] == 0 && !s.cleanup.releaseObserved {
					s.cleanup.releaseRejected = true
				}
			}
			break
		}
		if s.token != "" && binary.LittleEndian.Uint64(p) == s.pendingGUID() {
			if p[8] == 1 {
				s.state.ReleasesObserved++
				if !s.questGather && s.hasUnsettledLootMutation() {
					s.quarantineReleasedMutation(true)
					break
				}
				if s.questGather {
					s.state.LastGather.ReleaseObserved = true
				}
				s.state.Phase = "release_observed"
			} else {
				s.state.Enabled = false
				s.state.Reason = "server_release_unconfirmed"
				if !s.questGather && s.hasUnsettledLootMutation() {
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
			s.itemRequested = false
			if p[0] == 50 && s.state.Phase == "waiting_item" {
				s.state.InventoryPaused = true
				if s.inventoryWriteRevision > s.rejectedInventoryRevision {
					s.rejectedInventoryRevision = s.inventoryWriteRevision
				}
				s.pausedSource, s.pausedGather = s.pendingGUID(), s.questGather
				s.state.Reason = "inventory_full_paused_until_known_free_slot"
			} else {
				s.state.Enabled = false
				s.state.Reason = "inventory_error_automatic_loot_disabled"
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
	return &emptypb.Empty{}, nil
}
