package modrt

import "context"

const policyDisposalEntryPoint = "disposal_evaluation"

// publishDisposalEvaluation publishes only an accepted source's optional pure
// classification export. Its result grants no native action or packet authority.
func (p *SessionPolicy) publishDisposalEvaluation(source, revision string) {
	p.disposalEvaluation.Store(&policyFollowFallback{source: source, revision: revision,
		entryPoint: policyDisposalEntryPoint, available: p.vm.HasExport(policyDisposalEntryPoint), options: p.vm.Options()})
}

func (p *SessionPolicy) disposalDecision(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
	current := p.disposalEvaluation.Load()
	return p.readonlyExportDecision(ctx, current, func() bool { return p.disposalEvaluation.Load() == current }, input)
}
