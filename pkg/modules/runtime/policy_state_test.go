package modrt

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func TestSessionPolicyStateReadBoundIdentityAndReload(t *testing.T) {
	m := catalogManager(t, 4, "aw-4")
	const source = `local n=0
return {on_event=function(e)return true end,
 decide=function(frame)n=n+1;return {task={phase="waiting"},n=n} end,
 export_state=function()return {version=1,data={n=n}} end,import_state=function(s)n=s.data.n;return true end}`
	host, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	params := json.RawMessage(`{"sessionId":"aw-4","characterGuid":"4"}`)
	read := func() policyStateResponse {
		t.Helper()
		raw, err := m.InvokeJSON(context.Background(), "session.policy_state", params)
		if err != nil {
			t.Fatal(err)
		}
		var view policyStateResponse
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		if view.SessionID != "aw-4" || view.CharacterGUID != "4" || view.Clock.UnixMs <= 0 {
			t.Fatal(view)
		}
		return view
	}
	if read().LatestDecision != nil {
		t.Fatal("invented a latest decision")
	}
	if _, _, err := host.Step(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enqueue(behavior.Event{Kind: "input", Value: map[string]any{"task": "next"}}); err != nil {
		t.Fatal(err)
	}
	view := read()
	if view.Cursor != "0" || view.BufferedEvents != 1 || view.Generation != "1" || view.LatestDecision == nil ||
		view.LatestDecision.Output["task"].(map[string]any)["phase"] != "waiting" ||
		view.Checkpoint["data"].(map[string]any)["n"] != float64(1) {
		t.Fatal("RPC did not expose the committed Lua state", view)
	}
	if _, err := host.Reload(context.Background(), source+"\n-- replacement", view.Revision); err != nil {
		t.Fatal(err)
	}
	if reloaded := read(); reloaded.Revision == view.Revision || reloaded.LatestDecision != nil || reloaded.Cursor != "1" {
		t.Fatal("RPC lost reload/cursor provenance", reloaded)
	}
	catalog, _, err := m.observedSession()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, method := range catalog.HostMethods {
		if method.Name == "session.policy_state" {
			found = true
			if method.Effect != "read" {
				t.Fatal(method)
			}
			for _, field := range []string{"checkpoint", "latestDecision", "revision", "cursor", "clock"} {
				if method.Output["properties"].(map[string]any)[field] == nil {
					t.Fatal("missing reflected output", field)
				}
			}
		}
	}
	if !found {
		t.Fatal("policy_state missing from session.describe")
	}
	for _, invalid := range []string{`{}`, `null`, `[]`, `{"sessionId":"old","characterGuid":"4"}`,
		`{"sessionId":"aw-4","characterGuid":"5"}`, `{"sessionId":"aw-4","characterGuid":"4","extra":1}`,
		`{"sessionId":"aw-4","characterGuid":"4"}{}`} {
		if _, err := m.InvokeJSON(context.Background(), "session.policy_state", json.RawMessage(invalid)); err == nil {
			t.Fatal("accepted invalid identity", invalid)
		}
	}
	m.closing = true
	if _, err := m.InvokeJSON(context.Background(), "session.policy_state", params); err == nil {
		t.Fatal("read escaped lifecycle gate")
	}
}

func TestSessionPolicyStateRequiresAttachedPolicy(t *testing.T) {
	m := catalogManager(t, 4, "aw-4")
	if _, err := m.InvokeJSON(context.Background(), "session.policy_state", json.RawMessage(`{"sessionId":"aw-4","characterGuid":"4"}`)); err == nil {
		t.Fatal("read invented an unattached policy checkpoint")
	}
}
