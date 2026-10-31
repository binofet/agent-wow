package buddyinventory

import (
	"context"
	"encoding/binary"
	"errors"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	"sync"
	"testing"
	"time"
)

const junkGUID = uint64(0x4000000000000001)
const bagGUID = uint64(0x4000000000000002)
const vendorGUID = uint64(0xf1300003e8000001)

type fakeInventory struct {
	mu               sync.Mutex
	w                *worldv1.SnapshotResponse
	a                *actionsv1.State
	packets          []*modv1.SendPacketRequest
	leases, releases int
	renewals         int
	leaseToken       string
	trade            *tradev1.State
	loot             *lootv1.State
	prof             *professionsv1.State
	afterSend        func(*modv1.SendPacketRequest)
	afterLease       func()
	afterRenew       func()
	calls            []string
	snapshotVendor   uint64
	sendError        bool
}

func (f *fakeInventory) SendPacket(_ context.Context, r *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "packet")
	f.packets = append(f.packets, proto.Clone(r).(*modv1.SendPacketRequest))
	hook, bad := f.afterSend, f.sendError
	f.mu.Unlock()
	if hook != nil {
		hook(r)
	}
	if bad {
		return nil, errors.New("callback outcome lost")
	}
	return &emptypb.Empty{}, nil
}
func (f *fakeInventory) InvokeModule(_ context.Context, r *modv1.InvokeModuleRequest, _ ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Module+"."+r.Method)
	var out proto.Message
	var hook func()
	switch r.Module + "." + r.Method {
	case "buddy-world.snapshot":
		request := &worldv1.SnapshotRequest{}
		if e := r.Request.UnmarshalTo(request); e != nil {
			f.mu.Unlock()
			return nil, e
		}
		f.snapshotVendor = request.CreatureGuid
		out = proto.Clone(f.w)
	case "buddy-actions.status":
		out = proto.Clone(f.a)
	case "buddy-trade.state":
		if f.trade != nil {
			out = proto.Clone(f.trade)
		} else {
			out = &tradev1.State{SessionId: "body", Phase: "IDLE"}
		}
	case "buddy-loot.state":
		if f.loot != nil {
			out = proto.Clone(f.loot)
		} else {
			out = &lootv1.State{SessionId: "body", Phase: "disabled"}
		}
	case "buddy-professions.state":
		if f.prof != nil {
			out = proto.Clone(f.prof)
		} else {
			out = &professionsv1.State{SessionId: "body", SelfGuid: 6}
		}
	case "buddy-actions.acquire_interaction":
		request := &actionsv1.InteractionRequest{}
		if e := r.Request.UnmarshalTo(request); e != nil {
			f.mu.Unlock()
			return nil, e
		}
		if request.Token != "" {
			f.renewals++
			if request.Token != f.leaseToken {
				f.mu.Unlock()
				return nil, errors.New("lease token replaced")
			}
			hook = f.afterRenew
		} else {
			f.leases++
			f.leaseToken = "fresh-token"
			hook = f.afterLease
		}
		f.a.InteractionOwner = "trade"
		f.a.InteractionLeaseRemainingMs = 15000
		out = &actionsv1.InteractionLease{Token: f.leaseToken, State: proto.Clone(f.a).(*actionsv1.State)}
	case "buddy-actions.release_interaction":
		request := &actionsv1.InteractionRelease{}
		if e := r.Request.UnmarshalTo(request); e != nil {
			f.mu.Unlock()
			return nil, e
		}
		if request.Token != f.leaseToken {
			f.mu.Unlock()
			return nil, errors.New("release token replaced")
		}
		f.releases++
		f.a.InteractionOwner = ""
		f.leaseToken = ""
		out = proto.Clone(f.a)
	default:
		f.mu.Unlock()
		return nil, errors.New("unexpected invocation " + r.Module + "." + r.Method)
	}
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	a, e := anypb.New(out)
	return &modv1.InvokeModuleResponse{Result: a}, e
}
func fixtureInventory() (*Service, *fakeInventory) {
	w := &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{Guid: 6, Present: true, AliveKnown: true, Alive: true, HealthKnown: true, Health: 52, UnitFlagsKnown: true, UnitFlags: 8, MapKnown: true, PositionKnown: true, X: 100, Y: 100, Z: 10, MovementFlagsKnown: true, ChannelObjectKnown: true, ChannelSpellKnown: true}, InventoryKnown: true, InventoryRevision: 1, SelfCopperKnown: true, SelfCopper: 100, SelfCopperRevision: 1}
	for b := uint32(0); b < 5; b++ {
		bag := &worldv1.InventoryBag{Bag: b, SlotKnown: true, ContentsKnown: true}
		if b == 0 {
			bag.SlotCount = 16
			for i := uint32(0); i < 16; i++ {
				bag.Slots = append(bag.Slots, &worldv1.BackpackSlot{Slot: i})
			}
		}
		w.InventoryBags = append(w.InventoryBags, bag)
	}
	w.InventoryBags[0].Slots[0] = &worldv1.BackpackSlot{Slot: 0, ItemGuid: junkGUID, Entry: 4865, StackCount: 3, MetadataKnown: true}
	w.InventoryBags[0].Slots[1] = &worldv1.BackpackSlot{Slot: 1, ItemGuid: bagGUID, Entry: 41599, StackCount: 1, MetadataKnown: true}
	w.NearbyCreatures = []*worldv1.CreatureState{{Guid: vendorGUID, Entry: 1000, Present: true, MapKnown: true, PositionKnown: true, X: 103, Y: 100, Z: 10, AliveKnown: true, Alive: true, HealthKnown: true, Health: 20, NpcFlags: 0x80}}
	f := &fakeInventory{w: w, a: &actionsv1.State{Position: &actionsv1.ClientPosition{Known: true, MapKnown: true, X: 100, Y: 100, Z: 10}}}
	s := NewService(f, "body", 6)
	s.metadata[4865] = itemMetadata{known: true, entry: 4865, class: 15, sellPrice: 5}
	s.metadata[41599] = itemMetadata{known: true, entry: 41599, class: 1, inventoryType: 18, containerSlots: 20, stackable: 1, allowableClass: ^uint32(0), allowableRace: ^uint32(0)}
	s.queried[4865] = true
	s.queried[41599] = true
	return s, f
}
func stateInventory(t *testing.T, s *Service) *inventoryv1.State {
	t.Helper()
	r, e := s.GetState(context.Background(), &emptypb.Empty{})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func moveRequest(t *testing.T, s *Service) *inventoryv1.MoveRequest {
	v := stateInventory(t, s)
	return &inventoryv1.MoveRequest{SessionId: "body", RequestId: "move1", ExpectedRevision: v.Revision, ExpectedInventoryRevision: v.InventoryRevision, Source: &inventoryv1.ItemRef{Bag: 0, Slot: 0, ItemGuid: junkGUID, Entry: 4865, StackCount: 3}, DestinationSlot: 4}
}
func equipRequest(t *testing.T, s *Service) *inventoryv1.EquipRequest {
	v := stateInventory(t, s)
	return &inventoryv1.EquipRequest{SessionId: "body", RequestId: "equip1", ExpectedRevision: v.Revision, ExpectedInventoryRevision: v.InventoryRevision, Source: &inventoryv1.ItemRef{Slot: 1, ItemGuid: bagGUID, Entry: 41599, StackCount: 1}, EquipmentSlot: 19}
}
func sellRequest(t *testing.T, s *Service) *inventoryv1.SellRequest {
	v := stateInventory(t, s)
	return &inventoryv1.SellRequest{SessionId: "body", RequestId: "sell1", ExpectedRevision: v.Revision, ExpectedInventoryRevision: v.InventoryRevision, Source: &inventoryv1.ItemRef{Slot: 0, ItemGuid: junkGUID, Entry: 4865, StackCount: 3}, VendorGuid: vendorGUID, ExpectedUnitSellPriceCopper: 5, ExpectedCopperRevision: v.CopperRevision}
}
func waitInventory(t *testing.T, s *Service) *inventoryv1.State {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		r := s.snapshot()
		if !r.Active {
			return r
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("operation did not finish")
	return nil
}
func actualProof(s *Service, f *fakeInventory, credit bool) {
	f.afterSend = func(r *modv1.SendPacketRequest) {
		if r.Opcode == opcode.CMSGItemQuerySingle {
			return
		}
		s.mu.Lock()
		w := proto.Clone(s.last.expected.w).(*worldv1.SnapshotResponse)
		s.mu.Unlock()
		w.InventoryRevision++
		if credit {
			w.SelfCopper += 15
			w.SelfCopperRevision++
		}
		f.mu.Lock()
		f.w = w
		f.mu.Unlock()
	}
}

func TestInventoryExactMoveAndImmutableRequest(t *testing.T) {
	s, f := fixtureInventory()
	r := moveRequest(t, s)
	actualProof(s, f, false)
	if _, e := s.Move(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	st := waitInventory(t, s)
	if st.Operation.Status != "CONFIRMED" || !st.Operation.InventoryConfirmed || !st.Operation.LeaseReleased || f.leases != 1 || f.releases != 1 || len(f.packets) != 1 {
		t.Fatal(st, f.leases, f.releases)
	}
	if got := f.packets[0].Payload; len(got) != 4 || got[0] != 255 || got[1] != 27 || got[2] != 255 || got[3] != 23 {
		t.Fatal(got)
	}
	s.Move(context.Background(), r)
	if len(f.packets) != 1 {
		t.Fatal("replayed move")
	}
	changed := proto.Clone(r).(*inventoryv1.MoveRequest)
	changed.DestinationSlot = 5
	if _, e := s.Move(context.Background(), changed); e == nil {
		t.Fatal("same ID changed payload")
	}
}
func TestInventoryEquipObservedBagExpansion(t *testing.T) {
	s, f := fixtureInventory()
	r := equipRequest(t, s)
	actualProof(s, f, false)
	s.Equip(context.Background(), r)
	st := waitInventory(t, s)
	if st.Operation.Status != "CONFIRMED" || !st.Operation.EquipmentConfirmed || st.InventoryBags[1].ContainerGuid != bagGUID || len(st.InventoryBags[1].Slots) != 20 {
		t.Fatal(st)
	}
	if f.packets[0].Opcode != opcode.CMSGAutoequipItemSlot || len(f.packets[0].Payload) != 9 || f.packets[0].Payload[8] != 19 {
		t.Fatal(f.packets)
	}
}
func TestInventorySaleActualWalletAndRemovalDespiteCallbackError(t *testing.T) {
	s, f := fixtureInventory()
	r := sellRequest(t, s)
	actualProof(s, f, true)
	f.sendError = true
	s.Sell(context.Background(), r)
	st := waitInventory(t, s)
	if st.Operation.Status != "CONFIRMED" || st.Operation.TransportWritten || !st.Operation.InventoryConfirmed || !st.Operation.CopperConfirmed || st.Operation.ExpectedCreditCopper != 15 || st.Copper != 115 {
		t.Fatal(st)
	}
	p := f.packets[0]
	if p.Opcode != opcode.CMSGSellItem || len(p.Payload) != 20 || binary.LittleEndian.Uint32(p.Payload[16:]) != 3 {
		t.Fatal(p)
	}
}
func TestInventoryAmbiguousWriteNeverRetriesAndLateProofResolves(t *testing.T) {
	s, f := fixtureInventory()
	r := sellRequest(t, s)
	f.afterSend = func(_ *modv1.SendPacketRequest) { s.mu.Lock(); s.last.cancel(); s.mu.Unlock() }
	s.Sell(context.Background(), r)
	st := waitInventory(t, s)
	if st.Operation.Status != "OUTCOME_UNCONFIRMED" || len(f.packets) != 1 {
		t.Fatal(st)
	}
	s.Sell(context.Background(), r)
	if len(f.packets) != 1 {
		t.Fatal("ambiguous retry")
	}
	next := proto.Clone(r).(*inventoryv1.SellRequest)
	next.RequestId = "new-sale"
	next.ExpectedRevision = stateInventory(t, s).Revision
	if _, e := s.Sell(context.Background(), next); e == nil {
		t.Fatal("second operation after uncertainty")
	}
	s.mu.Lock()
	w := proto.Clone(s.last.expected.w).(*worldv1.SnapshotResponse)
	s.mu.Unlock()
	w.InventoryRevision++
	w.SelfCopper += 15
	w.SelfCopperRevision++
	f.mu.Lock()
	f.w = w
	f.mu.Unlock()
	st = stateInventory(t, s)
	if st.Operation.Status != "CONFIRMED" {
		t.Fatal(st)
	}
}
func TestInventorySalePartialProofsRemainUnconfirmed(t *testing.T) {
	for _, mode := range []string{"wallet-only", "removal-only", "wrong-credit", "unrelated-slot"} {
		t.Run(mode, func(t *testing.T) {
			s, f := fixtureInventory()
			r := sellRequest(t, s)
			f.afterSend = func(_ *modv1.SendPacketRequest) {
				s.mu.Lock()
				expected := proto.Clone(s.last.expected.w).(*worldv1.SnapshotResponse)
				s.last.cancel()
				s.mu.Unlock()
				f.mu.Lock()
				defer f.mu.Unlock()
				if mode != "wallet-only" {
					f.w = expected
					f.w.InventoryRevision++
				}
				if mode != "removal-only" {
					f.w.SelfCopper += 15
					f.w.SelfCopperRevision++
				}
				if mode == "wrong-credit" {
					f.w.SelfCopper++
				}
				if mode == "unrelated-slot" {
					f.w.InventoryBags[0].Slots[1].ItemGuid = 0
					f.w.InventoryBags[0].Slots[1].Entry = 0
					f.w.InventoryBags[0].Slots[1].StackCount = 0
				}
			}
			s.Sell(context.Background(), r)
			waitInventory(t, s)
			st := stateInventory(t, s)
			if st.Operation.Status == "CONFIRMED" {
				t.Fatal(st)
			}
		})
	}
}
func TestInventoryRechecksSourceAfterFreshLease(t *testing.T) {
	s, f := fixtureInventory()
	r := moveRequest(t, s)
	f.afterLease = func() {
		f.mu.Lock()
		f.w.InventoryRevision++
		f.w.InventoryBags[0].Slots[0].StackCount--
		f.mu.Unlock()
	}
	s.Move(context.Background(), r)
	st := waitInventory(t, s)
	if st.Operation.Status != "BLOCKED" || len(f.packets) != 0 || f.releases != 1 {
		t.Fatal(st, f.packets)
	}
}

func TestInventorySameOwnerReplacementCannotAuthorizeOldToken(t *testing.T) {
	s, f := fixtureInventory()
	r := moveRequest(t, s)
	f.afterLease = func() { f.mu.Lock(); f.leaseToken = "replacement-token"; f.a.InteractionOwner = "trade"; f.mu.Unlock() }
	s.Move(context.Background(), r)
	st := waitInventory(t, s)
	if st.Operation.Status != "BLOCKED" || st.Operation.TransportAttempted || len(f.packets) != 0 || f.renewals != 1 || f.releases != 0 || st.Operation.LeaseReleased {
		t.Fatal(st, f.packets, f.renewals, f.releases)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.leaseToken != "replacement-token" || f.a.InteractionOwner != "trade" {
		t.Fatal("replacement lease disturbed")
	}
}

func TestInventoryFinalWorldAfterRenewalGatesEveryWrite(t *testing.T) {
	for _, name := range []string{"source_changed", "destination_occupied", "combat", "pose_changed", "map_changed", "vendor_removed", "equipment_occupied", "unknown_inventory"} {
		t.Run(name, func(t *testing.T) {
			s, f := fixtureInventory()
			move := moveRequest(t, s)
			var sell *inventoryv1.SellRequest
			var equip *inventoryv1.EquipRequest
			if name == "vendor_removed" {
				sell = sellRequest(t, s)
			}
			if name == "equipment_occupied" {
				equip = equipRequest(t, s)
			}
			f.afterRenew = func() {
				f.mu.Lock()
				defer f.mu.Unlock()
				switch name {
				case "source_changed":
					f.w.InventoryBags[0].Slots[0].StackCount--
				case "destination_occupied":
					f.w.InventoryBags[0].Slots[4] = &worldv1.BackpackSlot{Slot: 4, ItemGuid: junkGUID + 2, Entry: 4865, StackCount: 1, MetadataKnown: true}
				case "combat":
					f.w.Self.UnitFlags |= 0x80000
				case "pose_changed":
					f.w.Self.PositionRevision++
					f.w.Self.X++
				case "map_changed":
					f.w.Self.MapId++
				case "vendor_removed":
					f.w.NearbyCreatures = nil
				case "equipment_occupied":
					b := f.w.InventoryBags[1]
					b.ContainerGuid, b.Entry, b.MetadataKnown, b.SlotCount = bagGUID+2, 41599, true, 20
					for i := uint32(0); i < 20; i++ {
						b.Slots = append(b.Slots, &worldv1.BackpackSlot{Bag: 1, Slot: i})
					}
				case "unknown_inventory":
					f.w.InventoryKnown = false
				}
			}
			var err error
			if sell != nil {
				_, err = s.Sell(context.Background(), sell)
			} else if equip != nil {
				_, err = s.Equip(context.Background(), equip)
			} else {
				_, err = s.Move(context.Background(), move)
			}
			if err != nil {
				t.Fatal(err)
			}
			st := waitInventory(t, s)
			if st.Operation.Status != "BLOCKED" || st.Operation.TransportAttempted || !st.Operation.LeaseReleased || len(f.packets) != 0 || f.renewals != 1 || f.releases != 1 {
				t.Fatal(st, f.packets, f.renewals, f.releases)
			}
		})
	}
}

func TestInventoryFinalSnapshotIsLastRPCBeforeSalePacket(t *testing.T) {
	s, f := fixtureInventory()
	r := sellRequest(t, s)
	actualProof(s, f, true)
	f.afterSend = func(packet *modv1.SendPacketRequest) {
		f.mu.Lock()
		calls := append([]string(nil), f.calls...)
		vendor := f.snapshotVendor
		f.mu.Unlock()
		if len(calls) < 2 || calls[len(calls)-2] != "buddy-world.snapshot" || vendor != vendorGUID {
			t.Errorf("last prewrite RPC=%v vendor=%x", calls, vendor)
		}
		s.mu.Lock()
		w := proto.Clone(s.last.expected.w).(*worldv1.SnapshotResponse)
		s.mu.Unlock()
		w.InventoryRevision++
		w.SelfCopper += 15
		w.SelfCopperRevision++
		f.mu.Lock()
		f.w = w
		f.mu.Unlock()
	}
	if _, err := s.Sell(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if st := waitInventory(t, s); st.Operation.Status != "CONFIRMED" {
		t.Fatal(st)
	}
}

func TestInventoryTimeoutFinishPreservesConcurrentDefinitiveEvidence(t *testing.T) {
	for _, terminal := range []string{"CONFIRMED", "SERVER_REJECTED"} {
		t.Run(terminal, func(t *testing.T) {
			s, _ := fixtureInventory()
			ctx, cancel := context.WithCancel(context.Background())
			op := &operation{receipt: &inventoryv1.Operation{Status: terminal, Reason: "actual_terminal_evidence"}, ctx: ctx, cancel: cancel}
			s.active = op
			s.state.Active = true
			s.finish(op, "OUTCOME_UNCONFIRMED", "timeout_raced_with_evidence")
			if op.receipt.Status != terminal || op.receipt.Reason != "actual_terminal_evidence" || !op.receipt.LeaseReleased || s.state.Active {
				t.Fatal(op.receipt)
			}
		})
	}
}

func TestInventoryActualBagCapacityMustMatchQueriedNormalBagBeforeMoveOrSell(t *testing.T) {
	for _, kind := range []string{"move", "sell"} {
		t.Run(kind, func(t *testing.T) {
			s, f := fixtureInventory()
			b := f.w.InventoryBags[1]
			b.ContainerGuid = bagGUID
			b.Entry = 41599
			b.MetadataKnown = true
			b.SlotCount = 36
			for i := uint32(0); i < 36; i++ {
				b.Slots = append(b.Slots, &worldv1.BackpackSlot{Bag: 1, Slot: i})
			}
			b.Slots[0] = proto.Clone(f.w.InventoryBags[0].Slots[0]).(*worldv1.BackpackSlot)
			b.Slots[0].Bag = 1
			f.w.InventoryBags[0].Slots[0] = &worldv1.BackpackSlot{Slot: 0}
			f.w.InventoryBags[0].Slots[1] = &worldv1.BackpackSlot{Slot: 1}
			st := stateInventory(t, s)
			if !st.InventoryKnown {
				t.Fatal("coherent layout must remain readable")
			}
			source := &inventoryv1.ItemRef{Bag: 1, Slot: 0, ItemGuid: junkGUID, Entry: 4865, StackCount: 3}
			var out *inventoryv1.State
			var e error
			if kind == "move" {
				out, e = s.Move(context.Background(), &inventoryv1.MoveRequest{SessionId: "body", RequestId: "capacity-move", ExpectedRevision: st.Revision, ExpectedInventoryRevision: st.InventoryRevision, Source: source, DestinationSlot: 4})
			} else {
				out, e = s.Sell(context.Background(), &inventoryv1.SellRequest{SessionId: "body", RequestId: "capacity-sell", ExpectedRevision: st.Revision, ExpectedInventoryRevision: st.InventoryRevision, Source: source, VendorGuid: vendorGUID, ExpectedUnitSellPriceCopper: 5, ExpectedCopperRevision: st.CopperRevision})
			}
			if e != nil || out.Operation.Status != "BLOCKED" || len(f.packets) != 0 || f.leases != 0 {
				t.Fatal(out, e, f.packets)
			}
		})
	}
}

func TestInventoryPendingCrossModuleAndOwnerCombatBlockBeforeLease(t *testing.T) {
	for _, kind := range []string{"owner-combat", "profession-outcome", "profession-cleanup", "loot-phase", "trade-accept"} {
		t.Run(kind, func(t *testing.T) {
			s, f := fixtureInventory()
			switch kind {
			case "owner-combat":
				f.w.Owner = &worldv1.PlayerState{Present: true, UnitFlagsKnown: true, UnitFlags: 0x80000}
			case "profession-outcome":
				f.prof = &professionsv1.State{SessionId: "body", SelfGuid: 6, Operation: &professionsv1.Operation{Status: professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED}}
			case "profession-cleanup":
				f.prof = &professionsv1.State{SessionId: "body", SelfGuid: 6, Operation: &professionsv1.Operation{CleanupUnconfirmed: true}}
			case "loot-phase":
				f.loot = &lootv1.State{SessionId: "body", Phase: "awaiting_release"}
			case "trade-accept":
				f.trade = &tradev1.State{SessionId: "body", Phase: "IDLE", AcceptTransportWritten: true}
			}
			r := moveRequest(t, s)
			st, e := s.Move(context.Background(), r)
			if e != nil || st.Operation.Status != "BLOCKED" || st.Operation.TransportAttempted || len(f.packets) != 0 || f.leases != 0 {
				t.Fatal(st, e, f.packets, f.leases)
			}
		})
	}
}
func TestInventoryWrongVendorUnsafeMetadataAndOccupiedDestNeverWrite(t *testing.T) {
	for _, mode := range []string{"occupied", "container-move", "vendor-far", "vendor-flags", "vendor-dead", "metadata", "channel", "wrong-session", "wrong-source", "bag-equipped"} {
		t.Run(mode, func(t *testing.T) {
			s, f := fixtureInventory()
			switch mode {
			case "metadata":
				m := s.metadata[4865]
				m.startQuest = 1
				s.metadata[4865] = m
			case "channel":
				f.w.Self.ChannelSpellId = 7620
			case "vendor-far":
				f.w.NearbyCreatures[0].X = 110
			case "vendor-flags":
				f.w.NearbyCreatures[0].NpcFlags = 2
			case "vendor-dead":
				f.w.NearbyCreatures[0].Alive = false
			}
			var st *inventoryv1.State
			var e error
			if mode == "occupied" || mode == "container-move" || mode == "wrong-session" || mode == "wrong-source" {
				r := moveRequest(t, s)
				if mode == "occupied" {
					r.DestinationSlot = 1
				}
				if mode == "container-move" {
					r.Source = &inventoryv1.ItemRef{Slot: 1, ItemGuid: bagGUID, Entry: 41599, StackCount: 1}
				}
				if mode == "wrong-session" {
					r.SessionId = "foreign"
				}
				if mode == "wrong-source" {
					r.Source.StackCount = 2
				}
				st, e = s.Move(context.Background(), r)
			} else if mode == "bag-equipped" {
				r := equipRequest(t, s)
				r.ExpectedContainerGuid = bagGUID
				st, e = s.Equip(context.Background(), r)
			} else {
				st, e = s.Sell(context.Background(), sellRequest(t, s))
			}
			if e == nil && st.Operation.Status != "BLOCKED" {
				t.Fatal(st)
			}
			if len(f.packets) != 0 || f.leases != 0 {
				t.Fatal(f.packets, f.leases)
			}
		})
	}
}
func TestInventoryWorldRejectsPartialDuplicateAndSpoofedLayouts(t *testing.T) {
	for _, mode := range []string{"actor", "bags", "slot", "guid", "unknown", "overflow", "empty-item-metadata", "empty-bag-metadata"} {
		t.Run(mode, func(t *testing.T) {
			_, f := fixtureInventory()
			switch mode {
			case "actor":
				f.w.Self.Guid = 3
			case "bags":
				f.w.InventoryBags = f.w.InventoryBags[:4]
			case "slot":
				f.w.InventoryBags[0].Slots[15].Slot = 14
			case "guid":
				f.w.InventoryBags[0].Slots[1].ItemGuid = junkGUID
			case "unknown":
				f.w.InventoryBags[1].ContainerGuid = bagGUID
				f.w.InventoryBags[1].ContentsKnown = false
			case "overflow":
				f.w.InventoryBags[1].SlotCount = 37
			case "empty-item-metadata":
				f.w.InventoryBags[0].Slots[15].MetadataKnown = true
			case "empty-bag-metadata":
				f.w.InventoryBags[1].MetadataKnown = true
			}
			if _, e := validateWorld(f.w, 6); e == nil {
				t.Fatal("unsafe layout accepted")
			}
		})
	}
}
func TestInventoryExactVectorRejectsLostOrChangedOtherItems(t *testing.T) {
	s, f := fixtureInventory()
	r := moveRequest(t, s)
	s.mu.Lock()
	_ = s
	s.mu.Unlock()
	v, e := validateWorld(f.w, 6)
	if e != nil {
		t.Fatal(e)
	}
	expected, e := expectedAfter(v, &inventoryv1.Operation{Kind: "move", Source: r.Source, DestinationSlot: 4}, itemMetadata{})
	if e != nil {
		t.Fatal(e)
	}
	actual := proto.Clone(expected.w).(*worldv1.SnapshotResponse)
	actual.InventoryBags[0].Slots[1].StackCount = 2
	changed, _ := validateWorld(actual, 6)
	if exactVector(expected, changed) {
		t.Fatal("changed unrelated stack accepted")
	}
}
