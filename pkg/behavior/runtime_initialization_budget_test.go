package behavior

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRuntimeExplicitInitializationBudgetDoesNotRelaxEvaluation(t *testing.T) {
	r, err := NewWithInitializationLimit(SessionOptions{}, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := r.Reload("return {decide=function(input) while true do end end}", "")
	if err != nil {
		t.Fatal("healthy standalone validation did not load watchdog source", err)
	}
	_, err = r.Evaluate(map[string]any{})
	if !errors.Is(err, ErrExecutionBudget) || !strings.Contains(err.Error(), "(5ms configured)") {
		t.Fatal("initialization allowance changed the actual default evaluation deadline", err)
	}
	state := r.Status()
	if state.Evaluations != 1 || state.Failures != 0 || state.QuarantinedRevision != "" ||
		state.Revision != revision || state.LastGoodRevision != "" {
		t.Fatal("resource turn was retried, quarantined or called successful", state)
	}
}

func TestRuntimeInitializationBudgetRemainsBoundedAndDefaultsArePreserved(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		execution, initialization time.Duration
		budget                    string
	}{
		{"original default", 0, 0, "(5ms configured)"},
		{"existing configured", 20 * time.Millisecond, 0, "(20ms configured)"},
		{"explicit initialization", 0, 40 * time.Millisecond, "(40ms configured)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewWithInitializationLimit(SessionOptions{ExecutionLimit: tc.execution}, tc.initialization)
			if err != nil {
				t.Fatal(err)
			}
			source := "while true do end return {decide=function(input)return {} end}"
			if _, err = r.Reload(source, ""); !errors.Is(err, ErrExecutionBudget) || !strings.Contains(err.Error(), tc.budget) {
				t.Fatal("standalone initialization lost its actual bounded deadline", err)
			}
			state := r.Status()
			if state.Evaluations != 0 || state.Failures != 0 || state.QuarantinedRevision != "" || state.Revision != "" {
				t.Fatal("bounded initialization installed, evaluated or quarantined source", state)
			}
		})
	}
}

func TestRuntimeInitializationConfigurationRejectsInvalidBudgets(t *testing.T) {
	for _, tc := range []struct {
		options        SessionOptions
		initialization time.Duration
	}{
		{SessionOptions{}, -time.Nanosecond},
		{SessionOptions{ExecutionLimit: -time.Nanosecond}, time.Millisecond},
		{SessionOptions{ValueBytes: -1}, time.Millisecond},
		{SessionOptions{SourceBytes: -1}, time.Millisecond},
	} {
		if r, err := NewWithInitializationLimit(tc.options, tc.initialization); err == nil || r != nil {
			t.Fatal("invalid standalone initialization configuration accepted", r, err)
		}
	}
}
