package buddyactions

import (
	"context"
	"net"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	buddyworld "github.com/agent-wow/agent-wow/modules/buddy-world"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestFlightpathTransferPublishedRoutesReachCompiledGuards(t *testing.T) {
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	s, _, _ := rig(t)
	listener := bufconn.Listen(64 << 10)
	server := grpc.NewServer()
	actionsv1.RegisterBuddyActionsServer(server, s)
	worldv1.RegisterBuddyWorldServer(server, buddyworld.NewIndependentService(1))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///flight-transfer-contract", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, route := range []struct {
		module, alias, path string
		request             proto.Message
	}{
		{"buddy-world", "adopt_spline_transfer", worldv1.BuddyWorld_AdoptSplineTransfer_FullMethodName, &worldv1.AdoptSplineTransferRequest{}},
		{"buddy-actions", "adopt_flightpath_transfer", actionsv1.BuddyActions_AdoptFlightpathTransfer_FullMethodName, &actionsv1.AdoptFlightpathTransferRequest{}},
	} {
		t.Run(route.module, func(t *testing.T) {
			module, ok := registry.Lookup(route.module)
			if !ok {
				t.Fatal("missing installed module contract")
			}
			method, ok := module.RPC(route.alias)
			if !ok || method.Path() != route.path || module.Manifest().ScriptAPI[route.alias].Effect != moddisc.EffectMutation ||
				method.Descriptor().Input().FullName() != route.request.ProtoReflect().Descriptor().FullName() {
				t.Fatal("published adoption alias/type/effect disagrees with compiled API")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := conn.Invoke(ctx, method.Path(), route.request, dynamicpb.NewMessage(method.Descriptor().Output()))
			if status.Code(err) != codes.InvalidArgument {
				t.Fatal("public route did not reach its compiled native exact-binding guard", err)
			}
		})
	}
}
