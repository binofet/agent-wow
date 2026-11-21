package modrt

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
)

func (p *healInputPeer) EngageNPC(context.Context, *actionsv1.EngageNPCRequest) (*actionsv1.State, error) {
	p.direct.Add(1)
	return &actionsv1.State{SessionId: p.sid, SelfGuid: p.body}, nil
}

func TestLegacyEngageInputUsesTheCurrentLuaCommandQueue(t *testing.T) {
	m, peer := healInputManager(t)
	if _, err := m.InvokeJSON(context.Background(), "buddy-actions.engage_npc", json.RawMessage(`{"enabled":true,"targetGuid":"17379390969991956137"}`)); err == nil || peer.direct.Load() != 0 {
		t.Fatal("missing Lua host started a native controller", err)
	}
	source := `local s={};return {on_event=function(e)if e.kind=="command" then s=e.value end return true end,
 decide=function(f)return s end,export_state=function()return {version=1,data=s}end,import_state=function(c)s=c.data;return true end}`
	host, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	raw, err := m.InvokeJSON(context.Background(), "buddy-actions.engage_npc", json.RawMessage(`{"enabled":true,"targetGuid":"17379390969991956137"}`))
	if err != nil {
		t.Fatal(err)
	}
	var ack map[string]any
	json.Unmarshal(raw, &ack)
	out, _, err := host.Step(context.Background(), nil, nil)
	if err != nil || out["task"] != "engage" || out["targetGuid"] != "17379390969991956137" || out["enabled"] != true || peer.direct.Load() != 0 || !strings.HasPrefix(ack["reason"].(string), "lua_combat_request_queued:") {
		t.Fatal("intent not delivered to current Lua", out, string(raw), err)
	}
	if _, err = m.InvokeJSON(context.Background(), "buddy-actions.engage_npc", json.RawMessage(`{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	out, _, err = host.Step(context.Background(), nil, nil)
	if err != nil || out["enabled"] != false || peer.direct.Load() != 0 {
		t.Fatal("cancellation invoked a second controller", out, err)
	}
}
