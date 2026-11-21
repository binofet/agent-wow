package buddytrade

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	discovery "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func junkHistoryFixture(carried bool, id string) (*Service, *fakeSession, *junkDispatchSession, *tradev1.JunkRequest) {
	s, f, wrapper, request := junkDispatchFixture(carried)
	if !carried {
		f.world.InventoryKnown, f.world.InventoryRevision = true, 42
		for bag := uint32(0); bag < 5; bag++ {
			row := &worldv1.InventoryBag{Bag: bag, SlotKnown: true, ContentsKnown: true}
			if bag == 0 {
				row.SlotCount = 16
				for _, item := range f.world.Backpack {
					row.Slots = append(row.Slots, proto.Clone(item).(*worldv1.BackpackSlot))
				}
			}
			f.world.InventoryBags = append(f.world.InventoryBags, row)
		}
		f.world.SelfEquipmentKnown, f.world.SelfEquipmentRevision = true, 9
		for slot := uint32(0); slot < 19; slot++ {
			f.world.SelfEquipment = append(f.world.SelfEquipment, &worldv1.EquipmentSlotState{Slot: slot, SlotKnown: true})
		}
	}
	f.world.SelfQuestLogKnown, f.world.SelfQuestLogRevision = true, 13
	for slot := uint32(0); slot < 25; slot++ {
		f.world.SelfQuestLog = append(f.world.SelfQuestLog, &worldv1.QuestLogEntry{Slot: slot})
	}
	request.RequestId, request.ExpectedEquipmentRevision, request.ExpectedQuestLogRevision = id, 9, 13
	installJunkPolicyFixture(s, wrapper, nil)
	return s, f, wrapper, request
}

func nextJunkHistoryRequest(s *Service, f *fakeSession, carried bool, id string) *tradev1.JunkRequest {
	var request *tradev1.JunkRequest
	if carried {
		request = carriedJunkRequest(s, f, 4, 0)
	} else {
		request = requestJunk(s, f)
	}
	request.RequestId, request.ExpectedEquipmentRevision, request.ExpectedQuestLogRevision = id, f.world.SelfEquipmentRevision, f.world.SelfQuestLogRevision
	return request
}

func removeJunkHistorySource(s *Service, f *fakeSession, carried bool) {
	if carried {
		f.world.InventoryBags[4].Slots[0] = &worldv1.BackpackSlot{Bag: 4}
		f.world.InventoryRevision++
	} else {
		f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2}
		f.world.BackpackRevision++
		f.world.InventoryBags[0].Slots[2] = proto.Clone(f.world.Backpack[2]).(*worldv1.BackpackSlot)
		f.world.InventoryRevision++
	}
	s.Tick(context.Background())
}

func refillJunkHistorySource(f *fakeSession, carried bool, sequence int) {
	item := &worldv1.BackpackSlot{ItemGuid: 0x4000000000100000 + uint64(sequence), Entry: 4865, StackCount: 5, MetadataKnown: true}
	if carried {
		item.Bag = 4
		f.world.InventoryBags[4].Slots[0] = item
		f.world.InventoryRevision++
	} else {
		item.Slot = 2
		f.world.Backpack[2] = item
		f.world.BackpackRevision++
		f.world.InventoryBags[0].Slots[2] = proto.Clone(item).(*worldv1.BackpackSlot)
		f.world.InventoryRevision++
	}
}

func replayJunkHistory(t *testing.T, s *Service, f *fakeSession, wrapper *junkDispatchSession, request *tradev1.JunkRequest, want string) *tradev1.State {
	t.Helper()
	dependencies, packets := len(wrapper.calls), f.count(opcode.CMSGDestroyitem)
	result, err := s.DestroyJunk(context.Background(), request)
	if err != nil || result.LastJunkDisposal.GetRequestId() != request.RequestId || result.LastJunkDisposal.Status != want ||
		len(wrapper.calls) != dependencies || f.count(opcode.CMSGDestroyitem) != packets {
		t.Fatal("exact repeat did not return its receipt without effects", result, err)
	}
	return result
}

func TestJunkRequestsReserveHistoryBeforePacketAndReturnDetachedReceipts(t *testing.T) {
	for _, carried := range []bool{false, true} {
		t.Run(fmt.Sprint(carried), func(t *testing.T) {
			s, f, wrapper, request := junkHistoryFixture(carried, "first-request_1")
			original := proto.Clone(request).(*tradev1.JunkRequest)
			wrapper.before = func(_ context.Context, _ string) error {
				if len(s.junkRequests) != 0 || len(s.snapshot().JunkOperations) != 0 {
					t.Fatal("history started before the final write reservation")
				}
				return nil
			}
			reserved := false
			wrapper.beforeWrite = func(packet *modv1.SendPacketRequest) {
				if packet.Opcode != opcode.CMSGDestroyitem {
					return
				}
				reserved = true
				s.mu.Lock()
				row := s.junkRequests[request.RequestId]
				valid := row != nil && proto.Equal(row.request, original) && row.receipt == s.junkPending &&
					len(s.state.JunkOperations) == 1 && s.state.JunkOperations[0] == row.receipt &&
					row.receipt.RequestId == request.RequestId && row.receipt.EquipmentRevision == 9 && row.receipt.QuestLogRevision == 13
				s.mu.Unlock()
				if !valid || f.count(opcode.CMSGDestroyitem) != 0 {
					t.Fatal("request, receipt and history were not reserved together before transport")
				}
			}
			result, err := s.DestroyJunk(context.Background(), request)
			if err != nil || !reserved || len(result.JunkOperations) != 1 {
				t.Fatal(result, err)
			}
			wrapper.before, wrapper.beforeWrite = nil, nil
			request.ExpectedItemGuid++
			result.LastJunkDisposal.Status = "CLIENT_CHANGED"
			result.JunkOperations[0].RequestId = "CLIENT_CHANGED"
			pending := replayJunkHistory(t, s, f, wrapper, original, "AWAITING_SERVER_SLOT_UPDATE")
			if pending.JunkOperations[0].RequestId != original.RequestId {
				t.Fatal("response history aliases retained receipt")
			}
			removeJunkHistorySource(s, f, carried)
			confirmed := replayJunkHistory(t, s, f, wrapper, original, "SERVER_SLOT_REMOVAL_CONFIRMED")
			if confirmed.JunkOperations[0].Status != "SERVER_SLOT_REMOVAL_CONFIRMED" || confirmed.JunkStacksDestroyed != 1 {
				t.Fatal("confirmation did not update original retained history", confirmed)
			}
		})
	}
}

func TestJunkRequestsReplayOriginalAfterAnotherOperationWithoutReplacingLatest(t *testing.T) {
	s, f, wrapper, first := junkHistoryFixture(true, "first")
	if _, err := s.DestroyJunk(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	removeJunkHistorySource(s, f, true)
	refillJunkHistorySource(f, true, 1)
	second := nextJunkHistoryRequest(s, f, true, "second")
	if _, err := s.DestroyJunk(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	old := replayJunkHistory(t, s, f, wrapper, first, "SERVER_SLOT_REMOVAL_CONFIRMED")
	if len(old.JunkOperations) != 2 || old.JunkOperations[1].RequestId != "second" ||
		s.snapshot().LastJunkDisposal.RequestId != "second" {
		t.Fatal("reading an older receipt replaced the latest operation", old)
	}
	dependencies := len(wrapper.calls)
	third := nextJunkHistoryRequest(s, f, true, "third")
	if _, err := s.DestroyJunk(context.Background(), third); status.Code(err) != codes.FailedPrecondition ||
		len(wrapper.calls) != dependencies || len(s.junkRequests) != 2 {
		t.Fatal("different request bypassed an outstanding reservation", err)
	}
}

func TestJunkRequestsUnknownAndTimedOutReceiptsStayReplayableWithoutResend(t *testing.T) {
	s, f, wrapper, request := junkHistoryFixture(true, "uncertain")
	now := time.Unix(100, 0)
	s.now = func() time.Time { return now }
	f.fail = opcode.CMSGDestroyitem
	if _, err := s.DestroyJunk(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	replayJunkHistory(t, s, f, wrapper, request, "TRANSPORT_UNCONFIRMED_NO_RETRY")
	_, _ = s.JunkDisposal(context.Background(), &tradev1.JunkToggle{Enabled: false})
	now = now.Add(6 * time.Second)
	s.Tick(context.Background())
	unknown := replayJunkHistory(t, s, f, wrapper, request, "UNCONFIRMED_NO_RETRY")
	if unknown.JunkOperations[0].Status != "UNCONFIRMED_NO_RETRY" {
		t.Fatal("timeout status was not retained in request history", unknown)
	}
	removeJunkHistorySource(s, f, true)
	replayJunkHistory(t, s, f, wrapper, request, "SERVER_SLOT_REMOVAL_CONFIRMED")
	if f.count(opcode.CMSGDestroyitem) != 1 {
		t.Fatal("uncertain write was replayed")
	}
}

func TestJunkRequestsChangedKnownPayloadIsConflictWithoutDependencies(t *testing.T) {
	common := map[string]func(*tradev1.JunkRequest){
		"state_revision":     func(r *tradev1.JunkRequest) { r.Revision++ },
		"item_guid":          func(r *tradev1.JunkRequest) { r.ExpectedItemGuid++ },
		"equipment_revision": func(r *tradev1.JunkRequest) { r.ExpectedEquipmentRevision++ },
		"quest_revision":     func(r *tradev1.JunkRequest) { r.ExpectedQuestLogRevision++ },
	}
	for _, carried := range []bool{false, true} {
		s, f, wrapper, request := junkHistoryFixture(carried, "stable-id")
		if _, err := s.DestroyJunk(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		changes := make(map[string]func(*tradev1.JunkRequest))
		for k, v := range common {
			changes[k] = v
		}
		if carried {
			changes["source_bag"] = func(r *tradev1.JunkRequest) { r.SourceBag = 3 }
			changes["source_slot"] = func(r *tradev1.JunkRequest) { r.SourceSlot = 1 }
			changes["inventory_revision"] = func(r *tradev1.JunkRequest) { r.InventoryRevision++ }
		} else {
			changes["backpack_slot"] = func(r *tradev1.JunkRequest) { r.BackpackSlot = 3 }
			changes["backpack_revision"] = func(r *tradev1.JunkRequest) { r.BackpackRevision++ }
		}
		for name, change := range changes {
			t.Run(fmt.Sprintf("%t/%s", carried, name), func(t *testing.T) {
				changed := proto.Clone(request).(*tradev1.JunkRequest)
				change(changed)
				before := len(wrapper.calls)
				if _, err := s.DestroyJunk(context.Background(), changed); status.Code(err) != codes.AlreadyExists ||
					len(wrapper.calls) != before || f.count(opcode.CMSGDestroyitem) != 1 || len(s.junkRequests) != 1 {
					t.Fatal("changed request payload was not a side-effect-free conflict", err)
				}
			})
		}
		foreign := proto.Clone(request).(*tradev1.JunkRequest)
		foreign.SessionId = "another-body"
		before := len(wrapper.calls)
		if _, err := s.DestroyJunk(context.Background(), foreign); status.Code(err) != codes.FailedPrecondition || len(wrapper.calls) != before {
			t.Fatal("foreign session read a prior session receipt", err)
		}
	}
}

func TestJunkRequestsValidateNewShapeBeforeAnyDependencyAndPreserveEmptyIDMode(t *testing.T) {
	changes := map[string]func(*tradev1.JunkRequest){
		"dot":                   func(r *tradev1.JunkRequest) { r.RequestId = "not.allowed" },
		"space":                 func(r *tradev1.JunkRequest) { r.RequestId = "not allowed" },
		"too_long":              func(r *tradev1.JunkRequest) { r.RequestId = strings.Repeat("a", 65) },
		"unicode":               func(r *tradev1.JunkRequest) { r.RequestId = "request-é" },
		"no_equipment_revision": func(r *tradev1.JunkRequest) { r.ExpectedEquipmentRevision = 0 },
		"no_quest_revision":     func(r *tradev1.JunkRequest) { r.ExpectedQuestLogRevision = 0 },
		"context_without_id":    func(r *tradev1.JunkRequest) { r.RequestId = "" },
		"oversize":              func(r *tradev1.JunkRequest) { r.SessionId = strings.Repeat("a", 1025) },
		"unknown_fields": func(r *tradev1.JunkRequest) {
			r.ProtoReflect().SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 1))
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s, f, wrapper, request := junkHistoryFixture(true, "valid")
			change(request)
			if _, err := s.DestroyJunk(context.Background(), request); status.Code(err) != codes.InvalidArgument ||
				len(wrapper.calls) != 0 || f.count(opcode.CMSGDestroyitem) != 0 || len(s.junkRequests) != 0 {
				t.Fatal("invalid new request reached a dependency or reserved history", err)
			}
		})
	}
	s, f := junkFixture() // Original empty-ID shape needs neither new context frame.
	legacy, err := s.DestroyJunk(context.Background(), requestJunk(s, f))
	if err != nil || legacy.LastJunkDisposal.RequestId != "" || len(legacy.JunkOperations) != 0 || len(s.junkRequests) != 0 {
		t.Fatal("old request shape was changed by optional request history", legacy, err)
	}
	boundary, _, _, request := junkHistoryFixture(true, strings.Repeat("A", 62)+"_-")
	if _, err := boundary.DestroyJunk(context.Background(), request); err != nil {
		t.Fatal("valid 64-character request ID rejected", err)
	}
}

func TestJunkRequestsRequireCompleteContextBeforeAcquisitionAndAfterFinalWorld(t *testing.T) {
	changes := map[string]func(*worldv1.SnapshotResponse){
		"equipment_revision":       func(w *worldv1.SnapshotResponse) { w.SelfEquipmentRevision++ },
		"quest_revision":           func(w *worldv1.SnapshotResponse) { w.SelfQuestLogRevision++ },
		"equipment_unknown":        func(w *worldv1.SnapshotResponse) { w.SelfEquipmentKnown = false },
		"quest_unknown":            func(w *worldv1.SnapshotResponse) { w.SelfQuestLogKnown = false },
		"short_equipment":          func(w *worldv1.SnapshotResponse) { w.SelfEquipment = w.SelfEquipment[:18] },
		"short_quest_log":          func(w *worldv1.SnapshotResponse) { w.SelfQuestLog = w.SelfQuestLog[:24] },
		"duplicate_equipment_slot": func(w *worldv1.SnapshotResponse) { w.SelfEquipment[1].Slot = 0 },
		"duplicate_quest_slot":     func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[1].Slot = 0 },
		"duplicate_quest_id":       func(w *worldv1.SnapshotResponse) { w.SelfQuestLog[0].QuestId, w.SelfQuestLog[1].QuestId = 7, 7 },
		"carried_equipment_alias": func(w *worldv1.SnapshotResponse) {
			w.SelfEquipment[8] = &worldv1.EquipmentSlotState{Slot: 8, SlotKnown: true,
				ItemGuid: w.Backpack[0].ItemGuid, Entry: 117, StackCount: 1, MetadataKnown: true}
		},
	}
	for _, carried := range []bool{false, true} {
		for _, moment := range []string{"initial", "status", "renewal"} {
			for name, change := range changes {
				t.Run(fmt.Sprintf("%t/%s/%s", carried, moment, name), func(t *testing.T) {
					s, f, wrapper, request := junkHistoryFixture(carried, "not-reserved-yet")
					original := proto.Clone(f.world).(*worldv1.SnapshotResponse)
					injected := moment == "initial"
					if injected {
						change(f.world)
					} else {
						wrapper.before = func(_ context.Context, method string) error {
							atStatus := moment == "status" && method == "buddy-actions.status"
							atRenewal := moment == "renewal" && method == "buddy-actions.acquire_interaction" && len(wrapper.acquisitions) == 2
							if !injected && (atStatus || atRenewal) {
								injected = true
								change(f.world)
							}
							return nil
						}
					}
					if _, err := s.DestroyJunk(context.Background(), request); !injected || status.Code(err) != codes.FailedPrecondition ||
						f.count(opcode.CMSGDestroyitem) != 0 || len(s.junkRequests) != 0 || len(s.snapshot().JunkOperations) != 0 {
						t.Fatal("changed context wrote or consumed a request ID", err, injected)
					}
					if moment == "initial" && f.leases != 0 {
						t.Fatal("invalid initial context acquired a lease")
					}
					wrapper.before = nil
					f.world = original
					if _, err := s.DestroyJunk(context.Background(), request); err != nil {
						t.Fatal("failed pre-write attempt permanently reserved its ID", err)
					}
				})
			}
		}
	}
}

func TestJunkRequestsCanceledNewAndReplayCallsHaveNoEffects(t *testing.T) {
	s, f, wrapper, request := junkHistoryFixture(true, "cancel-test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.DestroyJunk(ctx, request); status.Code(err) != codes.Canceled || len(wrapper.calls) != 0 || len(s.junkRequests) != 0 {
		t.Fatal("canceled new call reached a dependency or history", err)
	}
	if _, err := s.DestroyJunk(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	before := len(wrapper.calls)
	if _, err := s.DestroyJunk(ctx, request); status.Code(err) != codes.Canceled || len(wrapper.calls) != before || f.count(opcode.CMSGDestroyitem) != 1 {
		t.Fatal("canceled replay affected receipt custody", err)
	}
}

func TestJunkRequestsRetainAll256ReceiptsAndFailClosedWithoutEviction(t *testing.T) {
	s, f, wrapper, request := junkHistoryFixture(true, "bounded-000")
	first := proto.Clone(request).(*tradev1.JunkRequest)
	for index := 0; index < 256; index++ {
		if index != 0 {
			request = nextJunkHistoryRequest(s, f, true, fmt.Sprintf("bounded-%03d", index))
		}
		if _, err := s.DestroyJunk(context.Background(), request); err != nil {
			t.Fatal("history filled before the retained lifetime bound", index, err)
		}
		removeJunkHistorySource(s, f, true)
		refillJunkHistorySource(f, true, index+1)
	}
	if len(s.junkRequests) != 256 || len(s.snapshot().JunkOperations) != 256 || f.count(opcode.CMSGDestroyitem) != 256 {
		t.Fatal("completed operations were evicted or not retained")
	}
	before := len(wrapper.calls)
	if _, err := s.DestroyJunk(context.Background(), nextJunkHistoryRequest(s, f, true, "overflow")); status.Code(err) != codes.ResourceExhausted || len(wrapper.calls) != before || f.count(opcode.CMSGDestroyitem) != 256 {
		t.Fatal("full request history dispatched or evicted a prior request", err)
	}
	replayJunkHistory(t, s, f, wrapper, first, "SERVER_SLOT_REMOVAL_CONFIRMED")
}

func TestJunkRequestsAdditivePublicHistoryAndContextTagsRoundTrip(t *testing.T) {
	for _, test := range []struct {
		message proto.Message
		fields  map[protoreflect.Name]protoreflect.FieldNumber
	}{
		{&tradev1.JunkRequest{}, map[protoreflect.Name]protoreflect.FieldNumber{"request_id": 9, "expected_equipment_revision": 10, "expected_quest_log_revision": 11}},
		{&tradev1.JunkDisposal{}, map[protoreflect.Name]protoreflect.FieldNumber{"request_id": 11, "equipment_revision": 12, "quest_log_revision": 13}},
		{&tradev1.State{}, map[protoreflect.Name]protoreflect.FieldNumber{"junk_operations": 35}},
	} {
		for name, number := range test.fields {
			field := test.message.ProtoReflect().Descriptor().Fields().ByName(name)
			if field == nil || field.Number() != number {
				t.Fatal("new context/history field changed its additive tag", name, field)
			}
		}
	}
	s, _, _, request := junkHistoryFixture(true, "public-record")
	result, err := s.DestroyJunk(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []proto.Message{request, result} {
		wire, err := proto.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		decoded := value.ProtoReflect().New().Interface()
		if err := proto.Unmarshal(wire, decoded); err != nil || !proto.Equal(value, decoded) {
			t.Fatal("context binding lost in protobuf roundtrip", err)
		}
		body, err := protojson.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := protojson.Unmarshal(body, decoded); err != nil || !proto.Equal(value, decoded) {
			t.Fatal("context binding lost in protojson roundtrip", err)
		}
	}
	registry, err := discovery.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := registry.Catalog().ForMethod("buddy-trade.destroy_junk")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"JunkRequest", "JunkDisposal", "State"} {
		compiled := tradev1.File_modules_buddy_trade_trade_proto.Messages().ByName(protoreflect.Name(name))
		schema := catalog.Messages[discovery.SchemaKey("buddy-trade", "warcraftbuddy.trade.v1."+name)]
		if len(schema.Fields) != compiled.Fields().Len() {
			t.Fatal("advertised request history differs from compiled API", name)
		}
	}
}
