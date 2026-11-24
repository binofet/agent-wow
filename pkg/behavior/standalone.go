package behavior

import (
	"context"
	"fmt"
	"strings"
)

// EvaluateSourceContext performs one stateless evaluation. Compilation remains
// source-bounded; the existing cumulative execution budget covers the complete
// fresh chunk initialization and decide call. There is no candidate publication,
// validation execution, retry or last-good source to fall back to.
func EvaluateSourceContext(ctx context.Context, source string, input map[string]any, options SessionOptions) (map[string]any, error) {
	r, err := NewWithOptions(options)
	if err != nil {
		return nil, err
	}
	limit, _ := SourceLimit(r.sourceBytes)
	if len(source) == 0 || len(source) > limit {
		return nil, fmt.Errorf("policy source must contain 1..%d bytes (configured source budget)", limit)
	}
	byteLimit, inputNodes, outputNodes := r.valueBudget()
	if _, err := toLua(nil, input, &budget{ctx: ctx, byteLimit: byteLimit, nodeLimit: inputNodes}); err != nil {
		return nil, fmt.Errorf("invalid behavior input: %w", err)
	}
	if err := boundedJSONSize(input, byteLimit); err != nil {
		return nil, fmt.Errorf("invalid behavior input: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	L := newVM()
	fn, err := L.Load(strings.NewReader(source), "policy")
	L.Close()
	if err != nil {
		return nil, fmt.Errorf("invalid behavior policy: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return evaluateWithLimits(ctx, &policy{proto: fn.Proto}, input, byteLimit, inputNodes, outputNodes, r.evaluationBudget())
}
