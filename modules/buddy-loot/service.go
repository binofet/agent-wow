// Package buddyloot collects only ordinary, server-permitted nearby corpse loot.
// It never navigates, rolls, sells, destroys items or initiates mail/trades.
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
	seen                                       map[uint64]bool
}

func NewService(session sessionAPI, self uint64) *Service {
	return &Service{session: session, selfGUID: self, now: time.Now, state: lootv1.State{Phase: "disabled", Reason: "disabled"}, seen: map[uint64]bool{}}
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
	if r.Enabled {
		s.until = s.now().Add(4 * time.Hour)
		s.seen = map[uint64]bool{}
		if s.token == "" {
			s.state.Phase = "idle"
		}
		s.state.Reason = "automatic_nearby_ordinary_loot_enabled"
	} else if s.token != "" {
		s.state.Phase = "abort"
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
	}
}
func (s *Service) finish(ctx context.Context) {
	s.releaseLease(ctx)
	s.state.LastCorpseGuid = s.state.PendingCorpseGuid
	s.state.PendingCorpseGuid = 0
	s.items = nil
	s.window = lootWindow{}
	s.index = 0
	if s.state.Enabled {
		s.state.Phase = "idle"
	} else {
		s.state.Phase = "disabled"
	}
	s.nextTry = s.now().Add(time.Second)
}
func (s *Service) beginRelease(ctx context.Context) {
	s.state.Phase = "waiting_release"
	s.deadline = s.now().Add(2 * time.Second)
	if !s.write(ctx, opcode.CMSGLootRelease, guidPacket(s.state.PendingCorpseGuid)) {
		s.finish(ctx)
	}
}
func (s *Service) BeforeLogout(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.state.Enabled = false
	if s.token != "" && s.state.PendingCorpseGuid != 0 {
		_ = s.write(ctx, opcode.CMSGLootRelease, guidPacket(s.state.PendingCorpseGuid))
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

func eligible(self *worldv1.PlayerState, p *actionsv1.ClientPosition, c *worldv1.CreatureState) bool {
	if self == nil || p == nil || c == nil || !self.Present || !self.AliveKnown || !self.Alive || !self.UnitFlagsKnown || self.UnitFlags&(0x1|0x4|0x40000|0x80000|0x100000|0x400000|0x800000|0x1000000|0x8000000) != 0 {
		return false
	}
	if !p.Known || !p.MapKnown || !c.Present || !c.MapKnown || p.MapId != c.MapId || !c.PositionKnown || !c.HealthKnown || c.Health != 0 || !c.AliveKnown || c.Alive || c.DynamicFlags&1 == 0 {
		return false
	}
	if kind := c.Guid >> 48; kind != 0xf130 && kind != 0xf150 {
		return false
	}
	values := []float32{p.X, p.Y, p.Z, c.X, c.Y, c.Z}
	for _, v := range values {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return false
		}
	}
	dx, dy, dz := float64(p.X-c.X), float64(p.Y-c.Y), float64(p.Z-c.Z)
	return dx*dx+dy*dy+dz*dz <= 4.5*4.5 && math.Abs(dz) <= 2
}

func (s *Service) Tick(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	now := s.now()
	if s.token != "" {
		if s.state.Phase == "release_observed" {
			s.finish(ctx)
			return
		}
		if s.state.Phase != "waiting_release" && !now.Before(s.transactionUntil) {
			s.state.Enabled = false
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
			if s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, world) != nil || !ownerReady(world.Owner) || world.Self == nil || !world.Self.AliveKnown || !world.Self.Alive || world.Self.UnitFlags&0x80000 != 0 || (world.Owner != nil && world.Owner.UnitFlags&0x80000 != 0) {
				s.state.Reason = "loot_preempted_by_combat_or_unknown_state"
				s.beginRelease(ctx)
				return
			}
			active := &actionsv1.State{}
			if s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, active) != nil || active.InteractionOwner != "loot" || active.InteractionLeaseRemainingMs < 1000 {
				s.state.Enabled = false
				s.state.Reason = "interaction_lease_lost_no_further_loot_writes"
				s.finish(ctx)
				return
			}
		}
		if (s.state.Phase == "waiting_open" || s.state.Phase == "waiting_item" || s.state.Phase == "waiting_money" || s.state.Phase == "waiting_release") && !now.Before(s.deadline) {
			s.state.Enabled = false
			s.state.Reason = "loot_response_timeout_no_retry"
			if s.state.Phase == "waiting_release" {
				s.finish(ctx)
			} else {
				s.beginRelease(ctx)
			}
			return
		}
		switch s.state.Phase {
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
			if s.index >= len(s.items) {
				s.state.Reason = "ordinary_loot_cycle_released"
				s.beginRelease(ctx)
				return
			}
			item := s.items[s.index]
			s.removed = false
			s.pushed = false
			s.state.Phase = "waiting_item"
			s.deadline = now.Add(2 * time.Second)
			if !s.write(ctx, opcode.CMSGAutostoreLootItem, []byte{item.slot}) {
				s.state.Phase = "abort"
			}
		case "waiting_item":
			if s.removed && s.pushed {
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
	if !ownerReady(world.Owner) {
		s.state.Reason = "loot_deferred_owner_absent_afk_or_unsafe"
		return
	}
	if world.Owner.UnitFlags&0x80000 != 0 {
		s.state.Reason = "loot_deferred_owner_combat"
		return
	}
	current := map[uint64]bool{}
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
	var selected *worldv1.CreatureState
	best := math.Inf(1)
	for _, c := range world.LootableCorpses {
		if c == nil {
			continue
		}
		if s.seen[c.Guid] || !eligible(world.Self, actions.Position, c) {
			continue
		}
		d := math.Pow(float64(actions.Position.X-c.X), 2) + math.Pow(float64(actions.Position.Y-c.Y), 2) + math.Pow(float64(actions.Position.Z-c.Z), 2)
		if d < best {
			best = d
			selected = c
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
	if !eligible(world.Self, lease.State.Position, selected) {
		s.releaseLease(ctx)
		s.state.Reason = "corpse_out_of_range_after_stop"
		return
	}
	s.seen[selected.Guid] = true
	s.state.PendingCorpseGuid = selected.Guid
	s.transactionUntil = now.Add(12 * time.Second)
	s.deadline = now.Add(2 * time.Second)
	s.state.Phase = "waiting_open"
	s.state.Reason = "loot_open_transport_pending"
	s.moneySent = false
	s.moneyCleared = false
	if !s.write(ctx, opcode.CMSGLoot, guidPacket(selected.Guid)) {
		s.finish(ctx)
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
	case opcode.SMSGLootResponse:
		window, err := decodeWindow(p)
		if err != nil {
			bad = true
			break
		}
		if s.token == "" || window.guid != s.state.PendingCorpseGuid {
			break
		}
		if window.failed {
			s.state.LastLootError = uint32(window.errorCode)
			s.state.Reason = "server_loot_error"
			s.state.Phase = "abort"
			break
		}
		if s.state.Phase != "waiting_open" {
			break
		}
		s.window = window
		s.items = nil
		s.index = 0
		s.state.LootWindowsObserved++
		for _, item := range window.items {
			if item.slotType == 0 || item.slotType == 4 {
				s.items = append(s.items, item)
			}
		}
		if window.gold > 0 {
			s.state.Phase = "ready_money"
		} else {
			s.state.Phase = "ready_item"
		}
	case opcode.SMSGLootRemoved:
		if len(p) != 1 {
			bad = true
			break
		}
		if s.token != "" && s.state.Phase == "waiting_item" && s.index < len(s.items) && p[0] == s.items[s.index].slot && !s.removed {
			s.removed = true
			s.state.LootSlotsRemoved++
		}
	case opcode.SMSGItemPushResult:
		push, err := decodePush(p)
		if err != nil {
			bad = true
			break
		}
		if s.token != "" && s.state.Phase == "waiting_item" && s.index < len(s.items) && !s.pushed && push.player == s.selfGUID && push.received == 0 && push.created == 0 && push.id == s.items[s.index].id && push.count == s.items[s.index].count {
			s.pushed = true
			s.state.ItemsStoredConfirmed += uint64(push.count)
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
		if s.token != "" && binary.LittleEndian.Uint64(p) == s.state.PendingCorpseGuid {
			if p[8] == 1 {
				s.state.ReleasesObserved++
				s.state.Phase = "release_observed"
			} else {
				s.state.Enabled = false
				s.state.Reason = "server_release_unconfirmed"
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
			s.state.Enabled = false
			s.state.Reason = "inventory_error_automatic_loot_disabled"
			s.state.Phase = "abort"
		}
	}
	if bad {
		s.state.MalformedPackets++
	}
	return &emptypb.Empty{}, nil
}
