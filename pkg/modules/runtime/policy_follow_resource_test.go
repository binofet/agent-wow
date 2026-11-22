package modrt

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

const resourceFollowSource = `return {follow_formation=function(input)
 if input.loop then input.value=999; while true do end end
 return {value=input.value}
end}`

func TestPolicyPureResourceInitializationDoesNotPermanentlyDisableCache(t *testing.T) {
	cache := &policyFollowFallback{source: resourceFollowSource, revision: "same-accepted-source",
		entryPoint: policyFollowEntryPoint, options: behavior.SessionOptions{ExecutionLimit: time.Nanosecond}}
	out, err := cache.evaluate(context.Background(), map[string]any{"value": 7})
	if out != nil || !errors.Is(err, behavior.ErrExecutionBudget) || !strings.Contains(err.Error(), "chunk_initialize:") || cache.attempted || cache.runtime != nil {
		t.Fatal("initial resource miss was cached as a permanent source failure", out, err, cache)
	}
	// Declared limits control both paths deterministically: the first profile
	// has no usable allowance. No wall-clock scheduling assumption is needed.
	cache.options.ExecutionLimit = 50 * time.Millisecond
	out, err = cache.evaluate(context.Background(), map[string]any{"value": 7})
	if err != nil || out["value"] != float64(7) || !cache.attempted || cache.runtime == nil || cache.revision != "same-accepted-source" || cache.lastError != "" {
		t.Fatal("next normal read could not initialize the same source", out, err, cache)
	}
}

func TestPolicyPureResourceDecisionRecoversOnNormalNextInputWithoutReload(t *testing.T) {
	cache := &policyFollowFallback{source: resourceFollowSource, revision: "same-accepted-source",
		entryPoint: policyFollowEntryPoint, options: behavior.SessionOptions{ExecutionLimit: 50 * time.Millisecond}}
	input := map[string]any{"value": 7}
	if out, err := cache.evaluate(context.Background(), input); err != nil || out["value"] != float64(7) {
		t.Fatal(out, err)
	}
	runtime := cache.runtime
	input["loop"] = true
	out, err := cache.evaluate(context.Background(), input)
	if out != nil || !errors.Is(err, behavior.ErrExecutionBudget) || cache.runtime != runtime || input["value"] != 7 || cache.runtime.Status().Failures != 0 {
		t.Fatal("resource turn published a result/mutation or disabled accepted code", out, err, cache)
	}
	delete(input, "loop")
	if out, err := cache.evaluate(context.Background(), input); err != nil || out["value"] != float64(7) || cache.runtime != runtime || cache.lastError != "" {
		t.Fatal("normal next input did not recover the existing pure cache", out, err, cache)
	}
}

func TestPolicyPureRealInitializationFaultRemainsSticky(t *testing.T) {
	cache := &policyFollowFallback{source: `error("real_initializer_fault")`,
		entryPoint: policyFollowEntryPoint, options: behavior.SessionOptions{ExecutionLimit: 50 * time.Millisecond}}
	for range 2 {
		out, err := cache.evaluate(context.Background(), nil)
		if out != nil || err == nil || errors.Is(err, behavior.ErrExecutionBudget) || !strings.Contains(err.Error(), "real_initializer_fault") || !cache.attempted || cache.runtime != nil {
			t.Fatal("actual Lua fault became retryable or lost its cause", out, err, cache)
		}
	}
}
