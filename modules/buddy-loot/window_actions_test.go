package buddyloot

import (
	"context"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"strings"
	"testing"
	"time"
)

func windowTakeRequest(t *testing.T, s *Service, slot bool) *lootv1.TakeLootRequest {
	t.Helper()
	o, e := s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	if e != nil {
		t.Fatal(e)
	}
	r := &lootv1.TakeLootRequest{SessionId: o.SessionId, SelfGuid: o.SelfGuid, CorpseGuid: o.Window.CorpseGuid, OpenRequestId: o.Window.OpenRequestId, WindowRevision: o.Window.Revision, RequestId: "selected-money"}
	if slot {
		r.RequestId = "selected-slot"
		r.Slot = 2
		r.ExpectedItemId = 42
		r.ExpectedCount = 1
		r.ExpectedInventoryRevision = o.InventoryRevision
	}
	return r
}

func TestLuaWindowQuestSpaceChoiceRetunesInTheSameSession(t *testing.T) {
	s, f, open := exactCorpseRig(t)
	f.world.SelfQuestLogKnown = true
	f.world.SelfQuestLog = []*worldv1.QuestLogEntry{{QuestId: 386}}
	for i, slot := range f.world.Backpack[:15] {
		slot.ItemGuid = 0x4000000000000100 + uint64(i)
		slot.Entry = 25
		slot.StackCount = 1
		slot.MetadataKnown = true
	}
	s.OpenCorpse(context.Background(), open)
	emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 0, lootItem{slot: 2, id: 42, count: 1}))
	vm := corpsePolicyVM(t, open.SessionId)
	view, _ := s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	before := corpsePolicyDecision(t, vm, view, true, false, 1000)
	a, _ := before["action"].(map[string]any)
	if a["capability"] != "loot_close" || len(f.packets) != 1 {
		t.Fatal("quest-space policy did not reserve last slot without writing", before, f.packets)
	}
	source := strings.Replace(corpsePolicySource(t), "reserveQuestSlots=1", "reserveQuestSlots=0", 1)
	if _, e := vm.Reload(context.Background(), source, vm.Status().Revision); e != nil {
		t.Fatal(e)
	}
	after := corpsePolicyDecision(t, vm, view, true, false, 1001)
	a, _ = after["action"].(map[string]any)
	if a["capability"] != "loot_slot" || a["slot"] != float64(2) || vm.Status().SessionID != open.SessionId || len(f.packets) != 1 {
		t.Fatal("reload did not tune the same sole window policy", after, vm.Status(), f.packets)
	}
}
func TestExactWindowActionsArePassiveUntilSelectedAndBindEveryFact(t *testing.T) {
	s, f, open := exactCorpseRig(t)
	if _, e := s.OpenCorpse(context.Background(), open); e != nil {
		t.Fatal(e)
	}
	emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 5, lootItem{slot: 2, id: 42, count: 1}))
	for i := 0; i < 10; i++ {
		s.Tick(context.Background())
	}
	if len(f.packets) != 1 {
		t.Fatal("native Tick chose money or item", f.packets)
	}
	money := windowTakeRequest(t, s, false)
	r, e := s.TakeMoney(context.Background(), money)
	if e != nil || !r.TransportWritten || r.Terminal || r.MoneyCleared || len(f.packets) != 2 || f.packets[1].Opcode != opcode.CMSGLootMoney {
		t.Fatal(r, e, f.packets)
	}
	s.TakeMoney(context.Background(), money)
	if len(f.packets) != 2 {
		t.Fatal("same request replayed money")
	}
	emit(s, opcode.SMSGLootClearMoney, nil)
	item := windowTakeRequest(t, s, true)
	for _, field := range []string{"body", "session", "source", "open", "window", "item", "count", "inventory", "slot"} {
		bad := proto.Clone(item).(*lootv1.TakeLootRequest)
		bad.RequestId = field
		switch field {
		case "body":
			bad.SelfGuid++
		case "session":
			bad.SessionId = "old"
		case "source":
			bad.CorpseGuid++
		case "open":
			bad.OpenRequestId = "other"
		case "window":
			bad.WindowRevision++
		case "item":
			bad.ExpectedItemId++
		case "count":
			bad.ExpectedCount++
		case "inventory":
			bad.ExpectedInventoryRevision++
		case "slot":
			bad.Slot++
		}
		if _, e = s.TakeSlot(context.Background(), bad); status.Code(e) != codes.FailedPrecondition || len(f.packets) != 2 {
			t.Fatal("stale facts wrote a mutation", field, e, f.packets)
		}
	}
	r, e = s.TakeSlot(context.Background(), item)
	if e != nil || !r.TransportWritten || r.Terminal || len(f.packets) != 3 || f.packets[2].Opcode != opcode.CMSGAutostoreLootItem || f.packets[2].Payload[0] != 2 {
		t.Fatal(r, e, f.packets)
	}
	emit(s, opcode.SMSGItemPushResult, pushPacket(1, 42, 1))
	r, _ = s.TakeSlot(context.Background(), item)
	if r.Terminal || !r.ItemPushObserved || r.SlotRemoved {
		t.Fatal("push invented removed slot", r)
	}
	emit(s, opcode.SMSGLootRemoved, []byte{2})
	r, _ = s.TakeSlot(context.Background(), item)
	if !r.Terminal || !r.ItemPushObserved || !r.SlotRemoved || len(f.packets) != 3 {
		t.Fatal("actual matching effects not preserved idempotently", r)
	}
}
func TestLuaWindowUnknownItemNeverReplaysAfterReloadAndYieldsForHealing(t *testing.T) {
	s, f, open := exactCorpseRig(t)
	s.OpenCorpse(context.Background(), open)
	emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 0, lootItem{slot: 2, id: 42, count: 1}))
	vm := corpsePolicyVM(t, open.SessionId)
	view, _ := s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	d := corpsePolicyDecision(t, vm, view, true, false, 1000)
	a, _ := d["action"].(map[string]any)
	if a["capability"] != "loot_slot" || a["slot"] != float64(2) {
		t.Fatal("Lua did not choose exact offered slot", d)
	}
	f.failOpcode = opcode.CMSGAutostoreLootItem
	r, _ := s.TakeSlot(context.Background(), windowTakeRequest(t, s, true))
	if !r.TransportAttempted || r.TransportWritten || r.Terminal {
		t.Fatal("unknown send invented effect", r)
	}
	// The same actual policy checkpoint remains bound to this window.
	if _, e := vm.Reload(context.Background(), corpsePolicySource(t), vm.Status().Revision); e != nil {
		t.Fatal(e)
	}
	view, _ = s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	if d = corpsePolicyDecision(t, vm, view, true, false, 2000); d["action"] != nil {
		t.Fatal("reload replayed unknown mutation", d)
	}
	d = corpsePolicyDecision(t, vm, view, false, true, 2001)
	a, _ = d["action"].(map[string]any)
	if a["capability"] != "loot_close" {
		t.Fatal("injured ally could not preempt unknown pickup", d)
	}
	s.CloseCorpse(context.Background(), &lootv1.CloseCorpseRequest{SessionId: open.SessionId, SelfGuid: open.SelfGuid, CorpseGuid: open.CorpseGuid})
	if f.leaseOwner != "" || len(f.packets) != 3 {
		t.Fatal("uncertain loot retained healing writer or replayed item", f.packets)
	}
}
func TestLuaWindowTimeoutAndEmptyWindowCloseArePolicyChoices(t *testing.T) {
	s, _, open := exactCorpseRig(t)
	s.OpenCorpse(context.Background(), open)
	vm := corpsePolicyVM(t, open.SessionId)
	view, _ := s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	corpsePolicyDecision(t, vm, view, true, false, 0)
	d := corpsePolicyDecision(t, vm, view, true, false, int64(13*time.Second/time.Millisecond))
	a, _ := d["action"].(map[string]any)
	if a["capability"] != "loot_close" {
		t.Fatal("Lua did not close unobserved timed-out open", d)
	}
	emit(s, opcode.SMSGLootResponse, windowPacket(corpseGUID, 0))
	view, _ = s.ObserveCorpses(context.Background(), &emptypb.Empty{})
	d = corpsePolicyDecision(t, vm, view, true, false, 13001)
	a, _ = d["action"].(map[string]any)
	if a["capability"] != "loot_close" {
		t.Fatal("empty window not released", d)
	}
}
