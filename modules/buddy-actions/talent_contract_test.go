package buddyactions

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestTalentReleaseRoutesMatchCompiledServiceAndKeepLegacyAliases(t *testing.T) {
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	module, found := registry.Lookup("buddy-actions")
	if !found {
		t.Fatal("Actions registry missing")
	}
	s, game := talentRig(t)
	observeTalentPacket(t, s, 31, 0, talentSpec{})
	listener := bufconn.Listen(64 << 10)
	server := grpc.NewServer()
	actionsv1.RegisterBuddyActionsServer(server, s)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///talent-service", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	invoke := func(alias string, request []byte) *dynamicpb.Message {
		t.Helper()
		method, ok := module.RPC(alias)
		if !ok {
			t.Fatal("missing release alias", alias)
		}
		input, output := dynamicpb.NewMessage(method.Descriptor().Input()), dynamicpb.NewMessage(method.Descriptor().Output())
		if err := protojson.Unmarshal(request, input); err != nil {
			t.Fatal(err)
		}
		if err := conn.Invoke(ctx, method.Path(), input, output); err != nil {
			t.Fatal(alias, err)
		}
		return output
	}
	view := invoke("talent_state", []byte(`{"sessionId":"actions-test-body","selfGuid":"1"}`))
	if !view.Get(view.Descriptor().Fields().ByName("known")).Bool() || view.Get(view.Descriptor().Fields().ByName("free_points")).Uint() != 31 {
		t.Fatal("compiled raw observation disagrees with descriptor", view)
	}
	raw, err := protojson.Marshal(talentRequest(s, readTalent(t, s), "descriptor-test"))
	if err != nil {
		t.Fatal(err)
	}
	invoke("learn_talent", raw)
	if module.Manifest().ScriptAPI["talent_state"].Effect != moddisc.EffectRead || module.Manifest().ScriptAPI["learn_talent"].Effect != moddisc.EffectMutation {
		t.Fatal("wrong reflected effects")
	}
	for _, legacy := range []string{"resurrect_once", "cast_heal", "cast_spell", "cancel_spell", "activate_flightpath"} {
		if _, ok := module.RPC(legacy); !ok {
			t.Fatal("unrelated route removed", legacy)
		}
	}
	if len(game.packets) != 1 || game.packets[0].Opcode != opcode.CMSGLearnTalent {
		t.Fatal("release route did not execute exactly one ordinary request", game.packets)
	}
}

func TestTalentChoiceChangesInSameLuaVMWithoutNativeBuildOrDefaultPlan(t *testing.T) {
	s, game := talentRig(t)
	observeTalentPacket(t, s, 31, 0, talentSpec{})
	// This is the existing generic task-runner DTO. The Lua author selects a
	// build; the harness only validates/executes the selected normal request.
	source := `local chosen=123
	return {on_event=function()return true end,
	export_state=function()return {version=1,data={}} end,
	import_state=function(saved)return saved.version==1 end,
	decide=function(frame)
 local t=frame.input
 if t.known~=true then return {phase="waiting_for_talent_facts"} end
 return {action={id="learn:"..chosen,method="buddy-actions.learn_talent",params={
  sessionId=t.sessionId,selfGuid=t.selfGuid,expectedRevision=t.revision,
  specIndex=t.activeSpecIndex or 0,talentId=chosen,rankIndex=0,requestId="lua:"..chosen}}}
end}`
	vm, err := behavior.NewSession(context.Background(), s.sessionID, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vm.Close)
	choose := func() {
		t.Helper()
		raw, err := protojson.Marshal(readTalent(t, s))
		if err != nil {
			t.Fatal(err)
		}
		var facts map[string]any
		if err := json.Unmarshal(raw, &facts); err != nil {
			t.Fatal(err)
		}
		decision, err := vm.Decide(context.Background(), facts)
		if err != nil {
			t.Fatal(err)
		}
		action := decision.Output["action"].(map[string]any)
		if action["method"] != "buddy-actions.learn_talent" {
			t.Fatal(decision.Output)
		}
		requestJSON, err := json.Marshal(action["params"])
		if err != nil {
			t.Fatal(err)
		}
		request := &actionsv1.LearnTalentRequest{}
		if err := protojson.Unmarshal(requestJSON, request); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LearnTalent(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	choose()
	if len(game.packets) != 1 || binary.LittleEndian.Uint32(game.packets[0].Payload) != 123 {
		t.Fatal("first Lua choice was replaced", game.packets)
	}
	observeTalentPacket(t, s, 30, 0, talentSpec{ranks: []talentRank{{123, 0}}})
	before := vm.Status()
	if _, err := vm.Reload(context.Background(), strings.Replace(source, "chosen=123", "chosen=456", 1), before.Revision); err != nil {
		t.Fatal(err)
	}
	choose()
	if vm.Status().SessionID != before.SessionID || vm.Status().Revision == before.Revision ||
		len(game.packets) != 2 || binary.LittleEndian.Uint32(game.packets[1].Payload) != 456 {
		t.Fatal("same-session Lua edit did not select the second exact talent", vm.Status(), game.packets)
	}
}
