package behavior

import (
	"context"
	"errors"
	"testing"
	"time"

	lua "github.com/yuin/gopher-lua"
)

func TestSessionHostValidationDoesNotSpendLuaBudget(t *testing.T) {
	vm, err := newSessionVM(context.Background(), sessionPolicyV1, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer vm.close()
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	checking, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- vm.call(parent, "export_state", func(ctx context.Context, _ lua.LValue) error {
			close(checking)
			select {
			case <-release:
				return ctx.Err()
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-checking:
	case err := <-done:
		t.Fatalf("callback failed before host validation: %v", err)
	case <-parent.Done():
		t.Fatal(parent.Err())
	}
	// Hold host validation beyond the Lua watchdog. Only the parent context
	// may stop data conversion after the callback has returned successfully.
	time.Sleep(30 * time.Millisecond)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := vm.onEvent(parent, map[string]any{"sequence": "1", "value": map[string]any{"label": "next"}}); err != nil {
		t.Fatalf("subsequent callback: %v", err)
	}
}

func TestSessionConfiguredBudgetSurvivesReloadAndStopsRunawayLua(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session, err := NewSessionWithOptions(parent, "reload-budget", sessionPolicyV1, SessionOptions{ExecutionLimit: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	runaway := `return {
    decide=function() while true do end end,
    on_event=function() return true end,
    export_state=function() return {version=1, data={}} end,
    import_state=function() return true end,
}`
	if _, err := session.Reload(parent, runaway, session.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if session.vm.executionLimit != 10*time.Millisecond {
		t.Fatalf("reload lost configured budget: %s", session.vm.executionLimit)
	}
	if _, err := session.Decide(parent, nil); !errors.Is(err, context.DeadlineExceeded) || parent.Err() != nil {
		t.Fatalf("runaway error=%v parent=%v", err, parent.Err())
	}
	if session.Status().Fault == "" {
		t.Fatal("runaway callback did not fault its VM")
	}
}

func TestSessionMutexWaitDoesNotSpendLuaBudget(t *testing.T) {
	vm, err := newSessionVM(context.Background(), sessionPolicyV1, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer vm.close()
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	vm.mu.Lock()
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		done <- vm.onEvent(parent, map[string]any{"sequence": "1", "value": map[string]any{"label": "after lock"}})
	}()
	<-started
	time.Sleep(30 * time.Millisecond)
	vm.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatalf("mutex wait spent Lua budget: %v", err)
	}
}

func TestSessionBudgetHonorsParentCancellation(t *testing.T) {
	vm, err := newSessionVM(context.Background(), sessionPolicyV1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer vm.close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := vm.decide(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled parent error=%v", err)
	}
	loop, err := newSessionVM(context.Background(), `return {
    decide=function() while true do end end,
    on_event=function() return true end,
    export_state=function() return {version=1, data={}} end,
    import_state=function() return true end,
}`, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer loop.close()
	deadline, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if _, err := loop.decide(deadline, nil); !errors.Is(err, context.DeadlineExceeded) || deadline.Err() == nil {
		t.Fatalf("running callback ignored parent deadline: error=%v parent=%v", err, deadline.Err())
	}
	if session, err := NewSessionWithOptions(context.Background(), "negative", sessionPolicyV1, SessionOptions{ExecutionLimit: -1}); session != nil || err == nil {
		t.Fatal("accepted negative execution budget")
	}
}
