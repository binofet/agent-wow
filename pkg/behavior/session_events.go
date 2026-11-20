package behavior

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

const (
	maxSessionEvents     = 256
	maxSessionEventBytes = 4 * DefaultSessionValueBytes
	maxPendingActions    = 256
)

var (
	ErrEventBackpressure = errors.New("behavior event queue full")
	ErrInvalidReceipt    = errors.New("invalid or duplicate behavior action receipt")
	ErrActionCapacity    = errors.New("behavior action ledger full")
)

// ActionOutcome separates client transport from server-confirmed effect. A
// later correlated result can resolve an unknown action; it cannot be retried.
type ActionOutcome string

const (
	ActionReserved          ActionOutcome = "reserved"
	ActionTransportAccepted ActionOutcome = "transport_accepted"
	ActionUnknown           ActionOutcome = "unknown"
	ActionConfirmed         ActionOutcome = "confirmed"
	ActionFailed            ActionOutcome = "failed"
	ActionNotDispatched     ActionOutcome = "not_dispatched"
	ActionSettledUnverified ActionOutcome = "settled_unverified"
)

func (o ActionOutcome) terminal() bool {
	return o == ActionConfirmed || o == ActionFailed || o == ActionNotDispatched || o == ActionSettledUnverified
}

// ActionReceipt binds an observation to a harness reservation in this Session.
// The adapter classifies Outcome and projects the native typed receipt into
// Payload. An RPC result or a packet write alone does not confirm an effect.
type ActionReceipt struct {
	SessionID string
	ActionID  uint64
	Outcome   ActionOutcome
	Payload   map[string]any
}

// Event is an observation or a correlated action receipt from this Session's
// adapter. Kind is a stable event name chosen by the adapter.
type Event struct {
	Kind    string
	Value   map[string]any
	Receipt *ActionReceipt
}

type pendingAction struct {
	applied  ActionOutcome
	queued   ActionOutcome
	accepted bool
}

func (p pendingAction) accepts(next ActionOutcome) bool {
	switch next {
	case ActionTransportAccepted:
		return p.queued == ActionReserved
	case ActionUnknown:
		return p.queued == ActionReserved || p.queued == ActionTransportAccepted
	case ActionConfirmed, ActionFailed, ActionSettledUnverified:
		return !p.queued.terminal()
	case ActionNotDispatched:
		return p.queued == ActionReserved || (p.queued == ActionUnknown && !p.accepted)
	default:
		return false
	}
}

type queuedEvent struct {
	sequence uint64
	body     map[string]any
	bytes    int
	actionID uint64
	outcome  ActionOutcome
}

func prepareEvent(event Event) (queuedEvent, error) {
	return prepareEventWithValues(event, DefaultSessionValueBytes)
}

func prepareEventWithValues(event Event, valueBytes int) (queuedEvent, error) {
	if event.Kind == "" || len(event.Kind) > 128 {
		return queuedEvent{}, errors.New("behavior event needs a bounded kind")
	}
	body := map[string]any{"kind": event.Kind, "value": objectOrEmpty(event.Value)}
	out := queuedEvent{}
	if r := event.Receipt; r != nil {
		out.actionID, out.outcome = r.ActionID, r.Outcome
		body["receipt"] = map[string]any{
			"action_id": strconv.FormatUint(r.ActionID, 10),
			"outcome":   string(r.Outcome),
			"payload":   objectOrEmpty(r.Payload),
		}
	}
	// Include the largest possible sequence in the budget before taking the
	// ingress lock. JSON round-tripping detaches buffers supplied by the host.
	body["sequence"] = "18446744073709551615"
	if err := validateSessionInput(body, valueBytes); err != nil {
		return queuedEvent{}, fmt.Errorf("invalid behavior event: %w", err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return queuedEvent{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&out.body); err != nil {
		return queuedEvent{}, err
	}
	out.bytes = len(raw)
	return out, nil
}

func objectOrEmpty(object map[string]any) map[string]any {
	if object == nil {
		return map[string]any{}
	}
	return object
}

// Enqueue detaches and queues an event without waiting for Lua. The sequence is
// allocated at ingress, in lock acquisition order. Backpressure leaves the
// event and receipt ledger untouched. The adapter should retain and retry the
// same event before admitting later traffic when ordering matters.
func (s *Session) Enqueue(event Event) (uint64, error) {
	row, err := prepareEventWithValues(event, s.options.ValueBytes)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrSessionClosed
	}
	if event.Receipt != nil {
		operation, ok := s.pending[row.actionID]
		if event.Receipt.SessionID != s.id || !ok || !operation.accepts(row.outcome) {
			return 0, ErrInvalidReceipt
		}
	}
	if len(s.queue) == maxSessionEvents || s.queueBytes+row.bytes > max(maxSessionEventBytes, 4*s.options.ValueBytes) {
		return 0, ErrEventBackpressure
	}
	if s.tail == ^uint64(0) {
		return 0, errors.New("behavior event sequence exhausted")
	}
	s.tail++
	row.sequence = s.tail
	row.body["sequence"] = strconv.FormatUint(row.sequence, 10)
	s.queue = append(s.queue, row)
	s.queueBytes += row.bytes
	if event.Receipt != nil {
		op := s.pending[row.actionID]
		op.queued = row.outcome
		op.accepted = op.accepted || row.outcome == ActionTransportAccepted
		s.pending[row.actionID] = op
	}
	return row.sequence, nil
}

// commitEvents requires s.mu. Enqueue may append, but cannot consume the prefix.
func (s *Session) commitEvents(count int) {
	for i, row := range s.queue[:count] {
		s.cursor = row.sequence
		s.queueBytes -= row.bytes
		if row.actionID != 0 {
			if row.outcome.terminal() {
				delete(s.pending, row.actionID)
			} else {
				op := s.pending[row.actionID]
				op.applied = row.outcome
				s.pending[row.actionID] = op
			}
		}
		s.queue[i] = queuedEvent{}
	}
	s.queue = s.queue[count:]
	if len(s.queue) == 0 {
		s.queue = nil
	}
}
