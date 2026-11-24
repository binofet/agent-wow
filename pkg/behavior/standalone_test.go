package behavior

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStandaloneSourceMatchesAcceptedRuntimeAndKeepsInputDetached(t *testing.T) {
	// This is semantic parity, with an explicit healthy allowance. The default
	// CLI flight-frame tests continue to own their unchanged real 5 ms proof.
	options := SessionOptions{ExecutionLimit: 100 * time.Millisecond}
	source := `local initialized=1
return {decide=function(input)
 input.memory.pending.phase="local_only"
 return {memory=input.memory,body=input.body,initialized=initialized}
end}`
	input := map[string]any{"body": "17379391279565179449", "memory": map[string]any{"pending": map[string]any{"id": "one-unknown", "phase": "unknown"}}}
	before, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewWithOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reload(source, ""); err != nil {
		t.Fatal(err)
	}
	expected, err := r.Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := EvaluateSourceContext(context.Background(), source, input, options)
	if err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatal("one-shot changed complete semantic output", actual, expected, err)
	}
	after, err := json.Marshal(input)
	if err != nil || string(before) != string(after) {
		t.Fatal("one-shot mutated caller-owned pending state", err)
	}
	if EvaluationLimit != 5*time.Millisecond || MaxJSONBytes != 32*1024 || MaxInputNodes != 4096 || MaxNodes != 2048 {
		t.Fatal("standalone defaults changed")
	}
}

func TestStandaloneSourceValidatesInputBeforeAnyChunkExecution(t *testing.T) {
	source := `error("must_not_initialize") return {decide=function(input)return input end}`
	for _, input := range []map[string]any{
		{"text": strings.Repeat("x", MaxJSONBytes)},
		{"nodes": make([]any, MaxInputNodes)},
	} {
		out, err := EvaluateSourceContext(context.Background(), source, input, SessionOptions{})
		if out != nil || err == nil || !strings.Contains(err.Error(), "invalid behavior input:") || strings.Contains(err.Error(), "must_not_initialize") || errors.Is(err, ErrExecutionBudget) {
			t.Fatal("invalid input executed source or produced a partial result", out, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := EvaluateSourceContext(ctx, `while true do end return {decide=function(input)return input end}`, map[string]any{}, SessionOptions{})
	if out != nil || !errors.Is(err, context.Canceled) || errors.Is(err, ErrExecutionBudget) {
		t.Fatal("cancellation became a resource decision", out, err)
	}
}

func TestStandaloneSourceKeepsBoundsAndRejectsInvalidConfiguration(t *testing.T) {
	code := `return {decide=function(input)return {ok=true} end}`
	code += strings.Repeat(" ", 128-len(code))
	options := SessionOptions{SourceBytes: 128, ExecutionLimit: 100 * time.Millisecond}
	if out, err := EvaluateSourceContext(context.Background(), code, map[string]any{}, options); err != nil || out["ok"] != true {
		t.Fatal(out, err)
	}
	for _, source := range []string{"", code + " "} {
		if out, err := EvaluateSourceContext(context.Background(), source, map[string]any{}, options); err == nil || out != nil {
			t.Fatal("source bounds changed", out, err)
		}
	}
	for _, options := range []SessionOptions{{SourceBytes: -1}, {ValueBytes: -1}, {ExecutionLimit: -time.Nanosecond}} {
		if out, err := EvaluateSourceContext(context.Background(), code, map[string]any{}, options); err == nil || out != nil {
			t.Fatal("invalid standalone configuration accepted", out, err)
		}
	}
	// Output remains bounded independently from successful source execution.
	if out, err := EvaluateSourceContext(context.Background(), `return {decide=function(input)return {text="`+strings.Repeat("x", MaxJSONBytes)+`"}end}`, map[string]any{}, SessionOptions{ExecutionLimit: 100 * time.Millisecond}); err == nil || out != nil {
		t.Fatal("oversized output escaped the default value limit", out, err)
	}
}

func TestStandaloneSourceFaultsRetainActualExecutionPhaseAndNoResult(t *testing.T) {
	for _, tc := range []struct{ name, source, phase string }{
		{"initializer", `error("initializer_fault") return {decide=function(input)return {action="must_not_publish"}end}`, "chunk_initialize:"},
		{"decision", `return {decide=function(input)input.memory.pending="mutated";error("decision_fault")end}`, "decide:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := map[string]any{"memory": map[string]any{"pending": "unknown"}}
			before, _ := json.Marshal(input)
			out, err := EvaluateSourceContext(context.Background(), tc.source, input, SessionOptions{ExecutionLimit: 100 * time.Millisecond})
			after, _ := json.Marshal(input)
			if out != nil || err == nil || !strings.Contains(err.Error(), tc.phase) || errors.Is(err, ErrExecutionBudget) || string(before) != string(after) {
				t.Fatal("fault lost phase, published a result or changed pending input", out, err)
			}
		})
	}
}

func TestStandaloneDefaultWatchdogPublishesNoResourceDecision(t *testing.T) {
	for _, source := range []string{
		`while true do end return {decide=function(input)return {action="must_not_publish"}end}`,
		`return {decide=function(input)while true do end return {action="must_not_publish"}end}`,
	} {
		out, err := EvaluateSourceContext(context.Background(), source, map[string]any{}, SessionOptions{})
		if out != nil || !errors.Is(err, ErrExecutionBudget) || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "(5ms configured)") ||
			(!strings.HasPrefix(err.Error(), "chunk_initialize:") && !strings.HasPrefix(err.Error(), "decide:")) {
			t.Fatal("forced nontermination emitted a decision or lost the real default budget/phase", out, err)
		}
	}
}
