package buddyinventory

import (
	"context"
	"encoding/binary"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

type consumableTestSession struct {
	*fakeInventory
	auras *actionsv1.PlayerAuras
}

func (f *consumableTestSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "buddy-actions" && r.Method == "spell_toolkit" {
		f.mu.Lock()
		snapshot := proto.Clone(f.auras).(*actionsv1.PlayerAuras)
		f.mu.Unlock()
		body, err := anypb.New(&actionsv1.SpellToolkitState{SessionId: "body", SelfGuid: 6, PlayerAuras: []*actionsv1.PlayerAuras{snapshot}})
		return &modv1.InvokeModuleResponse{Result: body}, err
	}
	return f.fakeInventory.InvokeModule(ctx, r, opts...)
}

func exactConsumableFixture(t *testing.T, spell uint32) (*Service, *consumableTestSession, *inventoryv1.DrinkRequest) {
	t.Helper()
	s, f := fixtureInventory()
	f.a.SessionId, f.a.SelfGuid = "body", 6
	f.a.Following, f.a.Healing = true, true
	f.w.Self.LevelKnown, f.w.Self.Level = true, 30
	f.w.Self.PowerKnown, f.w.Self.Power, f.w.Self.MaxPower = true, 5, 150
	item := f.w.InventoryBags[0].Slots[0]
	item.Entry = 60001
	s.metadata[item.Entry] = itemMetadata{known: true, entry: item.Entry, class: 0, subclass: 5, onUseSpells: [5]uint32{spell}}
	s.queried[item.Entry] = true
	peer := &consumableTestSession{fakeInventory: f, auras: &actionsv1.PlayerAuras{TargetGuid: 6, Complete: true, Revision: 1}}
	s.session = peer
	return s, peer, &inventoryv1.DrinkRequest{SessionId: "body", SelfGuid: 6, Source: ref(item), SpellId: spell, ExpectedInventoryRevision: 1, RequestId: "chosen_drink"}
}

func TestExactConsumableUsesChosenOwnedItemAndRequiresConsumptionPlusFreshAura(t *testing.T) {
	s, f, r := exactConsumableFixture(t, 430)
	receipt, err := s.Drink(context.Background(), r)
	if err != nil || !receipt.TransportWritten || receipt.Operation.InventoryConfirmed || receipt.Operation.AuraObserved || len(f.packets) != 1 {
		t.Fatal(receipt, err, f.packets)
	}
	p := f.packets[0]
	if p.Opcode != opcode.CMSGUseItem || len(p.Payload) != 24 || p.Payload[0] != 255 || p.Payload[1] != 23 || binary.LittleEndian.Uint32(p.Payload[3:]) != 430 || binary.LittleEndian.Uint64(p.Payload[7:]) != junkGUID {
		t.Fatal("wrong exact item-use packet", p)
	}
	for _, v := range p.Payload[15:] {
		if v != 0 {
			t.Fatal("invented item target or cast flags", p)
		}
	}
	if again, err := s.Drink(context.Background(), r); err != nil || !again.TransportWritten || len(f.packets) != 1 {
		t.Fatal("request ID replayed item use", again, err)
	}
	f.auras.Revision = 2
	f.auras.Auras = []*actionsv1.ObservedAura{{SpellId: 430, Positive: true, Revision: 2, DurationKnown: true, RemainingMs: 30000}}
	view := stateInventory(t, s)
	if !view.LastConsumableUse.AuraObserved || view.LastConsumableUse.InventoryConfirmed {
		t.Fatal("aura alone claimed consumption", view.LastConsumableUse)
	}
	f.w.InventoryBags[0].Slots[0].StackCount--
	f.w.InventoryRevision++
	view = stateInventory(t, s)
	if !view.LastConsumableUse.InventoryConfirmed || view.LastConsumableUse.Phase != "confirmed_consumption_and_aura" || !f.a.Following || !f.a.Healing || f.leases != 1 || f.releases != 1 {
		t.Fatal("missing actual effects or standing intent changed", view.LastConsumableUse, f.a)
	}
}

func TestExactConsumableRejectsStaleForeignOrUnsafeRequestsWithoutChoosingAnotherItem(t *testing.T) {
	for _, mode := range []string{"foreign_session", "foreign_body", "wrong_item", "wrong_spell", "stale_revision", "slot_changed", "combat", "moving", "unknown_motion", "on_equip"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r := exactConsumableFixture(t, 430)
			switch mode {
			case "foreign_session":
				r.SessionId = "other"
			case "foreign_body":
				r.SelfGuid = 9
			case "wrong_item":
				r.Source.ItemGuid++
			case "wrong_spell":
				r.SpellId = 431
			case "stale_revision":
				r.ExpectedInventoryRevision++
			case "slot_changed":
				f.afterLease = func() { f.w.InventoryBags[0].Slots[0].StackCount-- }
			case "combat":
				f.w.Self.UnitFlags |= 0x80000
			case "moving":
				f.w.Self.MovementFlags = 1
			case "unknown_motion":
				f.a.MotionUnconfirmed = true
			case "on_equip":
				m := s.metadata[60001]
				m.onUseSpells = [5]uint32{}
				s.metadata[60001] = m
			}
			if _, err := s.Drink(context.Background(), r); err == nil || len(f.packets) != 0 {
				t.Fatal("invalid source used another consumable", mode, err, f.packets)
			}
		})
	}
}

func TestConsumableUnknownWriteNeverReplaysAndFoodLocationRemainsLuaPolicy(t *testing.T) {
	s, f, r := exactConsumableFixture(t, 430)
	f.sendError = true
	first, err := s.Drink(context.Background(), r)
	if err != nil || first.TransportWritten || !first.Operation.TransportAttempted {
		t.Fatal(first, err)
	}
	if _, err = s.Drink(context.Background(), r); err != nil || len(f.packets) != 1 {
		t.Fatal("ambiguous item use replayed", err, f.packets)
	}
	s, f, r = exactConsumableFixture(t, 5004)
	// An exact legal food request does not require a dungeon, stationary owner,
	// missing mana or absence of another food buff. Those are Lua choices.
	f.w.Self.MapId = 0
	f.w.Owner = &worldv1.PlayerState{Guid: 3, Present: true, MovementFlagsKnown: true, MovementFlags: 1, UnitFlagsKnown: true, UnitFlags: 0x80000}
	f.auras.Auras = []*actionsv1.ObservedAura{{SpellId: 19706, Positive: true, Revision: 1}}
	food, err := s.EatBuffFood(context.Background(), &inventoryv1.EatBuffFoodRequest{SessionId: r.SessionId, SelfGuid: r.SelfGuid, Source: r.Source, SpellId: r.SpellId, ExpectedInventoryRevision: r.ExpectedInventoryRevision, RequestId: "chosen_food"})
	if err != nil || !food.TransportWritten || food.FoodAuraSpellId != 19705 || food.TimeUntilBuffMs != 10000 || len(f.packets) != 1 {
		t.Fatal("native code retained food location/refresh policy", food, err)
	}
}
