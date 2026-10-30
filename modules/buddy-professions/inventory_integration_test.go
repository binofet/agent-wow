package buddyprofessions

import (
	"context"
	"encoding/binary"
	"testing"

	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestCarriedBagOnlyCraftUsesNewRevisionAndActualPushCoordinates(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	f.mu.Lock()
	for slot := uint32(0); slot < 16; slot++ {
		f.world.InventoryBags[0].Slots[slot] = &worldv1.BackpackSlot{Slot: slot, ItemGuid: 0x4000000000001000 + uint64(slot), Entry: 1596, StackCount: 1, MetadataKnown: true}
	}
	b := professionCarriedBag(f.world, 1, 41599, 20)
	b.Slots[0] = &worldv1.BackpackSlot{Bag: 1, ItemGuid: 0x4000000000001100, Entry: 2589, StackCount: 5, MetadataKnown: true}
	f.mu.Unlock()
	professionObserve(t, s)
	r := professionRequest(s, "bag-only-craft", 3275)
	if _, err := s.Attempt(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	professionWaitPacket(t, f, opcode.CMSGCastSpell)
	s.mu.Lock()
	op := s.active
	s.mu.Unlock()
	professionPacket(t, s, opcode.SMSGSpellGo, professionCastReceipt(opcode.SMSGSpellGo, 7, byte(op.receipt.CastCount), 3275))
	push := professionPushReceipt(7, true, true, 1251, 1, 1)
	push[20] = 19
	binary.LittleEndian.PutUint32(push[21:], 1)
	professionPacket(t, s, opcode.SMSGItemPushResult, push)
	f.mu.Lock()
	f.world = proto.Clone(f.world).(*worldv1.SnapshotResponse)
	f.world.InventoryRevision++ // The built-in backpack did not change.
	f.world.InventoryBags[1].Slots[0].StackCount--
	f.world.InventoryBags[1].Slots[1] = &worldv1.BackpackSlot{Bag: 1, Slot: 1, ItemGuid: 0x4000000000001101, Entry: 1251, StackCount: 1, MetadataKnown: true}
	f.mu.Unlock()
	professionObserve(t, s)
	professionWaitDone(t, op)
	got := professionReceipt(s, op)
	if got.Status != professionsv1.AttemptStatus_COMPLETED || !got.ReagentsConsumedObserved || !got.OutputInventoryObserved || got.OwnCreatedItemsObserved != 1 || got.BaselineInventoryRevision != 9 || got.ObservedInventoryRevision != 10 || got.ObservedBackpackRevision != 9 || got.SkillIncreased {
		t.Fatal(got)
	}
}

func TestCarriedInventoryRevisionIsRequiredEvenWhenBackpackUnchanged(t *testing.T) {
	for _, zero := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale", true: "missing"}[zero], func(t *testing.T) {
			s, f := newProfessionServiceFixture(t)
			r := professionRequest(s, "stale-carried", 3275)
			if zero {
				r.ExpectedInventoryRevision = 0
			} else {
				f.mu.Lock()
				f.world.InventoryRevision++
				f.mu.Unlock()
			}
			if _, err := s.Attempt(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.packets) != 0 || len(s.requests) != 0 {
				t.Fatal("stale all-bag proof authorized a cast")
			}
		})
	}
}

func TestCarriedPushLocationRequiresActualEquippedBagBounds(t *testing.T) {
	w := professionObservationFixture()
	professionCarriedBag(w, 4, 41599, 20)
	v, err := validateWorld(w, 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		bag  byte
		slot uint32
		ok   bool
	}{{22, 0, true}, {22, 19, true}, {22, 20, false}, {22, 0xffffffff, true}, {19, 0, false}, {0, 0, false}, {23, 0, false}} {
		if carriedPushLocation(v, tc.bag, tc.slot) != tc.ok {
			t.Fatal(tc)
		}
	}
}

func TestFinalWorldGatesGenericProfessionCastAfterTokenRenewal(t *testing.T) {
	changes := map[string]func(*worldv1.SnapshotResponse){
		"combat":             func(w *worldv1.SnapshotResponse) { w.Self.UnitFlags = 0x80000 },
		"death":              func(w *worldv1.SnapshotResponse) { w.Self.Alive = false },
		"pose":               func(w *worldv1.SnapshotResponse) { w.Self.PositionRevision++ },
		"map":                func(w *worldv1.SnapshotResponse) { w.Self.MapId++ },
		"unknown_inventory":  func(w *worldv1.SnapshotResponse) { w.InventoryKnown = false },
		"new_bag_revision":   func(w *worldv1.SnapshotResponse) { w.InventoryRevision++ },
		"new_skill_revision": func(w *worldv1.SnapshotResponse) { w.SelfSkillsRevision++ },
		"materials_gone": func(w *worldv1.SnapshotResponse) {
			for i := range w.InventoryBags[0].Slots {
				w.InventoryBags[0].Slots[i] = &worldv1.BackpackSlot{Slot: uint32(i)}
			}
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f := newProfessionServiceFixture(t)
			op := professionDirectOperation(t, s, f, 3275)
			f.mu.Lock()
			f.worldHook = func(_ *worldv1.SnapshotRequest) { change(f.world) }
			f.mu.Unlock()
			if err := s.writeOwned(context.Background(), op, opcode.CMSGCastSpell, castPacket(9, 3275)); err == nil {
				t.Fatal("final-world change accepted")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.packets) != 0 {
				t.Fatal("packet preceded final-world validation")
			}
		})
	}
}

func TestFinalWorldIsLastDependencyBeforeGenericProfessionPacket(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	op := professionDirectOperation(t, s, f, 3275)
	f.mu.Lock()
	f.rpcEvents = nil
	f.mu.Unlock()
	if err := s.writeOwned(context.Background(), op, opcode.CMSGCastSpell, castPacket(9, 3275)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rpcEvents) != 3 || f.rpcEvents[0] != "buddy-actions/acquire_interaction" || f.rpcEvents[1] != "buddy-world/snapshot" {
		t.Fatal(f.rpcEvents)
	}
}

func TestExpiredReturnedProfessionLeaseCannotAuthorizePacket(t *testing.T) {
	s, f := newProfessionServiceFixture(t)
	op := professionDirectOperation(t, s, f, 3275)
	f.mu.Lock()
	f.actions.InteractionLeaseRemainingMs = 0
	f.mu.Unlock()
	if err := s.writeOwned(context.Background(), op, opcode.CMSGCastSpell, castPacket(9, 3275)); err == nil {
		t.Fatal("expired returned exact-token lease accepted")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.packets) != 0 {
		t.Fatal("packet after expired lease")
	}
}

func TestGenericCasterBearingFailureCannotSettleAnotherActor(t *testing.T) {
	for _, actor := range []uint64{0, 8} {
		s, f := newProfessionServiceFixture(t)
		op := professionDirectOperation(t, s, f, 3275)
		p := protocolPackedFixture(actor)
		p = append(p, byte(op.receipt.CastCount))
		p = binary.LittleEndian.AppendUint32(p, 3275)
		p = append(p, 72)
		professionPacket(t, s, opcode.SMSGSpellFailure, p)
		if got := professionReceipt(s, op); got.Status == professionsv1.AttemptStatus_SERVER_REJECTED || got.FailureCode != 0 {
			t.Fatal(got)
		}
	}
}
