package modrt

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SessionPolicy binds a Lua runtime to one character session. The host owns
// all calls to Decide and Reload. Observations and receipts may arrive during
// reload and while an admitted action is executing.
type SessionPolicy struct {
	vm                 *behavior.Session
	manager            *Manager
	body               string
	source             string // guarded by advance
	builtin            string
	lifetime           context.Context
	advance            sync.Mutex
	dispatchDone       chan struct{}
	encounterLeaseHeld bool // guarded by advance
	followFallback     atomic.Pointer[policyFollowFallback]
	gearEvaluation     atomic.Pointer[policyFollowFallback]
	lastDispatch       atomic.Pointer[policyDispatchRecord]
}

// StartPolicy binds a runtime owned by the local host. The reflected attach
// method can inspect it, but cannot start or change it.
func (m *Manager) StartPolicy(ctx context.Context, source string) (*SessionPolicy, error) {
	return m.StartPolicyWithOptions(ctx, source, behavior.SessionOptions{})
}

// StartPolicyWithOptions binds a persistent VM with the configured Lua budget.
func (m *Manager) StartPolicyWithOptions(ctx context.Context, source string, options behavior.SessionOptions) (*SessionPolicy, error) {
	catalog, observer, err := m.observedSession()
	if err != nil {
		return nil, err
	}
	if observer != nil {
		return nil, status.Error(codes.AlreadyExists, "session already has a policy observer")
	}
	vm, err := behavior.NewSessionWithOptions(ctx, catalog.SessionID, source, options)
	if err != nil {
		return nil, err
	}
	host := &SessionPolicy{vm: vm, manager: m, body: catalog.CharacterGUID, lifetime: m.ctx, source: source, builtin: source}
	host.publishFollowFallback(source, vm.Status().Revision)
	host.publishGearEvaluation(source, vm.Status().Revision)
	m.mu.Lock()
	if m.policyObserver != nil || m.closing || m.preparing || m.prepared || m.err != nil || m.ctx.Err() != nil ||
		filepath.Base(m.dir) != catalog.SessionID || strconv.FormatUint(m.session.Identity.CharacterGUID, 10) != host.body {
		m.mu.Unlock()
		vm.Close()
		return nil, status.Error(codes.FailedPrecondition, "session changed while starting policy")
	}
	m.policyObserver = host.observe
	m.policy = host
	m.mu.Unlock()
	context.AfterFunc(m.ctx, host.close)
	return host, nil
}

func (p *SessionPolicy) close() {
	// A dispatch registers under advance. Once lifetime is canceled, no
	// further Step can register after this barrier.
	p.advance.Lock()
	done := p.dispatchDone
	p.advance.Unlock()
	if done != nil {
		<-done
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	_ = p.vm.Drain(ctx)
	p.vm.Close()
}

func (p *SessionPolicy) observe(_ context.Context, sessionID, guid string) (PolicySnapshot, error) {
	s, receipts := p.vm.Inspect()
	if s.SessionID != sessionID || p.body != guid || s.Closed {
		return PolicySnapshot{}, status.Error(codes.FailedPrecondition, "policy session is unavailable")
	}
	view := PolicySnapshot{Revision: s.Revision, Generation: strconv.FormatUint(s.Epoch, 10), Cursor: strconv.FormatUint(s.Cursor, 10), Faulted: s.Fault != ""}
	for _, receipt := range receipts {
		view.PendingActionIDs = append(view.PendingActionIDs, strconv.FormatUint(receipt.ActionID, 10))
	}
	return view, nil
}

// PolicyDispatch is the trusted host binding for a validated Lua proposal.
// A successful transport write must be returned as ActionTransportAccepted.
// The host must queue a separately correlated effect before reporting success.
type PolicyDispatch struct {
	Module string
	Send   func(context.Context) (behavior.ActionOutcome, map[string]any)
	spell  *policySpell
}

// Step advances the policy and dispatches at most one admitted action. Resolve
// only validates and prepares the typed action. Send is the first game write.
// Mutable world observations must arrive through Enqueue in source order.
// A nil resolver uses the production known-spell adapter. It retains receipts
// during backpressure. A custom resolver's caller must requeue its receipt
// after backpressure without resending the action.
func (p *SessionPolicy) Step(ctx context.Context, input map[string]any, resolve func(map[string]any) (PolicyDispatch, error)) (map[string]any, *behavior.ActionReceipt, error) {
	p.advance.Lock()
	if p.lifetime.Err() != nil {
		p.advance.Unlock()
		return nil, nil, behavior.ErrSessionClosed
	}
	if p.dispatchDone != nil {
		p.advance.Unlock()
		return nil, nil, status.Error(codes.FailedPrecondition, "policy action dispatch is in progress")
	}
	if err := p.flushSpellReceipts(ctx); err != nil {
		p.advance.Unlock()
		return nil, nil, err
	}
	decision, err := p.vm.Decide(ctx, input)
	if err != nil {
		p.advance.Unlock()
		return nil, nil, err
	}
	action, ok := decision.Output["action"].(map[string]any)
	if !ok {
		err = p.renewEncounterWriter(ctx, decision.Output, nil, PolicyDispatch{})
		p.advance.Unlock()
		return decision.Output, nil, err
	}
	if resolve == nil {
		resolve = p.resolveKnownSpell
	}
	dispatch, err := resolve(action)
	if err != nil || dispatch.Module == "" || dispatch.Send == nil {
		p.advance.Unlock()
		return decision.Output, nil, errors.Join(err, errors.New("policy action cannot be dispatched"))
	}
	if err := p.renewEncounterWriter(ctx, decision.Output, action, dispatch); err != nil {
		p.advance.Unlock()
		return decision.Output, nil, err
	}
	s := p.vm.Status()
	id, err := p.reserve(decision)
	if err == nil {
		p.dispatchDone = make(chan struct{})
	}
	p.advance.Unlock()
	if err != nil {
		return decision.Output, nil, err
	}
	defer func() {
		p.advance.Lock()
		close(p.dispatchDone)
		p.dispatchDone = nil
		p.advance.Unlock()
	}()
	origin := modsession.WriteOrigin{SessionID: s.SessionID, Module: dispatch.Module, Writer: "lua", PolicyRevision: s.Revision, Generation: s.Epoch, ActionID: strconv.FormatUint(id, 10)}
	dispatchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(p.lifetime, cancel)
	defer stop()
	outcome, payload := dispatch.Send(modsession.WithWriteOrigin(dispatchCtx, origin))
	p.publishDispatch(s, id, dispatch.Module, outcome, payload)
	receipt := &behavior.ActionReceipt{SessionID: s.SessionID, ActionID: id, Outcome: outcome, Payload: payload}
	if dispatch.spell != nil {
		err = p.recordSpellTransport(dispatch.spell, receipt)
	} else {
		_, err = p.Enqueue(behavior.Event{Kind: "action_result", Receipt: receipt})
	}
	return decision.Output, receipt, err
}

func (p *SessionPolicy) reserve(decision behavior.Decision) (uint64, error) {
	m := p.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing || m.preparing || m.prepared || m.err != nil {
		return 0, status.Error(codes.FailedPrecondition, "session is leaving the world")
	}
	return p.vm.ReserveAction(decision)
}

// Enqueue records a game observation or a result for a reserved action.
func (p *SessionPolicy) Enqueue(event behavior.Event) (uint64, error) {
	spells := &p.manager.policySpells
	spells.mu.Lock()
	defer spells.mu.Unlock()
	if err := spells.flush(p.vm); err != nil {
		return 0, err
	}
	return p.vm.Enqueue(event)
}

// Reload replaces the attached policy while the world session stays active.
func (p *SessionPolicy) Reload(ctx context.Context, source, expectedRevision string) (string, error) {
	p.advance.Lock()
	defer p.advance.Unlock()
	if p.lifetime.Err() != nil {
		return "", behavior.ErrSessionClosed
	}
	// A faulted VM may need a replacement to consume an ingress batch.
	// Keep backpressured receipts intact for the recovered VM.
	if err := p.flushSpellReceipts(ctx); err != nil && !errors.Is(err, behavior.ErrSessionFault) {
		return "", err
	}
	revision, err := p.vm.Reload(ctx, source, expectedRevision)
	if err == nil {
		p.source = source
		p.publishFollowFallback(source, revision)
		p.publishGearEvaluation(source, revision)
	}
	return revision, err
}
