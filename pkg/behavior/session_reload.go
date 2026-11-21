package behavior

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// Reload compiles a candidate in isolation and compare-and-swaps the policy at
// the next decision boundary. Ingress is available throughout compilation,
// import and catch-up. Rejection retains the active revision, committed state and
// unapplied queue. A rejected candidate that reached the fence still retires
// previously issued decision tickets.
//
// A bundle is qualified by its hooks, import and event replay. The first real
// decision runs only on actual input. A failed active callback fences its VM;
// a subsequent reload can recover from the last committed export.
func (s *Session) Reload(ctx context.Context, source, expectedRevision string) (string, error) {
	return s.reload(ctx, source, expectedRevision, nil)
}

func (s *Session) reload(ctx context.Context, source, expectedRevision string, afterFence func()) (string, error) {
	s.mu.Lock()
	rev, epoch := s.rev, s.epoch
	if s.closed {
		s.mu.Unlock()
		return rev, ErrSessionClosed
	}
	if rev != expectedRevision || s.swapping || s.staging {
		s.mu.Unlock()
		return rev, ErrRevisionConflict
	}
	s.staging = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.staging = false
		s.mu.Unlock()
	}()

	candidate, err := newSessionVMWithValues(ctx, source, s.options.ExecutionLimit, s.options.ValueBytes)
	if err != nil {
		return s.Status().Revision, err
	}
	committed := false
	defer func() {
		if !committed {
			candidate.close()
		}
	}()
	version := sourceRevision(source)

	if err := s.enter(ctx); err != nil {
		return s.Status().Revision, err
	}
	defer s.leave()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return s.rev, ErrSessionClosed
	}
	if s.rev != rev || s.epoch != epoch {
		s.mu.Unlock()
		return s.rev, ErrRevisionConflict
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return rev, err
	}
	if s.epoch == ^uint64(0) {
		s.mu.Unlock()
		return rev, errors.New("behavior writer epoch exhausted")
	}
	s.epoch++
	s.swapping = true
	s.admissible = 0

	// The exported object is detached and belongs to the committed old VM.
	// Calling the old export hook here could mutate the policy before rollback.
	input := map[string]any{
		"session_id": s.id,
		"cursor":     strconv.FormatUint(s.cursor, 10),
		"version":    s.state["version"],
		"data":       s.state["data"],
		"pending":    s.pendingFrameLocked(),
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.swapping = false
		s.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(ctx, sessionCatchupLimit)
	defer cancel()
	if afterFence != nil {
		afterFence()
	}
	if err := candidate.importState(ctx, input); err != nil {
		return rev, fmt.Errorf("import candidate policy: %w", err)
	}
	state, err := candidate.exportState(ctx)
	if err != nil {
		return rev, fmt.Errorf("export candidate policy: %w", err)
	}

	// Retain the queue until publication succeeds. Each iteration takes one
	// bounded prefix without holding the ingress mutex while running Lua.
	for consumed := 0; ; {
		s.mu.Lock()
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return rev, fmt.Errorf("candidate catch-up: %w", err)
		}
		if consumed == len(s.queue) {
			old := s.vm
			s.vm, s.state, s.rev, s.fault = candidate, state, version, nil
			s.latest = nil // the replacement has not made a decision yet
			s.commitEvents(consumed)
			s.swapping = false
			committed = true
			s.mu.Unlock()
			old.close()
			return version, nil
		}
		batch := append([]queuedEvent(nil), s.queue[consumed:]...)
		s.mu.Unlock()
		for _, row := range batch {
			if err := ctx.Err(); err != nil {
				return rev, fmt.Errorf("candidate catch-up: %w", err)
			}
			state, err = applyEvent(ctx, candidate, row)
			if err != nil {
				return rev, fmt.Errorf("replay candidate policy: %w", err)
			}
			consumed++
		}
	}
}
