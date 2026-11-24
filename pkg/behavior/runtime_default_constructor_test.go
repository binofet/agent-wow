package behavior

import (
	"testing"
	"time"
)

func TestNativeAndOrdinaryRuntimeConstructorDefaultsMatch(t *testing.T) {
	// starterSpellPolicy's lazy native route constructs New(). The ordinary
	// CLI normalizes its default source limit, then supplies zero value/time
	// overrides to NewWithOptions. Compare the actual constructor budgets;
	// this is not a wall-time or semantic allowance.
	native := New()
	limit, err := SourceLimit(0)
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := NewWithOptions(SessionOptions{SourceBytes: limit})
	if err != nil {
		t.Fatal(err)
	}
	for name, runtime := range map[string]*Runtime{"native": native, "ordinary": ordinary} {
		bytes, inputNodes, outputNodes := runtime.valueBudget()
		source, err := SourceLimit(runtime.sourceBytes)
		if err != nil || source != limit || runtime.evaluationBudget() != 5*time.Millisecond || runtime.initializationBudget() != 5*time.Millisecond ||
			bytes != 32*1024 || inputNodes != 4096 || outputNodes != 2048 || runtime.initializationLimit != 0 || runtime.executionLimit != 0 || runtime.valueBytes != 0 {
			t.Fatal("default constructor path changed its actual source/value/initialization/execution limits", name, source, bytes, inputNodes, outputNodes, err)
		}
		if runtime.active != nil || runtime.good != nil || runtime.Status().Revision != "" {
			t.Fatal("default constructor prepublished a policy", name)
		}
	}
}
