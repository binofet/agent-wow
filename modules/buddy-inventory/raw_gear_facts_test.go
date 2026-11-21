package buddyinventory

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type rawGearPeerTrap struct{ calls int }

func (f *rawGearPeerTrap) SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error) {
	f.calls++
	return nil, errors.New("raw gear read must not send a packet")
}

func (f *rawGearPeerTrap) InvokeModule(context.Context, *modv1.InvokeModuleRequest, ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	f.calls++
	return nil, errors.New("raw gear read must not invoke a dependency")
}

func rawGearFactsFixture() (*Service, *rawGearPeerTrap, *inventoryv1.State) {
	peer := &rawGearPeerTrap{}
	s := NewService(peer, "raw-body", 6)
	bag := &worldv1.InventoryBag{Bag: 0, SlotKnown: true, ContentsKnown: true, SlotCount: 16}
	for slot := uint32(0); slot < 16; slot++ {
		bag.Slots = append(bag.Slots, &worldv1.BackpackSlot{Slot: slot})
	}
	bag.Slots[1] = &worldv1.BackpackSlot{Slot: 1, ItemGuid: 0x4000000000000100, Entry: 5000, StackCount: 1,
		MetadataKnown: true, EnhancementsKnown: true, DurabilityKnown: true, Durability: 25, MaxDurability: 30}
	bags := []*worldv1.InventoryBag{bag}
	for number := uint32(1); number <= 4; number++ {
		bags = append(bags, &worldv1.InventoryBag{Bag: number, SlotKnown: true, ContentsKnown: true})
	}
	equipment := make([]*worldv1.EquipmentSlotState, 19)
	for slot := range equipment {
		equipment[slot] = &worldv1.EquipmentSlotState{Slot: uint32(slot), SlotKnown: true}
	}
	equipment[4] = &worldv1.EquipmentSlotState{Slot: 4, SlotKnown: true, ItemGuid: 0x4000000000000101,
		Entry: 5001, StackCount: 1, MetadataKnown: true, EnhancementsKnown: true,
		DurabilityKnown: true, Durability: 30, MaxDurability: 30}
	item := &inventoryv1.ItemEligibility{Item: &inventoryv1.ItemRef{Slot: 1, ItemGuid: bag.Slots[1].ItemGuid, Entry: 5000, StackCount: 1},
		MetadataKnown: true, SafeJunkSaleEligible: true, UnitSellPriceCopper: 50, NormalBag: true, ContainerSlots: 6,
		BlockedReason: "not a raw fact", Name: "policy-facing item", GearEquipEligible: true, GearEquipmentSlot: 4,
		ExpectedEquippedItemGuid: equipment[4].ItemGuid, ConservativeUpgrade: true, GearBlockedReason: "comparison result",
		Consumable: &inventoryv1.ConsumableMetadata{}, GearFacts: &inventoryv1.GearItemFacts{
			EnhancementsKnown: true, DurabilityKnown: true, Durability: 25, MaxDurability: 30,
			Destinations: []*inventoryv1.GearDestination{{EquipmentSlot: 4, ExpectedEquippedItemGuid: equipment[4].ItemGuid,
				Eligible: false, BlockedReason: "observed physical destination is blocked"}}}}
	gear := &inventoryv1.GearContext{ClassKnown: true, ClassId: 5, RaceKnown: true, RaceId: 1,
		LevelKnown: true, Level: 10, PolicyRevision: "must not escape", PolicyError: "must not escape",
		Templates: map[string]*inventoryv1.GearTemplate{"5001": {
			ItemId: 5001, Available: true, Name: "observed robe", ItemClass: 4, Subclass: 1, InventoryType: 20,
			Armor: 20, ArmorComparisonKnown: true, Stats: []*inventoryv1.GearStat{{Type: 5, Value: -1}},
			Damages: []*inventoryv1.GearDamage{{Minimum: 1, Maximum: 2}}, Resistances: []uint32{0, 1, 0, 0, 0, 0}}}}
	drinks := uint32(3)
	s.state = &inventoryv1.State{SessionId: "raw-body", SelfGuid: 6, Revision: 50,
		InventoryKnown: true, InventoryRevision: 20, InventoryBags: bags,
		EquipmentKnown: true, EquipmentRevision: 21, Equipment: equipment, Items: []*inventoryv1.ItemEligibility{item}, GearContext: gear,
		CopperKnown: true, Copper: 123, CopperRevision: 7, Active: true, Operation: &inventoryv1.Operation{RequestId: "pending"},
		BlockedReason: "operation guard", TransportWrites: 99, MalformedPackets: 2, Limitation: "operation policy",
		DrinkingSupported: true, LastGroupRoll: &inventoryv1.GroupRollReceipt{}, FoodBuffSupported: true,
		BuffFoodAvailable: true, ManaDrinkCount: &drinks, LastConsumableUse: &inventoryv1.ConsumableUse{}, ConsumableContext: &inventoryv1.ConsumableContext{}}
	s.known = true
	s.now = func() time.Time { panic("cached raw read must not refresh or run a clock") }
	expected := &inventoryv1.State{SessionId: "raw-body", SelfGuid: 6, Revision: 50,
		InventoryKnown: true, InventoryRevision: 20, InventoryBags: bags,
		EquipmentKnown: true, EquipmentRevision: 21, Equipment: equipment,
		Items:       []*inventoryv1.ItemEligibility{{Item: item.Item, MetadataKnown: true, GearFacts: item.GearFacts}},
		GearContext: &inventoryv1.GearContext{ClassKnown: true, ClassId: 5, RaceKnown: true, RaceId: 1, LevelKnown: true, Level: 10, Templates: gear.Templates}}
	return s, peer, proto.Clone(expected).(*inventoryv1.State)
}

func TestRawGearFactsReturnsOnlyCachedFactualProjectionWithoutPeers(t *testing.T) {
	s, peer, expected := rawGearFactsFixture()
	before := proto.Clone(s.state)
	actual, err := s.RawGearFacts(context.Background(), &emptypb.Empty{})
	if err != nil || !proto.Equal(actual, expected) || peer.calls != 0 || !proto.Equal(s.state, before) {
		t.Fatal("raw read changed facts, exposed policy fields, refreshed, or called a peer", actual, err, peer.calls)
	}
	if actual.Items[0].GearFacts.Destinations[0].Eligible || actual.Items[0].GearFacts.Destinations[0].BlockedReason == "" {
		t.Fatal("blocked physical destination was filtered or converted to permission")
	}
}

func TestRawGearFactsDeepCopiesEveryNestedFactualFrame(t *testing.T) {
	s, peer, expected := rawGearFactsFixture()
	before := proto.Clone(s.state)
	actual, err := s.RawGearFacts(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	actual.InventoryBags[0].Slots[1].Entry++
	actual.InventoryBags[1].SlotKnown = false
	actual.Equipment[4].Durability--
	actual.Items[0].Item.Entry++
	actual.Items[0].GearFacts.Durability--
	actual.Items[0].GearFacts.Destinations[0].ExpectedEquippedItemGuid++
	actual.GearContext.Templates["5001"].Stats[0].Value--
	actual.GearContext.Templates["5001"].Damages[0].Maximum++
	actual.GearContext.Templates["5001"].Resistances[1]++
	delete(actual.GearContext.Templates, "5001")
	if !proto.Equal(s.state, before) {
		t.Fatal("caller mutation reached raw cache")
	}
	again, err := s.RawGearFacts(context.Background(), &emptypb.Empty{})
	if err != nil || !proto.Equal(again, expected) {
		t.Fatal("independent read inherited caller mutation", again, err)
	}
	s.state.InventoryBags[0].Slots[1].Entry++
	s.state.Equipment[4].Entry++
	s.state.Items[0].GearFacts.Destinations[0].BlockedReason = "later observation"
	s.state.GearContext.Templates["5001"].Stats[0].Value++
	if !proto.Equal(again, expected) || peer.calls != 0 {
		t.Fatal("later cache update reached detached raw result")
	}
}

func TestRawGearFactsPreservesColdFailedAndClosingCacheKnowledge(t *testing.T) {
	peer := &rawGearPeerTrap{}
	cold := NewService(peer, "cold-body", 6)
	actual, err := cold.RawGearFacts(context.Background(), &emptypb.Empty{})
	expected := &inventoryv1.State{SessionId: "cold-body", SelfGuid: 6, Revision: 1}
	if err != nil || !proto.Equal(actual, expected) || peer.calls != 0 {
		t.Fatal("cold cache invented observations", actual, err)
	}
	s, peer, expected := rawGearFactsFixture()
	// A failed observe retains old arrays but explicitly revokes known flags.
	s.known = false
	s.state.InventoryKnown, s.state.EquipmentKnown = false, false
	s.state.BlockedReason = "coherent_current_observations_unavailable"
	s.state.Closing = true
	expected.InventoryKnown, expected.EquipmentKnown, expected.Closing = false, false, true
	actual, err = s.RawGearFacts(context.Background(), &emptypb.Empty{})
	if err != nil || !proto.Equal(actual, expected) || peer.calls != 0 {
		t.Fatal("raw read healed unknown cache or hid closing", actual, err)
	}
}

func TestRawGearFactsDoesNotHealAnActualFailedObservation(t *testing.T) {
	s, peer, _ := rawGearFactsFixture()
	if err := s.observe(context.Background(), nil); err == nil || peer.calls != 1 {
		t.Fatal("fixture did not actually fail its World dependency", err, peer.calls)
	}
	before := proto.Clone(s.state)
	actual, err := s.RawGearFacts(context.Background(), &emptypb.Empty{})
	if err != nil || peer.calls != 1 || actual.InventoryKnown || actual.EquipmentKnown ||
		actual.InventoryRevision != 20 || actual.EquipmentRevision != 21 || len(actual.InventoryBags) != 5 ||
		len(actual.Equipment) != 19 || len(actual.Items) != 0 || actual.GearContext == nil ||
		len(actual.GearContext.Templates) != 0 || !proto.Equal(s.state, before) {
		t.Fatal("raw read refreshed or healed a failed observation", actual, err, peer.calls)
	}
}

func TestRawGearFactsDoesNotTakeObservationOrSendLocks(t *testing.T) {
	s, peer, expected := rawGearFactsFixture()
	s.observeMu.Lock()
	s.sendMu.Lock()
	done := make(chan *inventoryv1.State, 1)
	go func() {
		result, _ := s.RawGearFacts(context.Background(), &emptypb.Empty{})
		done <- result
	}()
	var result *inventoryv1.State
	select {
	case result = <-done:
	case <-time.After(2 * time.Second):
	}
	s.sendMu.Unlock()
	s.observeMu.Unlock()
	if !proto.Equal(result, expected) || peer.calls != 0 {
		t.Fatal("cached read waited on observation/write locks or called peers", result)
	}
}

func TestRawGearFactsPublishedReadContractMatchesCompiledService(t *testing.T) {
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := registry.Lookup("buddy-inventory")
	if !ok {
		t.Fatal("inventory contract unavailable")
	}
	method, ok := definition.RPC("raw_gear_facts")
	if !ok || method.Path() != "/warcraftbuddy.inventory.v1.BuddyInventory/RawGearFacts" ||
		definition.Manifest().ScriptAPI["raw_gear_facts"].Effect != moddisc.EffectRead {
		t.Fatal("raw facts method is not a reflected read")
	}
	compiled := inventoryv1.File_modules_buddy_inventory_inventory_proto
	if !proto.Equal(protodesc.ToFileDescriptorProto(method.Descriptor().ParentFile()), protodesc.ToFileDescriptorProto(compiled)) ||
		method.Descriptor().Input().FullName() != "google.protobuf.Empty" || method.Descriptor().Output().FullName() != "warcraftbuddy.inventory.v1.State" {
		t.Fatal("published descriptor differs from compiled unchanged message contract")
	}
	for _, alias := range []string{"state", "discover", "move", "equip", "equip_gear", "sell", "drink", "eat_buff_food"} {
		if _, ok := definition.RPC(alias); !ok {
			t.Fatal("existing inventory alias removed", alias)
		}
	}
	s, peer, expected := rawGearFactsFixture()
	listener := bufconn.Listen(64 << 10)
	server := grpc.NewServer()
	inventoryv1.RegisterBuddyInventoryServer(server, s)
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpc.NewClient("passthrough:///raw-gear-facts", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	input := dynamicpb.NewMessage(method.Descriptor().Input())
	output := dynamicpb.NewMessage(method.Descriptor().Output())
	if err = conn.Invoke(context.Background(), method.Path(), input, output); err != nil {
		t.Fatal("reflected route did not reach compiled cached read", err)
	}
	encoded, err := proto.Marshal(output)
	actual := &inventoryv1.State{}
	if err != nil || proto.Unmarshal(encoded, actual) != nil || !proto.Equal(actual, expected) || peer.calls != 0 {
		t.Fatal("reflected route returned different facts or queried peers", actual, err, peer.calls)
	}
}
