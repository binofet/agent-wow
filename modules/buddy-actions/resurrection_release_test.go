package buddyactions

import (
	"context"
	"net"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestTargetedResurrectionReleaseContract(t *testing.T) {
	// Load the same checked-in manifest and descriptor the client loads.
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	actions, ok := registry.Lookup("buddy-actions")
	if !ok {
		t.Fatal("buddy-actions is missing from the module registry")
	}

	svc, game, _ := resRig(t)
	svc.sessionID = "actions-test-body"
	game.world.Self.RaceKnown, game.world.Self.RaceId = true, 1
	game.world.Owner.RaceKnown, game.world.Owner.RaceId = true, 1
	learnSpells(svc, 2006)

	listener := bufconn.Listen(64 << 10)
	server := grpc.NewServer()
	actionsv1.RegisterBuddyActionsServer(server, svc)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///buddy-actions",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	invoke := func(alias, request string) *dynamicpb.Message {
		t.Helper()
		method, ok := actions.RPC(alias)
		if !ok {
			t.Fatalf("buddy-actions.%s is missing from module.yaml", alias)
		}
		desc := method.Descriptor()
		input := dynamicpb.NewMessage(desc.Input())
		if err := protojson.Unmarshal([]byte(request), input); err != nil {
			t.Fatalf("encode %s with the release descriptor: %v", alias, err)
		}
		output := dynamicpb.NewMessage(desc.Output())
		if err := conn.Invoke(ctx, method.Path(), input, output); err != nil {
			t.Fatalf("invoke buddy-actions.%s with the release descriptor: %v", alias, err)
		}
		return output
	}

	toolkit := invoke("spell_toolkit", "{}")
	field := toolkit.Descriptor().Fields().ByJSONName("targetedResurrectionSupported")
	if field == nil || !toolkit.Get(field).Bool() {
		t.Fatal("the release descriptor must expose the service's targeted Resurrection capability")
	}
	invoke("resurrect_once", `{"sessionId":"actions-test-body","selfGuid":"1","targetGuid":"3"}`)
	if got := actions.Manifest().ScriptAPI["resurrect_once"].Effect; got != moddisc.EffectMutation {
		t.Fatalf("resurrect_once script effect = %q, want mutation", got)
	}

	game.mu.Lock()
	defer game.mu.Unlock()
	if countPackets(game, opcode.CMSGCastSpell) != 1 || lastCast(game) != 2006 {
		t.Fatal("registered Resurrection did not dispatch exactly one priest Resurrection cast")
	}
}
