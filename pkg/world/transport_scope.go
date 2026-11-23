package world

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

// This is transport-owner state, not a trajectory cache or completion timer.
// General barriers are deliberately conservative; only World can qualify its
// complete parser's current spline against the separately retained source.
type transportScopeState struct {
	scope                       *modv1.TransportScope
	visit                       uint64
	attemptedVisit, attemptedID uint64
	exhausted                   bool
	lastHeader                  [32]byte
	lastHeaderKnown             bool
	lastHeaderVisit             uint64
	currentHeader               [32]byte
	currentHeaderKnown          bool
	consumed                    map[[32]byte]struct{}
	consumedVisit               uint64
	transfer                    worldTransferState
	completionAttempt           *modv1.TransportScope
	completionReport            *modv1.TransportScope
	areaTriggerAttempts         map[areaTriggerAttempt]struct{}
}

func newTransportScope(self uint64) (*modv1.TransportScope, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	return &modv1.TransportScope{ConnectionId: hex.EncodeToString(nonce[:]), SelfGuid: self}, nil
}

func (t *transportScopeState) snapshot() *modv1.TransportScope {
	return cloneTransportScope(t.scope)
}

func cloneTransportScope(scope *modv1.TransportScope) *modv1.TransportScope {
	if scope == nil {
		return nil
	}
	return proto.Clone(scope).(*modv1.TransportScope)
}

func (t *transportScopeState) advance(revision *uint64) bool {
	if t.exhausted || *revision == math.MaxUint64 {
		t.exhausted = true
		t.scope.VisitBound, t.scope.SplineActive = false, false
		t.scope.VisitRevision = 0
		return false
	}
	(*revision)++
	return true
}

func (t *transportScopeState) clearSpline() {
	t.scope.SplineActive, t.scope.SplineIdKnown = false, false
	t.scope.SplineId, t.scope.SplineSourceOpcode = 0, 0
	t.scope.SplineSourceRevision = 0
	t.currentHeaderKnown = false
}

func (t *transportScopeState) boundary() {
	t.scope.VisitBound, t.scope.VisitRevision = false, 0
	t.clearSpline()
	t.advance(&t.scope.SplineRevision)
}

// These packet families can carry or invalidate own movement, including modes
// the core does not understand. Updates are not decoded twice: their barrier
// reaches World, whose whole-packet parser must prove the source still current.
func transportMotionBarrier(op uint32) bool {
	switch op {
	case opcode.SMSGLoginVerifyWorld, opcode.SMSGNewWorld, opcode.SMSGTransferPending,
		opcode.SMSGUpdateObject, opcode.SMSGCompressedUpdateObject, opcode.SMSGDestroyObject, opcode.SMSGCompressedMoves,
		opcode.SMSGClientControlUpdate, opcode.SMSGMonsterMove, opcode.SMSGMonsterMoveTransport,
		opcode.SMSGFlightSplineSync:
		return true
	}
	name := opcode.WorldName(op)
	return strings.HasPrefix(name, "MSG_MOVE_") || strings.HasPrefix(name, "SMSG_MOVE_") ||
		strings.HasPrefix(name, "SMSG_FORCE_") || strings.HasPrefix(name, "SMSG_SPLINE_")
}

// Filter only GUID prefixes already established by the existing World/core
// decoders. Unknown layouts still impose a conservative barrier.
func transportClearlyForeign(op uint16, body []byte, self uint64) bool {
	if op == opcode.SMSGDestroyObject {
		if len(body) < 8 {
			return false
		}
		c := teleportCursor{body: body}
		low, high := c.u32(), c.u32()
		guid := uint64(low) | uint64(high)<<32
		return guid != 0 && guid != self
	}
	switch op {
	case opcode.SMSGMonsterMove, opcode.SMSGMonsterMoveTransport, opcode.SMSGClientControlUpdate,
		opcode.SMSGForceMoveRoot, opcode.SMSGForceMoveUnroot, opcode.SMSGForceRunSpeedChange, opcode.SMSGForceWalkSpeedChange,
		opcode.SMSGMoveKnockBack, opcode.MSGMoveTeleportAck,
		opcode.MSGMoveStartForward, opcode.MSGMoveStartBackward, opcode.MSGMoveStop,
		opcode.MSGMoveStartStrafeLeft, opcode.MSGMoveStartStrafeRight, opcode.MSGMoveStopStrafe,
		opcode.MSGMoveJump, opcode.MSGMoveStartTurnLeft, opcode.MSGMoveStartTurnRight, opcode.MSGMoveStopTurn,
		opcode.MSGMoveStartPitchUp, opcode.MSGMoveStartPitchDown, opcode.MSGMoveStopPitch,
		opcode.MSGMoveSetRunMode, opcode.MSGMoveSetWalkMode, opcode.MSGMoveTeleport,
		opcode.MSGMoveFallLand, opcode.MSGMoveStartSwim, opcode.MSGMoveStopSwim,
		opcode.MSGMoveSetFacing, opcode.MSGMoveSetPitch, opcode.MSGMoveHeartbeat,
		opcode.MSGMoveStartAscend, opcode.MSGMoveStopAscend, opcode.MSGMoveStartDescend:
		c := teleportCursor{body: body}
		guid := c.guid()
		return !c.bad && guid != 0 && guid != self
	}
	return false
}

func scopeWorldEntry(body []byte) (uint32, bool) {
	c := teleportCursor{body: body}
	mapID := c.u32()
	for n := 0; n < 4; n++ {
		f := math.Float32frombits(c.u32())
		// Match World's existing visit acceptance, without changing the stricter
		// coordinate predicate used by the legacy teleport ACK implementation.
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) || math.Abs(float64(f)) >= 1e6 {
			return 0, false
		}
	}
	return mapID, c.complete()
}

func (t *transportScopeState) observe(op uint16, body []byte) {
	t.observeTransferBefore(op, body)
	if t.scope == nil || !transportMotionBarrier(uint32(op)) || transportClearlyForeign(op, body, t.scope.SelfGuid) {
		return
	}
	defer t.observeTransferAfter(op, body)
	if !t.advance(&t.scope.MotionRevision) {
		return
	}
	switch op {
	case opcode.SMSGTransferPending:
		// Even a malformed potential transfer cannot authorize the old map.
		t.boundary()
	case opcode.SMSGLoginVerifyWorld, opcode.SMSGNewWorld:
		t.boundary()
		mapID, valid := scopeWorldEntry(body)
		if !valid || !t.advance(&t.visit) || t.exhausted {
			return
		}
		t.scope.VisitBound, t.scope.VisitRevision, t.scope.MapId = true, t.visit, mapID
		t.consumed, t.consumedVisit = nil, t.visit
		t.lastHeaderKnown = false
	case opcode.SMSGMonsterMove, opcode.SMSGMonsterMoveTransport:
		t.observeSplineHeader(op, body)
	}
}

// Only this small standalone header is decoded here. World independently
// validates the complete packet before it can present the matching source.
func (t *transportScopeState) observeSplineHeader(op uint16, body []byte) {
	c := teleportCursor{body: body}
	mover := c.guid()
	if !c.bad && mover != 0 && mover != t.scope.SelfGuid {
		return
	}
	t.clearSpline()
	t.scope.SplineSourceOpcode, t.scope.SplineSourceRevision = uint32(op), t.scope.MotionRevision
	if op == opcode.SMSGMonsterMoveTransport {
		c.guid()
		c.take(1) // signed seat belongs to the complete World observation
	}
	toggle := c.take(1)
	for n := 0; n < 3; n++ {
		f := math.Float32frombits(c.u32())
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) || math.Abs(float64(f)) >= 1e6 {
			c.bad = true
		}
	}
	id := c.u32()
	kind := c.take(1)
	if c.bad || len(toggle) != 1 || toggle[0] > 1 || len(kind) != 1 || kind[0] > 4 {
		return
	}
	if mover != t.scope.SelfGuid {
		return
	}
	digest := transportSplineDigest(op, body)
	if !t.lastHeaderKnown || t.lastHeaderVisit != t.scope.VisitRevision || digest != t.lastHeader {
		if !t.advance(&t.scope.SplineRevision) {
			return
		}
		t.lastHeader, t.lastHeaderKnown, t.lastHeaderVisit = digest, true, t.scope.VisitRevision
	}
	t.currentHeader, t.currentHeaderKnown = digest, true
	t.scope.SplineIdKnown, t.scope.SplineId = true, id
	t.scope.SplineActive = t.scope.VisitBound && kind[0] != 1
}

func transportSplineDigest(op uint16, body []byte) [32]byte {
	h := sha256.New()
	h.Write([]byte{byte(op), byte(op >> 8)})
	h.Write(body)
	var digest [32]byte
	h.Sum(digest[:0])
	return digest
}

func (t *transportScopeState) sent(op uint32) {
	if op == opcode.CMSGMoveSplineDone {
		// Its attempt is already consumed. Reporting completion is neither a
		// new motion source nor an observation that the server actually arrived.
		if sameTransferSource(t.completionAttempt, t.scope) {
			t.completionReport = transferSource(t.completionAttempt)
		}
		return
	}
	name := opcode.WorldName(op)
	affectsMotion := transportMotionBarrier(op) || strings.HasPrefix(name, "CMSG_MOVE_") || strings.HasPrefix(name, "CMSG_FORCE_")
	switch op {
	case opcode.CMSGSetActiveMover, opcode.CMSGSetVehicleRecIDAck, opcode.CMSGDismissControlledVehicle,
		opcode.CMSGRequestVehicleExit, opcode.CMSGRequestVehiclePrevSeat, opcode.CMSGRequestVehicleNextSeat,
		opcode.CMSGRequestVehicleSwitchSeat, opcode.CMSGChangeSeatsOnControlledVehicle, opcode.CMSGPlayerVehicleEnter,
		opcode.CMSGActivatetaxi, opcode.CMSGActivatetaxiexpress:
		affectsMotion = true
	}
	if t.scope == nil || !affectsMotion {
		return
	}
	t.advance(&t.scope.MotionRevision)
	// Keep the latest observed header as history; a local control/movement
	// write cannot prove that this header still authorizes completion.
	t.scope.SplineActive = false
	if op != opcode.MSGMoveWorldportAck {
		t.supersedePendingTransfer("outbound_motion_superseded_transfer")
	}
}

// reserve is called only on the owner loop, immediately before transport.
// Its attempt key is independent of the general barrier: fetching a fresh
// scope after the write can never make the same spline replayable.
func (t *transportScopeState) reserve(op uint32, payload []byte, requested *modv1.TransportScope) error {
	if op == opcode.CMSGAreatrigger && requested != nil {
		return t.reserveAreaTrigger(payload, requested)
	}
	if op != opcode.CMSGMoveSplineDone {
		if requested != nil {
			return modcb.PacketNotDispatchedError("transport scope is only supported for spline completion or area triggers")
		}
		return nil
	}
	current := t.scope
	if requested == nil || current == nil || t.exhausted || current.ConnectionId == "" || current.SelfGuid == 0 ||
		!current.VisitBound || current.VisitRevision == 0 || !current.SplineActive || !current.SplineIdKnown || current.SplineRevision == 0 ||
		current.SplineSourceRevision == 0 || !t.currentHeaderKnown || !proto.Equal(requested, current) {
		return modcb.PacketNotDispatchedError("spline completion transport scope is absent or no longer current")
	}
	if t.attemptedVisit == current.VisitRevision && t.attemptedID == current.SplineRevision {
		return modcb.PacketNotDispatchedError("this spline already has a completion attempt")
	}
	if t.consumedVisit != current.VisitRevision {
		t.consumed, t.consumedVisit = nil, current.VisitRevision
	}
	if _, consumed := t.consumed[t.currentHeader]; consumed {
		return modcb.PacketNotDispatchedError("this exact spline source already has a completion attempt")
	}
	c := teleportCursor{body: payload}
	if c.guid() != current.SelfGuid || c.bad {
		return modcb.PacketNotDispatchedError("spline completion mover does not match this body")
	}
	info, consumed, err := movementinfo.DecodePrefix(payload[c.offset:])
	if err != nil || !info.AllFinite() {
		return modcb.PacketNotDispatchedError("spline completion MovementInfo is incomplete or nonfinite")
	}
	c.offset += consumed
	id := c.u32()
	if !c.complete() || id != current.SplineId {
		return modcb.PacketNotDispatchedError("spline completion counter or packet boundary does not match")
	}
	t.attemptedVisit, t.attemptedID = current.VisitRevision, current.SplineRevision
	if t.consumed == nil {
		t.consumed = make(map[[32]byte]struct{})
	}
	t.consumed[t.currentHeader] = struct{}{}
	t.completionAttempt = transferSource(current)
	return nil
}

// TransportScope is an immutable snapshot of facts accepted by the owner loop.
// Reading it does not reserve an attempt or grant permission to invent an ACK.
func (s *Session) TransportScope() *modv1.TransportScope {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.transportScopeSnapshot == nil {
		return nil
	}
	out := proto.Clone(s.transportScopeSnapshot).(*modv1.TransportScope)
	if s.state.Status == Closed || s.state.Status == Failed || s.state.Status == LoggingOut {
		out.VisitBound, out.SplineActive = false, false
	}
	return out
}

func (e *engine) publishTransportScope() {
	if e.s == nil {
		return
	}
	e.s.mu.Lock()
	e.s.transportScopeSnapshot = e.transport.snapshot()
	e.s.ownMovementReportSnapshot = e.ownMovementReport
	e.s.mu.Unlock()
}

func (e *engine) observeTransportScope(op uint16, body []byte) {
	if e.s == nil {
		return
	}
	e.transport.observe(op, body)
	e.publishTransportScope()
}

func (e *engine) publishObserved(op uint16, body []byte) error {
	if observed, ok := e.s.modules.(interface {
		PublishObserved(uint16, []byte, *modv1.TransportScope) error
	}); ok {
		return observed.PublishObserved(op, body, e.transport.snapshot())
	}
	return e.s.modules.Publish(op, body)
}
