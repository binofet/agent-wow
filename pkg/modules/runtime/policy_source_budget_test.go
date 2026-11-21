package modrt

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func TestPolicySourceBudgetIsReflectedAndReadbackPreservesLargeSource(t *testing.T) {
	m := catalogManager(t, 4, "aw-4")
	source := `return {on_event=function(e)return true end,decide=function(f)return {} end,export_state=function()return {version=1,data={}} end,import_state=function(s)return true end}` + "\n--" + strings.Repeat("x", 1200*1024)
	host, err := m.StartPolicyWithOptions(context.Background(), source, behavior.SessionOptions{SourceBytes: 2 * 1024 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	identity := json.RawMessage(`{"sessionId":"aw-4","characterGuid":"4"}`)
	read := func() policySourceResponse {
		t.Helper()
		raw, err := m.InvokeJSON(context.Background(), "session.policy_source", identity)
		if err != nil {
			t.Fatal(err)
		}
		var value policySourceResponse
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := read()
	if before.Source != source || before.SourceLimitBytes != 2*1024*1024 {
		t.Fatal("readback source or configured budget lost", before.SourceLimitBytes, len(before.Source))
	}
	params, _ := json.Marshal(map[string]any{"sessionId": "aw-4", "characterGuid": "4", "expectedRevision": before.Revision, "source": source + "\n-- reload"})
	if _, err := m.InvokeJSON(context.Background(), "session.reload_policy", params); err != nil {
		t.Fatal(err)
	}
	if after := read(); after.Source != source+"\n-- reload" || after.SourceLimitBytes != before.SourceLimitBytes || after.Revision == before.Revision {
		t.Fatal("reload lost effective source budget")
	}
	catalog, _, err := m.observedSession()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, method := range catalog.HostMethods {
		if method.Name == "session.policy_source" {
			found = true
			props := method.Output["properties"].(map[string]any)
			if props["sourceLimitBytes"].(map[string]any)["type"] != "integer" || props["revision"] == nil {
				t.Fatal("source budget/identity schema missing", method.Output)
			}
		}
	}
	if !found {
		t.Fatal("source read is not reflected")
	}
}
