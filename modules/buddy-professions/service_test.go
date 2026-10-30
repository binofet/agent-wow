package buddyprofessions

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
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

// These callbacks are entirely in memory. No actor, socket, lease manager,
// subject process or running game is used by the controller tests.
type professionSessionFixture struct {
	mu              sync.Mutex
	world           *worldv1.SnapshotResponse
	actions         *actionsv1.State
	loot            *lootv1.State
	token           string
	renewToken      string
	worldErr        error
	sendErr         map[uint32]error
	packets         []*modv1.SendPacketRequest
	events          []string
	rpcEvents       []string
	worldHook       func(*worldv1.SnapshotRequest)
	writes          chan *modv1.SendPacketRequest
	releases        []string
	beforeSend      func(*modv1.SendPacketRequest)
	releaseErr      error
	releaseStarted  chan struct{}
	releaseContinue chan struct{}
}

func (f *professionSessionFixture) SendPacket(ctx context.Context, p *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	copy := proto.Clone(p).(*modv1.SendPacketRequest)
	f.mu.Lock()
	f.packets = append(f.packets, copy)
	f.rpcEvents = append(f.rpcEvents, fmt.Sprintf("send:%x", p.Opcode))
	f.events = append(f.events, fmt.Sprintf("send:%x", p.Opcode))
	err, hook := f.sendErr[p.Opcode], f.beforeSend
	f.mu.Unlock()
	if hook != nil {
		hook(copy)
	}
	f.writes <- copy
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (f *professionSessionFixture) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, _ ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var result proto.Message
	f.rpcEvents = append(f.rpcEvents, r.Module+"/"+r.Method)
	switch r.Module + "/" + r.Method {
	case "buddy-world/snapshot":
		if f.worldHook != nil {
			request := &worldv1.SnapshotRequest{}
			if err := r.Request.UnmarshalTo(request); err != nil {
				return nil, err
			}
			f.worldHook(request)
		}
		if f.worldErr != nil {
			return nil, f.worldErr
		}
		result = proto.Clone(f.world)
	case "buddy-actions/status":
		result = proto.Clone(f.actions)
	case "buddy-loot/state":
		result = proto.Clone(f.loot)
	case "buddy-actions/acquire_interaction":
		input := &actionsv1.InteractionRequest{}
		if err := r.Request.UnmarshalTo(input); err != nil {
			return nil, err
		}
		if input.Owner != "professions" {
			return nil, errors.New("fixture requires actual professions ownership")
		}
		if input.Token == "" {
			f.events = append(f.events, "acquire")
			if f.token != "" {
				return nil, status.Error(codes.FailedPrecondition, "owned already")
			}
			f.token = "MOCK-professions-token-1"
			f.actions.InteractionOwner = "professions"
			f.actions.InteractionLeaseRemainingMs = 30000
		} else {
			f.events = append(f.events, "renew:"+input.Token)
			if input.Token != f.token {
				return nil, status.Error(codes.FailedPrecondition, "exact token mismatch")
			}
		}
		token := f.token
		if f.renewToken != "" && input.Token != "" {
			token = f.renewToken
		}
		result = &actionsv1.InteractionLease{Token: token, State: proto.Clone(f.actions).(*actionsv1.State)}
	case "buddy-actions/release_interaction":
		input := &actionsv1.InteractionRelease{}
		if err := r.Request.UnmarshalTo(input); err != nil {
			return nil, err
		}
		f.events = append(f.events, "release:"+input.Token)
		f.releases = append(f.releases, input.Token)
		if input.Owner != "professions" || input.Token == "" || input.Token != f.token {
			return nil, status.Error(codes.FailedPrecondition, "replacement token is protected")
		}
		if f.releaseStarted != nil {
			started, resume := f.releaseStarted, f.releaseContinue
			f.releaseStarted = nil
			f.mu.Unlock()
			close(started)
			select {
			case <-resume:
			case <-ctx.Done():
			}
			f.mu.Lock()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if f.releaseErr != nil {
			return nil, f.releaseErr
		}
		f.token = ""
		f.actions.InteractionOwner = ""
		result = proto.Clone(f.actions)
	case "buddy-trade/lookup_items":
		result = &tradev1.LookupItemsResponse{SessionId: "body"}
	default:
		return nil, fmt.Errorf("unexpected in-memory callback %s/%s", r.Module, r.Method)
	}
	raw, err := anypb.New(result)
	if err != nil {
		return nil, err
	}
	return &modv1.InvokeModuleResponse{Result: raw}, nil
}

func newProfessionServiceFixture(t *testing.T) (*Service, *professionSessionFixture) {
	t.Helper()
	f := &professionSessionFixture{world: professionObservationFixture(), actions: &actionsv1.State{}, loot: &lootv1.State{Phase: "disabled"}, sendErr: map[uint32]error{}, writes: make(chan *modv1.SendPacketRequest, 128)}
	s := NewService(f, "body", 7)
	professionPacket(t, s, opcode.SMSGInitialSpells, professionInitialFixture(3275, 2538, 2540, 7620))
	if _, err := s.GetState(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.mu.Lock()
		op := s.active
		if op != nil {
			op.cancel()
		}
		s.mu.Unlock()
		if op != nil && op.done != nil {
			select {
			case <-op.done:
			case <-time.After(2 * time.Second):
				t.Error("local operation did not stop")
			}
		}
	})
	return s, f
}

func professionInitialFixture(spells ...uint32) []byte {
	p := []byte{0, byte(len(spells)), byte(len(spells) >> 8)}
	for _, spell := range spells {
		p = binary.LittleEndian.AppendUint32(p, spell)
		p = append(p, 0, 0)
	}
	return append(p, 0, 0)
}

func professionPacket(t *testing.T, s *Service, code uint32, p []byte) {
	t.Helper()
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: code, Payload: p}); err != nil {
		t.Fatal(err)
	}
}

func professionRequest(s *Service, id string, spell uint32) *professionsv1.AttemptRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	spec, _ := recipeForSpell(spell)
	return &professionsv1.AttemptRequest{SessionId: "body", RequestId: id, ExpectedRevision: s.state.Revision, SkillId: spec.skill, SpellId: spell, ExpectedSkillsRevision: s.state.SkillsRevision, ExpectedBackpackRevision: s.state.BackpackRevision, ExpectedInventoryRevision: s.state.InventoryRevision}
}

func professionWaitPacket(t *testing.T, f *professionSessionFixture, code uint32) *modv1.SendPacketRequest {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case p := <-f.writes:
			if p.Opcode == code {
				return p
			}
		case <-deadline.C:
			t.Fatalf("local fixture did not send opcode%x", code)
		}
	}
}

func professionWaitDone(t *testing.T, op *operation) {
	t.Helper()
	select {
	case <-op.done:
	case <-time.After(2 * time.Second):
		t.Fatal("local operation did not finish")
	}
}

func professionReceipt(s *Service, op *operation) *professionsv1.Operation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.Clone(op.receipt).(*professionsv1.Operation)
}

func professionCastReceipt(code uint32, caster uint64, count byte, spell uint32) []byte {
	goEvent, flags, mask := code == opcode.SMSGSpellGo, uint32(2), uint32(0)
	if goEvent {
		flags, mask = 0x40100, 2
	}
	p := protocolCastFixture(goEvent, caster, spell, flags, mask)
	p[2*len(protocolPackedFixture(caster))] = count
	return p
}

func professionPushReceipt(player uint64, received, created bool, entry, added, total uint32) []byte {
	p := make([]byte, 45)
	binary.LittleEndian.PutUint64(p, player)
	if received {
		binary.LittleEndian.PutUint32(p[8:], 1)
	}
	if created {
		binary.LittleEndian.PutUint32(p[12:], 1)
	}
	binary.LittleEndian.PutUint32(p[16:], 1)
	// Item::GetBagSlot returns INVENTORY_SLOT_BAG_0=255. Item::GetSlot is
	// absolute inventory slot23..38, unlike world snapshot's normalized0..15.
	// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Item/Item.cpp
	// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Player/Player.h
	p[20] = 255
	binary.LittleEndian.PutUint32(p[21:], 24)
	binary.LittleEndian.PutUint32(p[25:], entry)
	binary.LittleEndian.PutUint32(p[37:], added)
	binary.LittleEndian.PutUint32(p[41:], total)
	return p
}

func professionObserve(t *testing.T, s *Service) {
	t.Helper()
	if err := s.observe(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestServiceFirstAidActualCraftAndIndependentRankEvidence(t *testing.T) {
	for _, increase := range []bool{false, true} {
		t.Run(fmt.Sprintf("rank_increase_%v", increase), func(t *testing.T) {
			s, f := newProfessionServiceFixture(t)
			r := professionRequest(s, "actual-bandage", 3275)
			state, err := s.Attempt(context.Background(), r)
			if err != nil || state.Operation == nil {
				t.Fatal(state, err)
			}
			packet := professionWaitPacket(t, f, opcode.CMSGCastSpell)
			s.mu.Lock()
			op := s.active
			s.mu.Unlock()
			if len(packet.Payload) != 10 || binary.LittleEndian.Uint32(packet.Payload[6:]) != 0 {
				t.Fatalf("non-implicit cast: %x", packet.Payload)
			}
			if receipt := professionReceipt(s, op); receipt.OwnGoObserved || receipt.OutputInventoryObserved || receipt.SkillIncreased || receipt.Status == professionsv1.AttemptStatus_COMPLETED {
				t.Fatal("transport manufactured a craft receipt", receipt)
			}
			for _, code := range []uint32{opcode.SMSGSpellStart, opcode.SMSGSpellGo} {
				professionPacket(t, s, code, professionCastReceipt(code, 7, byte(state.Operation.CastCount), 3275))
			}
			professionPacket(t, s, opcode.SMSGItemPushResult, professionPushReceipt(7, true, true, 1251, 1, 1))
			f.mu.Lock()
			f.world = proto.Clone(f.world).(*worldv1.SnapshotResponse)
			f.world.SelfSkillsRevision++
			if increase {
				f.world.SelfSkills[0].Value++
			}
			f.world.SelfSkills[0].TemporaryBonus += 20
			f.world.BackpackRevision++
			f.world.InventoryRevision++
			f.world.InventoryBags[0].Slots[0].StackCount--
			f.world.InventoryBags[0].Slots[1] = &worldv1.BackpackSlot{Slot: 1, ItemGuid: 0x4000000000000003, Entry: 1251, StackCount: 1, MetadataKnown: true}
			f.mu.Unlock()
			professionObserve(t, s)
			professionWaitDone(t, op)
			receipt := professionReceipt(s, op)
			if receipt.Status != professionsv1.AttemptStatus_COMPLETED || !receipt.CastTransportWritten || !receipt.OwnStartObserved || !receipt.OwnGoObserved || !receipt.ReagentsConsumedObserved || !receipt.OutputInventoryObserved || receipt.OwnCreatedItemsObserved != 1 || receipt.SkillIncreased != increase || receipt.BeforeRank != 12 || receipt.AfterRank != 12+boolUint(increase) || receipt.ObservedSkillsRevision != 5 || receipt.ObservedBackpackRevision != 10 || !receipt.LeaseReleased {
				t.Fatalf("actual craft evidence incorrect: %+v", receipt)
			}
			duplicate, err := s.Attempt(context.Background(), proto.Clone(r).(*professionsv1.AttemptRequest))
			if err != nil || duplicate.Operation.OperationId != receipt.OperationId || duplicate.Active {
				t.Fatal("completed duplicate was not replayed", duplicate, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.packets) != 1 || len(f.releases) != 1 {
				t.Fatalf("duplicate replay sent more work: packets%d releases%d", len(f.packets), len(f.releases))
			}
		})
	}
}

func boolUint(value bool) uint32 {
	if value {
		return 1
	}
	return 0
}

func TestServiceAttemptBindingAndReadinessBlocksBeforeTransport(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Service, *professionSessionFixture, *professionsv1.AttemptRequest)
		code   codes.Code
	}{
		{"wrong session", func(_ *Service, _ *professionSessionFixture, r *professionsv1.AttemptRequest) {
			r.SessionId = "foreign"
		}, codes.InvalidArgument},
		{"empty request", func(_ *Service, _ *professionSessionFixture, r *professionsv1.AttemptRequest) { r.RequestId = "" }, codes.InvalidArgument},
		{"unsafe request", func(_ *Service, _ *professionSessionFixture, r *professionsv1.AttemptRequest) {
			r.RequestId = "../retry"
		}, codes.InvalidArgument},
		{"unsupported spell", func(_ *Service, _ *professionSessionFixture, r *professionsv1.AttemptRequest) { r.SpellId = 99999 }, codes.InvalidArgument},
		{"wrong skill", func(_ *Service, _ *professionSessionFixture, r *professionsv1.AttemptRequest) { r.SkillId = 185 }, codes.InvalidArgument},
		{"stale state", func(_ *Service, _ *professionSessionFixture, r *professionsv1.AttemptRequest) { r.ExpectedRevision-- }, codes.FailedPrecondition},
		{"stale skills", func(_ *Service, _ *professionSessionFixture, r *professionsv1.AttemptRequest) {
			r.ExpectedSkillsRevision--
		}, codes.FailedPrecondition},
		{"stale bag", func(_ *Service, _ *professionSessionFixture, r *professionsv1.AttemptRequest) {
			r.ExpectedBackpackRevision--
		}, codes.FailedPrecondition},
		{"unknown spellbook", func(s *Service, _ *professionSessionFixture, _ *professionsv1.AttemptRequest) { s.book.known = false }, codes.FailedPrecondition},
		{"recipe absent", func(s *Service, _ *professionSessionFixture, _ *professionsv1.AttemptRequest) {
			delete(s.book.spells, 3275)
		}, codes.FailedPrecondition},
		{"unknown baseline", func(_ *Service, f *professionSessionFixture, _ *professionsv1.AttemptRequest) {
			f.world.SelfSkillsKnown = false
		}, codes.FailedPrecondition},
		{"missing materials", func(_ *Service, f *professionSessionFixture, _ *professionsv1.AttemptRequest) {
			f.world.InventoryBags[0].Slots[0] = &worldv1.BackpackSlot{Slot: 0}
			f.world.InventoryBags[0].Slots[15] = &worldv1.BackpackSlot{Slot: 15}
		}, codes.FailedPrecondition},
		{"no free slot", func(_ *Service, f *professionSessionFixture, _ *professionsv1.AttemptRequest) {
			for i := range f.world.InventoryBags[0].Slots {
				f.world.InventoryBags[0].Slots[i] = &worldv1.BackpackSlot{Slot: uint32(i), ItemGuid: 0x4000000000000000 + uint64(i+1), Entry: 2589, StackCount: 1, MetadataKnown: true}
			}
		}, codes.FailedPrecondition},
		{"moving", func(_ *Service, f *professionSessionFixture, _ *professionsv1.AttemptRequest) {
			f.actions.Moving = true
		}, codes.FailedPrecondition},
		{"loot active", func(_ *Service, f *professionSessionFixture, _ *professionsv1.AttemptRequest) { f.loot.Enabled = true }, codes.FailedPrecondition},
		{"foreign interaction", func(_ *Service, f *professionSessionFixture, _ *professionsv1.AttemptRequest) {
			f.actions.InteractionOwner = "mail"
		}, codes.FailedPrecondition},
		{"closing", func(s *Service, _ *professionSessionFixture, _ *professionsv1.AttemptRequest) { s.closed = true }, codes.FailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newProfessionServiceFixture(t)
			r := professionRequest(s, "blocked", 3275)
			tc.mutate(s, f, r)
			_, err := s.Attempt(context.Background(), r)
			if status.Code(err) != tc.code {
				t.Fatalf("got %v, want %v", err, tc.code)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.packets) != 0 || len(f.events) != 0 || len(s.requests) != 0 {
				t.Fatal("blocked request reserved mutation work", f.events)
			}
		})
	}
}

func TestServiceSessionReceiptCapacityDoesNotEvictOrRetry(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	for i := range 256 {
		request := professionRequest(s, fmt.Sprintf("old-%d", i), 3275)
		s.requests[request.RequestId] = &operation{request: request, receipt: &professionsv1.Operation{OperationId: request.RequestId, Status: professionsv1.AttemptStatus_COMPLETED}}
	}
	_, err := s.Attempt(context.Background(), professionRequest(s, "overflow", 3275))
	if status.Code(err) != codes.ResourceExhausted || len(s.requests) != 256 || len(f.packets) != 0 {
		t.Fatal("receipt capacity did not stop new work", err)
	}
	old := s.requests["old-0"]
	if replay, err := s.Attempt(context.Background(), old.request); err != nil || replay.Operation.OperationId != "old-0" {
		t.Fatal("capacity broke existing replay", replay, err)
	}
	changed := proto.Clone(old.request).(*professionsv1.AttemptRequest)
	changed.ExpectedRevision++
	if _, err := s.Attempt(context.Background(), changed); status.Code(err) != codes.AlreadyExists {
		t.Fatal("same ID accepted a different request", err)
	}
}

func TestServiceSnapshotCopiesAndReportsMissingObservations(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	state, err := s.GetState(context.Background(), &emptypb.Empty{})
	if err != nil || state.SessionId != "body" || state.SelfGuid != 7 || !state.SkillsKnown || !state.SpellsKnown || state.Skills[0].Rank != 12 {
		t.Fatal(state, err)
	}
	state.Skills[0].Rank = 999
	state.Recipes[0].Reason = "forged"
	again, _ := s.GetState(context.Background(), &emptypb.Empty{})
	if again.Skills[0].Rank != 12 || again.Recipes[0].Reason == "forged" {
		t.Fatal("caller mutated service state")
	}
	f.mu.Lock()
	f.worldErr = errors.New("MOCK observations unavailable")
	f.mu.Unlock()
	unknown, _ := s.GetState(context.Background(), &emptypb.Empty{})
	if unknown.SkillsKnown || unknown.BackpackKnown || unknown.Recipes[0].Ready || !strings.Contains(unknown.Recipes[0].Reason, "unavailable") {
		t.Fatal("missing observations left optimistic readiness", unknown)
	}
}

func TestServiceObservationsCannotRegressAfterTemporaryFailure(t *testing.T) {
	for _, temporaryFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(temporaryFailure), func(t *testing.T) {
			s, f := newProfessionServiceFixture(t)
			old := proto.Clone(f.world).(*worldv1.SnapshotResponse)
			f.world = proto.Clone(f.world).(*worldv1.SnapshotResponse)
			f.world.SelfSkillsRevision += 2
			f.world.BackpackRevision += 2
			f.world.InventoryRevision += 2
			f.world.SelfSkills[0].Value = 13
			professionObserve(t, s)
			if temporaryFailure {
				f.worldErr = errors.New("MOCK temporary lost observation")
				if err := s.observe(context.Background()); err == nil {
					t.Fatal("fixture error ignored")
				}
				f.worldErr = nil
			}
			f.world = old
			if err := s.observe(context.Background()); err == nil {
				t.Fatal("stale public snapshot accepted after newer revision")
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.state.SkillsRevision != 6 || s.state.BackpackRevision != 11 || s.state.SkillsKnown || s.state.BackpackKnown || s.state.Skills[0].Rank != 13 {
				t.Fatal("stale result reset admitted rank/revision or remained available", s.state)
			}
		})
	}
}

func TestServiceFocusQueriesBoundedReservedAndOnlyObserved(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	clock := time.Unix(123456, 0)
	s.now = func() time.Time { return clock }
	f.sendErr[opcode.CMSGGameobjectQuery] = errors.New("MOCK ambiguous read transport")
	for i := range 32 {
		f.world.SpellFocuses = append(f.world.SpellFocuses, &worldv1.GameObjectState{Guid: 0xf110000000000001 + uint64(i), Entry: uint32(100 + i), Present: true, TypeKnown: true, GameObjectType: 8})
	}
	professionObserve(t, s)
	if len(f.packets) != 4 || len(s.focusRequested) != 4 {
		t.Fatal("query batch did not cap at4", len(f.packets))
	}
	professionObserve(t, s)
	if len(f.packets) != 4 {
		t.Fatal("query batch ignored interval")
	}
	for range 9 {
		clock = clock.Add(time.Second)
		professionObserve(t, s)
	}
	if len(f.packets) != 32 || len(s.focusRequested) != 32 {
		t.Fatal("query cache unbounded or ambiguous read retried", len(f.packets), len(s.focusRequested))
	}
	for _, packet := range f.packets {
		if packet.Opcode != opcode.CMSGGameobjectQuery || len(packet.Payload) != 12 {
			t.Fatal("focus observation sent another action", packet)
		}
		entry := binary.LittleEndian.Uint32(packet.Payload)
		if entry < 100 || entry >= 132 {
			t.Fatal("unobserved focus queried", entry)
		}
	}
	p, _ := focusResponseFixture([4]string{"Misleading name"})
	professionPacket(t, s, opcode.SMSGGameobjectQueryResponse, p)
	if len(s.focuses) != 0 {
		t.Fatal("unsolicited template accepted")
	}
	binary.LittleEndian.PutUint32(p, 100)
	professionPacket(t, s, opcode.SMSGGameobjectQueryResponse, p)
	if s.focuses[100].focusID != 4 || s.focuses[100].radius != 17 {
		t.Fatal("actual observed/query-bound focus metadata missing", s.focuses)
	}
}

func TestServiceCookingRequiresCurrentActualFocus(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	f.world.InventoryBags[0].Slots[1] = &worldv1.BackpackSlot{Slot: 1, ItemGuid: 0x4000000000000077, Entry: 2672, StackCount: 1, MetadataKnown: true}
	professionObserve(t, s)
	if _, err := s.Attempt(context.Background(), professionRequest(s, "no-focus", 2538)); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "focus") {
		t.Fatal("cooking without observed focus admitted", err)
	}
	object := &worldv1.GameObjectState{Guid: 0xf110000000000077, Entry: 100, Present: true, TypeKnown: true, GameObjectType: 8, MapKnown: true, MapId: 0, PositionKnown: true, X: f.world.Self.X + 2, Y: f.world.Self.Y, Z: f.world.Self.Z, StateKnown: true, State: 1}
	f.world.SpellFocuses = []*worldv1.GameObjectState{object}
	s.focusRequested[100] = true
	s.focuses[100] = focusTemplate{entry: 100, focusID: 999, radius: 3}
	professionObserve(t, s)
	if s.reasonLocked(starterRecipes[1]) == "" {
		t.Fatal("matching display object with wrong actual focusID admitted")
	}
	s.focuses[100] = focusTemplate{entry: 100, focusID: 4, radius: 3}
	if reason := s.reasonLocked(starterRecipes[1]); reason != "" {
		t.Fatal("actual current same-map in-range focus rejected", reason)
	}
	f.world.SpellFocuses[0].X += 10
	professionObserve(t, s)
	if s.reasonLocked(starterRecipes[1]) == "" {
		t.Fatal("out-of-range focus authorized cooking")
	}
}

func TestServiceTerminalSuccessWaitsForOwnedCleanupAcrossBothAPIs(t *testing.T) {
	for _, releaseFails := range []bool{false, true} {
		t.Run(fmt.Sprint(releaseFails), func(t *testing.T) {
			s, f := newProfessionServiceFixture(t)
			started, resume := make(chan struct{}), make(chan struct{})
			var unblock sync.Once
			defer unblock.Do(func() { close(resume) })
			f.releaseStarted, f.releaseContinue = started, resume
			if releaseFails {
				f.releaseErr = errors.New("MOCK lease release response lost")
			}
			r := professionRequest(s, "cleanup-pending", 3275)
			first, err := s.Attempt(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			professionWaitPacket(t, f, opcode.CMSGCastSpell)
			s.mu.Lock()
			op := s.active
			s.mu.Unlock()
			professionPacket(t, s, opcode.SMSGSpellGo, professionCastReceipt(opcode.SMSGSpellGo, 7, byte(first.Operation.CastCount), 3275))
			professionPacket(t, s, opcode.SMSGItemPushResult, professionPushReceipt(7, true, true, 1251, 1, 1))
			f.mu.Lock()
			f.world = proto.Clone(f.world).(*worldv1.SnapshotResponse)
			f.world.BackpackRevision++
			f.world.InventoryRevision++
			f.world.InventoryBags[0].Slots[0].StackCount--
			f.world.InventoryBags[0].Slots[1] = &worldv1.BackpackSlot{Slot: 1, ItemGuid: 0x4000000000000066, Entry: 1251, StackCount: 1, MetadataKnown: true}
			f.mu.Unlock()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("cleanup did not reach local release barrier")
			}
			state, err := s.GetState(context.Background(), &emptypb.Empty{})
			if err != nil || !state.Active || terminalStatus(state.Operation.Status) || state.Operation.LeaseReleased {
				t.Fatal("GetState exposed terminal success before owned cleanup", state, err)
			}
			replayed, err := s.Attempt(context.Background(), r)
			if err != nil || !replayed.Active || terminalStatus(replayed.Operation.Status) || replayed.Operation.LeaseReleased {
				t.Fatal("duplicate Attempt exposed terminal success before owned cleanup", replayed, err)
			}
			unblock.Do(func() { close(resume) })
			professionWaitDone(t, op)
			final := professionReceipt(s, op)
			if releaseFails {
				if final.Status != professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED || !final.CleanupUnconfirmed || final.LeaseReleased {
					t.Fatal("failed owned cleanup promoted craft to success", final)
				}
			} else if final.Status != professionsv1.AttemptStatus_COMPLETED || final.CleanupUnconfirmed || !final.LeaseReleased {
				t.Fatal("confirmed cleanup did not finalize craft", final)
			}
		})
	}
}

func TestServiceCookingFocusUsesKnownSameMapStoppedActionEstimate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*actionsv1.ClientPosition)
		ready  bool
	}{
		{"current estimate", func(_ *actionsv1.ClientPosition) {}, true},
		{"wrong map", func(p *actionsv1.ClientPosition) { p.MapId = 0 }, false},
		{"unknown map", func(p *actionsv1.ClientPosition) { p.MapKnown = false }, false},
		{"NaN", func(p *actionsv1.ClientPosition) { p.X = float32(math.NaN()) }, false},
		{"infinite", func(p *actionsv1.ClientPosition) { p.Z = float32(math.Inf(1)) }, false},
		{"outside range", func(p *actionsv1.ClientPosition) { p.X = 100 }, false},
		{"unknown falls back to far world", func(p *actionsv1.ClientPosition) { p.Known = false }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newProfessionServiceFixture(t)
			f.world.Self.MapId = 1
			f.world.Self.X, f.world.Self.Y, f.world.Self.Z = -100, 0, 1
			f.world.InventoryBags[0].Slots[1] = &worldv1.BackpackSlot{Slot: 1, ItemGuid: 0x4000000000000077, Entry: 2672, StackCount: 1, MetadataKnown: true}
			f.world.SpellFocuses = []*worldv1.GameObjectState{{Guid: 0xf110000000000077, Entry: 100, Present: true, TypeKnown: true, GameObjectType: 8, MapKnown: true, MapId: 1, PositionKnown: true, X: 10, Y: 20, Z: 30, StateKnown: true, State: 1}}
			s.focusRequested[100] = true
			s.focuses[100] = focusTemplate{entry: 100, focusID: 4, radius: 3}
			f.actions.Position = &actionsv1.ClientPosition{Known: true, MapKnown: true, MapId: 1, X: 10.5, Y: 20, Z: 30, Estimated: true}
			tc.change(f.actions.Position)
			professionObserve(t, s)
			if ready := s.reasonLocked(starterRecipes[1]) == ""; ready != tc.ready {
				t.Fatalf("focus readiness=%v want%v: %s", ready, tc.ready, s.reasonLocked(starterRecipes[1]))
			}
		})
	}
}
