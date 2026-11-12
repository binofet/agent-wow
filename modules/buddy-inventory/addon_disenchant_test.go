package buddyinventory

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"testing"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type addonDEFixture struct {
	base         *fakeInventory
	mu           sync.Mutex
	state        *professionsv1.State
	inspections  []*professionsv1.DisenchantInspectRequest
	attempts     []*professionsv1.AttemptRequest
	attemptError bool
}

func (f *addonDEFixture) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	return f.base.SendPacket(ctx, r, opts...)
}
func (f *addonDEFixture) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module != "buddy-professions" {
		return f.base.InvokeModule(ctx, r, opts...)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case "inspect_disenchant":
		q := &professionsv1.DisenchantInspectRequest{}
		if err := r.Request.UnmarshalTo(q); err != nil {
			return nil, err
		}
		f.inspections = append(f.inspections, q)
	case "attempt":
		q := &professionsv1.AttemptRequest{}
		if err := r.Request.UnmarshalTo(q); err != nil {
			return nil, err
		}
		f.attempts = append(f.attempts, q)
		if f.attemptError {
			return nil, errors.New("reply lost after possible reservation")
		}
		f.state = addonDECompleted()
	case "state":
	default:
		return nil, errors.New("unexpected DE callback")
	}
	a, err := anypb.New(proto.Clone(f.state))
	return &modv1.InvokeModuleResponse{Result: a}, err
}

func addonDECompleted() *professionsv1.State {
	return &professionsv1.State{SessionId: "body", SelfGuid: 4, InventoryKnown: true, InventoryRevision: 2, Operation: &professionsv1.Operation{OperationId: "actual-op", RequestId: "addon_d_0123456789ab", SkillId: 333, SpellId: 13262, TargetGuid: junkGUID, TargetEntry: 4865, SourceBag: 0, SourceSlot: 0, BaselineInventoryRevision: 1, ObservedInventoryRevision: 2, Status: professionsv1.AttemptStatus_COMPLETED, CastTransportWritten: true, OwnGoObserved: true, LootWindowObserved: true, LootSlotsRemoved: 1, OwnLootedItemsObserved: 2, LootReleaseObserved: true, InputConsumedObserved: true, OutputInventoryObserved: true, LeaseReleased: true}}
}

func addonDETestFixture(t *testing.T) (*addonBridge, *addonDEFixture, *addonCommand, *inventoryv1.State) {
	t.Helper()
	_, base := fixtureInventory()
	base.w.Self.Guid = 4
	base.w.InventoryBags[0].Slots[0].StackCount = 1
	f := &addonDEFixture{base: base, state: &professionsv1.State{SessionId: "body", SelfGuid: 4, Revision: 17, SkillsKnown: true, SkillsRevision: 9, SpellsKnown: true, InventoryKnown: true, InventoryRevision: 1, DisenchantItem: &professionsv1.DisenchantItem{ItemGuid: junkGUID, Entry: 4865, StackCount: 1, TemplateKnown: true, Ready: true}}}
	s := NewService(f, "body", 4)
	t.Cleanup(s.addon.close)
	s.queried[4865] = true
	s.queried[41599] = true
	state := stateInventory(t, s)
	c := &addonCommand{kind: "D", nonce: "0123456789ab", snapshot: "abcdef012345", revision: 1, itemGUID: junkGUID, raw: "1|D|0123456789ab|abcdef012345|1|0|0|4000000000000001"}
	b := s.addon
	b.snapshots[c.snapshot] = addonSnapshot{state: proto.Clone(state).(*inventoryv1.State), at: b.now()}
	b.requests[c.nonce] = &addonReceipt{raw: c.raw, snapshot: c.snapshot, status: "QUEUED"}
	return b, f, c, state
}

func TestInventoryAddonDEOnlyOwnerPartyExactDAndReadOnlyP(t *testing.T) {
	for _, raw := range []string{"1|D|0123456789ab|abcdef012345|7|4|35|4000000000000001", "1|P|0123456789ab|abcdef012345"} {
		p := addonInbound(raw)
		c, yes, err := decodeInventoryAddon(p)
		if err != nil || !yes || c == nil {
			t.Fatal(raw, c, err)
		}
		binary.LittleEndian.PutUint64(p[5:], 4)
		if c, yes, _ = decodeInventoryAddon(p); c != nil || yes {
			t.Fatal("nonowner authorized DE/poll")
		}
	}
	for _, raw := range []string{"1|P|0123456789ab", "1|P|0123456789ab|abcdef012345|extra", "1|D|0123456789ab|abcdef012345|7|0|0|0000000000000004", "1|D|0123456789ab|abcdef012345|7|0|0|4000000000000001|extra"} {
		if c, _, err := decodeInventoryAddon(addonInbound(raw)); c != nil || err == nil {
			t.Fatal("malformed DE command accepted", raw)
		}
	}
}

func TestInventoryAddonDEUsesOneExactInspectedFixedAttempt(t *testing.T) {
	b, f, c, _ := addonDETestFixture(t)
	b.process(c)
	if len(f.inspections) != 1 || f.inspections[0].SessionId != "body" || f.inspections[0].TargetGuid != junkGUID || len(f.attempts) != 1 {
		t.Fatal(f.inspections, f.attempts)
	}
	q := f.attempts[0]
	if q.SkillId != 333 || q.SpellId != 13262 || q.TargetGuid != junkGUID || q.TargetPositionRevision != 0 || q.ExpectedRevision != 17 || q.ExpectedSkillsRevision != 9 || q.ExpectedInventoryRevision != 1 || q.RequestId != "addon_d_0123456789ab" {
		t.Fatal(q)
	}
	if b.requests[c.nonce].status != "CONFIRMED" {
		t.Fatal(b.requests[c.nonce])
	}
	b.receive(addonInbound(c.raw))
	if len(b.queue) != 0 || len(f.attempts) != 1 {
		t.Fatal("D replay queued another attempt")
	}
}

func TestInventoryAddonDEInspectionNeverRebindsOwnerSelection(t *testing.T) {
	for _, mode := range []string{"actor", "session", "inventory", "guid", "entry", "bag", "slot", "count", "notready", "stop", "skills", "spells", "worldsource"} {
		t.Run(mode, func(t *testing.T) {
			b, f, c, _ := addonDETestFixture(t)
			switch mode {
			case "actor":
				b.service.selfGUID = 1
			case "session":
				f.state.SessionId = "foreign"
			case "inventory":
				f.state.InventoryRevision++
			case "guid":
				f.state.DisenchantItem.ItemGuid++
			case "entry":
				f.state.DisenchantItem.Entry++
			case "bag":
				f.state.DisenchantItem.Bag = 1
			case "slot":
				f.state.DisenchantItem.Slot = 1
			case "count":
				f.state.DisenchantItem.StackCount = 2
			case "notready":
				f.state.DisenchantItem.Ready = false
			case "stop":
				f.state.StopPrepared = true
			case "skills":
				f.state.SkillsKnown = false
			case "spells":
				f.state.SpellsKnown = false
			case "worldsource":
				f.base.w.InventoryBags[0].Slots[0].ItemGuid++
			}
			b.process(c)
			if len(f.attempts) != 0 || b.requests[c.nonce].status != "BLOCKED" {
				t.Fatal(mode, f.attempts, b.requests[c.nonce])
			}
			if mode == "actor" && len(f.inspections) != 0 {
				t.Fatal("nonMira inspected")
			}
		})
	}
}

func TestInventoryAddonDEAmbiguousAttemptAndLatePollNeverResends(t *testing.T) {
	b, f, c, _ := addonDETestFixture(t)
	f.attemptError = true
	b.process(c)
	if b.requests[c.nonce].status != "UNCONFIRMED" || b.requests[c.nonce].disenchant == nil || len(f.attempts) != 1 {
		t.Fatal("lost response not retained")
	}
	b.receive(addonInbound(c.raw))
	if len(b.queue) != 0 {
		t.Fatal("replay queued mutation")
	}
	changed := "1|D|0123456789ab|abcdef012345|1|0|1|4000000000000001"
	b.receive(addonInbound(changed))
	if len(b.queue) != 0 || !strings.Contains(b.outgoing[len(b.outgoing)-1], "|UNCONFIRMED|") {
		t.Fatal("changed pending nonce falsely cleared uncertainty")
	}
	f.state = addonDECompleted()
	b.receive(addonInbound("1|P|0123456789ab|abcdef012345"))
	if len(b.queue) != 1 || b.queue[0].kind != "P" {
		t.Fatal("owned receipt poll not queued")
	}
	p := b.queue[0]
	b.queue = nil
	b.process(p)
	if b.requests[c.nonce].status != "CONFIRMED" || len(f.attempts) != 1 || len(f.inspections) != 1 {
		t.Fatal("late proof replayed selected action")
	}
	// An already queued poll cannot erase established proof if other work later
	// replaces the module's last operation.
	f.state.Operation = nil
	b.process(p)
	if b.requests[c.nonce].status != "CONFIRMED" {
		t.Fatal("late poll erased proven terminal receipt")
	}
	b.receive(addonInbound(changed))
	if !strings.Contains(b.outgoing[len(b.outgoing)-1], "|CONFIRMED|") || len(b.queue) != 0 {
		t.Fatal("changed nonce erased established terminal wire receipt")
	}
	b.receive(addonInbound("1|P|aaaaaaaaaaaa|abcdef012345"))
	b.receive(addonInbound("1|P|0123456789ab|aaaaaaaaaaaa"))
	if len(b.queue) != 0 {
		t.Fatal("foreign poll queued")
	}
}

func TestInventoryAddonDEConfirmationRequiresEveryBoundMaterialProof(t *testing.T) {
	d := &addonDisenchant{session: "body", request: "addon_d_0123456789ab", guid: junkGUID, entry: 4865, inventory: 1}
	for _, mode := range []string{"success", "active", "cleanup", "lease", "request", "actor", "session", "skill", "spell", "guid", "entry", "bag", "slot", "baseline", "inventory", "revision", "input", "output", "go", "window", "removed", "push", "release", "cast", "rejected_window"} {
		s := addonDECompleted()
		o := s.Operation
		switch mode {
		case "active":
			s.Active = true
		case "cleanup":
			o.CleanupUnconfirmed = true
		case "lease":
			o.LeaseReleased = false
		case "request":
			o.RequestId = "other"
		case "actor":
			s.SelfGuid = 1
		case "session":
			s.SessionId = "foreign"
		case "skill":
			o.SkillId = 182
		case "spell":
			o.SpellId = 2366
		case "guid":
			o.TargetGuid++
		case "entry":
			o.TargetEntry++
		case "bag":
			o.SourceBag = 1
		case "slot":
			o.SourceSlot = 1
		case "baseline":
			o.BaselineInventoryRevision++
		case "inventory":
			s.InventoryKnown = false
		case "revision":
			o.ObservedInventoryRevision = 1
		case "input":
			o.InputConsumedObserved = false
		case "output":
			o.OutputInventoryObserved = false
		case "go":
			o.OwnGoObserved = false
		case "window":
			o.LootWindowObserved = false
		case "removed":
			o.LootSlotsRemoved = 0
		case "push":
			o.OwnLootedItemsObserved = 0
		case "release":
			o.LootReleaseObserved = false
		case "cast":
			o.CastTransportWritten = false
		case "rejected_window":
			o.Status = professionsv1.AttemptStatus_SERVER_REJECTED
			o.FailureCode = 1
		}
		status, _ := d.outcome(s)
		if (status == "CONFIRMED") != (mode == "success" || mode == "cast") {
			t.Fatal(mode, status)
		}
		if mode == "rejected_window" && status != "UNCONFIRMED" {
			t.Fatal("uncertain rejected window cleared pending")
		}
	}
}
