package behavior

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	lua "github.com/yuin/gopher-lua"
)

// Compile without running the initializer: Reload correctly rejects a runaway
// candidate, but these diagnostics exercise both phases of actual evaluation.
func runtimePhasePolicy(t *testing.T, source string) *policy {
	t.Helper()
	L := newVM()
	defer L.Close()
	chunk, err := L.LoadString(source)
	if err != nil {
		t.Fatal(err)
	}
	return &policy{proto: chunk.Proto}
}

func TestRuntimeExecutionBudgetErrorsIdentifyActualLuaPhase(t *testing.T) {
	r := New()
	bytes, inputNodes, outputNodes := r.valueBudget()
	if EvaluationLimit != 5*time.Millisecond || r.evaluationBudget() != EvaluationLimit ||
		MaxJSONBytes != 32*1024 || bytes != MaxJSONBytes || inputNodes != MaxInputNodes || outputNodes != MaxNodes {
		t.Fatal("phase diagnostics changed the standalone execution or value defaults")
	}
	for _, test := range []struct {
		name, phase, other, source string
	}{
		{"initializer", "chunk_initialize: ", "decide: ", `while true do end
return {decide=function(input) input.child.value=999; return {value=input.child.value} end}`},
		{"decision", "decide: ", "chunk_initialize: ", `return {decide=function(input)
input.child.value=999
while true do end
end}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := map[string]any{"child": map[string]any{"value": 7}}
			original := map[string]any{"child": map[string]any{"value": 7}}
			out, err := evaluate(runtimePhasePolicy(t, test.source), input)
			if out != nil || !errors.Is(err, ErrExecutionBudget) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("runaway did not retain its actual default watchdog causes", out, err)
			}
			if !strings.HasPrefix(err.Error(), test.phase) || strings.Contains(err.Error(), test.other) {
				t.Fatal("execution failure identified the wrong Lua phase", err)
			}
			if !reflect.DeepEqual(input, original) {
				t.Fatal("failed Lua evaluation published its input mutation", input)
			}
		})
	}
}

func TestRuntimeLuaFaultErrorsRetainPhaseAndUnderlyingCause(t *testing.T) {
	for _, test := range []struct {
		name, phase, other, fault, source string
	}{
		{"initializer", "chunk_initialize: ", "decide: ", "initializer_fault", `error("initializer_fault")
return {decide=function(input) return {value=input.child.value} end}`},
		{"decision", "decide: ", "chunk_initialize: ", "decision_fault", `return {decide=function(input)
input.child.value=999
error("decision_fault")
end}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := map[string]any{"child": map[string]any{"value": 7}}
			original := map[string]any{"child": map[string]any{"value": 7}}
			out, err := evaluate(runtimePhasePolicy(t, test.source), input)
			var cause *lua.ApiError
			if out != nil || err == nil || !errors.As(err, &cause) ||
				errors.Is(err, ErrExecutionBudget) || errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("ordinary Lua fault lost its cause or became a budget failure", out, err)
			}
			if !strings.HasPrefix(err.Error(), test.phase) || strings.Contains(err.Error(), test.other) ||
				!strings.Contains(cause.Error(), test.fault) {
				t.Fatal("Lua fault lost its exact diagnostic phase or underlying fault", err)
			}
			if !reflect.DeepEqual(input, original) {
				t.Fatal("failed Lua evaluation published its input mutation", input)
			}
		})
	}
}
