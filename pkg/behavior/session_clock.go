package behavior

import "time"

// ClockReading is an observation, not a scheduler. MonotonicMs measures elapsed
// time in this Session, including time between callbacks and during reloads.
// UnixMs is wall time for correlation; use MonotonicMs for Lua deadlines and do
// not compare monotonic readings from different sessions.
type ClockReading struct {
	MonotonicMs int64 `json:"monotonicMs"`
	UnixMs      int64 `json:"unixMs"`
}

func (c ClockReading) frame() map[string]any {
	return map[string]any{"monotonicMs": float64(c.MonotonicMs), "unixMs": float64(c.UnixMs)}
}

// Clock reads the same clock supplied to persistent Lua decisions. Its origin
// belongs to the host Session and is never replaced with the Lua VM.
func (s *Session) Clock() (ClockReading, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ClockReading{}, ErrSessionClosed
	}
	now := s.clockNow()
	return ClockReading{MonotonicMs: max(0, now.Sub(s.clockStart).Milliseconds()), UnixMs: now.UnixMilli()}, nil
}

func newSessionClock() (time.Time, func() time.Time) { return time.Now(), time.Now }
