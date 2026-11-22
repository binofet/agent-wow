package behavior

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRuntimeResourceInitializationKeepsAcceptedSourceAndAllowsExplicitNextLoad(t *testing.T) {
	r, err := NewWithOptions(SessionOptions{ExecutionLimit: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	accepted := load(t, r, goodPolicy)
	if _, err := r.Evaluate(nil); err != nil {
		t.Fatal(err)
	}
	const loop = "while true do end"
	for range 2 {
		got, err := r.Reload(loop, accepted)
		if got != accepted || !errors.Is(err, ErrExecutionBudget) || !strings.Contains(err.Error(), "chunk_initialize:") {
			t.Fatal("resource failure became a source ban or lost its phase", got, err)
		}
		state := r.Status()
		if state.Revision != accepted || state.LastGoodRevision != accepted || state.QuarantinedRevision != "" || state.Failures != 0 || state.Evaluations != 1 {
			t.Fatal("failed initializer changed accepted policy or retried a decision", state)
		}
	}
	if out, err := r.Evaluate(map[string]any{"health": 7}); err != nil || out["health"] != float64(7) {
		t.Fatal("accepted source was lost after an unavailable candidate", out, err)
	}
}

func TestRuntimeResourceFailureInFallbackRetainsLastGoodWithoutFurtherRetry(t *testing.T) {
	r, err := NewWithOptions(SessionOptions{ExecutionLimit: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	good := load(t, r, `return {decide=function(input)
 if input.loop then while true do end end
 return {kind="last_good"}
end}`)
	if _, err := r.Evaluate(nil); err != nil {
		t.Fatal(err)
	}
	bad := load(t, r, `return {decide=function(input) error("actual_lua_fault") end}`)
	out, err := r.Evaluate(map[string]any{"loop": true})
	state := r.Status()
	if out != nil || !errors.Is(err, ErrExecutionBudget) || !strings.Contains(err.Error(), "decide:") ||
		state.Revision != good || state.LastGoodRevision != good || state.QuarantinedRevision != bad || state.Failures != 1 || state.Evaluations != 2 {
		t.Fatal("fallback resource failure quarantined good code or erased the real Lua fault", out, err, state)
	}
	if out, err := r.Evaluate(nil); err != nil || out["kind"] != "last_good" || r.Status().Evaluations != 3 {
		t.Fatal("next normal read could not recover last good policy", out, err, r.Status())
	}
}

func TestRuntimeResourceCandidateDoesNotReturnAnOlderFallbackDecision(t *testing.T) {
	r, err := NewWithOptions(SessionOptions{ExecutionLimit: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	older := load(t, r, `return {decide=function(input)return {kind="older_action"}end}`)
	if _, err := r.Evaluate(nil); err != nil {
		t.Fatal(err)
	}
	current := load(t, r, `return {decide=function(input)
 if input.loop then while true do end end
 return {kind="current_action"}
end}`)
	out, err := r.Evaluate(map[string]any{"loop": true})
	state := r.Status()
	if out != nil || !errors.Is(err, ErrExecutionBudget) || state.Revision != current ||
		state.LastGoodRevision != older || state.QuarantinedRevision != "" || state.Failures != 0 || state.Evaluations != 2 {
		t.Fatal("resource miss returned an old action, banned current code or retried the turn", out, err, state)
	}
	if out, err := r.Evaluate(nil); err != nil || out["kind"] != "current_action" || r.Status().Revision != current || r.Status().LastGoodRevision != current {
		t.Fatal("later normal input did not use the accepted candidate", out, err, r.Status())
	}
}

func TestRuntimeResourceTurnStillHonorsOuterDeadline(t *testing.T) {
	r, err := NewWithOptions(SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	revision := load(t, r, `return {decide=function(input)
 if input.loop then while true do end end
 return {ok=true}
end}`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	out, err := r.EvaluateContext(ctx, map[string]any{"loop": true})
	if out != nil || !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrExecutionBudget) ||
		r.Status().Revision != revision || r.Status().Failures != 0 {
		t.Fatal("outer deadline became a source fault or was ignored", out, err, r.Status())
	}
	if out, err := r.Evaluate(nil); err != nil || out["ok"] != true {
		t.Fatal("outer cancellation disabled future calls", out, err)
	}
}
