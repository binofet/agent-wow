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
		var sets []*actionsv1.PlayerAuras
		if f.auras != nil {
			sets = []*actionsv1.PlayerAuras{proto.Clone(f.auras).(*actionsv1.PlayerAuras)}
		}
		f.mu.Unlock()
		body, err := anypb.New(&actionsv1.SpellToolkitState{SessionId: "body", SelfGuid: 6, PlayerAuras: sets})
		return &modv1.InvokeModuleResponse{Result: body}, err
	}
	return f.fakeInventory.InvokeModule(ctx, r, opts...)
}

func exactConsumableFixture(t *testing.T, spell uint32) (*Service, *consumableTestSession, *inventoryv1.DrinkRequest) {
	t.Helper()
	s, f := fixtureInventory()
	f.a.SessionId, f.a.SelfGuid = "body", 6
	f.a.Following, f.a.Assisting = true, true
	// Full own CREATE omits zero words but establishes their known-zero baseline.
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
	f.auras.Auras = []*actionsv1.ObservedAura{{SpellId: 430, CasterGuid: 6, Positive: true, Revision: 2, DurationKnown: true, RemainingMs: 30000}}
	view := stateInventory(t, s)
	if !view.LastConsumableUse.AuraObserved || view.LastConsumableUse.InventoryConfirmed {
		t.Fatal("aura alone claimed consumption", view.LastConsumableUse)
	}
	f.w.InventoryBags[0].Slots[0].StackCount--
	f.w.InventoryRevision++
	view = stateInventory(t, s)
	if !view.LastConsumableUse.InventoryConfirmed || view.LastConsumableUse.Phase != "confirmed_consumption_and_aura" || !f.a.Following || !f.a.Assisting || f.leases != 1 || f.releases != 1 {
		t.Fatal("missing actual effects or standing intent changed", view.LastConsumableUse, f.a)
	}
}

func TestExactConsumableRejectsStaleForeignOrUnsafeRequestsWithoutChoosingAnotherItem(t *testing.T) {
	for _, mode := range []string{"foreign_session", "foreign_body", "wrong_item", "wrong_spell", "stale_revision", "slot_changed", "combat", "moving", "channel", "channel_object", "channel_spell_unknown", "channel_object_unknown", "unknown_motion", "on_equip"} {
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
			case "channel":
				f.w.Self.ChannelSpellId = 12051
			case "channel_object":
				f.w.Self.ChannelObjectGuid = 99
			case "channel_spell_unknown":
				f.w.Self.ChannelSpellKnown = false
			case "channel_object_unknown":
				f.w.Self.ChannelObjectKnown = false
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

func TestConsumableMetadataComesFromCompleteOwnedItemQuery(t *testing.T) {
	s, f, _ := exactConsumableFixture(t, 430)
	for _, trigger := range []uint32{0, 1} {
		packet := metadataFixture(60001, 0, 0, 0, 0)
		binary.LittleEndian.PutUint32(packet[8:], 5)
		at := 16 + len("Fixture\x00\x00\x00\x00") + 88 + 8 + 24 + 28 + 12
		binary.LittleEndian.PutUint32(packet[at:], 430)
		binary.LittleEndian.PutUint32(packet[at+4:], trigger)
		if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGItemQuerySingleResponse, Payload: packet}); err != nil {
			t.Fatal(err)
		}
		view := stateInventory(t, s)
		metadata := view.Items[0].GetConsumable()
		if trigger == 0 && (!metadata.GetUsable() || len(metadata.Spells) != 1 || !metadata.Spells[0].DrinkSupported || metadata.Spells[0].ManaPerTick <= 0 || view.GetManaDrinkCount() != 3) {
			t.Fatal("complete on-use spell fact not reflected", view.Items[0])
		}
		if trigger == 1 && metadata.GetUsable() {
			t.Fatal("on-equip spell became a consumable", view.Items[0])
		}
	}
	if len(f.packets) != 0 {
		t.Fatal("metadata projection chose an item use", f.packets)
	}
}

func TestConsumableColdAuraBaselineAndConcurrentInventoryFacts(t *testing.T) {
	for _, mode := range []string{"cold_aura_baseline", "unrelated_arrival", "unrelated_only"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r := exactConsumableFixture(t, 430)
			if mode == "cold_aura_baseline" {
				f.auras = nil
			}
			if _, err := s.Drink(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			f.auras = &actionsv1.PlayerAuras{TargetGuid: 6, Revision: 2, Auras: []*actionsv1.ObservedAura{{SpellId: 430, CasterGuid: 6, Positive: true, Revision: 2}}}
			if mode != "unrelated_only" {
				f.w.InventoryBags[0].Slots[0].StackCount--
			}
			if mode != "cold_aura_baseline" {
				f.w.InventoryBags[0].Slots[8] = &worldv1.BackpackSlot{Slot: 8, ItemGuid: 0x4000000000000009, Entry: 60009, StackCount: 1, MetadataKnown: true}
				s.metadata[60009] = itemMetadata{known: true, entry: 60009}
				s.queried[60009] = true
			}
			f.w.InventoryRevision++
			view := stateInventory(t, s).LastConsumableUse
			if mode == "unrelated_only" {
				if view.InventoryConfirmed {
					t.Fatal("unrelated item change claimed selected consumption", view)
				}
			} else if !view.InventoryConfirmed || !view.AuraObserved || view.Phase != "confirmed_consumption_and_aura" {
				t.Fatal("actual selected consumption/fresh aura could not settle", mode, view)
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
