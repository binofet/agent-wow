package modrt

import "context"

const policyStarterSpellEntryPoint = "starter_spell"

func (p *SessionPolicy) publishStarterSpell(source, revision string) {
	p.starterSpell.Store(&policyFollowFallback{source: source, revision: revision,
		entryPoint: policyStarterSpellEntryPoint, available: p.vm.HasExport(policyStarterSpellEntryPoint), options: p.vm.Options()})
}

func (p *SessionPolicy) starterSpellDecision(ctx context.Context, input map[string]any) (bool, string, map[string]any, error) {
	current := p.starterSpell.Load()
	return p.readonlyExportDecision(ctx, current, func() bool { return p.starterSpell.Load() == current }, input)
}

func (p *SessionPolicy) starterSpellMetadata() policyFollowFallbackMetadata {
	view := policyFollowFallbackMetadata{EntryPoint: policyStarterSpellEntryPoint}
	current := p.starterSpell.Load()
	if current == nil {
		return view
	}
	view.Available, view.Revision = current.available, current.revision
	current.mu.Lock()
	view.LastError = current.lastError
	current.mu.Unlock()
	return view
}
