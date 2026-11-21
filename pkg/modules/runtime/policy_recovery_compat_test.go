package modrt

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
)

func (p *healInputPeer) Recovery(_ context.Context, q *actionsv1.ToggleRequest) (*actionsv1.State, error) {
	p.direct.Add(1)
	return &actionsv1.State{SessionId: p.sid, SelfGuid: p.body, Recovery: &actionsv1.RecoveryState{Enabled: q.Enabled}}, nil
}
func TestLegacyRecoveryBothValuesReachCurrentEditedPolicyWithoutNativeOffState(t *testing.T) {
	m, peer := healInputManager(t)
	if _, err := m.InvokeJSON(context.Background(), "buddy-actions.recovery", json.RawMessage(`{"enabled":true}`)); err == nil {
		t.Fatal("new module silently restored native recovery without Lua")
	}
	source := `local s={requests=0};return {on_event=function(e)if e.kind=='death_recovery_request' then s.requests=s.requests+1;s.source=e.value.source end return true end,decide=function(f)return {requests=s.requests,source=s.source,revision='first'}end,export_state=function()return {version=1,data=s}end,import_state=function(c)s=c.data;return true end}`
	host, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	if _, err = host.Reload(context.Background(), strings.Replace(source, "revision='first'", "revision='edited'", 1), host.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"enabled":true}`, `{"enabled":false}`} {
		raw, err := m.InvokeJSON(context.Background(), "buddy-actions.recovery", json.RawMessage(body))
		if err != nil {
			t.Fatal(err)
		}
		var ack map[string]any
		if err = json.Unmarshal(raw, &ack); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(ack["reason"].(string), "lua_death_recovery_request_queued:") || peer.direct.Load() != 0 {
			t.Fatal("intent bypassed current Lua or claimed resurrection", ack, peer.direct.Load())
		}
	}
	out, err := host.vm.Decide(context.Background(), map[string]any{})
	if err != nil || out.Output["requests"] != float64(2) || out.Output["source"] != "recovery" || out.Output["revision"] != "edited" {
		t.Fatal(out, err)
	}
	peer.sid = "replacement-body"
	if _, err = m.InvokeJSON(context.Background(), "buddy-actions.recovery", json.RawMessage(`{"enabled":true}`)); err == nil {
		t.Fatal("foreign body queued recovery intent")
	}
	if host.vm.Status().Buffered != 0 || peer.direct.Load() != 0 {
		t.Fatal("rejected body reached VM/native", host.vm.Status(), peer.direct.Load())
	}
}
