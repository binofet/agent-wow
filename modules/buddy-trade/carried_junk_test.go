package buddytrade

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	discovery "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
)

func carriedJunkFixture(bag, slot uint32) (*Service, *fakeSession) {
	s, f := junkFixture()
	junk := proto.Clone(f.world.Backpack[2]).(*worldv1.BackpackSlot)
	junk.Bag, junk.Slot = bag, slot
	f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2, ItemGuid: 0x4000000000003000,
		Entry: 117, StackCount: 1, MetadataKnown: true}
	f.world.InventoryKnown, f.world.InventoryRevision = true, 42
	f.world.SelfEquipmentKnown, f.world.SelfEquipmentRevision = true, 9
	for slot := uint32(0); slot < 19; slot++ {
		f.world.SelfEquipment = append(f.world.SelfEquipment, &worldv1.EquipmentSlotState{Slot: slot, SlotKnown: true})
	}
	for b := uint32(0); b <= 4; b++ {
		frame := &worldv1.InventoryBag{Bag: b, SlotKnown: true, ContentsKnown: true}
		if b == 0 {
			frame.SlotCount = 16
			for _, item := range f.world.Backpack {
				frame.Slots = append(frame.Slots, proto.Clone(item).(*worldv1.BackpackSlot))
			}
		} else if b == bag {
			frame.ContainerGuid, frame.Entry, frame.MetadataKnown, frame.SlotCount = 0x4000000000004000+uint64(b), 41599, true, 36
			for at := uint32(0); at < 36; at++ {
				frame.Slots = append(frame.Slots, &worldv1.BackpackSlot{Bag: b, Slot: at,
					ItemGuid: 0x4000000000005000 + uint64(at), Entry: 117, StackCount: 1, MetadataKnown: true})
			}
			frame.Slots[slot] = junk
		}
		f.world.InventoryBags = append(f.world.InventoryBags, frame)
	}
	return s, f
}

func carriedJunkRequest(s *Service, f *fakeSession, bag, slot uint32) *tradev1.JunkRequest {
	state := s.snapshot()
	return &tradev1.JunkRequest{SessionId: state.SessionId, Revision: state.Revision,
		SourceBag: bag, SourceSlot: slot, InventoryRevision: f.world.InventoryRevision,
		ExpectedItemGuid: f.world.InventoryBags[bag].Slots[slot].ItemGuid}
}

type carriedJunkWriteSession struct {
	*fakeSession
	beforeDestroy func(*modv1.SendPacketRequest)
}

func (f *carriedJunkWriteSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if r.Opcode == opcode.CMSGDestroyitem && f.beforeDestroy != nil {
		f.beforeDestroy(r)
	}
	return f.fakeSession.SendPacket(ctx, r, opts...)
}

func TestCarriedJunkReservesExactSourceBeforeEachNormalWholeStackPacket(t *testing.T) {
	for bag := uint32(1); bag <= 4; bag++ {
		for _, slot := range []uint32{0, 35} {
			t.Run(fmt.Sprintf("bag%d/slot%d", bag, slot), func(t *testing.T) {
				s, f := carriedJunkFixture(bag, slot)
				r := carriedJunkRequest(s, f, bag, slot)
				observed := false
				s.session = &carriedJunkWriteSession{fakeSession: f, beforeDestroy: func(packet *modv1.SendPacketRequest) {
					observed = true
					state := s.snapshot()
					s.mu.Lock()
					pending := proto.Clone(s.junkPending).(*tradev1.JunkDisposal)
					s.mu.Unlock()
					if pending.SourceBag != bag || pending.SourceSlot != slot || pending.InventoryRevision != 42 ||
						pending.ItemGuid != r.ExpectedItemGuid || pending.Count != 5 || pending.Entry != 4865 ||
						pending.BackpackSlot != 0 || pending.BackpackRevision != 0 ||
						pending.Status != "AWAITING_SERVER_SLOT_UPDATE" || !proto.Equal(pending, state.LastJunkDisposal) ||
						state.JunkStacksDestroyed != 0 || f.count(opcode.CMSGDestroyitem) != 0 {
						t.Fatalf("exact pending source was not reserved before the write: %v", state)
					}
					if want := []byte{byte(18 + bag), byte(slot), 0, 0, 0, 0}; !bytes.Equal(packet.Payload, want) {
						t.Fatalf("whole-stack destroy packet %x, want %x", packet.Payload, want)
					}
				}}
				got, err := s.DestroyJunk(context.Background(), r)
				if err != nil || !observed || got.JunkStacksDestroyed != 0 || f.count(opcode.CMSGDestroyitem) != 1 {
					t.Fatal(got, err, observed)
				}
				got.LastJunkDisposal.SourceBag = 99
				if s.snapshot().LastJunkDisposal.SourceBag != bag || s.junkPending.SourceBag != bag {
					t.Fatal("RPC response aliases the pending source receipt")
				}
			})
		}
	}
}

func TestCarriedJunkLogicalAndLegacyRequestBindingsStayDisjoint(t *testing.T) {
	for name, change := range map[string]func(*tradev1.JunkRequest){
		"bag_zero":               func(r *tradev1.JunkRequest) { r.SourceBag = 0 },
		"bag_above_four":         func(r *tradev1.JunkRequest) { r.SourceBag = 5 },
		"slot_above_thirty_five": func(r *tradev1.JunkRequest) { r.SourceSlot = 36 },
		"no_inventory_revision":  func(r *tradev1.JunkRequest) { r.InventoryRevision = 0 },
		"legacy_revision_mixed":  func(r *tradev1.JunkRequest) { r.BackpackRevision = 4 },
		"legacy_slot_mixed":      func(r *tradev1.JunkRequest) { r.BackpackSlot = 2 },
		"empty_guid":             func(r *tradev1.JunkRequest) { r.ExpectedItemGuid = 0 },
		"stale_inventory":        func(r *tradev1.JunkRequest) { r.InventoryRevision++ },
	} {
		t.Run(name, func(t *testing.T) {
			s, f := carriedJunkFixture(4, 0)
			r := carriedJunkRequest(s, f, 4, 0)
			change(r)
			if _, err := s.DestroyJunk(context.Background(), r); err == nil || f.count(opcode.CMSGDestroyitem) != 0 {
				t.Fatal("ambiguous or stale carried source admitted", r, err)
			}
		})
	}
	for name, change := range map[string]func(*tradev1.JunkRequest){
		"legacy_slot_bounds": func(r *tradev1.JunkRequest) { r.BackpackSlot = 16 },
		"source_slot_mixed":  func(r *tradev1.JunkRequest) { r.SourceSlot = 2 },
		"inventory_mixed":    func(r *tradev1.JunkRequest) { r.InventoryRevision = 42 },
	} {
		t.Run(name, func(t *testing.T) {
			s, f := junkFixture()
			r := requestJunk(s, f)
			change(r)
			if _, err := s.DestroyJunk(context.Background(), r); err == nil || f.count(opcode.CMSGDestroyitem) != 0 {
				t.Fatal("mixed legacy source admitted", r, err)
			}
		})
	}
	// The original public field set still routes its exact backpack packet.
	s, f := junkFixture()
	got, err := s.DestroyJunk(context.Background(), requestJunk(s, f))
	if err != nil || got.LastJunkDisposal.BackpackSlot != 2 || got.LastJunkDisposal.BackpackRevision != f.world.BackpackRevision ||
		got.LastJunkDisposal.SourceBag != 0 || got.LastJunkDisposal.InventoryRevision != 0 {
		t.Fatal("legacy request or receipt changed", got, err)
	}
}

func TestCarriedJunkRequiresCompleteUniqueOccupiedFiveBagFrame(t *testing.T) {
	changes := map[string]func(*worldv1.SnapshotResponse){
		"inventory_unknown": func(w *worldv1.SnapshotResponse) { w.InventoryKnown = false },
		"missing_bag":       func(w *worldv1.SnapshotResponse) { w.InventoryBags = w.InventoryBags[:4] },
		"nil_bag":           func(w *worldv1.SnapshotResponse) { w.InventoryBags[1] = nil },
		"duplicate_bag":     func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Bag = 2 },
		"unknown_bag_slot":  func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].SlotKnown = false },
		"unknown_contents":  func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].ContentsKnown = false },
		"oversize_bag":      func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].SlotCount = 37 },
		"short_bag":         func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].Slots = w.InventoryBags[4].Slots[:35] },
		"nil_slot":          func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].Slots[1] = nil },
		"duplicate_slot":    func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].Slots[1].Slot = 0 },
		"misaddressed_slot": func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].Slots[1].Bag = 3 },
		"duplicate_item": func(w *worldv1.SnapshotResponse) {
			w.InventoryBags[4].Slots[1].ItemGuid = w.InventoryBags[0].Slots[0].ItemGuid
		},
		"item_container_alias": func(w *worldv1.SnapshotResponse) {
			w.InventoryBags[4].Slots[1].ItemGuid = w.InventoryBags[4].ContainerGuid
		},
		"selected_guid_in_partial_equipment": func(w *worldv1.SnapshotResponse) {
			w.SelfEquipmentKnown = false
			w.SelfEquipment = []*worldv1.EquipmentSlotState{{Slot: 8, SlotKnown: true,
				ItemGuid: w.InventoryBags[4].Slots[0].ItemGuid, Entry: 4865, StackCount: 1, MetadataKnown: true}}
		},
		"duplicate_container": func(w *worldv1.SnapshotResponse) {
			w.InventoryBags[1] = &worldv1.InventoryBag{Bag: 1, SlotKnown: true, ContentsKnown: true, MetadataKnown: true,
				Entry: 41599, ContainerGuid: w.InventoryBags[4].ContainerGuid, SlotCount: 1,
				Slots: []*worldv1.BackpackSlot{{Bag: 1, ItemGuid: 0x4000000000007000, Entry: 117, StackCount: 1, MetadataKnown: true}}}
		},
		"unowned_container": func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].ContainerGuid = 3 },
		"unknown_container": func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].MetadataKnown = false },
		"unknown_item":      func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].Slots[1].MetadataKnown = false },
		"zero_entry":        func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].Slots[1].Entry = 0 },
		"zero_count":        func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].Slots[1].StackCount = 0 },
		"backpack_space": func(w *worldv1.SnapshotResponse) {
			w.InventoryBags[0].Slots[0] = &worldv1.BackpackSlot{Slot: 0}
		},
		"carried_space": func(w *worldv1.SnapshotResponse) {
			w.InventoryBags[4].Slots[1] = &worldv1.BackpackSlot{Bag: 4, Slot: 1}
		},
		"contradictory_empty_header": func(w *worldv1.SnapshotResponse) { w.InventoryBags[1].Entry = 41599 },
		"contradictory_backpack_header": func(w *worldv1.SnapshotResponse) {
			w.InventoryBags[0].ContainerGuid = 0x4000000000008000
		},
		"not_gray_misc": func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].Slots[0].Entry = 117 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f := carriedJunkFixture(4, 0)
			r := carriedJunkRequest(s, f, 4, 0)
			change(f.world)
			if _, err := s.DestroyJunk(context.Background(), r); err == nil || f.count(opcode.CMSGDestroyitem) != 0 {
				t.Fatal("partial, aliased, unnecessary or non-junk source admitted", err, s.snapshot())
			}
		})
	}
}

func TestCarriedJunkRechecksSourceAndRevisionAfterLease(t *testing.T) {
	for _, change := range []func(*worldv1.SnapshotResponse){
		func(w *worldv1.SnapshotResponse) { w.InventoryRevision++ },
		func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].Slots[0].ItemGuid++ },
		func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].Slots[0].Entry = 117 },
		func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].Slots[0].StackCount-- },
		func(w *worldv1.SnapshotResponse) { w.InventoryBags[4].ContentsKnown = false },
	} {
		s, f := carriedJunkFixture(4, 0)
		f.leaseChange = func() { change(f.world) }
		if _, err := s.DestroyJunk(context.Background(), carriedJunkRequest(s, f, 4, 0)); err == nil ||
			f.count(opcode.CMSGDestroyitem) != 0 || f.releases != 1 {
			t.Fatal("post-lease change was not rejected before write", err, f.releases)
		}
	}
}

func TestCarriedJunkConfirmationNeedsOriginalEmptySlotAndGuidAbsentEverywhere(t *testing.T) {
	for name, change := range map[string]func(*worldv1.SnapshotResponse, uint64){
		"same_revision":     func(w *worldv1.SnapshotResponse, _ uint64) { w.InventoryRevision = 42 },
		"unknown_inventory": func(w *worldv1.SnapshotResponse, _ uint64) { w.InventoryKnown = false },
		"partial_contents":  func(w *worldv1.SnapshotResponse, _ uint64) { w.InventoryBags[4].ContentsKnown = false },
		"missing_bag":       func(w *worldv1.SnapshotResponse, _ uint64) { w.InventoryBags = w.InventoryBags[:4] },
		"replacement_in_original_slot": func(w *worldv1.SnapshotResponse, _ uint64) {
			w.InventoryBags[4].Slots[0] = &worldv1.BackpackSlot{Bag: 4, ItemGuid: 0x4000000000009000, Entry: 117, StackCount: 1, MetadataKnown: true}
		},
		"moved_to_backpack": func(w *worldv1.SnapshotResponse, guid uint64) {
			w.InventoryBags[0].Slots[0] = &worldv1.BackpackSlot{ItemGuid: guid, Entry: 4865, StackCount: 5, MetadataKnown: true}
		},
		"retained_as_container": func(w *worldv1.SnapshotResponse, guid uint64) { w.InventoryBags[4].ContainerGuid = guid },
		"removed_body":          func(w *worldv1.SnapshotResponse, _ uint64) { w.Self.Present = false },
		"wrong_body_guid":       func(w *worldv1.SnapshotResponse, _ uint64) { w.Self.Guid++ },
		"unknown_equipment":     func(w *worldv1.SnapshotResponse, _ uint64) { w.SelfEquipmentKnown = false },
		"partial_equipment":     func(w *worldv1.SnapshotResponse, _ uint64) { w.SelfEquipment = w.SelfEquipment[:18] },
		"moved_to_equipment": func(w *worldv1.SnapshotResponse, guid uint64) {
			w.SelfEquipment[8] = &worldv1.EquipmentSlotState{Slot: 8, SlotKnown: true, ItemGuid: guid, Entry: 4865, StackCount: 1, MetadataKnown: true}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, f := carriedJunkFixture(4, 0)
			r := carriedJunkRequest(s, f, 4, 0)
			if _, err := s.DestroyJunk(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			complete := proto.Clone(f.world).(*worldv1.SnapshotResponse)
			complete.InventoryBags[4].Slots[0] = &worldv1.BackpackSlot{Bag: 4}
			complete.InventoryRevision++
			f.world = proto.Clone(complete).(*worldv1.SnapshotResponse)
			change(f.world, r.ExpectedItemGuid)
			s.Tick(context.Background())
			if state := s.snapshot(); state.JunkStacksDestroyed != 0 || s.junkPending == nil {
				t.Fatal("non-removal observation confirmed destruction", state)
			}
			f.world = complete
			f.world.InventoryRevision++
			s.Tick(context.Background())
			state := s.snapshot()
			if state.JunkStacksDestroyed != 1 || state.LastJunkDisposal.Status != "SERVER_SLOT_REMOVAL_CONFIRMED" ||
				state.LastJunkDisposal.SourceBag != 4 || state.LastJunkDisposal.InventoryRevision != 42 || s.junkPending != nil {
				t.Fatal("complete newer removal failed to settle the exact source", state)
			}
			s.Tick(context.Background())
			if s.snapshot().JunkStacksDestroyed != 1 || f.count(opcode.CMSGDestroyitem) != 1 {
				t.Fatal("removal receipt counted twice or destroy replayed")
			}
		})
	}
}

func TestCarriedJunkNewerPresenceRejectsRolledBackOrSameRevisionAbsence(t *testing.T) {
	s, f := carriedJunkFixture(4, 0)
	if _, err := s.DestroyJunk(context.Background(), carriedJunkRequest(s, f, 4, 0)); err != nil {
		t.Fatal(err)
	}
	f.world.InventoryRevision = 44
	s.Tick(context.Background())
	f.world.InventoryBags[4].Slots[0] = &worldv1.BackpackSlot{Bag: 4}
	for _, revision := range []uint64{43, 44} {
		f.world.InventoryRevision = revision
		s.Tick(context.Background())
		if state := s.snapshot(); state.JunkStacksDestroyed != 0 || s.junkPending == nil {
			t.Fatal("older or same-revision absence overrode newer ownership", revision, state)
		}
	}
	f.world.InventoryRevision = 45
	s.Tick(context.Background())
	if state := s.snapshot(); state.JunkStacksDestroyed != 1 || state.LastJunkDisposal.InventoryRevision != 42 ||
		state.LastJunkDisposal.Status != "SERVER_SLOT_REMOVAL_CONFIRMED" || f.count(opcode.CMSGDestroyitem) != 1 {
		t.Fatal("newer complete absence did not settle original source", state)
	}
}

func TestCarriedJunkAmbiguousWriteRemainsReservedWhenDisabledAndNeverReplays(t *testing.T) {
	s, f := carriedJunkFixture(4, 0)
	now := time.Unix(100, 0)
	s.now = func() time.Time { return now }
	_, _ = s.JunkDisposal(context.Background(), &tradev1.JunkToggle{Enabled: true})
	f.fail = opcode.CMSGDestroyitem
	got, err := s.DestroyJunk(context.Background(), carriedJunkRequest(s, f, 4, 0))
	if err != nil || got.LastJunkDisposal.Status != "TRANSPORT_UNCONFIRMED_NO_RETRY" || f.count(opcode.CMSGDestroyitem) != 1 {
		t.Fatal(got, err)
	}
	_, _ = s.JunkDisposal(context.Background(), &tradev1.JunkToggle{Enabled: false})
	now = now.Add(6 * time.Second)
	for range 3 {
		s.Tick(context.Background())
		if _, err := s.DestroyJunk(context.Background(), carriedJunkRequest(s, f, 4, 0)); err == nil {
			t.Fatal("disabled or timed-out pending mutation was replayed")
		}
	}
	if state := s.snapshot(); state.JunkStacksDestroyed != 0 || state.JunkDisposalEnabled ||
		state.LastJunkDisposal.Status != "UNCONFIRMED_NO_RETRY" || f.count(opcode.CMSGDestroyitem) != 1 {
		t.Fatal(state)
	}
	f.world.InventoryBags[4].Slots[0] = &worldv1.BackpackSlot{Bag: 4}
	f.world.InventoryRevision++
	s.Tick(context.Background())
	if state := s.snapshot(); state.JunkStacksDestroyed != 1 || state.JunkDisposalEnabled ||
		state.LastJunkDisposal.Status != "SERVER_SLOT_REMOVAL_CONFIRMED" || f.count(opcode.CMSGDestroyitem) != 1 {
		t.Fatal("toggle-off prevented reconciliation or caused replay", state)
	}
}

func TestCarriedJunkGetStateOwnershipReadRaisesReceiptRevisionFloor(t *testing.T) {
	s, f := carriedJunkFixture(4, 0)
	if _, err := s.DestroyJunk(context.Background(), carriedJunkRequest(s, f, 4, 0)); err != nil {
		t.Fatal(err)
	}
	f.world.InventoryRevision = 44
	observed, err := s.GetState(context.Background(), nil)
	if err != nil || observed.LastJunkDisposal.InventoryRevision != 42 {
		t.Fatal("status read changed the original receipt revision", observed, err)
	}
	f.world.InventoryBags[4].Slots[0] = &worldv1.BackpackSlot{Bag: 4}
	for _, revision := range []uint64{43, 44} {
		f.world.InventoryRevision = revision
		s.Tick(context.Background())
		if s.snapshot().JunkStacksDestroyed != 0 || s.junkPending == nil {
			t.Fatal("status-observed ownership was forgotten before reconciliation", revision, s.snapshot())
		}
	}
	f.world.InventoryRevision = 45
	s.Tick(context.Background())
	if s.snapshot().JunkStacksDestroyed != 1 || f.count(opcode.CMSGDestroyitem) != 1 {
		t.Fatal("later complete absence did not settle after status observation", s.snapshot())
	}
}

func TestCarriedJunkPartialOwnershipStillRaisesReceiptRevisionFloor(t *testing.T) {
	for _, view := range []string{"equipment", "backpack"} {
		t.Run(view, func(t *testing.T) {
			s, f := carriedJunkFixture(4, 0)
			r := carriedJunkRequest(s, f, 4, 0)
			if _, err := s.DestroyJunk(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			complete := proto.Clone(f.world).(*worldv1.SnapshotResponse)
			complete.InventoryBags[4].Slots[0] = &worldv1.BackpackSlot{Bag: 4}
			complete.InventoryRevision = 44
			f.world = proto.Clone(complete).(*worldv1.SnapshotResponse)
			if view == "equipment" {
				f.world.SelfEquipmentKnown = false
				f.world.SelfEquipment[8] = &worldv1.EquipmentSlotState{Slot: 8, SlotKnown: true,
					ItemGuid: r.ExpectedItemGuid, Entry: 4865, StackCount: 1, MetadataKnown: true}
			} else {
				f.world.BackpackKnown = false
				f.world.Backpack[0] = &worldv1.BackpackSlot{ItemGuid: r.ExpectedItemGuid,
					Entry: 4865, StackCount: 5, MetadataKnown: true}
			}
			s.Tick(context.Background())
			if s.snapshot().JunkStacksDestroyed != 0 {
				t.Fatal("partial ownership observation was treated as absence")
			}
			f.world = complete
			s.Tick(context.Background())
			if s.snapshot().JunkStacksDestroyed != 0 || s.junkPending == nil {
				t.Fatal("same-revision complete absence overrode partial ownership")
			}
			f.world.InventoryRevision = 45
			s.Tick(context.Background())
			if s.snapshot().JunkStacksDestroyed != 1 || f.count(opcode.CMSGDestroyitem) != 1 {
				t.Fatal("later complete absence failed to settle without replay", s.snapshot())
			}
		})
	}
}

func TestCarriedJunkPublicFieldsRetainLegacyTagsAndReflectedSourceReceipt(t *testing.T) {
	for _, test := range []struct {
		message proto.Message
		fields  []string
	}{
		{&tradev1.JunkRequest{}, []string{"session_id", "revision", "backpack_revision", "backpack_slot", "expected_item_guid", "source_bag", "source_slot", "inventory_revision"}},
		{&tradev1.JunkDisposal{}, []string{"backpack_slot", "item_guid", "entry", "count", "name", "status", "backpack_revision", "source_bag", "source_slot", "inventory_revision"}},
	} {
		fields := test.message.ProtoReflect().Descriptor().Fields()
		for i, name := range test.fields {
			field := fields.ByName(protoreflect.Name(name))
			if field == nil || field.Number() != protoreflect.FieldNumber(i+1) {
				t.Fatal("public legacy/additive field tag changed", name, field)
			}
		}
	}
	s, f := carriedJunkFixture(4, 35)
	state, err := s.DestroyJunk(context.Background(), carriedJunkRequest(s, f, 4, 35))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := protojson.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var jsonState map[string]any
	if err := json.Unmarshal(encoded, &jsonState); err != nil {
		t.Fatal(err)
	}
	receipt := jsonState["lastJunkDisposal"].(map[string]any)
	if receipt["sourceBag"] != float64(4) || receipt["sourceSlot"] != float64(35) || receipt["inventoryRevision"] != "42" {
		t.Fatal("actual RPC receipt lost carried binding in protojson", receipt)
	}
	var roundTrip tradev1.State
	if err := protojson.Unmarshal(encoded, &roundTrip); err != nil || !proto.Equal(state, &roundTrip) {
		t.Fatal("source receipt did not survive JSON roundtrip", &roundTrip, err)
	}
	registry, err := discovery.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := registry.Catalog().ForMethod("buddy-trade.destroy_junk")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		first int
	}{
		{"JunkRequest", 6}, {"JunkDisposal", 8},
	} {
		schema := catalog.Messages[discovery.SchemaKey("buddy-trade", "warcraftbuddy.trade.v1."+test.name)]
		for i, name := range []string{"sourceBag", "sourceSlot", "inventoryRevision"} {
			found := false
			for _, field := range schema.Fields {
				if field.Name == name && int(field.Number) == test.first+i {
					found = true
				}
			}
			if !found {
				t.Fatal("destroy_junk descriptor lacks exact source field", test.name, name)
			}
		}
	}
}
