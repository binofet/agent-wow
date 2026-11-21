package buddyloot

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func observedGatherWindow(t *testing.T) (*Service, *fakeSession, *lootv1.CorpseObservation) {
	t.Helper()
	s, f, _ := harvestRig(t)
	enable(t, s)
	s.Tick(context.Background())
	emit(s, opcode.SMSGLootResponse, harvestWindow(harvestGUID, lootItem{slot: 1, id: 42, count: 1}, lootItem{slot: 2, id: harvestItemID, count: 1, slotType: 4}))
	for i := 0; i < 3; i++ {
		s.Tick(context.Background())
	}
	if len(f.packets) != 1 {
		t.Fatal("native chose a quest-container slot", f.packets)
	}
	o, e := s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	if e != nil {
		t.Fatal(e)
	}
	return s, f, o
}
func gatherWindowTake(t *testing.T, o *lootv1.CorpseObservation, item uint32, slot uint32) *lootv1.TakeLootRequest {
	t.Helper()
	return &lootv1.TakeLootRequest{SessionId: o.SessionId, SelfGuid: o.SelfGuid, SourceGuid: o.Window.SourceGuid, OpenRequestId: o.Window.OpenRequestId,
		WindowRevision: o.Window.Revision, RequestId: "take-" + strconv.FormatUint(uint64(item), 10), Slot: slot, ExpectedItemId: item, ExpectedCount: 1, ExpectedInventoryRevision: o.InventoryRevision}
}
func TestGatherWindowUsesSameLuaAndActualGameObjectBinding(t *testing.T) {
	s, f, o := observedGatherWindow(t)
	if o.Window == nil || o.Window.SourceGuid != harvestGUID || o.Window.CorpseGuid != 0 || o.Window.SourceKind != "quest_container" || o.Window.BoundItemId != harvestItemID || o.Window.BoundQuestId != harvestQuestID {
		t.Fatal("window invented a corpse or lost bound goal", o)
	}
	vm := corpsePolicyVM(t, o.SessionId)
	d := corpsePolicyDecision(t, vm, o, true, false)
	a, _ := d["action"].(map[string]any)
	if a["capability"] != "loot_slot" || a["sourceGuid"] != strconv.FormatUint(harvestGUID, 10) || a["corpseGuid"] != nil || a["expectedItemId"] != float64(harvestItemID) {
		t.Fatal("shared Lua did not select bound quest item", d)
	}
	delete(a, "capability")
	delete(a, "observationSequence")
	raw, _ := json.Marshal(a)
	r := &lootv1.TakeLootRequest{}
	if e := protojson.Unmarshal(raw, r); e != nil {
		t.Fatal(e)
	}
	receipt, e := s.TakeSlot(context.Background(), r)
	if e != nil || !receipt.TransportWritten || receipt.SourceGuid != harvestGUID || receipt.CorpseGuid != 0 || len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGAutostoreLootItem || f.packets[1].Payload[0] != 2 {
		t.Fatal(receipt, e, f.packets)
	}
	emit(s, opcode.SMSGItemPushResult, pushPacket(4, harvestItemID, 1))
	emit(s, opcode.SMSGLootRemoved, []byte{1})
	if current(t, s).LastGather.ItemPushObserved || current(t, s).LastGather.SlotRemoved {
		t.Fatal("foreign actor/slot became quest proof", current(t, s))
	}
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, harvestItemID, 1))
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	tickCorpseLua(t, s)
	if len(f.packets) != 3 || f.packets[2].Opcode != opcode.CMSGLootRelease || f.leaseOwner != "" {
		t.Fatal("same Lua did not close collected container", current(t, s), f.packets)
	}
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(harvestGUID), 1))
	s.Tick(context.Background())
	if current(t, s).LastGather.Status != lootv1.GatherStatus_GATHER_CONFIRMED {
		t.Fatal("actual selected item/slot/release did not settle", current(t, s))
	}
}
func TestGatherWindowPrimitiveDoesNotSelectItemsOrMisreportGoalEffects(t *testing.T) {
	s, f, o := observedGatherWindow(t)
	request := gatherWindowTake(t, o, 42, 1)
	receipt, e := s.TakeSlot(context.Background(), request)
	if e != nil || !receipt.TransportWritten || f.packets[1].Payload[0] != 1 {
		t.Fatal("native replaced a valid caller-selected slot", receipt, e, f.packets)
	}
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 42, 1))
	emit(s, opcode.SMSGLootRemoved, []byte{1})
	if current(t, s).ItemsStoredConfirmed != 1 || current(t, s).LastGather.ItemPushObserved || current(t, s).LastGather.SlotRemoved {
		t.Fatal("another permitted item became the bound quest item", current(t, s))
	}
}
func TestGatherWindowMoneyCannotBeSettledByUnrelatedInventoryError(t *testing.T) {
	s, _, o := observedGatherWindow(t)
	s.window.gold = 5
	r := gatherWindowTake(t, o, 0, 0)
	r.ExpectedCount = 0
	receipt, e := s.TakeMoney(context.Background(), r)
	if e != nil || !receipt.TransportWritten {
		t.Fatal(receipt, e)
	}
	failure := make([]byte, 18)
	failure[0] = 50
	emit(s, opcode.SMSGInventoryChangeFailure, failure)
	view, e := s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	if e != nil || view.Window.Mutation.Terminal || view.Window.Mutation.ServerRejected || view.Window.Mutation.MoneyCleared {
		t.Fatal("inventory error invented a source-less money effect", view, e)
	}
}
func TestGatherWindowExactActionsRejectChangedBoundQuestSourceAndRevisions(t *testing.T) {
	for _, kind := range []string{"source", "recipe", "item", "window", "inventory", "corpse_alias"} {
		t.Run(kind, func(t *testing.T) {
			s, f, o := observedGatherWindow(t)
			r := gatherWindowTake(t, o, harvestItemID, 2)
			switch kind {
			case "source":
				r.SourceGuid++
			case "recipe":
				f.world.SelfQuestLog[0].QuestId = 0
			case "item":
				r.ExpectedItemId++
			case "window":
				r.WindowRevision++
			case "inventory":
				r.ExpectedInventoryRevision++
			case "corpse_alias":
				r.CorpseGuid = r.SourceGuid
				r.SourceGuid = 0
			}
			_, e := s.TakeSlot(context.Background(), r)
			if status.Code(e) != codes.FailedPrecondition || len(f.packets) != 1 {
				t.Fatal("changed quest/source/item facts wrote a pickup", kind, e, f.packets)
			}
		})
	}
}
func TestGatherWindowUnknownMutationSurvivesReloadAndYieldsForDefense(t *testing.T) {
	s, f, o := observedGatherWindow(t)
	vm := corpsePolicyVM(t, o.SessionId)
	corpsePolicyDecision(t, vm, o, true, false, 1000)
	f.failOpcode = opcode.CMSGAutostoreLootItem
	request := gatherWindowTake(t, o, harvestItemID, 2)
	receipt, _ := s.TakeSlot(context.Background(), request)
	if !receipt.TransportAttempted || receipt.TransportWritten || receipt.Terminal {
		t.Fatal("unknown write became success", receipt)
	}
	if _, e := vm.Reload(context.Background(), corpsePolicySource(t), vm.Status().Revision); e != nil {
		t.Fatal(e)
	}
	fresh, _ := s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	if d := corpsePolicyDecision(t, vm, fresh, true, false, 1001); d["action"] != nil {
		t.Fatal("reloaded policy replayed unknown pickup", d)
	}
	d := corpsePolicyDecision(t, vm, fresh, false, true, 1002)
	a, _ := d["action"].(map[string]any)
	if a["capability"] != "loot_close" || a["sourceGuid"] != strconv.FormatUint(harvestGUID, 10) {
		t.Fatal("defense could not release bound object", d)
	}
	s.CloseCorpse(context.Background(), &lootv1.CloseCorpseRequest{SessionId: o.SessionId, SelfGuid: o.SelfGuid, SourceGuid: harvestGUID})
	if f.leaseOwner != "" || len(f.packets) != 3 {
		t.Fatal("unknown gather blocked defense or replayed pickup", f.packets)
	}
	emit(s, opcode.SMSGLootReleaseResponse, append(guidPacket(harvestGUID), 1))
	s.Tick(context.Background())
	repeat, _ := s.TakeSlot(context.Background(), proto.Clone(request).(*lootv1.TakeLootRequest))
	if repeat == nil || repeat.Terminal || repeat.ItemPushObserved || len(f.packets) != 3 {
		t.Fatal("release invented pickup confirmation", repeat, f.packets)
	}
}
