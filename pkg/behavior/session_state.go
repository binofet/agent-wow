package behavior

import "context"

// DecisionSnapshot is committed with the checkpoint exported by that decision.
// Its cursor can precede the current cursor when events have since been drained.
type DecisionSnapshot struct {
	Revision   string         `json:"revision"`
	Generation string         `json:"generation"`
	Cursor     string         `json:"cursor"`
	Clock      ClockReading   `json:"clock"`
	Output     map[string]any `json:"output"`
}

// SessionState describes one committed boundary without running Lua or draining
// events. Checkpoint and Output are detached copies, never writable VM state.
type SessionState struct {
	SourceLimitBytes int
	Status           SessionStatus
	Checkpoint       map[string]any
	LatestDecision   *DecisionSnapshot
	Clock            ClockReading
	Pending          []ActionReceipt
}

func (s *Session) State(ctx context.Context) (SessionState, error) {
	if err := ctx.Err(); err != nil {
		return SessionState{}, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return SessionState{}, ErrSessionClosed
	}
	now := s.clockNow()
	out := SessionState{SourceLimitBytes: s.options.SourceBytes, Status: s.statusLocked(), Pending: s.outstandingLocked(),
		Clock: ClockReading{MonotonicMs: max(0, now.Sub(s.clockStart).Milliseconds()), UnixMs: now.UnixMilli()}}
	checkpoint, latest := s.state, s.latest
	s.mu.Unlock()
	// Published values are immutable. Copy outside the ingress lock so a large
	// legitimate checkpoint does not prevent observations from being enqueued.
	out.Checkpoint = cloneSessionObject(checkpoint)
	if latest != nil {
		copy := *latest
		copy.Output = cloneSessionObject(latest.Output)
		out.LatestDecision = &copy
	}
	if err := ctx.Err(); err != nil {
		return SessionState{}, err
	}
	return out, nil
}

// SourceLimitBytes is immutable for the lifetime of this session and every reload.
func (s *Session) SourceLimitBytes() int { return s.options.SourceBytes }

// Session VM values have already passed the configured JSON value budget and
// contain only detached objects, arrays and immutable JSON scalar values.
func cloneSessionObject(value map[string]any) map[string]any {
	copy := make(map[string]any, len(value))
	for key, entry := range value {
		copy[key] = cloneSessionValue(entry)
	}
	return copy
}

func cloneSessionValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneSessionObject(value)
	case []any:
		copy := make([]any, len(value))
		for index, entry := range value {
			copy[index] = cloneSessionValue(entry)
		}
		return copy
	default:
		return value
	}
}
