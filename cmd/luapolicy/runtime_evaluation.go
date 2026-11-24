package main

import "github.com/agent-wow/agent-wow/pkg/behavior"

// This explicit mode exercises the fresh Runtime lifecycle used by native
// adapters: bounded source validation, then full fresh initialization+decide.
// It neither changes the default evaluator mode nor grants a separate budget.
func evaluateRuntimeSource(source string, input map[string]any, options behavior.SessionOptions) (map[string]any, error) {
	r, err := behavior.NewWithOptions(options)
	if err != nil {
		return nil, err
	}
	if _, err := r.Reload(source, ""); err != nil {
		return nil, err
	}
	return r.Evaluate(input)
}
