package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func TestExplicitRuntimeEvaluationRetainsDefaultValidationAndDecisionBudgets(t *testing.T) {
	if behavior.EvaluationLimit != 5*time.Millisecond || behavior.MaxJSONBytes != 32*1024 {
		t.Fatal("native Runtime defaults changed")
	}
	path := filepath.Join(t.TempDir(), "runtime.lua")
	if err := os.WriteFile(path, []byte(`return {decide=function(input)return {guid=input.guid,memory=input.memory}end}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	input := `{"guid":"17379391279565179449","memory":{"pending":"one-unknown"}}`
	if err := run([]string{"-mode", "evaluate-runtime", "-source", path}, strings.NewReader(input), &out); err != nil || strings.TrimSpace(out.String()) != input {
		t.Fatal("ordinary Runtime mode lost exact scalar/pending output", out.String(), err)
	}
	if err := os.WriteFile(path, []byte(`while true do end return {decide=function(input)return {action="must_not_publish"}end}`), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err := run([]string{"-mode", "evaluate-runtime", "-source", path}, strings.NewReader(input), &out)
	if out.Len() != 0 || !errors.Is(err, behavior.ErrExecutionBudget) || !strings.Contains(err.Error(), "behavior initialization unavailable: chunk_initialize:") || !strings.Contains(err.Error(), "(5ms configured)") {
		t.Fatal("explicit mode bypassed real Runtime.Reload validation or published a resource result", out.String(), err)
	}
}

func TestExplicitRuntimeEvaluationDoesNotRetryOrPublishFaultedDecisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.lua")
	if err := os.WriteFile(path, []byte(`return {decide=function(input)input.memory.pending="local-only";error("decision_fault")end}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	// Isolate an ordinary Lua fault from machine timing; the positive and
	// forced-watchdog cases above retain the real default limits.
	err := run([]string{"-mode", "evaluate-runtime", "-source", path, "-execution-ms", "100"}, strings.NewReader(`{"memory":{"pending":"one-unknown"}}`), &out)
	if out.Len() != 0 || err == nil || !strings.Contains(err.Error(), "decide:") || !strings.Contains(err.Error(), "decision_fault") {
		t.Fatal("faulted Runtime mode published output or lost its actual failure", out.String(), err)
	}
	input := `{"text":"` + strings.Repeat("x", behavior.MaxJSONBytes) + `"}`
	out.Reset()
	if err := run([]string{"-mode", "evaluate-runtime", "-source", path}, strings.NewReader(input), &out); err == nil || out.Len() != 0 {
		t.Fatal("explicit Runtime mode widened the default JSON bound", out.String(), err)
	}
}
