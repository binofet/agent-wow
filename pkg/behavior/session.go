package behavior

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

const sessionCatchupLimit = 500 * time.Millisecond

var (
	ErrSessionClosed = errors.New("behavior session closed")
	ErrSessionFault  = errors.New("behavior session policy faulted")
	ErrFenced        = errors.New("behavior writer fenced")
)

// SessionStatus identifies the current revision and the state of the Go-owned
// queue and action ledger. Fault fences action reservation until a valid reload.
type SessionStatus struct {
	SessionID string
	Revision  string
	Epoch     uint64
	Cursor    uint64
	Buffered  int
	Pending   int
	Swapping  bool
	Closed    bool
	Fault     string
}

// Decision contains a detached Lua object and a one-use admission ticket
// scoped to the Session and its current writer epoch.
type Decision struct {
	Output map[string]any
	owner  *Session
	epoch  uint64
	serial uint64
}

// Session owns a persistent Lua policy for one connected character. It is
// independent of the legacy per-evaluation Runtime, whose contract is unchanged.
// A Session is not a transport and never submits an action by itself.
type Session struct {
	id       string
	step     chan struct{} // VM execution, replacement, and close
	mu       sync.Mutex    // ingress, status, and action reservation
	vm       *sessionVM
	state    map[string]any // detached last successfully exported state
	rev      string
	epoch    uint64
	cursor   uint64
	tail     uint64
	swapping bool
	staging  bool
	closed   bool
	fault    error

	queue      []queuedEvent
	queueBytes int
	pending    map[uint64]pendingAction
	nextAction uint64
	serial     uint64
	admissible uint64
}

// NewSession loads a Lua bundle with decide, on_event, export_state and
// import_state functions. export_state returns {version, data}; the session
// checkpoints this detached envelope after each committed policy call.
func NewSession(ctx context.Context, sessionID, source string) (*Session, error) {
	if sessionID == "" || len(sessionID) > 128 || !utf8.ValidString(sessionID) {
		return nil, errors.New("behavior session needs a bounded session id")
	}
	vm, err := newSessionVM(ctx, source)
	if err != nil {
		return nil, err
	}
	state, err := vm.exportState(ctx)
	if err != nil {
		vm.close()
		return nil, fmt.Errorf("initial policy export: %w", err)
	}
	return &Session{
		id: sessionID, step: make(chan struct{}, 1), vm: vm, state: state, rev: sourceRevision(source), epoch: 1,
		pending: make(map[uint64]pendingAction),
	}, nil
}

func sourceRevision(source string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
}

func (s *Session) enter(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.step <- struct{}{}:
		if err := ctx.Err(); err != nil {
			s.leave()
			return err
		}
		return nil
	}
}

func (s *Session) leave() { <-s.step }

func (s *Session) Status() SessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

// Inspect ties the revision and pending receipts to the same observation.
func (s *Session) Inspect() (SessionStatus, []ActionReceipt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked(), s.outstandingLocked()
}

func (s *Session) statusLocked() SessionStatus {
	out := SessionStatus{
		SessionID: s.id, Revision: s.rev, Epoch: s.epoch, Cursor: s.cursor, Buffered: len(s.queue),
		Pending: len(s.pending), Swapping: s.swapping, Closed: s.closed,
	}
	if s.fault != nil {
		out.Fault = s.fault.Error()
	}
	return out
}

// Outstanding provides the committed lifecycle status of each unretired
// reservation, in action ID order. Buffered receipts remain ordered events.
func (s *Session) Outstanding() []ActionReceipt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outstandingLocked()
}

func (s *Session) outstandingLocked() []ActionReceipt {
	out := make([]ActionReceipt, 0, len(s.pending))
	for id, operation := range s.pending {
		out = append(out, ActionReceipt{SessionID: s.id, ActionID: id, Outcome: operation.applied})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ActionID < out[j].ActionID })
	return out
}

// pendingFrameLocked uses decimal strings so Lua never rounds uint64 IDs.
func (s *Session) pendingFrameLocked() []any {
	state := make([]any, 0, len(s.pending))
	for _, r := range s.outstandingLocked() {
		state = append(state, map[string]any{
			"action_id": strconv.FormatUint(r.ActionID, 10), "outcome": string(r.Outcome),
		})
	}
	return state
}

// Decide runs on the current persistent VM after consuming a bounded prefix of
// ingress. An output action object is a proposal. The host must validate it and
// reserve immediately before accepting one dispatch. Lua should advance
// effect-dependent state from receipts rather than from proposing an action.
func (s *Session) Decide(ctx context.Context, input map[string]any) (Decision, error) {
	frame := map[string]any{"session_id": s.id, "cursor": "18446744073709551615", "input": objectOrEmpty(input)}
	if err := validateInput(frame); err != nil {
		return Decision{}, fmt.Errorf("invalid behavior decision input: %w", err)
	}
	s.mu.Lock()
	swapping := s.swapping
	s.mu.Unlock()
	if swapping {
		return Decision{}, ErrFenced
	}
	if err := s.enter(ctx); err != nil {
		return Decision{}, err
	}
	defer s.leave()
	if err := s.drain(ctx); err != nil {
		return Decision{}, err
	}
	s.mu.Lock()
	frame["cursor"] = strconv.FormatUint(s.cursor, 10)
	frame["pending"] = s.pendingFrameLocked()
	s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	output, err := s.vm.decide(ctx, frame)
	if err != nil {
		return Decision{}, s.fail(err)
	}
	var hasAction bool
	if raw, exists := output["action"]; exists {
		action, valid := raw.(map[string]any)
		if !valid || len(action) == 0 {
			return Decision{}, s.fail(errors.New("policy action must be a nonempty object"))
		}
		hasAction = true
	}
	state, err := s.vm.exportState(ctx)
	if err != nil {
		return Decision{}, s.fail(err)
	}
	s.state = state
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Decision{Output: output}
	if hasAction {
		if s.serial == ^uint64(0) {
			return Decision{}, errors.New("behavior decision sequence exhausted")
		}
		s.serial++
		s.admissible = s.serial
		out.owner, out.epoch, out.serial = s, s.epoch, s.serial
	}
	return out, nil
}

// ReserveAction consumes the current ticket exactly once and allocates the ID
// accepted by the receipt adapter. This is the dispatch linearization point.
// A reservation made before a reload remains outstanding even if the send or
// result arrives after handoff. The host must dispatch only once per ID and
// report a definitive unsent reservation as ActionNotDispatched.
func (s *Session) ReserveAction(decision Decision) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrSessionClosed
	}
	if s.swapping || s.fault != nil || s.tail != s.cursor || decision.owner != s || decision.epoch != s.epoch ||
		decision.serial == 0 || decision.serial != s.admissible {
		return 0, ErrFenced
	}
	if len(s.pending) == maxPendingActions {
		return 0, ErrActionCapacity
	}
	if s.nextAction == ^uint64(0) {
		return 0, errors.New("behavior action sequence exhausted")
	}
	s.nextAction++
	s.admissible = 0
	s.pending[s.nextAction] = pendingAction{applied: ActionReserved, queued: ActionReserved}
	return s.nextAction, nil
}

// Drain applies the events currently buffered for the active VM. New ingress
// remains available and is processed at the next boundary. A failed callback
// leaves the unapplied suffix queued and fences the possibly modified VM.
func (s *Session) Drain(ctx context.Context) error {
	if err := s.enter(ctx); err != nil {
		return err
	}
	defer s.leave()
	return s.drain(ctx)
}

func (s *Session) drain(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrSessionClosed
	}
	if s.fault != nil {
		err := errors.Join(ErrSessionFault, s.fault)
		s.mu.Unlock()
		return err
	}
	s.admissible = 0
	count := len(s.queue)
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, sessionCatchupLimit)
	defer cancel()
	for range count {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		row := s.queue[0]
		s.mu.Unlock()
		state, err := applyEvent(ctx, s.vm, row)
		if err != nil {
			return s.fail(err)
		}
		s.state = state
		s.mu.Lock()
		s.commitEvents(1)
		s.mu.Unlock()
	}
	return ctx.Err()
}

func applyEvent(ctx context.Context, vm *sessionVM, row queuedEvent) (map[string]any, error) {
	if err := vm.onEvent(ctx, row.body); err != nil {
		return nil, fmt.Errorf("event %d: %w", row.sequence, err)
	}
	return vm.exportState(ctx)
}

func (s *Session) fail(err error) error {
	s.mu.Lock()
	s.fault = err
	s.admissible = 0
	s.mu.Unlock()
	s.vm.close()
	return errors.Join(ErrSessionFault, err)
}

// Close fences proposals and releases the VM. Integrators should detach
// observers and reconcile outstanding effects before discarding the Session.
func (s *Session) Close() {
	s.step <- struct{}{}
	defer s.leave()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.admissible = 0
	s.mu.Unlock()
	s.vm.close()
}
