package modrt

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SessionPolicy binds a Lua runtime to one character session. The host owns
// all calls to Decide and Reload. Observations and receipts may arrive during
// reload and while an admitted action is executing.
type SessionPolicy struct {
	vm         *behavior.Session
	body       string
	lifetime   context.Context
	advance    sync.Mutex
	dispatches sync.WaitGroup
}

// StartPolicy binds a runtime owned by the local host. The reflected attach
// method can inspect it, but cannot start or change it.
func (m *Manager) StartPolicy(ctx context.Context, source string) (*SessionPolicy, error) {
	catalog, observer, err := m.observedSession()
	if err != nil {
		return nil, err
	}
	if observer != nil {
		return nil, status.Error(codes.AlreadyExists, "session already has a policy observer")
	}
	vm, err := behavior.NewSession(ctx, catalog.SessionID, source)
	if err != nil {
		return nil, err
	}
	host := &SessionPolicy{vm: vm, body: catalog.CharacterGUID, lifetime: m.ctx}
	m.mu.Lock()
	if m.policyObserver != nil || m.closing || m.preparing || m.prepared || m.err != nil || m.ctx.Err() != nil ||
		filepath.Base(m.dir) != catalog.SessionID || strconv.FormatUint(m.session.Identity.CharacterGUID, 10) != host.body {
		m.mu.Unlock()
		vm.Close()
		return nil, status.Error(codes.FailedPrecondition, "session changed while starting policy")
	}
	m.policyObserver = host.observe
	m.mu.Unlock()
	context.AfterFunc(m.ctx, host.close)
	return host, nil
}

func (p *SessionPolicy) close() {
	// A dispatch registers under advance. Once lifetime is canceled, no
	// further Step can register after this barrier.
	p.advance.Lock()
	p.advance.Unlock()
	p.dispatches.Wait()
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
}

// Step advances the policy and dispatches at most one admitted action. Resolve
// only validates and prepares the typed action. Send is the first game write.
// Mutable world observations must arrive through Enqueue in source order.
// If event ingress is full, the returned receipt must be retried via Enqueue
// before accepting the next event. The action itself must never be resent.
func (p *SessionPolicy) Step(ctx context.Context, input map[string]any, resolve func(map[string]any) (PolicyDispatch, error)) (map[string]any, *behavior.ActionReceipt, error) {
	p.advance.Lock()
	if p.lifetime.Err() != nil {
		p.advance.Unlock()
		return nil, nil, behavior.ErrSessionClosed
	}
	decision, err := p.vm.Decide(ctx, input)
	if err != nil {
		p.advance.Unlock()
		return nil, nil, err
	}
	action, ok := decision.Output["action"].(map[string]any)
	if !ok {
		p.advance.Unlock()
		return decision.Output, nil, nil
	}
	if resolve == nil {
		p.advance.Unlock()
		return decision.Output, nil, errors.New("no typed policy action resolver")
	}
	dispatch, err := resolve(action)
	if err != nil || dispatch.Module == "" || dispatch.Send == nil {
		p.advance.Unlock()
		return decision.Output, nil, errors.Join(err, errors.New("policy action cannot be dispatched"))
	}
	s := p.vm.Status()
	id, err := p.vm.ReserveAction(decision)
	if err == nil {
		p.dispatches.Add(1)
	}
	p.advance.Unlock()
	if err != nil {
		return decision.Output, nil, err
	}
	defer p.dispatches.Done()
	origin := modsession.WriteOrigin{SessionID: s.SessionID, Module: dispatch.Module, Writer: "lua", PolicyRevision: s.Revision, Generation: s.Epoch, ActionID: strconv.FormatUint(id, 10)}
	dispatchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(p.lifetime, cancel)
	defer stop()
	outcome, payload := dispatch.Send(modsession.WithWriteOrigin(dispatchCtx, origin))
	receipt := &behavior.ActionReceipt{SessionID: s.SessionID, ActionID: id, Outcome: outcome, Payload: payload}
	_, err = p.Enqueue(behavior.Event{Kind: "action_result", Receipt: receipt})
	return decision.Output, receipt, err
}

// Enqueue records a game observation or a result for a reserved action.
func (p *SessionPolicy) Enqueue(event behavior.Event) (uint64, error) {
	return p.vm.Enqueue(event)
}

// Reload replaces the attached policy while the world session stays active.
func (p *SessionPolicy) Reload(ctx context.Context, source, expectedRevision string) (string, error) {
	p.advance.Lock()
	defer p.advance.Unlock()
	if p.lifetime.Err() != nil {
		return "", behavior.ErrSessionClosed
	}
	return p.vm.Reload(ctx, source, expectedRevision)
}
