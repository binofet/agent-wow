package world

import (
	"context"
	"time"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

// Every revision in (from, through] is a separately committed, harmless World
// object update. A skipped revision, physical write, or other incoming movement
// can never join this interval. The session mutex protects these fields.
type parsedBodyUpdates struct {
	through, pending      uint64
	self, follow, settled bodyUpdateRange
	scope                 *modv1.TransportScope
	changed               chan struct{}
}

type bodyUpdateRange struct {
	from, barrier uint64
	reason        string
}

func (r *bodyUpdateRange) observe(revision uint64, continuous bool, result string) {
	if result != modcb.BodyUpdateHarmless {
		r.from, r.barrier, r.reason = revision, revision, result
	} else if !continuous {
		r.from = revision - 1
	}
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
		u.self, u.follow, u.settled = bodyUpdateRange{}, bodyUpdateRange{}, bodyUpdateRange{}
	}
	continuous := same && u.through == scope.MotionRevision-1
	u.follow.observe(scope.MotionRevision, continuous, result)
	if modcb.BodyUpdateIsOwnerOnly(result) {
		result = modcb.BodyUpdateHarmless
	}
	u.self.observe(scope.MotionRevision, continuous, result)
	if result == modcb.BodyUpdatePosture {
		result = modcb.BodyUpdateHarmless
	}
	u.settled.observe(scope.MotionRevision, continuous, result)
	u.scope, u.through = cloneTransportScope(scope), scope.MotionRevision
	u.signal()
}

// An empty reason qualifies the original physical source. A pending callback
// may justify waiting before the final socket drain, never after that drain.
func (s *Session) bodyProofLocked(ctx context.Context, before, current *modv1.TransportScope) (reason string, pending bool) {
	if s.state.Status != InWorld || current.GetSelfGuid() != s.state.Character.GUID || !sameBodyScope(before, current) ||
		!proto.Equal(s.transportScopeSnapshot, current) || before.MotionRevision > current.MotionRevision {
		return "body_or_visit_changed", false
	}
	if before.MotionRevision == current.MotionRevision {
		return "", false
	}
	u := &s.bodyUpdates
	same := sameBodyScope(u.scope, current)
	r := &u.follow
	if modsession.IsSettledBodySource(ctx) {
		r = &u.settled
	} else if modsession.IsSelfBodyInput(ctx) {
		r = &u.self
	}
	if same && r.barrier > before.MotionRevision {
		return r.reason, false
	}
	if same && u.through == current.MotionRevision && r.from <= before.MotionRevision {
		return "", false
	}
	if u.pending == current.MotionRevision && (!same || u.through < current.MotionRevision) {
		return modcb.BodyUpdateMissing, true
	}
	return "intervening_motion_or_unobserved_update", false
}

func (s *Session) bodyProof(ctx context.Context, before, current *modv1.TransportScope) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	reason, _ := s.bodyProofLocked(ctx, before, current)
	return reason
}

func (s *Session) waitBodyProof(ctx context.Context, before, current *modv1.TransportScope, deadline time.Time) (waited bool) {
	for time.Now().Before(deadline) {
		s.mu.Lock()
		_, pending := s.bodyProofLocked(ctx, before, current)
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
		if modsession.IsSettledBodySource(ctx) {
			i, ok := movementinfo.OwnFrame(report.Payload, scope.GetSelfGuid())
			if id != "" || report.Opcode != opcode.MSGMoveStop || !ok || !(i.BodyWaterEntry(false) || movementflags.GroundCompatible(i.Flags, uint32(i.ExtraFlags)) && i.Flags&0x1ff == 0) {
				s.mu.RUnlock()
				return nil, "settled_stop_unavailable"
			}
		}
		reason, pending := s.bodyProofLocked(ctx, report.TransportScope, scope)
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
