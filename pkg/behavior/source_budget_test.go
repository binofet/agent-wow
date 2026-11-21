package behavior

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSessionSourceBudgetAllowsComposedPolicyReloadAndPreservesPending(t *testing.T) {
	const limit = 512 * 1024
	source := sessionPolicyV1 + "\n--" + strings.Repeat("x", 256*1024)
	s, err := NewSessionWithOptions(context.Background(), "offline-session", source, SessionOptions{SourceBytes: limit, ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	decision := sessionDecide(t, s)
	id := sessionReserve(t, s, decision)
	sessionEnqueue(t, s, 1, sessionReceipt(id, ActionUnknown))
	before := s.Status()
	revision, err := s.Reload(context.Background(), source+"\n-- another module", before.Revision)
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.State(context.Background())
	if err != nil || view.Status.SessionID != before.SessionID || view.Status.Revision != revision ||
		view.SourceLimitBytes != limit || view.Status.Cursor != 1 || len(view.Pending) != 1 || view.Pending[0].ActionID != id || view.Pending[0].Outcome != ActionUnknown {
		t.Fatal("large reload changed identity, budget or pending outcome", view, err)
	}
	if _, err := s.Reload(context.Background(), strings.Repeat("x", limit+1), revision); err == nil || !strings.Contains(err.Error(), "configured source budget") {
		t.Fatal("source exceeding this session's configured budget was accepted", err)
	}
	if after := s.Status(); after.Revision != revision || after.Cursor != view.Status.Cursor || after.Epoch != view.Status.Epoch || after.Pending != 1 {
		t.Fatal("oversized candidate disturbed the committed writer/ledger", after)
	}
}

func TestSourceBudgetDefaultsAndStatelessOverride(t *testing.T) {
	source := `return {decide=function(input)return {ok=true} end}` + "\n--" + strings.Repeat("x", 256*1024)
	runtime := New()
	if _, err := runtime.Reload(source, ""); err != nil {
		t.Fatal("default rejected realistic composed source", err)
	}
	configured, err := NewWithSourceLimit(len(source))
	if err != nil {
		t.Fatal(err)
	}
	revision, err := configured.Reload(source, "")
	if err != nil {
		t.Fatal("exact configured source budget rejected", err)
	}
	if _, err := configured.Reload(source+" ", revision); err == nil || configured.Status().Revision != revision {
		t.Fatal("configured source limit changed the active policy", err)
	}
	if _, err := NewWithSourceLimit(-1); err == nil {
		t.Fatal("accepted negative source budget")
	}
	if _, err := NewSessionWithOptions(context.Background(), "invalid", sessionPolicyV1, SessionOptions{SourceBytes: -1}); err == nil {
		t.Fatal("accepted negative session source budget")
	}
	s := newTestSession(t)
	if s.SourceLimitBytes() != DefaultSourceBytes || DefaultSourceBytes != 8*1024*1024 {
		t.Fatal("default source budget is not explicit", s.SourceLimitBytes())
	}
}
