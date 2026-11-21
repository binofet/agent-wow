package modrt

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func dispatchStateRead(t *testing.T, m *Manager, sid, body string) policyStateResponse {
	t.Helper()
	params, _ := json.Marshal(policyStateRequest{SessionID: sid, CharacterGUID: body})
	raw, err := m.InvokeJSON(context.Background(), "session.policy_state", params)
	if err != nil {
		t.Fatal(err)
	}
	var out policyStateResponse
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPolicyDispatchAdmissionDiagnosticsReadWithoutConsumingOrReplay(t *testing.T) {
	m := New(packagedPolicySpellRegistry(t), nil, nil)
	m.started, m.dir = true, "/private/dispatch-body"
	m.session.Identity = modsession.Identity{CharacterGUID: 22}
	for _, inst := range m.instances {
		inst.healthy = true
	}
	var calls atomic.Int32
	const reason = "exact native admission: current encounter access required"
	conn, err := grpc.NewClient("passthrough:///dispatch-admission", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(_ context.Context, method string, _ any, reply any, _ *grpc.ClientConn, _ grpc.UnaryInvoker, _ ...grpc.CallOption) error {
			if method == grpc_health_v1.Health_Check_FullMethodName {
				reply.(*grpc_health_v1.HealthCheckResponse).Status = grpc_health_v1.HealthCheckResponse_SERVING
				return nil
			}
			if method != actionsv1.BuddyActions_CastHeal_FullMethodName {
				t.Errorf("unexpected mutation route %s", method)
			}
			calls.Add(1)
			return status.Error(codes.FailedPrecondition, reason)
		}))
	if err != nil {
		t.Fatal(err)
	}
	m.instances[actionsModule].conn = conn
	t.Cleanup(func() { m.cancel(); conn.Close() })
	const source = `local s={events=0};return {
on_event=function(e)s.events=s.events+1;return true end,
decide=function(f)return {action={capability="heal",sessionId=f.session_id,selfGuid="22",targetGuid="23",spellId=2050}}end,
export_state=function()return {version=1,data=s}end,import_state=function(c)s=c.data;return true end}`
	host, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	if dispatchStateRead(t, m, "dispatch-body", "22").LastDispatch != nil {
		t.Fatal("proposal-free session invented a dispatch")
	}
	_, receipt, err := host.Step(context.Background(), nil, nil)
	if err != nil || receipt == nil || receipt.Outcome != behavior.ActionNotDispatched ||
		receipt.Payload["phase"] != "admission_rejected" || receipt.Payload["grpc_status"] != "FailedPrecondition" || receipt.Payload["grpc_message"] != reason {
		t.Fatal("native rejection lost exact diagnostics", receipt, err)
	}
	before := host.vm.Status()
	view := dispatchStateRead(t, m, "dispatch-body", "22")
	last := view.LastDispatch
	if last == nil || last.SessionID != "dispatch-body" || last.CharacterGUID != "22" || last.Revision != before.Revision ||
		last.Generation != "1" || last.Cursor != "0" || last.ActionID != "1" || last.Module != actionsModule ||
		last.TransportOutcome != behavior.ActionNotDispatched || !last.SameRevisionAsState ||
		last.Payload["grpc_message"] != reason || last.Payload["effect_observed"] != false ||
		view.BufferedEvents != 1 || len(view.PendingActionIDs) != 1 || calls.Load() != 1 || host.vm.Status() != before {
		t.Fatal("read consumed an event, replayed, or fabricated an effect", view, before, host.vm.Status())
	}
	// A diagnostic read must also work while dispatch/reload's advance mutex is
	// held. It takes only the existing VM state read and an immutable atomic load.
	host.advance.Lock()
	read := make(chan error, 1)
	go func() {
		_, err := m.InvokeJSON(context.Background(), "session.policy_state", json.RawMessage(`{"sessionId":"dispatch-body","characterGuid":"22"}`))
		read <- err
	}()
	select {
	case err := <-read:
		host.advance.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		host.advance.Unlock()
		t.Fatal("diagnostic read acquired advance")
	}
	if _, err = host.Reload(context.Background(), source+"\n-- same body replacement", before.Revision); err != nil {
		t.Fatal(err)
	}
	reloaded := dispatchStateRead(t, m, "dispatch-body", "22")
	if reloaded.Revision == before.Revision || reloaded.LatestDecision != nil || reloaded.LastDispatch == nil ||
		reloaded.LastDispatch.Revision != before.Revision || reloaded.LastDispatch.Generation != "1" || reloaded.LastDispatch.SameRevisionAsState || calls.Load() != 1 {
		t.Fatal("reload replayed or relabeled a historical transport result", reloaded)
	}
	m.dir = "/private/replacement-body"
	if _, err = m.InvokeJSON(context.Background(), "session.policy_state", json.RawMessage(`{"sessionId":"replacement-body","characterGuid":"22"}`)); err == nil {
		t.Fatal("old dispatch escaped current-body fence")
	}
}

func TestPolicyDispatchSnapshotDetachesPayloadAndDoesNotBecomeGameEffect(t *testing.T) {
	m := catalogManager(t, 4, "aw-4")
	const source = `return {on_event=function(e)return true end,decide=function(f)return {action={chosen=true}}end,
export_state=function()return {version=1,data={}}end,import_state=function(c)return true end}`
	host, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	payload := map[string]any{"nested": map[string]any{"values": []any{map[string]any{"phase": "written"}}}, "effectObserved": false}
	sends := 0
	_, receipt, err := host.Step(context.Background(), nil, func(map[string]any) (PolicyDispatch, error) {
		return PolicyDispatch{Module: "fixture", Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
			sends++
			return behavior.ActionTransportAccepted, payload
		}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Neither adapter-owned nested maps nor caller-owned detached reads can
	// rewrite an already published transport boundary.
	payload["nested"].(map[string]any)["values"].([]any)[0].(map[string]any)["phase"] = "mutated adapter"
	read := host.dispatchSnapshot(host.vm.Status())
	read.Payload["nested"].(map[string]any)["values"].([]any)[0].(map[string]any)["phase"] = "mutated reader"
	if _, err = host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "aw-4", ActionID: receipt.ActionID,
		Outcome: behavior.ActionConfirmed, Payload: map[string]any{"effectObserved": true}}}); err != nil {
		t.Fatal(err)
	}
	view := dispatchStateRead(t, m, "aw-4", "4")
	if view.LastDispatch.TransportOutcome != behavior.ActionTransportAccepted || view.LastDispatch.Payload["effectObserved"] != false ||
		view.LastDispatch.Payload["nested"].(map[string]any)["values"].([]any)[0].(map[string]any)["phase"] != "written" || sends != 1 {
		t.Fatal("transport snapshot was mutated or conflated with later effect", view.LastDispatch, sends)
	}
}

func TestPolicyDispatchNoSnapshotForUnsentProposalAndExactErrorStatus(t *testing.T) {
	m := catalogManager(t, 4, "aw-4")
	host, err := m.StartPolicy(context.Background(), `return {on_event=function(e)return true end,decide=function(f)return {action={x=1}}end,
export_state=function()return {version=1,data={}}end,import_state=function(c)return true end}`)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	if _, _, err = host.Step(context.Background(), nil, func(map[string]any) (PolicyDispatch, error) {
		return PolicyDispatch{}, status.Error(codes.InvalidArgument, "no selected capability")
	}); err == nil || dispatchStateRead(t, m, "aw-4", "4").LastDispatch != nil {
		t.Fatal("unresolved proposal invented an actual dispatch", err)
	}
	for _, code := range []codes.Code{codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.PermissionDenied, codes.DeadlineExceeded} {
		message := "actual RPC detail: " + code.String()
		payload := policyDispatchError(map[string]any{"phase": "legacy_phase"}, status.Error(code, message))
		if payload["grpc_status"] != code.String() || payload["grpc_message"] != message || payload["phase"] != "legacy_phase" || strings.Contains(payload["grpc_message"].(string), "rpc error:") {
			t.Fatal("status detail changed", payload)
		}
	}
}
