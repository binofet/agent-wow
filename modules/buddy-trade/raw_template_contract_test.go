package buddytrade

import (
	"os"
	"testing"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	discovery "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestRawTemplateFactsAdvertisedContractMatchesCompiledTrade(t *testing.T) {
	body, err := os.ReadFile("module.pb")
	if err != nil {
		t.Fatal(err)
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(body, &set); err != nil {
		t.Fatal(err)
	}
	compiled := tradev1.File_modules_buddy_trade_trade_proto
	var advertised *descriptorpb.FileDescriptorProto
	for _, file := range set.File {
		if file.GetName() == compiled.Path() {
			advertised = file
		}
	}
	if advertised == nil || !proto.Equal(advertised, protodesc.ToFileDescriptorProto(compiled)) {
		t.Fatal("advertised Trade descriptor differs from the compiled API")
	}
	service := compiled.Services().ByName("BuddyTrade")
	if service == nil || string(service.FullName()) != tradev1.BuddyTrade_ServiceDesc.ServiceName || service.Methods().Len() != len(tradev1.BuddyTrade_ServiceDesc.Methods) {
		t.Fatal("compiled Trade RPC registry differs from its descriptor")
	}
	for _, method := range tradev1.BuddyTrade_ServiceDesc.Methods {
		if service.Methods().ByName(protoreflect.Name(method.MethodName)) == nil {
			t.Fatalf("compiled Trade RPC missing from descriptor: %s", method.MethodName)
		}
	}
	registry, err := discovery.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := registry.Lookup("buddy-trade")
	if !ok {
		t.Fatal("Trade manifest was not loaded")
	}
	for alias := range definition.Manifest().RPC {
		method, ok := definition.RPC(alias)
		if !ok || service.Methods().ByName(method.Descriptor().Name()) == nil {
			t.Fatalf("Trade manifest RPC is not registered: %s", alias)
		}
	}
	catalog, err := registry.Catalog().ForMethod("buddy-trade.lookup_items")
	if err != nil {
		t.Fatal(err)
	}
	item := catalog.Messages[discovery.SchemaKey("buddy-trade", "warcraftbuddy.trade.v1.ItemDetails")]
	for _, field := range item.Fields {
		if field.Name != "rawTemplateFacts" {
			continue
		}
		facts := catalog.Messages[field.Type]
		if field.Number != 32 || field.Kind != "message" || !field.Optional || field.Repeated || field.Map || len(facts.Fields) != 8 {
			t.Fatal("raw template facts lost their reflected presence contract", field, facts)
		}
		for _, raw := range facts.Fields {
			if raw.Kind != "uint32" || raw.Repeated || raw.Map {
				t.Fatal("raw template facts must remain scalar wire values", raw)
			}
		}
		return
	}
	t.Fatal("lookup_items does not advertise raw template facts")
}
