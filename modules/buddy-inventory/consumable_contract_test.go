package buddyinventory

import (
	"context"
	"net"
	"testing"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestConsumableContractsPreserveDeployedInventoryABI(t *testing.T) {
	// The 2026-11-21 live descriptor predates public main. Preserve its exact
	// State21..25 and request/receipt prefixes, including the legacy type name.
	state := (&inventoryv1.State{}).ProtoReflect().Descriptor().Fields()
	for _, row := range []struct {
		name protoreflect.Name
		tag  protoreflect.FieldNumber
		kind protoreflect.Kind
	}{{"drinking_supported", 21, protoreflect.BoolKind}, {"last_group_roll", 22, protoreflect.MessageKind}, {"food_buff_supported", 23, protoreflect.BoolKind}, {"buff_food_available", 24, protoreflect.BoolKind}, {"mana_drink_count", 25, protoreflect.Uint32Kind}} {
		f := state.ByName(row.name)
		if f == nil || f.Number() != row.tag || f.Kind() != row.kind {
			t.Fatal("deployed State field changed", row, f)
		}
	}
	if !state.ByName("mana_drink_count").HasOptionalKeyword() || state.ByName("last_group_roll").Message().FullName() != "warcraftbuddy.inventory.v1.GroupRollReceipt" {
		t.Fatal("deployed optional count/roll receipt changed")
	}
	for _, message := range []protoreflect.MessageDescriptor{(&inventoryv1.DrinkRequest{}).ProtoReflect().Descriptor(), (&inventoryv1.EatBuffFoodRequest{}).ProtoReflect().Descriptor()} {
		if f := message.Fields().ByNumber(1); f.Name() != "session_id" || f.Kind() != protoreflect.StringKind {
			t.Fatal(message.FullName(), f)
		}
		if f := message.Fields().ByNumber(2); f.Name() != "self_guid" || f.Kind() != protoreflect.Uint64Kind {
			t.Fatal(message.FullName(), f)
		}
	}
	for _, row := range []struct {
		message protoreflect.MessageDescriptor
		names   []protoreflect.Name
	}{
		{(&inventoryv1.DrinkReceipt{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "self_guid", "item_guid", "item_entry", "spell_id", "stack_count_before", "transport_written", "reason", "baseline_mana", "baseline_inventory_revision"}},
		{(&inventoryv1.EatBuffFoodReceipt{}).ProtoReflect().Descriptor(), []protoreflect.Name{"session_id", "self_guid", "item_entry", "spell_id", "food_aura_spell_id", "transport_written", "time_until_buff_ms"}},
		{(&inventoryv1.GroupRollReceipt{}).ProtoReflect().Descriptor(), []protoreflect.Name{"roll_guid", "item_entry", "loot_slot", "offered_mask", "choice", "phase", "reason", "transport_written", "server_vote_observed"}},
	} {
		for index, name := range row.names {
			f := row.message.Fields().ByNumber(protoreflect.FieldNumber(index + 1))
			if f == nil || f.Name() != name {
				t.Fatal("deployed receipt prefix changed", row.message.FullName(), index, name, f)
			}
			kind := protoreflect.Uint32Kind
			switch name {
			case "session_id", "reason", "choice", "phase":
				kind = protoreflect.StringKind
			case "self_guid", "item_guid", "baseline_inventory_revision", "roll_guid":
				kind = protoreflect.Uint64Kind
			case "transport_written", "server_vote_observed":
				kind = protoreflect.BoolKind
			}
			if f.Kind() != kind {
				t.Fatal("deployed receipt wire kind changed", row.message.FullName(), name, f.Kind(), kind)
			}
		}
	}
}

func TestConsumablePublishedDescriptorsRouteToCompiledExactPrimitives(t *testing.T) {
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := registry.Lookup("buddy-inventory")
	s, f, _ := exactConsumableFixture(t, 430)
	listener := bufconn.Listen(64 << 10)
	server := grpc.NewServer()
	inventoryv1.RegisterBuddyInventoryServer(server, s)
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpc.NewClient("passthrough:///consumables", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, alias := range []string{"drink", "eat_buff_food"} {
		method, ok := definition.RPC(alias)
		if !ok || definition.Manifest().ScriptAPI[alias].Effect != moddisc.EffectMutation {
			t.Fatal("missing reflected consumable method", alias)
		}
		input, output := dynamicpb.NewMessage(method.Descriptor().Input()), dynamicpb.NewMessage(method.Descriptor().Output())
		input.Set(input.Descriptor().Fields().ByName("session_id"), protoreflect.ValueOfString("body"))
		input.Set(input.Descriptor().Fields().ByName("self_guid"), protoreflect.ValueOfUint64(6))
		err = conn.Invoke(context.Background(), method.Path(), input, output)
		if status.Code(err) != codes.FailedPrecondition || len(f.packets) != 0 {
			t.Fatal("descriptor did not reach exact primitive/no-selector precondition", alias, err)
		}
	}
}
