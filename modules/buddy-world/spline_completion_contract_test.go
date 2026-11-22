package buddyworld

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestTransportScopePublishedWithEverySessionImport(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "*", "module.pb"))
	if err != nil || len(paths) == 0 {
		t.Fatal("missing production descriptor cohort", paths, err)
	}
	paths = append(paths, filepath.Join("..", "..", "internal", "modulefixture", "fixture.pb"))
	want := protodesc.ToFileDescriptorProto(modv1.File_api_module_v1_session_proto)
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			set := new(descriptorpb.FileDescriptorSet)
			if err := proto.Unmarshal(body, set); err != nil {
				t.Fatal(err)
			}
			files, err := protodesc.NewFiles(set)
			if err != nil {
				t.Fatal("incomplete published descriptor closure", err)
			}
			common, err := files.FindFileByPath(want.GetName())
			if err != nil || !proto.Equal(protodesc.ToFileDescriptorProto(common), want) {
				t.Fatal("published Session import differs from compiled callback API", err)
			}
		})
	}
}

func TestCompleteSplinePublishedAliasRoutesCompiledService(t *testing.T) {
	registry, err := moddisc.Load("..")
	if err != nil {
		t.Fatal(err)
	}
	module, ok := registry.Lookup("buddy-world")
	if !ok {
		t.Fatal("missing buddy-world")
	}
	method, ok := module.RPC("complete_spline")
	if !ok || method.Path() != worldv1.BuddyWorld_CompleteSpline_FullMethodName ||
		method.Descriptor().Input().FullName() != "warcraftbuddy.world.v1.CompleteSplineRequest" ||
		method.Descriptor().Output().FullName() != "warcraftbuddy.world.v1.SplineCompletionObservation" ||
		module.Manifest().ScriptAPI["complete_spline"].Effect != "mutation" {
		t.Fatal("public native completion alias/descriptor/effect mismatch", method)
	}
	x := newCompletionRig(t)
	x.now = x.now.Add(time.Second)
	listener := bufconn.Listen(64 * 1024)
	server := grpc.NewServer()
	worldv1.RegisterBuddyWorldServer(server, x.s)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///world-completion-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := new(worldv1.SplineCompletionObservation)
	if err := conn.Invoke(ctx, method.Path(), x.request(t), result); err != nil {
		t.Fatal("published RPC did not reach compiled native handler", err)
	}
	if !result.GetOperation().GetTransportWritten() || result.GetOperation().GetArrivalKnown() || len(x.f.packets) != 1 {
		t.Fatal("public RPC did not preserve scoped attempt semantics", result, x.f.packets)
	}
	method, ok = module.RPC("retire_spline_completion")
	if !ok || method.Path() != worldv1.BuddyWorld_RetireSplineCompletion_FullMethodName ||
		method.Descriptor().Input().FullName() != "warcraftbuddy.world.v1.RetireSplineCompletionRequest" ||
		module.Manifest().ScriptAPI["retire_spline_completion"].Effect != "mutation" {
		t.Fatal("public retirement alias/descriptor/effect mismatch", method)
	}
	retirementGround(t, x, false)
	result = new(worldv1.SplineCompletionObservation)
	if err := conn.Invoke(ctx, method.Path(), retirementRequest(t, x), result); err != nil {
		t.Fatal("published retirement RPC did not reach compiled handler", err)
	}
	if !result.GetRetirementCurrent() || !result.GetOperation().GetRetired() || result.GetOperation().GetPending() ||
		result.GetOperation().GetArrivalKnown() || len(x.f.packets) != 1 {
		t.Fatal("retirement route rewrote delivery or dispatched another report", result)
	}
}
