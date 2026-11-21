package behavior

import (
	"context"
	"errors"
	"testing"
	"time"

	lua "github.com/yuin/gopher-lua"
)

type delayedConversionContext struct {
	context.Context
	wait time.Duration
}

func (c *delayedConversionContext) Err() error {
	if c.wait > 0 {
		wait := c.wait
		c.wait = 0
		time.Sleep(wait)
	}
	return c.Context.Err()
}

func TestRuntimeExecutionBudgetExcludesHostConversionWithoutRestartingAllowance(t *testing.T) {
	L := newVM()
	defer L.Close()
	chunk, err := L.LoadString(goodPolicy)
	if err != nil {
		t.Fatal(err)
	}
	parent := &delayedConversionContext{Context: context.Background()}
	watchdog := newExecutionBudget(parent, 50*time.Millisecond)
	var decide *lua.LFunction
	if err := watchdog.call(L, func() error {
		var err error
		decide, err = decisionFunction(L, chunk)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	remaining := watchdog.remaining
	if remaining <= 0 || remaining >= watchdog.limit {
		t.Fatal("chunk execution was not charged to the shared allowance", remaining)
	}
	// Actual host conversion outlasts the first execution timer. It uses the
	// caller context, not a fired or cancelled VM context from initialization.
	parent.wait = 3 * watchdog.limit
	input, err := toLua(L, map[string]any{"health": 37}, &budget{ctx: parent})
	if err != nil || watchdog.remaining != remaining {
		t.Fatal("host conversion spent the Lua allowance", err, remaining, watchdog.remaining)
	}
	if err := watchdog.call(L, func() error {
		return L.CallByParam(lua.P{Fn: decide, NRet: 1, Protect: true}, input)
	}); err != nil {
		t.Fatal("decide reused the old context or lost its remaining budget", err)
	}
	if watchdog.remaining >= remaining {
		t.Fatal("decide received a fresh full allowance", remaining, watchdog.remaining)
	}
	remaining = watchdog.remaining
	parent.wait = 3 * watchdog.limit
	out, err := fromLua(L.Get(-1), &budget{ctx: parent}, map[*lua.LTable]bool{}, 0)
	if err != nil || out.(map[string]any)["health"] != float64(37) || watchdog.remaining != remaining {
		t.Fatal("host output conversion consumed the execution watchdog", out, err, watchdog.remaining)
	}
	if EvaluationLimit != 5*time.Millisecond {
		t.Fatal("standalone default changed")
	}
}

func TestRuntimeExecutionBudgetCannotResumeAnExhaustedAllowance(t *testing.T) {
	L := newVM()
	defer L.Close()
	chunk, err := L.LoadString("while true do end")
	if err != nil {
		t.Fatal(err)
	}
	watchdog := newExecutionBudget(context.Background(), 5*time.Millisecond)
	err = watchdog.call(L, func() error {
		return L.CallByParam(lua.P{Fn: chunk, NRet: 1, Protect: true})
	})
	if !errors.Is(err, ErrExecutionBudget) || !errors.Is(err, context.DeadlineExceeded) || watchdog.remaining > 0 {
		t.Fatal("runaway was not stopped by its configured allowance", err, watchdog.remaining)
	}
	called := false
	err = watchdog.call(L, func() error { called = true; return nil })
	if called || !errors.Is(err, ErrExecutionBudget) {
		t.Fatal("another phase received a new budget after exhaustion", called, err)
	}
}

func TestRuntimeBudgetExpiryStillQuarantinesRunawayWithoutPublishingMutatedInput(t *testing.T) {
	r, err := NewWithOptions(SessionOptions{ExecutionLimit: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	revision := load(t, r, `return {decide=function(s)
if s.loop then s.child.value=999; while true do end end
return {value=s.child.value}
end}`)
	input := map[string]any{"child": map[string]any{"value": 7}}
	if out, err := r.Evaluate(input); err != nil || out["value"] != float64(7) {
		t.Fatal(out, err)
	}
	input["loop"] = true
	out, err := r.Evaluate(input)
	if out != nil || !errors.Is(err, ErrExecutionBudget) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("watchdog expiry was not distinguished from a policy result", out, err)
	}
	if input["child"].(map[string]any)["value"] != 7 {
		t.Fatal("the rejected VM mutated caller input", input)
	}
	state := r.Status()
	if state.QuarantinedRevision != revision || state.Failures != 1 || state.Revision != "" {
		t.Fatal("runaway policy was rearmed", state)
	}
}

func TestRuntimeBudgetPreservesParentCancellationDuringHostWork(t *testing.T) {
	r, err := NewWithOptions(SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	revision := load(t, r, goodPolicy)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = r.EvaluateContext(ctx, map[string]any{"health": 1})
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrExecutionBudget) || r.Status().Revision != revision || r.Status().Failures != 0 {
		t.Fatal("caller cancellation quarantined valid code", err, r.Status())
	}
	if out, err := r.Evaluate(map[string]any{"health": 42}); err != nil || out["health"] != float64(42) {
		t.Fatal("cancelled caller poisoned later admission", out, err)
	}
}
