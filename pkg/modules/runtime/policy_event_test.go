package modrt

import (
	"context"
	"encoding/json"
	"testing"
)

func TestAttachedPolicyAcceptsOrderedOwnerInputWithoutReload(t *testing.T) {
	m := catalogManager(t, 4, "aw-4")
	host, err := m.StartPolicy(context.Background(), `local state={}
return {on_event=function(e) state.kind=e.kind;state.value=e.value;return true end,
 decide=function(frame)return state end,export_state=function()return {version=1,data=state} end,
 import_state=function(s)state=s.data;return true end}`)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	before := host.vm.Status().Revision
	raw, err := m.InvokeJSON(context.Background(), "session.send_event", json.RawMessage(`{"sessionId":"aw-4","characterGuid":"4","kind":"command","value":{"task":"sheep","mark":"moon"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var ack map[string]string
	json.Unmarshal(raw, &ack)
	if ack["status"] != "queued" || ack["sequence"] != "1" {
		t.Fatal(string(raw))
	}
	out, _, err := host.Step(context.Background(), nil, nil)
	if err != nil || out["kind"] != "command" || out["value"].(map[string]any)["mark"] != "moon" || host.vm.Status().Revision != before {
		t.Fatalf("input not delivered into unchanged policy: %+v %v", out, err)
	}
	for _, bad := range []string{`{"sessionId":"foreign","characterGuid":"4","kind":"command"}`, `{"sessionId":"aw-4","characterGuid":"5","kind":"command"}`, `{"sessionId":"aw-4","characterGuid":"4","kind":"command","receipt":{}}`} {
		if _, err := m.InvokeJSON(context.Background(), "session.send_event", json.RawMessage(bad)); err == nil {
			t.Fatal("accepted foreign or malformed input")
		}
	}
	catalog, _, err := m.observedSession()
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range catalog.HostMethods {
		if method.Name == "session.send_event" && method.Input["properties"] != nil {
			return
		}
	}
	t.Fatal("input API is not reflected")
}
