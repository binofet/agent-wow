package modrt

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSessionClockReadMatchesFrameAndPreservesIdentityAcrossReload(t *testing.T) {
	m := catalogManager(t, 4, "aw-4")
	const source = `return {on_event=function(e)return true end,
 decide=function(frame)return {clock=frame.clock} end,
 export_state=function()return {version=1,data={}} end,import_state=function(s)return true end}`
	host, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	params := json.RawMessage(`{"sessionId":"aw-4","characterGuid":"4"}`)
	read := func() policyClockResponse {
		t.Helper()
		raw, err := m.InvokeJSON(context.Background(), "session.clock", params)
		if err != nil {
			t.Fatal(err)
		}
		var value policyClockResponse
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		if value.SessionID != "aw-4" || value.CharacterGUID != "4" || value.MonotonicMs < 0 || value.UnixMs <= 0 {
			t.Fatalf("invalid bound clock read: %+v", value)
		}
		return value
	}
	before := read()
	out, _, err := host.Step(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	frame := out["clock"].(map[string]any)
	after := read()
	if frame["monotonicMs"].(float64) < float64(before.MonotonicMs) || frame["monotonicMs"].(float64) > float64(after.MonotonicMs) {
		t.Fatal("read and frame have different timebases", before, frame, after)
	}
	if _, err := host.Reload(context.Background(), source+"\n-- reload", host.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if reloaded := read(); reloaded.MonotonicMs < after.MonotonicMs {
		t.Fatal("reload reset the clock", after, reloaded)
	}
	catalog, _, err := m.observedSession()
	if err != nil {
		t.Fatal(err)
	}
	var clockMethod *HostMethod
	for i := range catalog.HostMethods {
		if catalog.HostMethods[i].Name == "session.clock" {
			clockMethod = &catalog.HostMethods[i]
		}
	}
	if clockMethod == nil || clockMethod.Effect != "read" {
		t.Fatal("clock read is not reflected")
	}
	for _, key := range []string{"monotonicMs", "unixMs"} {
		property := clockMethod.Output["properties"].(map[string]any)[key].(map[string]any)
		if property["type"] != "integer" || property["optional"] != false {
			t.Fatal("clock output schema disagrees with response", clockMethod.Output)
		}
	}
	for _, bad := range []string{`{}`, `null`, `[]`, `{"sessionId":"foreign","characterGuid":"4"}`,
		`{"sessionId":"aw-4","characterGuid":"5"}`, `{"sessionId":"aw-4","characterGuid":"4","extra":1}`,
		`{"sessionId":"aw-4","characterGuid":"4"}{}`} {
		if _, err := m.InvokeJSON(context.Background(), "session.clock", json.RawMessage(bad)); err == nil {
			t.Fatal("accepted foreign or malformed clock request", bad)
		}
	}
	m.closing = true
	if _, err := m.InvokeJSON(context.Background(), "session.clock", params); err == nil {
		t.Fatal("clock read escaped session lifecycle gate")
	}
}

func TestSessionClockRequiresAttachedPolicy(t *testing.T) {
	m := catalogManager(t, 4, "aw-4")
	if _, err := m.InvokeJSON(context.Background(), "session.clock", json.RawMessage(`{"sessionId":"aw-4","characterGuid":"4"}`)); err == nil {
		t.Fatal("clock read invented an unattached policy timebase")
	}
}
