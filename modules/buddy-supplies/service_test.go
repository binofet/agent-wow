package buddysupplies

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

const vendorGUID = uint64(0xf130000000000000 | 177<<24 | 1)
const weaponGUID = uint64(0x4000000000000031)
const poleGUID = uint64(0x4000000000000032)

type supplyFixture struct {
	mu                        sync.Mutex
	service                   *Service
	world                     *worldv1.SnapshotResponse
	actions                   *actionsv1.State
	loot                      *lootv1.State
	trade                     *tradev1.State
	professions               *professionsv1.State
	items                     map[uint32]*tradev1.ItemDetails
	packets                   []*modv1.SendPacketRequest
	acquires, releases        int
	leaseToken, releasedToken string
	failOpcode                uint32
	onAcquire                 func()
	onSend                    func(*modv1.SendPacketRequest)
}

func newFixture(t *testing.T) (*Service, *supplyFixture) {
	t.Helper()
	w := &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{Guid: 6, Present: true, AliveKnown: true, Alive: true, HealthKnown: true, Health: 50, MaxHealth: 50, ClassKnown: true, ClassId: 8, LevelKnown: true, Level: 5, MapKnown: true, PositionKnown: true, MovementFlagsKnown: true, UnitFlagsKnown: true, ChannelObjectKnown: true, ChannelSpellKnown: true, Mainhand: &worldv1.EquippedWeapon{SlotKnown: true, ItemGuid: weaponGUID, MetadataKnown: true, Entry: 35}, Offhand: &worldv1.EquippedWeapon{SlotKnown: true}}, BackpackKnown: true, BackpackRevision: 10, SelfCopperKnown: true, SelfCopper: 1000, SelfCopperRevision: 20, SelfSkillsKnown: true, SelfSkillsRevision: 30, NearbyCreatures: []*worldv1.CreatureState{{Guid: vendorGUID, Entry: 177, NpcFlags: 0x80, Present: true, MapKnown: true, PositionKnown: true, X: 2, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100}}}
	for i := range 16 {
		w.Backpack = append(w.Backpack, &worldv1.BackpackSlot{Slot: uint32(i)})
	}
	for i := range 128 {
		w.SelfSkills = append(w.SelfSkills, &worldv1.SkillState{Slot: uint32(i)})
	}
	for n, id := range []uint32{356, 393, 186, 333, 185, 197} {
		w.SelfSkills[n] = &worldv1.SkillState{Slot: uint32(n), SkillId: id, Value: 1, MaxValue: 75}
	}
	f := &supplyFixture{world: w, actions: &actionsv1.State{Position: &actionsv1.ClientPosition{Known: true, MapKnown: true}, Following: true, Healing: true, Assisting: true}, loot: &lootv1.State{SessionId: "body", Phase: "disabled"}, trade: &tradev1.State{SessionId: "body", Phase: "IDLE"}, professions: &professionsv1.State{SessionId: "body", SelfGuid: 6}, items: map[uint32]*tradev1.ItemDetails{}, leaseToken: "fresh-secret-trade-token"}
	for _, id := range []uint32{35, 6256, 7005, 2901, 6217, 4470, 4471, 2320} {
		f.items[id] = &tradev1.ItemDetails{ItemId: id, Available: true, AllowableClass: math.MaxUint32, AllowableRace: math.MaxUint32}
	}
	f.items[6256].ItemClass = 2
	f.items[6256].Subclass = 20
	f.items[6256].InventoryType = 17
	s := NewService(f, "body", 6)
	f.service = s
	t.Cleanup(func() { _, _ = s.BeforeLogout(context.Background(), &emptypb.Empty{}) })
	return s, f
}
func (f *supplyFixture) InvokeModule(_ context.Context, r *modv1.InvokeModuleRequest, _ ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	f.mu.Lock()
	var m proto.Message
	hook := func() {}
	switch r.Module + "/" + r.Method {
	case "buddy-world/snapshot":
		m = proto.Clone(f.world)
	case "buddy-actions/status":
		m = proto.Clone(f.actions)
	case "buddy-loot/state":
		m = proto.Clone(f.loot)
	case "buddy-trade/state":
		m = proto.Clone(f.trade)
	case "buddy-professions/state":
		m = proto.Clone(f.professions)
	case "buddy-trade/lookup_items":
		q := &tradev1.LookupItemsRequest{}
		_ = r.Request.UnmarshalTo(q)
		if len(q.ItemIds) > 6 {
			f.mu.Unlock()
			return nil, errors.New("lookup bound")
		}
		out := &tradev1.LookupItemsResponse{SessionId: "body"}
		for _, id := range q.ItemIds {
			if i := f.items[id]; i != nil {
				out.Items = append(out.Items, proto.Clone(i).(*tradev1.ItemDetails))
			} else {
				out.Items = append(out.Items, &tradev1.ItemDetails{ItemId: id, Pending: true})
			}
		}
		m = out
	case "buddy-actions/acquire_interaction":
		q := &actionsv1.InteractionRequest{}
		_ = r.Request.UnmarshalTo(q)
		if q.Owner != "trade" || q.Token != "" || q.LeaseMs != 20000 {
			f.mu.Unlock()
			return nil, errors.New("not a fresh trade lease")
		}
		f.acquires++
		f.actions.InteractionOwner = "trade"
		f.actions.InteractionLeaseRemainingMs = 20000
		m = &actionsv1.InteractionLease{Token: f.leaseToken, State: proto.Clone(f.actions).(*actionsv1.State)}
		if f.onAcquire != nil {
			hook = f.onAcquire
		}
	case "buddy-actions/release_interaction":
		q := &actionsv1.InteractionRelease{}
		_ = r.Request.UnmarshalTo(q)
		f.releases++
		f.releasedToken = q.Token
		if q.Owner != "trade" || q.Token != f.leaseToken {
			f.mu.Unlock()
			return nil, errors.New("wrong release identity")
		}
		f.actions.InteractionOwner = ""
		f.actions.InteractionLeaseRemainingMs = 0
		m = proto.Clone(f.actions)
	default:
		f.mu.Unlock()
		return nil, errors.New("unexpected dependency")
	}
	f.mu.Unlock()
	hook()
	value, e := anypb.New(m)
	return &modv1.InvokeModuleResponse{Result: value}, e
}
func (f *supplyFixture) SendPacket(_ context.Context, r *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.mu.Lock()
	f.packets = append(f.packets, proto.Clone(r).(*modv1.SendPacketRequest))
	hook := f.onSend
	fail := f.failOpcode == r.Opcode
	f.mu.Unlock()
	if hook != nil {
		hook(r)
	}
	if fail {
		return nil, errors.New("ambiguous transport")
	}
	return &emptypb.Empty{}, nil
}
func (f *supplyFixture) count(code uint32) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.packets {
		if p.Opcode == code {
			n++
		}
	}
	return n
}
func (f *supplyFixture) change(fn func()) { f.mu.Lock(); defer f.mu.Unlock(); fn() }
func sendPacket(s *Service, op uint32, p []byte) {
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: p})
}
func buyAck(npc uint64, slot, count uint32) []byte {
	p := binary.LittleEndian.AppendUint64(nil, npc)
	p = binary.LittleEndian.AppendUint32(p, slot)
	p = binary.LittleEndian.AppendUint32(p, math.MaxUint32)
	return binary.LittleEndian.AppendUint32(p, count)
}
func vendorPacket(entry, price uint32) []byte {
	p := binary.LittleEndian.AppendUint64(nil, vendorGUID)
	p = append(p, 1)
	for _, n := range []uint32{1, entry, 0, math.MaxUint32, price, 0, 1, 0} {
		p = binary.LittleEndian.AppendUint32(p, n)
	}
	return p
}
func readyBuy(t *testing.T, s *Service, f *supplyFixture, entry uint32) *suppliesv1.BuyRequest {
	t.Helper()
	f.onSend = func(r *modv1.SendPacketRequest) {
		if r.Opcode == opcode.CMSGListInventory {
			sendPacket(s, opcode.SMSGListInventory, vendorPacket(entry, 10))
		}
	}
	listed, e := s.List(context.Background(), &suppliesv1.ListRequest{SessionId: "body", NpcGuid: vendorGUID})
	if e != nil || listed.Active || listed.Offer == nil {
		t.Fatal("list", listed, e)
	}
	st, e := s.GetState(context.Background(), &emptypb.Empty{})
	if e != nil {
		t.Fatal(e)
	}
	return &suppliesv1.BuyRequest{SessionId: "body", RequestId: "purchase", ExpectedRevision: st.Revision, NpcGuid: vendorGUID, OfferRevision: st.Offer.Revision, VendorSlot: 1, ItemId: entry, Count: 1, ExpectedPriceCopper: 10, MaxSpendCopper: 10, ExpectedBackpackRevision: st.BackpackRevision, ExpectedCopperRevision: st.CopperRevision, ExpectedSkillsRevision: st.SkillsRevision}
}
func waitReceipt(t *testing.T, s *Service, predicate func(*suppliesv1.State) bool) *suppliesv1.State {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		st := s.snapshot()
		if predicate(st) {
			return st
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("receipt did not reach expected state", s.snapshot())
	return nil
}
func TestPurchaseRequiresMatchingServerInventoryAndExactDebit(t *testing.T) {
	s, f := newFixture(t)
	r := readyBuy(t, s, f, 2320)
	f.onSend = nil
	if _, e := s.Buy(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	waitReceipt(t, s, func(st *suppliesv1.State) bool { return st.Operation.TransportWritten })
	sendPacket(s, opcode.SMSGBuyItem, buyAck(vendorGUID, 2, 1))
	if s.snapshot().Operation.ServerSuccessObserved {
		t.Fatal("wrong slot accepted")
	}
	sendPacket(s, opcode.SMSGBuyItem, buyAck(vendorGUID, 1, 1))
	if s.snapshot().Operation.Status == "CONFIRMED" {
		t.Fatal("transport and ack alone confirmed")
	}
	f.change(func() {
		f.world.Backpack[3] = &worldv1.BackpackSlot{Slot: 3, ItemGuid: poleGUID, Entry: 2320, StackCount: 1, MetadataKnown: true}
		f.world.BackpackRevision++
		f.world.SelfCopper = 989
		f.world.SelfCopperRevision++
	})
	_, _ = s.GetState(context.Background(), &emptypb.Empty{})
	if s.snapshot().Operation.Status == "CONFIRMED" {
		t.Fatal("inexact debit accepted")
	}
	f.change(func() { f.world.SelfCopper = 990; f.world.SelfCopperRevision++ })
	_, _ = s.GetState(context.Background(), &emptypb.Empty{})
	st := waitReceipt(t, s, func(st *suppliesv1.State) bool { return !st.Active && st.Operation.LeaseReleased })
	if st.Operation.Status != "CONFIRMED" || !st.Operation.ServerSuccessObserved || !st.Operation.InventoryConfirmed || !st.Operation.CopperConfirmed || f.count(opcode.CMSGBuyItem) != 1 || f.releasedToken != f.leaseToken {
		t.Fatal(st)
	}
	before := f.count(opcode.CMSGBuyItem)
	again, e := s.Buy(context.Background(), r)
	if e != nil || again.Operation.Status != "CONFIRMED" || f.count(opcode.CMSGBuyItem) != before {
		t.Fatal("request replay mutated", again, e)
	}
}
func TestAmbiguousWriteNeverRetriesAndLocksNewMutation(t *testing.T) {
	s, f := newFixture(t)
	r := readyBuy(t, s, f, 2320)
	f.onSend = nil
	f.failOpcode = opcode.CMSGBuyItem
	_, e := s.Buy(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	waitReceipt(t, s, func(st *suppliesv1.State) bool { return f.count(opcode.CMSGBuyItem) == 1 })
	s.mu.Lock()
	s.active.cancel()
	s.mu.Unlock()
	st := waitReceipt(t, s, func(st *suppliesv1.State) bool { return !st.Active })
	if st.Operation.Status != "OUTCOME_UNCONFIRMED" || st.Operation.TransportWritten {
		t.Fatal(st)
	}
	retry := proto.Clone(r).(*suppliesv1.BuyRequest)
	retry.RequestId = "another"
	retry.ExpectedRevision = st.Revision
	if _, e = s.Buy(context.Background(), retry); e == nil {
		t.Fatal("new id bypassed unresolved write")
	}
	if _, e = s.Buy(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	if f.count(opcode.CMSGBuyItem) != 1 {
		t.Fatal("write retried")
	}
}
func TestLeaseReobservationBlocksChangedWalletBeforePacket(t *testing.T) {
	s, f := newFixture(t)
	r := readyBuy(t, s, f, 2320)
	f.onAcquire = func() { f.change(func() { f.world.SelfCopperRevision++; f.world.SelfCopper = 5 }) }
	st, e := s.Buy(context.Background(), r)
	if e != nil || st.Operation.Status != "BLOCKED" || f.count(opcode.CMSGBuyItem) != 0 || !st.Operation.LeaseReleased {
		t.Fatal(st, e)
	}
}
func TestLogoutDuringLeaseAcquisitionReleasesExactTokenWithoutWrite(t *testing.T) {
	s, f := newFixture(t)
	r := readyBuy(t, s, f, 2320)
	f.onAcquire = func() { _, _ = s.BeforeLogout(context.Background(), &emptypb.Empty{}) }
	st, e := s.Buy(context.Background(), r)
	if e != nil || f.count(opcode.CMSGBuyItem) != 0 || !st.Operation.LeaseReleased || f.releasedToken != f.leaseToken {
		t.Fatal(st, e)
	}
}
func TestBuySafetyBlocksWithoutPacket(t *testing.T) {
	cases := map[string]func(*Service, *supplyFixture, *suppliesv1.BuyRequest){"count2": func(_ *Service, _ *supplyFixture, r *suppliesv1.BuyRequest) { r.Count = 2 }, "budget": func(_ *Service, _ *supplyFixture, r *suppliesv1.BuyRequest) { r.MaxSpendCopper = 10001 }, "unknownitem": func(_ *Service, _ *supplyFixture, r *suppliesv1.BuyRequest) { r.ItemId = 99999 }, "price": func(_ *Service, _ *supplyFixture, r *suppliesv1.BuyRequest) { r.ExpectedPriceCopper = 9 }, "moving": func(_ *Service, f *supplyFixture, _ *suppliesv1.BuyRequest) { f.actions.Moving = true }, "channel": func(_ *Service, f *supplyFixture, _ *suppliesv1.BuyRequest) { f.world.Self.ChannelSpellId = 7620 }, "profession": func(_ *Service, f *supplyFixture, _ *suppliesv1.BuyRequest) { f.professions.Active = true }, "trade": func(_ *Service, f *supplyFixture, _ *suppliesv1.BuyRequest) { f.trade.Phase = "OPEN" }, "loot": func(_ *Service, f *supplyFixture, _ *suppliesv1.BuyRequest) { f.loot.Enabled = true }, "range": func(_ *Service, f *supplyFixture, _ *suppliesv1.BuyRequest) { f.world.NearbyCreatures[0].X = 5 }, "pendingmetadata": func(s *Service, f *supplyFixture, r *suppliesv1.BuyRequest) {
		delete(s.metadata, r.ItemId)
		f.items[r.ItemId].Pending = true
	}, "race": func(s *Service, _ *supplyFixture, r *suppliesv1.BuyRequest) { s.metadata[r.ItemId].AllowableRace = 1 }, "skill": func(_ *Service, f *supplyFixture, _ *suppliesv1.BuyRequest) {
		f.world.SelfSkills[5] = &worldv1.SkillState{Slot: 5}
		f.world.SelfSkillsRevision++
	}, "extendedcost": func(s *Service, _ *supplyFixture, _ *suppliesv1.BuyRequest) { s.state.Offer.Items[0].ExtendedCost = 1 }, "bundle": func(s *Service, _ *supplyFixture, _ *suppliesv1.BuyRequest) { s.state.Offer.Items[0].BuyCount = 5 }}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s, f := newFixture(t)
			r := readyBuy(t, s, f, 2320)
			change(s, f, r)
			st, e := s.Buy(context.Background(), r)
			if e != nil || st.Operation.Status != "BLOCKED" || f.count(opcode.CMSGBuyItem) != 0 {
				t.Fatal(st, e)
			}
			if st.Operation.NpcGuid != r.NpcGuid || st.Operation.ItemId != r.ItemId || st.Operation.Count != r.Count || st.Operation.CostCopper != r.ExpectedPriceCopper*r.Count || st.Operation.BaselineBackpackRevision != r.ExpectedBackpackRevision || st.Operation.BaselineCopperRevision != r.ExpectedCopperRevision || !st.Operation.LeaseReleased {
				t.Fatal("blocked receipt lost its request identity", st.Operation)
			}
		})
	}
}
func TestOwnedToolCannotBePurchasedAgain(t *testing.T) {
	s, f := newFixture(t)
	f.world.Backpack[1] = &worldv1.BackpackSlot{Slot: 1, ItemGuid: poleGUID, Entry: 6256, StackCount: 1, MetadataKnown: true}
	r := readyBuy(t, s, f, 6256)
	st, e := s.Buy(context.Background(), r)
	if e != nil || st.Operation.Status != "BLOCKED" || f.count(opcode.CMSGBuyItem) != 0 {
		t.Fatal(st, e)
	}
}
func equipRequest(t *testing.T, s *Service, id string, item uint64) *suppliesv1.EquipmentRequest {
	t.Helper()
	st, e := s.GetState(context.Background(), &emptypb.Empty{})
	if e != nil {
		t.Fatal(e)
	}
	return &suppliesv1.EquipmentRequest{SessionId: "body", RequestId: id, ExpectedRevision: st.Revision, ExpectedBackpackRevision: st.BackpackRevision, ExpectedSkillsRevision: st.SkillsRevision, BackpackSlot: 2, ItemGuid: item, OriginalWeaponGuid: weaponGUID}
}
func TestPoleEquipAndRememberedRestoreRequireExactSwaps(t *testing.T) {
	s, f := newFixture(t)
	f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: poleGUID, Entry: 6256, StackCount: 1, MetadataKnown: true}
	r := equipRequest(t, s, "equip", poleGUID)
	_, e := s.Equip(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	waitReceipt(t, s, func(st *suppliesv1.State) bool { return st.Operation.TransportWritten })
	f.change(func() {
		f.world.Self.Mainhand = &worldv1.EquippedWeapon{SlotKnown: true, ItemGuid: poleGUID, Entry: 6256, MetadataKnown: true}
		f.world.BackpackRevision++
	})
	_, _ = s.GetState(context.Background(), &emptypb.Empty{})
	if s.snapshot().Operation.EquipmentConfirmed {
		t.Fatal("hand alone confirmed without original swap")
	}
	f.change(func() {
		f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: weaponGUID, Entry: 35, StackCount: 1, MetadataKnown: true}
		f.world.BackpackRevision++
	})
	_, _ = s.GetState(context.Background(), &emptypb.Empty{})
	st := waitReceipt(t, s, func(st *suppliesv1.State) bool { return !st.Active })
	if st.Operation.Status != "CONFIRMED" || !st.RestoreRequired || st.OriginalWeaponGuid != weaponGUID || st.EquippedPoleGuid != poleGUID {
		t.Fatal(st)
	}
	restore := equipRequest(t, s, "restore", weaponGUID)
	_, e = s.Restore(context.Background(), restore)
	if e != nil {
		t.Fatal(e)
	}
	waitReceipt(t, s, func(st *suppliesv1.State) bool {
		return st.Operation.RequestId == "restore" && st.Operation.TransportWritten
	})
	f.change(func() {
		f.world.Self.Mainhand = &worldv1.EquippedWeapon{SlotKnown: true, ItemGuid: weaponGUID, Entry: 35, MetadataKnown: true}
		f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: poleGUID, Entry: 6256, StackCount: 1, MetadataKnown: true}
		f.world.BackpackRevision++
	})
	_, _ = s.GetState(context.Background(), &emptypb.Empty{})
	st = waitReceipt(t, s, func(st *suppliesv1.State) bool { return !st.Active })
	if st.Operation.Status != "CONFIRMED" || st.RestoreRequired || st.Operation.ItemGuid != weaponGUID || st.Operation.OriginalWeaponGuid != weaponGUID || f.count(opcode.CMSGAutoequipItemSlot) != 2 {
		t.Fatal(st)
	}
}
func TestEquipBlocksMissingOffhandAndRestoreLostHistory(t *testing.T) {
	s, f := newFixture(t)
	f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: poleGUID, Entry: 6256, StackCount: 1, MetadataKnown: true}
	f.world.Self.Offhand = nil
	r := equipRequest(t, s, "equip", poleGUID)
	st, e := s.Equip(context.Background(), r)
	if e != nil || st.Operation.Status != "BLOCKED" || f.count(opcode.CMSGAutoequipItemSlot) != 0 {
		t.Fatal(st, e)
	}
	f.world.Self.Offhand = &worldv1.EquippedWeapon{SlotKnown: true}
	r = equipRequest(t, s, "restore", poleGUID)
	st, e = s.Restore(context.Background(), r)
	if e != nil || st.Operation.Status != "BLOCKED" || f.count(opcode.CMSGAutoequipItemSlot) != 0 {
		t.Fatal(st, e)
	}
}
func TestBeforeLogoutNeverRestoresOrAddsAssetPacket(t *testing.T) {
	s, f := newFixture(t)
	r := readyBuy(t, s, f, 2320)
	f.onSend = nil
	_, _ = s.Buy(context.Background(), r)
	waitReceipt(t, s, func(st *suppliesv1.State) bool { return st.Operation.TransportWritten })
	_, _ = s.BeforeLogout(context.Background(), &emptypb.Empty{})
	if f.count(opcode.CMSGBuyItem) != 1 || f.count(opcode.CMSGAutoequipItemSlot) != 0 || s.snapshot().Operation.Status != "OUTCOME_UNCONFIRMED" {
		t.Fatal(s.snapshot())
	}
}

func TestObservedCombatBlocksBeforePurchaseAndReleasesPendingLease(t *testing.T) {
	for _, ownerCombat := range []bool{false, true} {
		t.Run(map[bool]string{false: "own", true: "owner"}[ownerCombat], func(t *testing.T) {
			s, f := newFixture(t)
			r := readyBuy(t, s, f, 2320)
			f.onSend = nil
			_, e := s.Buy(context.Background(), r)
			if e != nil {
				t.Fatal(e)
			}
			waitReceipt(t, s, func(st *suppliesv1.State) bool { return st.Operation.TransportWritten })
			f.change(func() {
				if ownerCombat {
					f.world.Owner = &worldv1.PlayerState{Guid: 3, Present: true, UnitFlagsKnown: true, UnitFlags: 0x80000}
				} else {
					f.world.Self.UnitFlags = 0x80000
				}
			})
			st := waitReceipt(t, s, func(st *suppliesv1.State) bool { return !st.Active })
			if st.Operation.Status != "OUTCOME_UNCONFIRMED" || !st.Operation.LeaseReleased || f.count(opcode.CMSGBuyItem) != 1 || f.count(opcode.CMSGAutoequipItemSlot) != 0 {
				t.Fatal(st)
			}
		})
	}
	s, f := newFixture(t)
	r := readyBuy(t, s, f, 2320)
	f.world.Owner = &worldv1.PlayerState{Guid: 3, Present: true, UnitFlagsKnown: true, UnitFlags: 0x80000}
	st, e := s.Buy(context.Background(), r)
	if e != nil || st.Operation.Status != "BLOCKED" || f.count(opcode.CMSGBuyItem) != 0 {
		t.Fatal(st, e)
	}
}

func TestToolConfirmedHistoryBlocksRepurchaseAfterRemoval(t *testing.T) {
	s, f := newFixture(t)
	r := readyBuy(t, s, f, 6256)
	f.onSend = nil
	_, e := s.Buy(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	waitReceipt(t, s, func(st *suppliesv1.State) bool { return st.Operation.TransportWritten })
	sendPacket(s, opcode.SMSGBuyItem, buyAck(vendorGUID, 1, 1))
	f.change(func() {
		f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: poleGUID, Entry: 6256, StackCount: 1, MetadataKnown: true}
		f.world.BackpackRevision++
		f.world.SelfCopper = 990
		f.world.SelfCopperRevision++
	})
	_, _ = s.GetState(context.Background(), &emptypb.Empty{})
	waitReceipt(t, s, func(st *suppliesv1.State) bool { return !st.Active && st.Operation.Status == "CONFIRMED" })
	f.change(func() { f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2}; f.world.BackpackRevision++ })
	st, _ := s.GetState(context.Background(), &emptypb.Empty{})
	next := proto.Clone(r).(*suppliesv1.BuyRequest)
	next.RequestId = "repurchase"
	next.ExpectedRevision = st.Revision
	next.ExpectedBackpackRevision = st.BackpackRevision
	next.ExpectedCopperRevision = st.CopperRevision
	blocked, e := s.Buy(context.Background(), next)
	if e != nil || blocked.Operation.Status != "BLOCKED" || blocked.Operation.Reason != "tool_purchase_already_confirmed_in_this_body" || f.count(opcode.CMSGBuyItem) != 1 {
		t.Fatal(blocked, e)
	}
	prior, e := s.Buy(context.Background(), r)
	if e != nil || prior.Operation.RequestId != r.RequestId || prior.Operation.Status != "CONFIRMED" || f.count(opcode.CMSGBuyItem) != 1 {
		t.Fatal("replay returned another receipt", prior, e)
	}
}
func TestRequestHistoryIsBoundedAndChangedRequestCannotReuseID(t *testing.T) {
	s, f := newFixture(t)
	r := readyBuy(t, s, f, 2320)
	r.Count = 2
	first, e := s.Buy(context.Background(), r)
	if e != nil || first.Operation.Status != "BLOCKED" {
		t.Fatal(first, e)
	}
	changed := proto.Clone(r).(*suppliesv1.BuyRequest)
	changed.Count = 1
	if _, e = s.Buy(context.Background(), changed); e == nil {
		t.Fatal("changed request reused ID")
	}
	for n := 1; n < 128; n++ {
		next := proto.Clone(r).(*suppliesv1.BuyRequest)
		next.RequestId = fmt.Sprintf("bounded-%d", n)
		next.ExpectedRevision = s.snapshot().Revision
		st, e := s.Buy(context.Background(), next)
		if e != nil || st.Operation.Status != "BLOCKED" {
			t.Fatal(n, st, e)
		}
	}
	next := proto.Clone(r).(*suppliesv1.BuyRequest)
	next.RequestId = "over-limit"
	next.ExpectedRevision = s.snapshot().Revision
	if _, e = s.Buy(context.Background(), next); e == nil {
		t.Fatal("unbounded request history")
	}
	if f.count(opcode.CMSGBuyItem) != 0 || len(s.requests) != 128 {
		t.Fatal("history cap mutated inventory")
	}
}
