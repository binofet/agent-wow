package behavior

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSessionLargePolicyDoesNotSpendExecutionBudgetOnCompilation(t *testing.T) {
	// Compile the helper without running its body at initialization.
	source := "local function helper(n)\n" + strings.Repeat("n = n + 1\n", 7000) + "return n\nend\n" + `
return {
    decide = function(frame) return {cursor=frame.cursor} end,
    on_event = function() return true end,
    export_state = function() return {version=1, data={}} end,
    import_state = function() return true end,
}
`
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session, err := NewSession(ctx, "compile-budget", source)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	decision, err := session.Decide(ctx, nil)
	if err != nil || decision.Output["cursor"] != "0" {
		t.Fatalf("first decision %+v: %v", decision.Output, err)
	}
}

func TestSessionInitializationHonorsParentAndExecutionDeadlines(t *testing.T) {
	t.Run("canceled parent", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		session, err := NewSession(ctx, "canceled", "this is invalid Lua")
		if session != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("session=%v error=%v", session, err)
		}
	})
	t.Run("nonterminating initialization", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		session, err := NewSession(ctx, "loop", "while true do end")
		if session != nil || !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			t.Fatalf("session=%v error=%v parent=%v", session, err, ctx.Err())
		}
	})
}
