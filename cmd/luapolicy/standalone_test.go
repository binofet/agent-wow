package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func standalonePolicyFile(t *testing.T, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "standalone.lua")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStandaloneCLIUsesRealInputAndFreshStateWithoutPublishingExtraValues(t *testing.T) {
	path := standalonePolicyFile(t, `local calls=0
return {decide=function(input)
 calls=calls+1
 if input.required~="present" then error("actual input required") end
 return {kind="wait",calls=calls,guid=input.guid,memory=input.memory}
end}`)
	input := `{"required":"present","guid":"17379391279565179449","memory":{"stage":"observe"}}`
	for invocation := 0; invocation < 2; invocation++ {
		var stdout bytes.Buffer
		if err := run([]string{"--source", path}, strings.NewReader(input), &stdout); err != nil {
			t.Fatal("one-shot default evaluation rejected actual input", invocation, err)
		}
		decoder := json.NewDecoder(&stdout)
		var result map[string]any
		if err := decoder.Decode(&result); err != nil || len(result) != 4 || result["kind"] != "wait" ||
			result["calls"] != float64(1) || result["guid"] != "17379391279565179449" {
			t.Fatal("stateless invocation lost input, reused state or emitted another decision", invocation, result, err)
		}
		if memory, ok := result["memory"].(map[string]any); !ok || memory["stage"] != "observe" {
			t.Fatal("caller-owned task memory was not preserved", result)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			t.Fatal("CLI emitted more than one result", err)
		}
	}
}

func TestStandaloneCLIDefaultWatchdogReportsTheActualExecutionPhaseAndNoAction(t *testing.T) {
	if behavior.EvaluationLimit != 5*time.Millisecond || behavior.MaxJSONBytes != 32*1024 || behavior.DefaultSourceBytes != 8*1024*1024 {
		t.Fatal("standalone defaults changed")
	}
	for _, test := range []struct{ name, source string }{
		{"initializer", `while true do end
return {decide=function(input)return {action={kind="must_not_publish"}}end}`},
		{"decide", `return {decide=function(input)
 local proposed={action={kind="must_not_publish"}}
 while true do end
 return proposed
end}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := standalonePolicyFile(t, test.source)
			var stdout bytes.Buffer
			err := run([]string{"--source", path}, strings.NewReader(`{}`), &stdout)
			if !errors.Is(err, behavior.ErrExecutionBudget) || !errors.Is(err, context.DeadlineExceeded) || stdout.Len() != 0 {
				t.Fatal("runaway CLI lost its watchdog causes or published an action", err, stdout.String())
			}
			initializer := strings.HasPrefix(err.Error(), "chunk_initialize: ")
			decision := strings.HasPrefix(err.Error(), "decide: ")
			// A default-budget turn can expire before reaching decide. Preserve
			// that actual diagnostic; this is strictly a negative/no-result proof.
			if initializer == decision || test.name == "initializer" && !initializer ||
				initializer && strings.Contains(err.Error(), "decide: ") || decision && strings.Contains(err.Error(), "chunk_initialize: ") ||
				!strings.Contains(err.Error(), "(5ms configured)") {
				t.Fatal("watchdog attributed the fault to another phase or budget", err)
			}
		})
	}
}

func TestStandaloneCLIFaultsNeverPublishPartialActionsOrLastGoodResults(t *testing.T) {
	for _, test := range []struct{ name, source, diagnostic string }{
		{"syntax", `return {`, "invalid behavior policy"},
		{"initializer", `local action={kind="must_not_publish"}; error("initializer_fault")
return {decide=function(input)return action end}`, "chunk_initialize:"},
		{"decide", `return {decide=function(input)
 input.changed=true
 local action={kind="must_not_publish"}
 error("decision_fault")
 return action
end}`, "decide:"},
		{"missing_handler", `return {unrelated=function()return {action=true}end}`, "chunk_initialize:"},
		{"scalar_output", `return {decide=function(input)return 7 end}`, "decide must return a JSON object table"},
		{"array_output", `return {decide=function(input)return {"action"} end}`, "decide must return a JSON object table"},
		{"cyclic_output", `return {decide=function(input)local a={};a.self=a;return {action=a}end}`, "cyclic policy output"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := standalonePolicyFile(t, `return {decide=function(input)return {kind="previous_success"}end}`)
			var previous bytes.Buffer
			if err := run([]string{"--source", path}, strings.NewReader(`{}`), &previous); err != nil || !strings.Contains(previous.String(), "previous_success") {
				t.Fatal("healthy preceding stateless invocation failed", err, previous.String())
			}
			if err := os.WriteFile(path, []byte(test.source), 0600); err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			err := run([]string{"--source", path}, strings.NewReader(`{}`), &stdout)
			if err == nil || errors.Is(err, behavior.ErrExecutionBudget) || !strings.Contains(err.Error(), test.diagnostic) || stdout.Len() != 0 {
				t.Fatal("fault published an intermediate/old result or lost its actual cause", err, stdout.String())
			}
		})
	}
}

func TestStandaloneCLIRejectsMalformedOrOverbudgetInputBeforeInitializingLua(t *testing.T) {
	const sentinel = "invalid_input_reached_initializer"
	path := standalonePolicyFile(t, `error("`+sentinel+`");return {decide=function(input)return {action=true}end}`)
	nodes := `{"items":[` + strings.Repeat("0,", behavior.MaxInputNodes) + `0]}`
	if len(nodes) > behavior.MaxJSONBytes {
		t.Fatal("input-node fixture does not isolate the node limit")
	}
	for _, test := range []struct{ name, input string }{
		{"empty", ""}, {"malformed", `{"unterminated":`}, {"multiple", `{} {}`},
		{"array", `[]`}, {"string", `"text"`}, {"boolean", `true`},
		{"inexact_numeric_identity", `{"guid":9007199254740992}`},
		{"input_nodes", nodes},
		{"input_bytes", `{"text":"` + strings.Repeat("x", behavior.MaxJSONBytes) + `"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			err := run([]string{"--source", path}, strings.NewReader(test.input), &stdout)
			if err == nil || errors.Is(err, behavior.ErrExecutionBudget) || strings.Contains(err.Error(), sentinel) || stdout.Len() != 0 {
				t.Fatal("invalid input initialized Lua or produced a result", err, stdout.String())
			}
		})
	}
}

func TestStandaloneCLIValueAndConfiguredSourceByteBoundariesRemainExact(t *testing.T) {
	path := standalonePolicyFile(t, `return {decide=function(input)return input end}`)
	input := `{"text":"` + strings.Repeat("x", behavior.MaxJSONBytes-len(`{"text":""}`)) + `"}`
	if len(input) != 32*1024 {
		t.Fatal("value fixture did not hit the actual default byte boundary", len(input))
	}
	var stdout bytes.Buffer
	if err := run([]string{"--source", path}, strings.NewReader(input), &stdout); err != nil || stdout.String() != input+"\n" {
		t.Fatal("default exact JSON boundary or single framing newline changed", err, stdout.Len())
	}
	stdout.Reset()
	tooLarge := strings.Replace(input, `"}`, `x"}`, 1)
	if err := run([]string{"--source", path}, strings.NewReader(tooLarge), &stdout); err == nil || !strings.Contains(err.Error(), "bounded JSON input") || stdout.Len() != 0 {
		t.Fatal("one excess input byte escaped admission", err, stdout.Len())
	}
	outputPath := standalonePolicyFile(t, `return {decide=function(input)return {text="`+strings.Repeat("x", behavior.MaxJSONBytes)+`"}end}`)
	if err := run([]string{"--source", outputPath}, strings.NewReader(`{}`), &stdout); err == nil || errors.Is(err, behavior.ErrExecutionBudget) || !strings.Contains(err.Error(), "bound") || stdout.Len() != 0 {
		t.Fatal("oversized output produced a result or evaded the value limit", err, stdout.Len())
	}
	const limit = 256
	code := `return {decide=function(input)return {kind="wait"}end}`
	code += "\n--" + strings.Repeat("x", limit-len(code)-3)
	if len(code) != limit {
		t.Fatal("source fixture did not hit its configured boundary")
	}
	sourcePath := standalonePolicyFile(t, code)
	if err := run([]string{"--source", sourcePath, "--source-bytes", "256"}, strings.NewReader(`{}`), &stdout); err != nil {
		t.Fatal("configured exact source boundary was rejected", err)
	}
	if err := os.WriteFile(sourcePath, []byte(code+"x"), 0600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := run([]string{"--source", sourcePath, "--source-bytes", "256"}, strings.NewReader(`{}`), &stdout); err == nil || !strings.Contains(err.Error(), "configured limit 256 bytes") || stdout.Len() != 0 {
		t.Fatal("CLI read beyond its configured source limit", err, stdout.String())
	}
}
