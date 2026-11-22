package buddyworld

import (
	"context"
	"encoding/binary"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// This proof describes the current, fully parsed standalone server trajectory.
// It is deliberately separate from both historical raw facts and the legacy
// trajectory that Snapshot may settle to an inferred endpoint.
type ownMotionAuthority struct {
	revision              uint64
	known                 bool
	reason                string
	visit, splineRevision uint64
	scope                 *modv1.TransportScope
	header                observedTrajectory
	unitFlagsKnown        bool
	unitFlags             uint32
	unitFlagsRevision     uint64
	unitFlagsAt           time.Time
	unitFlagsOpcode       uint32
	unitFlagsMotion       uint64
}

type splinePhysicalKey struct {
	connection    string
	visit, spline uint64
}

type splineCompletionState struct {
	revision  uint64
	request   *worldv1.CompleteSplineRequest
	operation *worldv1.SplineCompletionOperation
	consumed  map[splinePhysicalKey]bool
}

// ConfigureSession attaches the authenticated callback before serving RPCs.
// Observation-only constructors remain usable without a callback.
func (s *Service) ConfigureSession(session modv1.SessionClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session = session
}

func cloneMotionScope(scope *modv1.TransportScope) *modv1.TransportScope {
	if scope == nil {
		return nil
	}
	return proto.Clone(scope).(*modv1.TransportScope)
}

func sameMotionVisit(a, b *modv1.TransportScope) bool {
	return a != nil && b != nil && a.ConnectionId == b.ConnectionId && a.SelfGuid == b.SelfGuid && a.VisitBound && b.VisitBound && a.VisitRevision == b.VisitRevision && a.MapId == b.MapId
}

func sameMotionSource(a, b *modv1.TransportScope) bool {
	return sameMotionVisit(a, b) && a.SplineIdKnown && b.SplineIdKnown && a.SplineId == b.SplineId && a.SplineRevision == b.SplineRevision && a.SplineSourceRevision == b.SplineSourceRevision && a.SplineSourceOpcode == b.SplineSourceOpcode
}

func scopeMatchesSelf(scope *modv1.TransportScope, p observed, visit uint64) bool {
	// Core and World independently count accepted visits. Bind their actual
	// body/map and require each counter, without equating different domains.
	return scope != nil && scope.ConnectionId != "" && scope.SelfGuid != 0 && scope.SelfGuid == p.guid && scope.VisitBound && scope.VisitRevision != 0 && scope.MotionRevision != 0 && p.present && p.mapKnown && visit != 0 && scope.MapId == p.mapID
}

func (s *Service) invalidateMotionLocked(reason string, flagsUnknown bool) {
	s.motion.revision++
	s.motion.known = false
	s.motion.reason = reason
	if flagsUnknown {
		s.motion.unitFlagsKnown = false
	}
}

func potentialMotionOpcode(op uint32) bool {
	return movementOpcode(op) || op == opcode.MSGMoveTeleportAck || op == opcode.SMSGUpdateObject || op == opcode.SMSGCompressedUpdateObject || op == opcode.SMSGDestroyObject || op == opcode.SMSGMonsterMove || op == opcode.SMSGMonsterMoveTransport || op == opcode.SMSGMoveKnockBack || op == opcode.SMSGForceMoveRoot || op == opcode.SMSGForceMoveUnroot || op == opcode.SMSGForceRunSpeedChange || op == opcode.SMSGForceWalkSpeedChange || op == opcode.SMSGClientControlUpdate || op == opcode.SMSGLoginVerifyWorld || op == opcode.SMSGNewWorld || op == opcode.SMSGTransferPending
}

func (s *Service) invalidateMalformedMotionLocked(packet *modv1.WorldPacket) {
	// This must live outside observations' atomic rollback: an undecodable
	// possible supersession cannot leave the previous completion proof current.
	if potentialMotionOpcode(packet.Opcode) || packet.GetTransportScope().GetMotionRevision() != s.motion.scope.GetMotionRevision() {
		s.invalidateMotionLocked("malformed potential motion packet", true)
	}
}

func (s *Service) observeUnsupportedMotionLocked(packet *modv1.WorldPacket) {
	if potentialMotionOpcode(packet.Opcode) || packet.GetTransportScope().GetMotionRevision() != s.motion.scope.GetMotionRevision() {
		s.invalidateMotionLocked("unsupported motion barrier", true)
	}
}

func (s *Service) observeMotionLocked(packet *modv1.WorldPacket, old, current observed, next *observations, now time.Time) {
	m := &s.motion
	scope := packet.GetTransportScope()
	bound := scopeMatchesSelf(scope, current, next.visitRevision)
	continuous := bound && sameMotionVisit(m.scope, scope) && scope.MotionRevision >= m.scope.MotionRevision && scope.MotionRevision-m.scope.MotionRevision <= 1
	if !continuous {
		s.invalidateMotionLocked("motion scope gap or replaced visit", true)
	}
	if !bound {
		s.invalidateMotionLocked("missing current bound transport scope", true)
		m.scope = nil
		return
	}
	// The envelope becomes caught up only after the complete packet committed.
	previousScope := m.scope
	m.scope = cloneMotionScope(scope)
	m.revision++
	if current.receivedUnitFlags {
		m.unitFlagsKnown = true
		m.unitFlags = current.fields[unitFlags]
		m.unitFlagsRevision++
		m.unitFlagsAt, m.unitFlagsOpcode, m.unitFlagsMotion = now, packet.Opcode, scope.MotionRevision
	}
	if !current.present || old.guid != current.guid || next.visitRevision != m.visit && m.visit != 0 || current.receivedOwnCreate || current.receivedMotionInvalid || current.receivedPosition && !current.receivedMovement && !current.receivedSpline || current.receivedMovement && current.receivedMovementInfo.Flags&0x08000000 == 0 {
		s.invalidateMotionLocked("own movement or body superseded standalone spline", false)
	}
	if current.receivedSpline {
		if packet.Opcode != opcode.SMSGMonsterMove && packet.Opcode != opcode.SMSGMonsterMoveTransport {
			s.invalidateMotionLocked("CREATE-only spline has no standalone transport proof", false)
			return
		}
		header := current.receivedSplineHeader
		if !next.ownSpline.known || !scope.SplineActive || !scope.SplineIdKnown || scope.SplineRevision == 0 || scope.SplineSourceRevision != scope.MotionRevision || scope.SplineSourceOpcode != packet.Opcode || scope.SplineId != header.id || header.stopped {
			s.invalidateMotionLocked("standalone spline does not match transport source", false)
			return
		}
		m.known, m.reason = true, ""
		m.visit, m.splineRevision, m.header = next.visitRevision, next.ownSpline.revision, header
	} else if m.known && (!continuous || !scope.SplineActive || !sameMotionSource(previousScope, scope)) {
		s.invalidateMotionLocked("current transport spline superseded", false)
	}
}

func splineRemaining(header observedTrajectory, now time.Time) uint64 {
	elapsed := uint64(header.elapsed) + age(now, header.at)
	if elapsed >= uint64(header.duration) {
		return 0
	}
	return uint64(header.duration) - elapsed
}

func (s *Service) motionSnapshotLocked(now time.Time) *worldv1.OwnMotionAuthority {
	m, p, raw := &s.motion, s.state.players[0], s.state.ownMovement
	r := &worldv1.OwnMotionAuthority{SelfGuid: p.guid, VisitRevision: s.state.visitRevision, Revision: m.revision, MovementRevision: raw.revision, SplineRevision: m.splineRevision, TransportScope: cloneMotionScope(m.scope), Reason: m.reason, DurationRemainingMs: splineRemaining(m.header, now), UnitFlagsKnown: m.unitFlagsKnown, UnitFlags: m.unitFlags, UnitFlagsRevision: m.unitFlagsRevision, UnitFlagsAgeMs: age(now, m.unitFlagsAt), UnitFlagsSourceOpcode: m.unitFlagsOpcode, UnitFlagsMotionRevision: m.unitFlagsMotion}
	if !m.known {
		return r
	}
	life := snapshot(p, now)
	reason := ""
	switch {
	case !scopeMatchesSelf(m.scope, p, s.state.visitRevision) || m.visit != s.state.visitRevision:
		reason = "body or visit is not current"
	case !life.AliveKnown || !life.Alive:
		reason = "actual alive body unavailable"
	case !raw.known || len(raw.wire) == 0 || raw.self != p.guid || raw.visit != s.state.visitRevision || !raw.mapKnown || raw.mapID != p.mapID || !raw.info.AllFinite():
		reason = "actual same-visit MovementInfo unavailable"
	case !m.unitFlagsKnown || m.unitFlags&0x100000 == 0:
		reason = "actual taxi unit flag unavailable"
	// Build12340 MoveSplineFlag::Flying is 0x2000; 0x200 is Falling.
	// This first image excludes final-facing, relative and special modes.
	case !m.header.known || !m.header.idKnown || m.header.kind != 0 || m.header.stopped || m.header.relative || m.header.cyclic || !m.header.destinationKnown || m.header.duration == 0 || m.header.flags&0x2000 == 0 || m.header.flags&(unsafeEndpointFlags|0x200|0x800|0x200000|0x800000|0x1000000) != 0:
		reason = "unsupported standalone spline mode"
	}
	r.Known = reason == ""
	if reason != "" {
		r.Reason = reason
	}
	return r
}

func (s *Service) completionSnapshotLocked() *worldv1.SplineCompletionObservation {
	r := &worldv1.SplineCompletionObservation{SelfGuid: s.state.players[0].guid, VisitRevision: s.state.visitRevision, Revision: s.completion.revision}
	if s.completion.operation != nil {
		r.Operation = proto.Clone(s.completion.operation).(*worldv1.SplineCompletionOperation)
	}
	return r
}

func completionKey(scope *modv1.TransportScope) splinePhysicalKey {
	return splinePhysicalKey{scope.GetConnectionId(), scope.GetVisitRevision(), scope.GetSplineRevision()}
}

func (s *Service) completionBindingLocked(req *worldv1.CompleteSplineRequest, now time.Time) bool {
	a := s.motionSnapshotLocked(now)
	return a.Known && req.SelfGuid == a.SelfGuid && req.VisitRevision == a.VisitRevision && req.AuthorityRevision == a.Revision && req.MovementRevision == a.MovementRevision && req.SplineRevision == a.SplineRevision && req.TransportScope != nil && proto.Equal(req.TransportScope, a.TransportScope)
}

// CompleteSpline reports only one scoped transport attempt. The endpoint image
// is an inference from an advertised destination, never a server pose/arrival.
func (s *Service) CompleteSpline(ctx context.Context, req *worldv1.CompleteSplineRequest) (*worldv1.SplineCompletionObservation, error) {
	if req == nil || req.SelfGuid == 0 || req.VisitRevision == 0 || req.AuthorityRevision == 0 || req.MovementRevision == 0 || req.SplineRevision == 0 || req.TransportScope == nil {
		return nil, status.Error(codes.InvalidArgument, "exact current spline authority binding is required")
	}
	req = proto.Clone(req).(*worldv1.CompleteSplineRequest)
	s.mu.Lock()
	c := &s.completion
	if c.request != nil && proto.Equal(c.request, req) {
		r := s.completionSnapshotLocked()
		s.mu.Unlock()
		return r, nil
	}
	if req.ExpectedRevision != c.revision || c.operation.GetPending() {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "completion CAS is stale or an attempt remains uncertain")
	}
	if s.session == nil || !s.completionBindingLocked(req, s.now()) || splineRemaining(s.motion.header, s.now()) != 0 || c.consumed[completionKey(req.TransportScope)] {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "current due unconsumed standalone taxi spline is required")
	}
	c.revision++
	c.request = proto.Clone(req).(*worldv1.CompleteSplineRequest)
	c.operation = &worldv1.SplineCompletionOperation{Revision: c.revision, SelfGuid: req.SelfGuid, VisitRevision: req.VisitRevision, AuthorityRevision: req.AuthorityRevision, MovementRevision: req.MovementRevision, SplineRevision: req.SplineRevision, TransportScope: cloneMotionScope(req.TransportScope), Pending: true, Phase: "reserved", SplineIdKnown: true, SplineId: s.motion.header.id, MovementImageInferred: true, MovementImageSource: "received_movement_info_with_advertised_spline_endpoint"}
	op := c.operation
	image, destination, session := s.state.ownMovement.info, s.motion.header.destination, s.session
	s.mu.Unlock()

	clock, clockErr := session.GetClock(ctx, &emptypb.Empty{})
	s.mu.Lock()
	if clockErr != nil || clock == nil || !s.completionBindingLocked(req, s.now()) {
		op.Pending, op.Phase = false, "not_dispatched"
		op.Error = "clock unavailable or current spline authority changed before dispatch"
		r := s.completionSnapshotLocked()
		s.mu.Unlock()
		return r, nil
	}
	image.Time = clock.GetClientTimeMs()
	image.X, image.Y, image.Z = destination[0], destination[1], destination[2]
	wire, err := movementinfo.Encode(image)
	if err != nil {
		op.Pending, op.Phase, op.Error = false, "not_dispatched", "received movement image cannot be encoded"
		r := s.completionSnapshotLocked()
		s.mu.Unlock()
		return r, nil
	}
	body := appendCompletionGUID(nil, req.SelfGuid)
	body = append(body, wire...)
	body = binary.LittleEndian.AppendUint32(body, op.SplineId)
	if c.consumed == nil {
		c.consumed = make(map[splinePhysicalKey]bool)
	}
	key := completionKey(req.TransportScope)
	c.consumed[key] = true
	op.Phase = "dispatching"
	s.mu.Unlock()
	_, sendErr := session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: opcode.CMSGMoveSplineDone, Payload: body, TransportScope: cloneMotionScope(req.TransportScope)})
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case sendErr == nil:
		op.Pending, op.TransportWritten, op.Phase = false, true, "transport_written"
	case modcb.PacketWasNotDispatched(sendErr):
		delete(c.consumed, key)
		op.Pending, op.Phase, op.Error = false, "not_dispatched", "core refused current transport scope before dispatch"
	default:
		op.TransportUnknown, op.Phase, op.Error = true, "transport_unknown", "completion transport outcome is unknown"
	}
	return s.completionSnapshotLocked(), nil
}

func appendCompletionGUID(b []byte, guid uint64) []byte {
	maskAt := len(b)
	b = append(b, 0)
	for n := uint(0); n < 8; n++ {
		if v := byte(guid >> (n * 8)); v != 0 {
			b[maskAt] |= 1 << n
			b = append(b, v)
		}
	}
	return b
}
