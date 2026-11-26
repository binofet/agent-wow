package world

import (
	"context"
	"time"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/protobuf/proto"
)

// Every revision in (from, through] is a separately committed, harmless World
// object update. A skipped revision, physical write, or other incoming movement
// can never join this interval. The session mutex protects these fields.
type parsedBodyUpdates struct {
	from, through, pending, barrier uint64
	scope                           *modv1.TransportScope
	reason                          string
	changed                         chan struct{}
}

const bodyUpdateWait = 200 * time.Millisecond

func sameBodyScope(a, b *modv1.TransportScope) bool {
	if a == nil || b == nil || !a.VisitBound || !b.VisitBound || a.SplineActive || b.SplineActive || a.ConnectionId == "" || a.SelfGuid == 0 || a.VisitRevision == 0 {
		return false
	}
	c := cloneTransportScope(a)
	c.MotionRevision = b.MotionRevision
	return proto.Equal(c, b)
}

func (u *parsedBodyUpdates) signal() {
	if u.changed != nil {
		close(u.changed)
		u.changed = nil
	}
}

func (s *Session) recordParsedBodyUpdate(scope *modv1.TransportScope, result string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, current := &s.bodyUpdates, s.transportScopeSnapshot
	if !sameBodyScope(scope, current) || scope.MotionRevision == 0 || scope.MotionRevision > current.MotionRevision {
		return
	}
	same := sameBodyScope(u.scope, scope)
	if same && scope.MotionRevision <= u.through {
		return
	}
	if !same {
		u.barrier, u.reason = 0, ""
	}
	if result != modcb.BodyUpdateHarmless {
		u.from, u.barrier, u.reason = scope.MotionRevision, scope.MotionRevision, result
	} else if !same || u.through != scope.MotionRevision-1 {
		u.from = scope.MotionRevision - 1
	}
	u.scope, u.through = cloneTransportScope(scope), scope.MotionRevision
	u.signal()
}

// An empty reason qualifies the original physical source. A pending callback
// may justify waiting before the final socket drain, never after that drain.
func (s *Session) bodyProofLocked(before, current *modv1.TransportScope) (reason string, pending bool) {
	if s.state.Status != InWorld || current.GetSelfGuid() != s.state.Character.GUID || !sameBodyScope(before, current) ||
		!proto.Equal(s.transportScopeSnapshot, current) || before.MotionRevision > current.MotionRevision {
		return "body_or_visit_changed", false
	}
	if before.MotionRevision == current.MotionRevision {
		return "", false
	}
	u := &s.bodyUpdates
	same := sameBodyScope(u.scope, current)
	if same && u.barrier > before.MotionRevision {
		return u.reason, false
	}
	if same && u.through == current.MotionRevision && u.from <= before.MotionRevision {
		return "", false
	}
	if u.pending == current.MotionRevision && (!same || u.through < current.MotionRevision) {
		return modcb.BodyUpdateMissing, true
	}
	return "intervening_motion_or_unobserved_update", false
}

func (s *Session) bodyProof(before, current *modv1.TransportScope) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	reason, _ := s.bodyProofLocked(before, current)
	return reason
}

func (s *Session) waitBodyProof(ctx context.Context, before, current *modv1.TransportScope, deadline time.Time) (waited bool) {
	for time.Now().Before(deadline) {
		s.mu.Lock()
		_, pending := s.bodyProofLocked(before, current)
		if !pending {
			s.mu.Unlock()
			return waited
		}
		if s.bodyUpdates.changed == nil {
			s.bodyUpdates.changed = make(chan struct{})
		}
		ch := s.bodyUpdates.changed
		s.mu.Unlock()
		waited = true
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-ctx.Done():
			timer.Stop()
			return true
		case <-timer.C:
			return true
		case <-ch:
			timer.Stop()
		}
	}
	return waited
}

func (s *Session) bodyInputReport(ctx context.Context, id string) (*modv1.WorldPacket, string) {
	deadline := time.Now().Add(bodyUpdateWait)
	s.mu.RLock()
	original := s.ownMovementReportSnapshot
	s.mu.RUnlock()
	for {
		s.mu.RLock()
		report, scope := s.ownMovementReportSnapshot, s.transportScopeSnapshot
		if ctx.Err() != nil || report == nil || report != original || id != "" && id != s.ownMovementAttemptSnapshot {
			s.mu.RUnlock()
			return nil, "exact_write_unavailable"
		}
		reason, pending := s.bodyProofLocked(report.TransportScope, scope)
		if reason == "" {
			packet := proto.Clone(report).(*modv1.WorldPacket)
			s.mu.RUnlock()
			return packet, ""
		}
		s.mu.RUnlock()
		if !pending || !time.Now().Before(deadline) {
			return nil, reason
		}
		s.waitBodyProof(ctx, report.TransportScope, scope, deadline)
	}
}
