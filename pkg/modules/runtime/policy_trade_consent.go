package modrt

import "context"

const policyTradeConsentEntryPoint = "trade_consent"

func (p *SessionPolicy) publishTradeConsent(source, revision string) {
	p.tradeConsent.Store(&policyFollowFallback{source: source, revision: revision,
		entryPoint: policyTradeConsentEntryPoint, available: p.vm.HasExport(policyTradeConsentEntryPoint), options: p.vm.Options()})
}

func (p *SessionPolicy) tradeConsentDecision(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
	current := p.tradeConsent.Load()
	return p.readonlyExportDecision(ctx, current, func() bool { return p.tradeConsent.Load() == current }, input)
}

func (p *SessionPolicy) tradeConsentMetadata() policyFollowFallbackMetadata {
	view := policyFollowFallbackMetadata{EntryPoint: policyTradeConsentEntryPoint}
	current := p.tradeConsent.Load()
	if current == nil {
		return view
	}
	view.Available, view.Revision = current.available, current.revision
	current.mu.Lock()
	view.LastError = current.lastError
	current.mu.Unlock()
	return view
}
