package buddyinventory

import (
	"context"
	"encoding/binary"
	"os"
	"testing"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestRollCompatibilityPreservesCurrentInventoryAndImportedTradeContracts(t *testing.T) {
	state := (&inventoryv1.State{}).ProtoReflect().Descriptor().Fields()
	for _, expected := range []struct {
		name     protoreflect.Name
		tag      protoreflect.FieldNumber
		kind     protoreflect.Kind
		typeName protoreflect.FullName
	}{
		{"drinking_supported", 21, protoreflect.BoolKind, ""},
		{"last_group_roll", 22, protoreflect.MessageKind, "warcraftbuddy.inventory.v1.GroupRollReceipt"},
		{"food_buff_supported", 23, protoreflect.BoolKind, ""},
		{"buff_food_available", 24, protoreflect.BoolKind, ""},
		{"mana_drink_count", 25, protoreflect.Uint32Kind, ""},
		{"last_consumable_use", 40, protoreflect.MessageKind, "warcraftbuddy.inventory.v1.ConsumableUse"},
		{"consumable_context", 41, protoreflect.MessageKind, "warcraftbuddy.inventory.v1.ConsumableContext"},
		{"gear_context", 42, protoreflect.MessageKind, "warcraftbuddy.inventory.v1.GearContext"},
	} {
		field := state.ByName(expected.name)
		if field == nil || field.Number() != expected.tag || field.Kind() != expected.kind ||
			expected.typeName != "" && field.Message().FullName() != expected.typeName {
			t.Fatal("current Inventory field changed during roll integration", expected, field)
		}
	}
	if !state.ByName("mana_drink_count").HasOptionalKeyword() {
		t.Fatal("current known-zero consumable count lost optional presence")
	}
	rolls := (&inventoryv1.RollState{}).ProtoReflect().Descriptor().Fields()
	metadata := rolls.ByName("metadata")
	if metadata == nil || metadata.Number() != 15 || !metadata.IsList() || metadata.Message().FullName() != "warcraftbuddy.trade.v1.ItemDetails" {
		t.Fatal("roll metadata stopped reusing full Trade item facts", metadata)
	}
	for _, expected := range []struct {
		name protoreflect.Name
		tag  protoreflect.FieldNumber
		kind protoreflect.Kind
	}{{"armor_comparison_known", 30, protoreflect.BoolKind}, {"weapon_comparison_known", 31, protoreflect.BoolKind}, {"raw_template_facts", 32, protoreflect.MessageKind}} {
		field := metadata.Message().Fields().ByName(expected.name)
		if field == nil || field.Number() != expected.tag || field.Kind() != expected.kind {
			t.Fatal("roll imported Trade facts collided with current tags", expected, field)
		}
	}
	raw := metadata.Message().Fields().ByName("raw_template_facts")
	stack := raw.Message().Fields().ByName("max_stack_count")
	if raw.Message().FullName() != "warcraftbuddy.trade.v1.RawTemplateFacts" || !raw.HasPresence() || stack == nil ||
		stack.Number() != 9 || stack.Kind() != protoreflect.Uint32Kind || !stack.HasOptionalKeyword() {
		t.Fatal("roll imported raw stack wire bits lost their presence contract", raw, stack)
	}
	// Both old unknown and newly observed zero must survive nesting in the
	// restored roll feed. Neither is an inferred normalized stack size.
	zero := uint32(0)
	for _, maximum := range []*uint32{nil, &zero} {
		feed := &inventoryv1.RollState{Metadata: []*tradev1.ItemDetails{{ItemId: 99000, Available: true,
			ArmorComparisonKnown: true, WeaponComparisonKnown: true, RawTemplateFacts: &tradev1.RawTemplateFacts{MaxStackCount: maximum}}}}
		body, err := proto.Marshal(feed)
		var roundtrip inventoryv1.RollState
		if err != nil || proto.Unmarshal(body, &roundtrip) != nil || !proto.Equal(feed, &roundtrip) ||
			(roundtrip.Metadata[0].RawTemplateFacts.MaxStackCount == nil) != (maximum == nil) {
			t.Fatal("nested roll metadata changed rich main facts or optional zero", feed, roundtrip, err)
		}
	}
}

func TestRollCompatibilityPreservesAllFourExactRollMessagePrefixes(t *testing.T) {
	type field struct {
		name protoreflect.Name
		kind protoreflect.Kind
	}
	for _, contract := range []struct {
		message protoreflect.MessageDescriptor
		fields  []field
	}{
		{(&inventoryv1.RollOffer{}).ProtoReflect().Descriptor(), []field{
			{"roll_guid", protoreflect.Uint64Kind}, {"revision", protoreflect.Uint64Kind}, {"visit_id", protoreflect.Uint64Kind},
			{"map_id", protoreflect.Uint32Kind}, {"loot_slot", protoreflect.Uint32Kind}, {"item_entry", protoreflect.Uint32Kind},
			{"random_suffix", protoreflect.Uint32Kind}, {"random_property", protoreflect.Uint32Kind}, {"item_count", protoreflect.Uint32Kind},
			{"offered_mask", protoreflect.Uint32Kind}, {"remaining_ms", protoreflect.Uint64Kind}, {"available", protoreflect.BoolKind}, {"phase", protoreflect.StringKind},
		}},
		{(&inventoryv1.RollReceipt{}).ProtoReflect().Descriptor(), []field{
			{"request_id", protoreflect.StringKind}, {"roll_guid", protoreflect.Uint64Kind}, {"offer_revision", protoreflect.Uint64Kind},
			{"visit_id", protoreflect.Uint64Kind}, {"choice", protoreflect.StringKind}, {"transport_attempted", protoreflect.BoolKind},
			{"transport_written", protoreflect.BoolKind}, {"server_vote_observed", protoreflect.BoolKind}, {"phase", protoreflect.StringKind}, {"loot_slot", protoreflect.Uint32Kind},
		}},
		{(&inventoryv1.RollState{}).ProtoReflect().Descriptor(), []field{
			{"session_id", protoreflect.StringKind}, {"self_guid", protoreflect.Uint64Kind}, {"revision", protoreflect.Uint64Kind},
			{"visit_id", protoreflect.Uint64Kind}, {"offers", protoreflect.MessageKind}, {"receipts", protoreflect.MessageKind},
			{"player", protoreflect.MessageKind}, {"equipment_known", protoreflect.BoolKind}, {"equipment_revision", protoreflect.Uint64Kind},
			{"equipment", protoreflect.MessageKind}, {"skills_known", protoreflect.BoolKind}, {"skills_revision", protoreflect.Uint64Kind},
			{"skills_complete", protoreflect.BoolKind}, {"skills", protoreflect.MessageKind}, {"metadata", protoreflect.MessageKind}, {"reason", protoreflect.StringKind},
		}},
		{(&inventoryv1.RollRequest{}).ProtoReflect().Descriptor(), []field{
			{"session_id", protoreflect.StringKind}, {"request_id", protoreflect.StringKind}, {"roll_guid", protoreflect.Uint64Kind},
			{"expected_offer_revision", protoreflect.Uint64Kind}, {"choice", protoreflect.StringKind}, {"visit_id", protoreflect.Uint64Kind}, {"expected_loot_slot", protoreflect.Uint32Kind},
		}},
	} {
		for index, expected := range contract.fields {
			actual := contract.message.Fields().ByNumber(protoreflect.FieldNumber(index + 1))
			if actual == nil || actual.Name() != expected.name || actual.Kind() != expected.kind {
				t.Fatal("restored roll contract changed its exact wire prefix", contract.message.FullName(), index+1, expected, actual)
			}
		}
	}
}

func TestRollCompatibilityPublishedDescriptorsAndAllCurrentRPCsMatchCompiledServices(t *testing.T) {
	body, err := os.ReadFile("module.pb")
	if err != nil {
		t.Fatal(err)
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(body, &set); err != nil {
		t.Fatal(err)
	}
	for _, compiled := range []protoreflect.FileDescriptor{inventoryv1.File_modules_buddy_inventory_inventory_proto, tradev1.File_modules_buddy_trade_trade_proto} {
		var published *descriptorpb.FileDescriptorProto
		for _, file := range set.File {
			if file.GetName() == compiled.Path() {
				published = file
			}
		}
		if published == nil || !proto.Equal(published, protodesc.ToFileDescriptorProto(compiled)) {
			t.Fatal("Inventory published an old or conflicting compiled/imported contract", compiled.Path())
		}
	}
	compiled := inventoryv1.File_modules_buddy_inventory_inventory_proto.Services().ByName("BuddyInventory")
	if compiled == nil || string(compiled.FullName()) != inventoryv1.BuddyInventory_ServiceDesc.ServiceName || compiled.Methods().Len() != len(inventoryv1.BuddyInventory_ServiceDesc.Methods) {
		t.Fatal("Inventory compiled RPC registry differs from its descriptor")
	}
	for _, method := range inventoryv1.BuddyInventory_ServiceDesc.Methods {
		if compiled.Methods().ByName(protoreflect.Name(method.MethodName)) == nil {
			t.Fatal("compiled Inventory RPC absent from descriptor", method.MethodName)
		}
	}
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := registry.Lookup("buddy-inventory")
	if !ok {
		t.Fatal("Inventory manifest unavailable")
	}
	for _, expected := range []struct{ alias, method, input, output string }{
		{"state", "GetState", "google.protobuf.Empty", "warcraftbuddy.inventory.v1.State"},
		{"raw_gear_facts", "RawGearFacts", "google.protobuf.Empty", "warcraftbuddy.inventory.v1.State"},
		{"discover", "Discover", "warcraftbuddy.inventory.v1.DiscoverRequest", "warcraftbuddy.inventory.v1.Discovery"},
		{"move", "Move", "warcraftbuddy.inventory.v1.MoveRequest", "warcraftbuddy.inventory.v1.State"},
		{"equip", "Equip", "warcraftbuddy.inventory.v1.EquipRequest", "warcraftbuddy.inventory.v1.State"},
		{"equip_gear", "EquipGear", "warcraftbuddy.inventory.v1.EquipGearRequest", "warcraftbuddy.inventory.v1.State"},
		{"sell", "Sell", "warcraftbuddy.inventory.v1.SellRequest", "warcraftbuddy.inventory.v1.State"},
		{"drink", "Drink", "warcraftbuddy.inventory.v1.DrinkRequest", "warcraftbuddy.inventory.v1.DrinkReceipt"},
		{"eat_buff_food", "EatBuffFood", "warcraftbuddy.inventory.v1.EatBuffFoodRequest", "warcraftbuddy.inventory.v1.EatBuffFoodReceipt"},
		{"rolls", "Rolls", "google.protobuf.Empty", "warcraftbuddy.inventory.v1.RollState"},
		{"roll", "Roll", "warcraftbuddy.inventory.v1.RollRequest", "warcraftbuddy.inventory.v1.RollState"},
	} {
		method, ok := definition.RPC(expected.alias)
		if !ok || string(method.Descriptor().Name()) != expected.method ||
			string(method.Descriptor().Input().FullName()) != expected.input || string(method.Descriptor().Output().FullName()) != expected.output {
			t.Fatal("restoring rolls changed or removed an Inventory capability", expected, method)
		}
	}
	for alias, effect := range map[string]string{"rolls": moddisc.EffectRead, "roll": moddisc.EffectMutation, "raw_gear_facts": moddisc.EffectRead, "drink": moddisc.EffectMutation, "eat_buff_food": moddisc.EffectMutation} {
		if definition.Manifest().ScriptAPI[alias].Effect != effect {
			t.Fatal("manifest lost combined explicit observational/mutation metadata", alias, effect)
		}
	}
	if definition.Manifest().Packets["SMSG_LOOT_START_ROLL"] != "/warcraftbuddy.inventory.v1.BuddyInventory/OnPacket" ||
		definition.Manifest().Packets["SMSG_LOOT_ROLL"] != "/warcraftbuddy.inventory.v1.BuddyInventory/OnPacket" ||
		definition.Manifest().Packets["SMSG_CAST_FAILED"] != "/warcraftbuddy.inventory.v1.BuddyInventory/OnPacket" ||
		definition.Manifest().Lifecycle.BeforeLogout != "/warcraftbuddy.inventory.v1.BuddyInventory/BeforeLogout" {
		t.Fatal("roll merge lost normal packet or logout routing")
	}
}

// The existing exact consumable and roll fixtures share factual peers without
// entering either native choice policy. Each normal packet retains its parser.
type rollCompatibilitySession struct{ *consumableTestSession }

func (s *rollCompatibilitySession) InvokeModule(ctx context.Context, request *modv1.InvokeModuleRequest, options ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if request.Module == "buddy-trade" && request.Method == "lookup_items" {
		return (&rollTestSession{s.fakeInventory}).InvokeModule(ctx, request, options...)
	}
	return s.consumableTestSession.InvokeModule(ctx, request, options...)
}

func TestRollCompatibilityKeepsConsumableAndVoteReceiptsIndependent(t *testing.T) {
	s, peer, drink := exactConsumableFixture(t, 430)
	s.session = &rollCompatibilitySession{peer}
	peer.w.Self.MapId, peer.w.VisitRevision = 389, 1
	rollTestPacket(t, s, opcode.SMSGLoginVerifyWorld, rollTestEntryPacket())
	vote := addRollTest(t, s, rollTestGUID, 15)
	// An identical caller ID belongs to two distinct typed operation ledgers.
	vote.RequestId = drink.RequestId
	used, err := s.Drink(context.Background(), drink)
	if err != nil || !used.TransportWritten || used.Operation.InventoryConfirmed || used.Operation.AuraObserved {
		t.Fatal("current exact consumable no longer admits or fabricated effects", used, err)
	}
	state, err := s.Roll(context.Background(), vote)
	if err != nil || receiptRollTest(t, state, vote.RequestId).ServerVoteObserved {
		t.Fatal("explicit roll changed consumable admission or fabricated an echo", state, err)
	}
	// The consumable parser still handles its exact failure while a vote is
	// pending. That failure cannot settle the separate roll operation.
	failure := append(binary.LittleEndian.AppendUint32([]byte{s.lastConsumable.count}, drink.SpellId), 47)
	rollTestPacket(t, s, opcode.SMSGCastFailed, failure)
	consumable := s.snapshot().LastConsumableUse
	if consumable == nil || !consumable.ServerRejected || consumable.ServerError != 47 || consumable.RequestId != drink.RequestId ||
		receiptRollTest(t, readRollTest(t, s), vote.RequestId).ServerVoteObserved {
		t.Fatal("normal failure parser was dropped or crossed operation ledgers", consumable)
	}
	rollTestPacket(t, s, opcode.SMSGLootRoll, rollTestEcho(7, rollTestEntry, 17, 23, 0, 0, 0))
	if receiptRollTest(t, readRollTest(t, s), vote.RequestId).ServerVoteObserved {
		t.Fatal("foreign echo settled an own vote")
	}
	rollTestPacket(t, s, opcode.SMSGLootRoll, rollTestEcho(6, rollTestEntry, 17, 23, 0, 0, 0))
	if !receiptRollTest(t, readRollTest(t, s), vote.RequestId).ServerVoteObserved || !proto.Equal(consumable, s.snapshot().LastConsumableUse) {
		t.Fatal("own roll echo lost its receipt or rewrote the consumable result")
	}
	if _, err := s.Drink(context.Background(), drink); err != nil {
		t.Fatal("exact retained consumable receipt unavailable", err)
	}
	if _, err := s.Roll(context.Background(), vote); err != nil {
		t.Fatal("exact retained roll receipt unavailable", err)
	}
	packets := rollTestWrites(peer.fakeInventory)
	if len(packets) != 2 || packets[0].Opcode != opcode.CMSGUseItem || packets[1].Opcode != opcode.CMSGLootRoll {
		t.Fatal("restored capability replayed or displaced another native write", packets)
	}
}

func TestRollCompatibilityLogoutRetainsBothUnconfirmedHistoriesAndRawClosing(t *testing.T) {
	s, peer, drink := exactConsumableFixture(t, 430)
	s.session = &rollCompatibilitySession{peer}
	peer.w.Self.MapId, peer.w.VisitRevision = 389, 1
	rollTestPacket(t, s, opcode.SMSGLoginVerifyWorld, rollTestEntryPacket())
	vote := addRollTest(t, s, rollTestGUID, 15)
	if _, err := s.Drink(context.Background(), drink); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Roll(context.Background(), vote); err != nil {
		t.Fatal(err)
	}
	before := s.snapshot().LastConsumableUse
	if _, err := s.BeforeLogout(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatal(err)
	}
	raw, err := s.RawGearFacts(context.Background(), &emptypb.Empty{})
	if err != nil || !raw.Closing || raw.Operation != nil || raw.LastConsumableUse != nil || !proto.Equal(before, s.snapshot().LastConsumableUse) {
		t.Fatal("logout erased retained consumable evidence or changed raw-facts scope", raw, err)
	}
	roll := receiptRollTest(t, readRollTest(t, s), vote.RequestId)
	if roll.ServerVoteObserved || roll.Phase != "outcome_unconfirmed_no_retry" || !roll.TransportAttempted || !roll.TransportWritten {
		t.Fatal("logout erased or falsely confirmed an uncertain native vote", roll)
	}
	if _, err := s.Roll(context.Background(), vote); err != nil || len(rollTestWrites(peer.fakeInventory)) != 2 {
		t.Fatal("closing replay lost the retained receipt or sent another vote", err)
	}
}
