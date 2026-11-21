package modrt

import (
	"context"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestPolicyMovementOwnerRoutesTypedReflectedAcquireAndRelease(t *testing.T) {
	const sid = "lease-policy"
	manager := New(packagedPolicySpellRegistry(t), nil, nil)
	t.Cleanup(manager.cancel)
	vm, err := behavior.NewSession(context.Background(), sid, `return {decide=function(f)return {} end,on_event=function(e)return true end,export_state=function()return {version=1,data={}} end,import_state=function(s)return true end}`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vm.Close)
	calls := 0
	failNext := false
	conn, err := grpc.NewClient("passthrough:///lease-fixture", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithUnaryInterceptor(func(_ context.Context, method string, request, reply any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, _ ...grpc.CallOption) error {
		calls++
		if failNext {
			return status.Error(codes.DeadlineExceeded, "lease response unknown")
		}
		raw, err := proto.Marshal(request.(proto.Message))
		if err != nil {
			t.Fatal(err)
		}
		switch method {
		case actionsv1.BuddyActions_AcquireMovement_FullMethodName:
			req := &actionsv1.MovementOwnerRequest{}
			if err = proto.Unmarshal(raw, req); err != nil {
				t.Fatal(err)
			}
			if req.SessionId != sid || req.SelfGuid != 4 || req.Owner != "wait:123" || req.LeaseMs != 1500 {
				t.Fatal("Lua request lost exact identity/TTL", req)
			}
			out := &actionsv1.MovementOwnerLease{Token: "private-token", State: &actionsv1.State{SessionId: sid, SelfGuid: 4, Following: true, MovementOwner: &actionsv1.MovementOwnerState{Active: true, Owner: req.Owner, RemainingMs: 1500, Revision: 1}}}
			raw, err = proto.Marshal(out)
		case actionsv1.BuddyActions_ReleaseMovement_FullMethodName:
			req := &actionsv1.MovementOwnerRelease{}
			if err = proto.Unmarshal(raw, req); err != nil {
				t.Fatal(err)
			}
			if req.Token != "private-token" || req.SessionId != sid || req.SelfGuid != 4 {
				t.Fatal(req)
			}
			raw, err = proto.Marshal(&actionsv1.State{SessionId: sid, SelfGuid: 4, Following: true, MovementOwner: &actionsv1.MovementOwnerState{Revision: 2}})
		default:
			t.Fatal("unexpected capability route", method)
		}
		if err != nil {
			t.Fatal(err)
		}
		return proto.Unmarshal(raw, reply.(proto.Message))
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	manager.instances[actionsModule].conn, manager.instances[actionsModule].healthy = conn, true
	host := &SessionPolicy{vm: vm, manager: manager, body: "4"}
	acquire := map[string]any{"capability": "movement_owner", "sessionId": sid, "selfGuid": "4", "owner": "wait:123", "mode": "acquire", "leaseMs": float64(1500)}
	dispatch, err := host.resolveKnownSpell(acquire)
	if err != nil {
		t.Fatal(err)
	}
	outcome, payload := dispatch.Send(context.Background())
	if outcome != behavior.ActionConfirmed || payload["result"].(map[string]any)["token"] != "private-token" {
		t.Fatal(outcome, payload)
	}
	release := map[string]any{"capability": "movement_owner", "sessionId": sid, "selfGuid": "4", "owner": "wait:123", "mode": "release", "token": "private-token"}
	dispatch, err = host.resolveKnownSpell(release)
	if err != nil {
		t.Fatal(err)
	}
	outcome, payload = dispatch.Send(context.Background())
	if outcome != behavior.ActionConfirmed || calls != 2 {
		t.Fatal(outcome, payload, calls)
	}
	failNext = true
	dispatch, err = host.resolveKnownSpell(acquire)
	if err != nil {
		t.Fatal(err)
	}
	outcome, payload = dispatch.Send(context.Background())
	if outcome != behavior.ActionSettledUnverified || payload["reconciliationRequired"] != true || payload["error"] == nil || calls != 3 {
		t.Fatal("unknown finite metadata became success or a global pending fence", outcome, payload, calls)
	}
	for _, mode := range []string{"wrong_session", "wrong_body", "no_token", "unbounded_time"} {
		bad := map[string]any{}
		for k, v := range acquire {
			bad[k] = v
		}
		switch mode {
		case "wrong_session":
			bad["sessionId"] = "old"
		case "wrong_body":
			bad["selfGuid"] = "5"
		case "no_token":
			bad["mode"] = "renew"
		case "unbounded_time":
			bad["leaseMs"] = float64(0)
		}
		if _, err = host.resolveKnownSpell(bad); status.Code(err) != codes.InvalidArgument {
			t.Fatal("invalid owner action admitted", mode, err)
		}
	}
	if calls != 3 {
		t.Fatal("validation dispatched gameplay", calls)
	}
}
