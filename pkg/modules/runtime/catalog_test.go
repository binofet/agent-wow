package modrt

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modtest "github.com/agent-wow/agent-wow/pkg/modules/internal/testutil"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
)

func catalogManager(t *testing.T, guid uint64, sid string) *Manager {
	t.Helper()
	root := t.TempDir()
	modtest.WriteModule(t, root, "fixture", "")
	r, err := moddisc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	m := New(r, nil, nil)
	m.started, m.dir = true, "/private/"+sid
	m.session.Identity = modsession.Identity{CharacterGUID: guid}
	m.instances["fixture"].healthy = true
	return m
}

func TestDescribeOwnedLiveBodyAndIdentityIndependentRevision(t *testing.T) {
	var revision string
	for _, guid := range []uint64{1, 17379390969991956137} {
		m := catalogManager(t, guid, fmt.Sprint("aw-", guid))
		body, err := m.InvokeJSON(context.Background(), "session.describe", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		var c SessionCatalog
		if err := json.Unmarshal(body, &c); err != nil || c.SessionID != fmt.Sprint("aw-", guid) || c.CharacterGUID != fmt.Sprint(guid) || len(c.Methods) != 1 || c.Methods[0].Callable() {
			t.Fatal(string(body), err)
		}
		if revision != "" && c.Revision != revision {
			t.Fatal("body identity changed contract hash")
		}
		revision = c.Revision
	}
}

func TestDescribeRejectsStartupLogoutUnhealthyAndForeignParams(t *testing.T) {
	for _, mode := range []string{"not_started", "no_guid", "no_session", "closing", "preparing", "prepared", "error", "unhealthy", "module_prepared"} {
		t.Run(mode, func(t *testing.T) {
			m := catalogManager(t, 4, "aw-4")
			switch mode {
			case "not_started":
				m.started = false
			case "no_guid":
				m.session.Identity.CharacterGUID = 0
			case "no_session":
				m.dir = ""
			case "closing":
				m.closing = true
			case "preparing":
				m.preparing = true
			case "prepared":
				m.prepared = true
			case "error":
				m.err = fmt.Errorf("failed")
			case "unhealthy":
				m.instances["fixture"].healthy = false
			case "module_prepared":
				m.instances["fixture"].prepared = true
			}
			if _, err := m.InvokeJSON(context.Background(), "session.describe", nil); err == nil {
				t.Fatal("catalog escaped live-state gate")
			}
		})
	}
	for _, params := range []string{`{"sessionId":"foreign"}`, `[]`, `1`, `"x"`, `{broken`} {
		if _, err := catalogManager(t, 4, "aw-4").InvokeJSON(context.Background(), "session.describe", json.RawMessage(params)); err == nil {
			t.Fatal("accepted invalid discovery arguments", params)
		}
	}
}
