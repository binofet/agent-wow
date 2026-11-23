package buddyworld

import (
	"encoding/binary"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

// This retains one adjacent observed transfer, not a journey or booking. It
// never changes the predecessor report, its uncertainty, or replay protection.
type worldTransferObservation struct {
	revision                 uint64
	known, waiting, blocked  bool
	reason                   string
	self, fromVisit, toVisit uint64
	core                     *modv1.ObservedWorldTransfer
	predecessor              *worldv1.OwnMotionAuthority
	// Keep this exact attempt pointer: Pending can be delivered before its
	// scoped send callback returns. All readers/writers hold Service.mu.
	completion        *worldv1.SplineCompletionOperation
	completionBinding *worldv1.SplineCompletionOperation
	firstSource       *modv1.TransportScope
	qualifiedSource   *modv1.TransportScope
	lastScope         *modv1.TransportScope
}

func cloneWorldTransfer(edge *modv1.ObservedWorldTransfer) *modv1.ObservedWorldTransfer {
	if edge == nil {
		return nil
	}
	return proto.Clone(edge).(*modv1.ObservedWorldTransfer)
}

func worldTransferFromMatches(edge *modv1.ObservedWorldTransfer, scope *modv1.TransportScope) bool {
	return edge != nil && scope != nil && scope.VisitBound && scope.ConnectionId != "" && scope.SelfGuid != 0 && scope.VisitRevision != 0 && scope.MotionRevision != 0 &&
		edge.ConnectionId == scope.ConnectionId && edge.SelfGuid == scope.SelfGuid && edge.FromVisitRevision == scope.VisitRevision && edge.FromMapId == scope.MapId && edge.FromMotionRevision == scope.MotionRevision &&
		edge.FromSplineIdKnown == scope.SplineIdKnown && edge.FromSplineId == scope.SplineId && edge.FromSplineRevision == scope.SplineRevision && edge.FromSplineSourceRevision == scope.SplineSourceRevision && edge.FromSplineSourceOpcode == scope.SplineSourceOpcode && edge.FromSplineActive == scope.SplineActive
}

func sameWorldTransferPending(a, b *modv1.ObservedWorldTransfer) bool {
	if a == nil || b == nil {
		return false
	}
	return a.ConnectionId == b.ConnectionId && a.SelfGuid == b.SelfGuid && a.FromVisitRevision == b.FromVisitRevision && a.FromMapId == b.FromMapId && a.FromMotionRevision == b.FromMotionRevision &&
		a.FromSplineIdKnown == b.FromSplineIdKnown && a.FromSplineId == b.FromSplineId && a.FromSplineRevision == b.FromSplineRevision && a.FromSplineSourceRevision == b.FromSplineSourceRevision && a.FromSplineSourceOpcode == b.FromSplineSourceOpcode && a.FromSplineActive == b.FromSplineActive &&
		a.PendingMotionRevision == b.PendingMotionRevision && a.TargetMapKnown && b.TargetMapKnown && a.TargetMapId == b.TargetMapId && a.CompletionReportWritten == b.CompletionReportWritten && a.CompletionReportMotionRevision == b.CompletionReportMotionRevision
}

func (s *Service) captureWorldTransferBeforeLocked(packet *modv1.WorldPacket, now time.Time) *worldTransferObservation {
	if packet.Opcode != opcode.SMSGTransferPending {
		return nil
	}
	p, scope := s.state.players[0], s.motion.scope
	f := &worldTransferObservation{self: p.guid, fromVisit: s.state.visitRevision, core: cloneWorldTransfer(packet.GetTransportScope().GetWorldTransfer()), predecessor: s.motionSnapshotLocked(now), completion: s.completion.operation, lastScope: cloneMotionScope(packet.TransportScope)}
	if f.completion != nil {
		f.completionBinding = proto.Clone(f.completion).(*worldv1.SplineCompletionOperation)
	}
	edge, after := f.core, packet.TransportScope
	if len(packet.Payload) != 4 || !p.present || !p.mapKnown || s.state.visitRevision == 0 || !scopeMatchesSelf(scope, p, s.state.visitRevision) || !worldTransferFromMatches(edge, scope) || edge.Revision == 0 || !edge.TargetMapKnown || edge.TargetMapId != binary.LittleEndian.Uint32(packet.Payload) || scope.MotionRevision == ^uint64(0) || edge.PendingMotionRevision != scope.MotionRevision+1 || after == nil || after.ConnectionId != scope.ConnectionId || after.SelfGuid != p.guid || after.MotionRevision != edge.PendingMotionRevision || after.VisitBound || after.VisitRevision != 0 {
		f.blocked = true
		f.reason = "supported paired World/Core transfer predecessor unavailable"
		return f
	}
	f.waiting = true
	f.reason = "matching acknowledged World entry unavailable"
	return f
}

func (s *Service) blockWorldTransferLocked(reason string) {
	f := &s.worldTransfer
	if f.revision == 0 {
		return
	}
	f.blocked = true
	f.reason = reason
	if f.revision != ^uint64(0) {
		f.revision++
	}
}

func (s *Service) invalidateWorldTransferPacketLocked(packet *modv1.WorldPacket) {
	// The flight proof deliberately clears its scope while unbound. Compare
	// against this observation's captured Pending/last-committed envelope so an
	// ignored nonmotion packet cannot fabricate a gap during the transfer.
	f := &s.worldTransfer
	if f.revision == 0 {
		return
	}
	if potentialMotionOpcode(packet.Opcode) || packet.TransportScope == nil || f.lastScope == nil || !proto.Equal(packet.TransportScope, f.lastScope) {
		s.blockWorldTransferLocked("malformed or unsupported transfer continuation motion")
	}
}

func (s *Service) observeWorldTransferLocked(packet *modv1.WorldPacket, current observed, next *observations, before *worldTransferObservation, now time.Time) {
	f := &s.worldTransfer
	if packet.Opcode == opcode.SMSGTransferPending {
		revision := f.revision
		if before == nil {
			return
		}
		*f = *before
		if revision == ^uint64(0) {
			f.blocked = true
			f.reason = "World transfer revision exhausted"
			f.revision = revision
			return
		}
		f.revision = revision + 1
		return
	}
	if f.revision == 0 {
		return
	}
	scope, edge := packet.TransportScope, packet.GetTransportScope().GetWorldTransfer()
	if packet.Opcode == opcode.SMSGNewWorld || packet.Opcode == opcode.SMSGLoginVerifyWorld {
		if packet.Opcode != opcode.SMSGNewWorld || !f.waiting || f.blocked || !sameWorldTransferPending(f.core, edge) || !edge.Known || !edge.WorldportAckWritten || edge.WorldportAckUnknown || edge.Revision <= f.core.Revision || edge.FromVisitRevision == ^uint64(0) || edge.ToVisitRevision != edge.FromVisitRevision+1 || edge.ToMapId != edge.TargetMapId || edge.PendingMotionRevision == ^uint64(0) || edge.EntryMotionRevision != edge.PendingMotionRevision+1 || edge.EntryMotionRevision == ^uint64(0) || edge.WorldportAckMotionRevision != edge.EntryMotionRevision+1 || !scopeMatchesSelf(scope, current, next.visitRevision) || scope.ConnectionId != edge.ConnectionId || scope.SelfGuid != f.self || scope.VisitRevision != edge.ToVisitRevision || scope.MapId != edge.ToMapId || scope.MotionRevision != edge.WorldportAckMotionRevision || scope.SplineActive || scope.SplineIdKnown || f.fromVisit == ^uint64(0) || next.visitRevision != f.fromVisit+1 {
			f.known = false
			s.blockWorldTransferLocked("World entry does not match observed acknowledged Core transfer")
			return
		}
		f.known, f.waiting = true, false
		f.toVisit = next.visitRevision
		f.core = cloneWorldTransfer(edge)
		f.lastScope = cloneMotionScope(scope)
		f.reason = "new-visit normal taxi continuation facts unavailable"
		if f.revision != ^uint64(0) {
			f.revision++
		}
		return
	}
	if f.waiting {
		if scope == nil || scope.ConnectionId != f.core.ConnectionId || scope.SelfGuid != f.self || scope.MotionRevision != f.core.PendingMotionRevision || !sameWorldTransferPending(f.core, edge) {
			s.blockWorldTransferLocked("intervening motion replaced pending World transfer")
		}
		return
	}
	if !f.known || f.blocked {
		return
	}
	// Existing motion proof has already processed this complete packet. Its
	// explicit envelope counter must remain adjacent to the prior transfer-side
	// observation; neither a fresh flag nor a later header repairs a missed one.
	if !scopeMatchesSelf(scope, current, next.visitRevision) || current.guid != f.self || next.visitRevision != f.toVisit || !proto.Equal(edge, f.core) || scope.VisitRevision != f.core.ToVisitRevision || scope.ConnectionId != f.core.ConnectionId || scope.MapId != f.core.ToMapId || !sameMotionVisit(f.lastScope, scope) || scope.MotionRevision < f.lastScope.MotionRevision || scope.MotionRevision-f.lastScope.MotionRevision > 1 || current.receivedMotionInvalid || current.receivedPosition && !current.receivedMovement && !current.receivedSpline || current.receivedOwnCreate && f.firstSource != nil {
		s.blockWorldTransferLocked("current transfer continuation body, source or control replaced")
		return
	}
	f.lastScope = cloneMotionScope(scope)
	life := snapshot(current, now)
	if life.AliveKnown && !life.Alive || current.receivedUnitFlags && current.fields[unitFlags]&0x100000 == 0 {
		s.blockWorldTransferLocked("observed death or taxi-clear replaced continuation candidate")
		return
	}
	if current.receivedSpline && (packet.Opcode == opcode.SMSGMonsterMove || packet.Opcode == opcode.SMSGMonsterMoveTransport) {
		if f.firstSource != nil || !normalWorldTransferSpline(current.receivedSplineHeader) {
			s.blockWorldTransferLocked("standalone transfer continuation source replaced or unsupported")
			return
		}
		f.firstSource = cloneMotionScope(scope)
	}
	if current.receivedMovement && !current.receivedMovementInfo.AllFinite() {
		s.blockWorldTransferLocked("nonfinite transfer continuation MovementInfo")
		return
	}
	if f.qualifiedSource != nil && !s.motionSnapshotLocked(now).Known {
		s.blockWorldTransferLocked("qualified taxi continuation no longer current")
		return
	}
	if a := s.motionSnapshotLocked(now); f.qualifiedSource == nil && a.Known && worldTransferNewFacts(f, a, next) {
		f.qualifiedSource = cloneMotionScope(scope)
		if f.revision != ^uint64(0) {
			f.revision++
		}
	}
}

func normalWorldTransferSpline(t observedTrajectory) bool {
	return t.known && t.idKnown && t.kind == 0 && !t.stopped && !t.relative && !t.cyclic && t.destinationKnown && t.duration > 0 && t.flags&0x2000 != 0 && t.flags&(unsafeEndpointFlags|0x200|0x800|0x200000|0x800000|0x1000000) == 0
}

func worldTransferNewFacts(f *worldTransferObservation, a *worldv1.OwnMotionAuthority, state *observations) bool {
	raw := state.ownMovement
	scope := a.TransportScope
	return f.known && !f.blocked && f.core != nil && f.firstSource != nil && a.Known && scope != nil && sameMotionSource(f.firstSource, scope) && scope.SplineSourceRevision > f.core.WorldportAckMotionRevision &&
		raw.known && raw.scope != nil && proto.Equal(raw.scope.WorldTransfer, f.core) && sameMotionVisit(raw.scope, scope) && raw.scope.MotionRevision > f.core.WorldportAckMotionRevision && raw.scope.MotionRevision <= scope.MotionRevision &&
		a.UnitFlagsKnown && a.UnitFlags&0x100000 != 0 && a.UnitFlagsMotionRevision > f.core.WorldportAckMotionRevision && a.UnitFlagsMotionRevision <= scope.MotionRevision
}

func worldTransferPredecessorQualified(f *worldTransferObservation) bool {
	a, op, b, edge := f.predecessor, f.completion, f.completionBinding, f.core
	if a == nil || !a.Known || a.SelfGuid != f.self || a.VisitRevision != f.fromVisit || op == nil || b == nil || edge == nil || !edge.CompletionReportWritten || op.Retired || op.ArrivalKnown || !op.SplineIdKnown || op.SplineId != edge.FromSplineId || !op.TransportScope.GetSplineActive() || op.Revision != b.Revision || op.SelfGuid != f.self || op.VisitRevision != f.fromVisit || op.SelfGuid != b.SelfGuid || op.VisitRevision != b.VisitRevision || op.AuthorityRevision != b.AuthorityRevision || op.MovementRevision != b.MovementRevision || op.SplineRevision != b.SplineRevision || !proto.Equal(op.TransportScope, b.TransportScope) || !sameMotionSource(op.TransportScope, a.TransportScope) || edge.CompletionReportMotionRevision != op.TransportScope.GetMotionRevision() || edge.CompletionReportMotionRevision > edge.FromMotionRevision {
		return false
	}
	return op.Phase == "transport_written" && op.TransportWritten && !op.TransportUnknown && !op.Pending || op.Phase == "transport_unknown" && op.TransportUnknown && !op.TransportWritten && op.Pending
}

func (s *Service) worldTransferSnapshotLocked(now time.Time) *worldv1.WorldTransferObservation {
	f := &s.worldTransfer
	a := s.motionSnapshotLocked(now)
	r := &worldv1.WorldTransferObservation{Revision: f.revision, Known: f.known, Reason: f.reason, SelfGuid: f.self, FromVisitRevision: f.fromVisit, ToVisitRevision: f.toVisit, Core: cloneWorldTransfer(f.core), CurrentScope: cloneMotionScope(a.TransportScope), CurrentAuthorityRevision: a.Revision, CurrentMovementRevision: a.MovementRevision, CurrentSplineRevision: a.SplineRevision, CurrentUnitFlagsRevision: a.UnitFlagsRevision, CurrentUnitFlagsMotionRevision: a.UnitFlagsMotionRevision, CurrentUnitFlagsKnown: a.UnitFlagsKnown, CurrentUnitFlags: a.UnitFlags}
	if f.predecessor != nil {
		r.PredecessorAuthority = proto.Clone(f.predecessor).(*worldv1.OwnMotionAuthority)
	}
	if f.completion != nil {
		r.PredecessorCompletion = proto.Clone(f.completion).(*worldv1.SplineCompletionOperation)
	}
	r.PredecessorQualified = worldTransferPredecessorQualified(f)
	r.ContinuationKnown = r.PredecessorQualified && f.qualifiedSource != nil && worldTransferNewFacts(f, a, &s.state)
	if r.ContinuationKnown {
		r.Reason = ""
	} else if f.known && !f.blocked && !r.PredecessorQualified {
		r.Reason = "qualified predecessor completion and Core report unavailable"
	}
	return r
}
