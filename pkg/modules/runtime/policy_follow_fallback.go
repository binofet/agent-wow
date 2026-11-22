package modrt

import (
	"context"
	"errors"
	"sync"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

const policyFollowEntryPoint = "follow_formation"

// Source identity and availability never change after publication. The cache
// belongs only to this accepted revision; it cannot restore an older policy.
type policyFollowFallback struct {
	source     string
	revision   string
	available  bool
	entryPoint string
	options    behavior.SessionOptions

	mu        sync.Mutex
	runtime   *behavior.Runtime
	attempted bool
	lastError string
}

type policyFollowFallbackMetadata struct {
	EntryPoint string `json:"entryPoint"`
	Available  bool   `json:"available"`
	Revision   string `json:"revision"`
	LastError  string `json:"lastError"`
}

// publishFollowFallback is called only after the persistent policy is accepted.
// Optional export facts come from that VM's load, without running its hooks.
func (p *SessionPolicy) publishFollowFallback(source, revision string) {
	p.followFallback.Store(&policyFollowFallback{source: source, revision: revision,
		available: p.vm.HasExport(policyFollowEntryPoint), entryPoint: policyFollowEntryPoint, options: p.vm.Options()})
}

func (p *SessionPolicy) followFallbackMetadata() policyFollowFallbackMetadata {
	view := policyFollowFallbackMetadata{EntryPoint: policyFollowEntryPoint}
	current := p.followFallback.Load()
	if current == nil {
		return view
	}
	view.Available, view.Revision = current.available, current.revision
	current.mu.Lock()
	view.LastError = current.lastError
	current.mu.Unlock()
	return view
}

// followDecision evaluates only the current source's pure optional export.
// It neither enters the persistent VM nor acquires advance, so it remains safe
// while Step is waiting for Actions. Callers must still validate body, visit,
// movement authority and the resulting proposal before any packet write.
func (p *SessionPolicy) followDecision(ctx context.Context, input map[string]any) (available bool, revision string, decision map[string]any, err error) {
	current := p.followFallback.Load()
	return p.readonlyExportDecision(ctx, current, func() bool { return p.followFallback.Load() == current }, input)
}

// A pure accepted-source export never enters the persistent VM or advance.
// Identity/revision fences are shared by reflected non-gameplay read adapters.
func (p *SessionPolicy) readonlyExportDecision(ctx context.Context, current *policyFollowFallback, stillCurrent func() bool, input map[string]any) (available bool, revision string, decision map[string]any, err error) {
	if current == nil {
		return false, "", nil, nil
	}
	available, revision = current.available, current.revision
	if err = ctx.Err(); err != nil {
		return available, revision, nil, err
	}
	if p.lifetime != nil && p.lifetime.Err() != nil {
		return available, revision, nil, behavior.ErrSessionClosed
	}
	currentVM := p.vm.Status()
	if currentVM.Closed || currentVM.Swapping || currentVM.Revision != revision {
		return available, revision, nil, errors.New("follow source publication is changing")
	}
	if !available {
		return false, revision, nil, nil
	}
	decision, err = current.evaluate(ctx, input)
	if err != nil {
		return true, revision, nil, err
	}
	if err = ctx.Err(); err != nil {
		return true, revision, nil, err
	}
	if p.lifetime != nil && p.lifetime.Err() != nil {
		return true, revision, nil, behavior.ErrSessionClosed
	}
	currentVM = p.vm.Status()
	if currentVM.Closed || currentVM.Swapping || currentVM.Revision != revision || !stillCurrent() {
		return true, revision, nil, errors.New("follow fallback policy changed during evaluation")
	}
	return true, revision, decision, nil
}

func (p *policyFollowFallback) evaluate(ctx context.Context, input map[string]any) (map[string]any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.attempted {
		p.attempted = true
		// The original source already passed the session's configured source
		// budget. Permit exactly its bytes plus this fixed enclosing wrapper.
		wrapped := "local policy = (function()\n" + p.source + "\nend)()\nreturn {decide=policy." + p.entryPoint + "}\n"
		options := p.options
		options.SourceBytes = len(wrapped)
		runtime, err := behavior.NewWithOptions(options)
		if err == nil {
			_, err = runtime.Reload(wrapped, "")
		}
		if err != nil {
			if errors.Is(err, behavior.ErrExecutionBudget) {
				// No pure runtime was published. Re-admit this accepted source
				// on the next normal read, without an inline retry.
				p.attempted = false
			}
			p.recordError(err)
			return nil, err
		}
		p.runtime = runtime
	}
	if p.runtime == nil {
		return nil, errors.New(p.lastError)
	}
	decision, err := p.runtime.EvaluateContext(ctx, input)
	p.recordError(err)
	if err != nil && p.runtime.Status().Revision == "" {
		p.runtime = nil // Retain this revision's original quarantine cause.
	}
	return decision, err
}

func (p *policyFollowFallback) recordError(err error) {
	p.lastError = ""
	if err != nil {
		p.lastError = err.Error()
		if len(p.lastError) > 512 {
			p.lastError = p.lastError[:512]
		}
	}
}
