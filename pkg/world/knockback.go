package world

import (
	"context"
	"math"
	"time"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

// Only the connection owner can write an ACK or advance the clocked impulse.
// Geometry belongs to the installed Actions module. World consumes the final
// successful contact since the realm broadcasts the ACK to other players.
type knockbackFrameKey struct{}
type knockbackMotion struct {
	order     movementinfo.Knockback
	source    *modv1.WorldPacket
	launch    movementinfo.Info
	start     time.Time
	lastScope *modv1.TransportScope
	contact   *movementinfo.Info
	next      <-chan time.Time
	finished  bool
}

// All fields are protected by the Session mutex. A new received order
// replaces the prior object even when the realm legitimately reuses counter 0.
type knockbackReports struct {
	order, ack, land *modv1.WorldPacket
	contact          *movementinfo.Info
}

func (s *Session) recordOwnPose(scope *modv1.TransportScope, wire []byte) {
	if len(wire) > 128 || scope == nil {
		return
	}
	if _, ok := movementinfo.OwnFrame(wire, scope.SelfGuid); !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.transportScopeSnapshot
	if !sameBodyScope(scope, cur) || scope.MotionRevision == 0 || scope.MotionRevision > cur.MotionRevision {
		return
	}
	if r := s.parsedOwnPose; r != nil && sameBodyScope(r.TransportScope, scope) && r.TransportScope.MotionRevision >= scope.MotionRevision {
		return
	}
	s.parsedOwnPose = &modv1.WorldPacket{TransportScope: cloneTransportScope(scope), Payload: append([]byte(nil), wire...)}
}

func (s *Session) readKnockback(p *modv1.WorldPacket, landing bool) *modv1.WorldPacket {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := &s.knockbackReports
	if r.order == nil || !proto.Equal(p, r.order) || s.state.Status != InWorld || s.state.Character.GUID != p.TransportScope.SelfGuid || !sameBodyScope(p.TransportScope, s.transportScopeSnapshot) {
		return nil
	}
	out := r.ack
	if landing {
		out = r.land
		if out == nil {
			return nil
		}
		reason, _ := s.bodyProofLocked(modsession.WithSelfBodyInput(context.Background()), out.TransportScope, s.transportScopeSnapshot)
		if reason != "" {
			return nil
		}
	}
	return proto.Clone(out).(*modv1.WorldPacket)
}

func (s *Session) acceptKnockbackContact(order *modv1.WorldPacket, data []byte) {
	if len(data) > 128 || order.GetTransportScope() == nil {
		return
	}
	i, ok := movementinfo.OwnFrame(data, order.TransportScope.SelfGuid)
	if !ok || i.Time == 0 || i.ExtraFlags != 0 || i.FallTime != 0 || !(i.Flags == 0 || i.BodyWaterEntry(false)) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &s.knockbackReports
	if r.order == nil || !proto.Equal(r.order, order) || r.contact != nil || r.land != nil {
		return
	}
	r.contact = &i
	select {
	case s.knockbackReady <- struct{}{}:
	default:
	}
}

func (k *knockbackMotion) point(age time.Duration) buddynav.Point {
	i := k.launch
	return buddynav.KnockbackPoint(buddynav.Point{X: i.X, Y: i.Y, Z: i.Z}, math.Atan2(float64(k.order.Sin), float64(k.order.Cos)), float64(k.order.Horizontal), -float64(k.order.Vertical), age)
}

func (e *engine) impulseOrigin() (movementinfo.Info, bool) {
	ctx := modsession.WithSelfBodyInput(context.Background())
	current := e.transport.scope
	proved := func(p *modv1.WorldPacket) bool {
		return p != nil && e.s.bodyProof(ctx, p.TransportScope, current) == ""
	}
	// Give the ordered World parser its existing bounded window to commit a
	// just-received CREATE/correction or classify intervening object updates.
	if current != nil && current.MotionRevision > 1 {
		before := cloneTransportScope(current)
		before.MotionRevision--
		e.s.waitBodyProof(ctx, before, current, time.Now().Add(bodyUpdateWait))
	}
	if k := e.impulse; k != nil && !k.finished && e.s.bodyProof(ctx, k.lastScope, current) == "" {
		if k.contact == nil {
			return movementinfo.Info{}, false
		}
		age := time.Since(k.start)
		if age >= time.Duration(k.contact.Time)*time.Millisecond {
			return movementinfo.Info{}, false
		}
		q := k.point(age)
		i := k.launch
		i.X, i.Y, i.Z = q.X, q.Y, q.Z
		return i, true
	}
	e.s.mu.RLock()
	raw := e.s.parsedOwnPose
	e.s.mu.RUnlock()
	source := e.lastWrittenPose
	if !proved(source) {
		source = nil
	}
	if proved(raw) && (source == nil || raw.TransportScope.MotionRevision > source.TransportScope.MotionRevision) {
		source = raw
	}
	if source == nil {
		return movementinfo.Info{}, false
	}
	i, ok := movementinfo.OwnFrame(source.Payload, current.GetSelfGuid())
	// A transport-relative or flying body needs a different coordinate model.
	return i, ok && !i.TransportPresent && i.ExtraFlags == 0 && i.Flags & ^uint32(0x1ff|0x1000|0x2000|movementinfo.SwimmingFlag) == 0
}

// Unit::KnockbackFrom / WorldSession::HandleMoveKnockBackAck use the supplied
// order counter and full MovementInfo, with the launch velocity signed down.
func (e *engine) knockbackOrder(body []byte) error {
	k, err := movementinfo.DecodeKnockback(body)
	ready := err == nil && e.scopedWriteAvailable(context.Background()) && k.Mover == e.s.state.Character.GUID
	var origin movementinfo.Info
	if ready {
		origin, ready = e.impulseOrigin()
	}
	e.observeTransportScope(opcode.SMSGMoveKnockBack, body)
	e.impulse = nil
	e.movement.info = nil
	e.s.mu.Lock()
	e.s.knockbackReports = knockbackReports{}
	e.s.mu.Unlock()
	if !ready || e.transport.reserveBodySource(e.transport.scope) != nil {
		return nil
	}
	now := time.Now()
	launch := k.Launch(origin, e.timestamp())
	ack := k.Acknowledgement(launch)
	if err := e.write(opcode.CMSGMoveKnockBackAck, ack); err != nil {
		return err
	}
	scope := e.transport.snapshot()
	order := &modv1.WorldPacket{Opcode: opcode.SMSGMoveKnockBack, Payload: append([]byte(nil), body...), TransportScope: scope}
	e.impulse = &knockbackMotion{order: k, source: order, launch: launch, start: now, lastScope: scope}
	e.s.mu.Lock()
	e.s.knockbackReports = knockbackReports{order: order, ack: &modv1.WorldPacket{Opcode: opcode.CMSGMoveKnockBackAck, Payload: ack, TransportScope: scope}}
	e.s.mu.Unlock()
	return nil
}

func (e *engine) startKnockbackPlan() {
	k := e.impulse
	if k == nil || k.finished || k.contact != nil {
		return
	}
	e.s.mu.RLock()
	r := e.s.knockbackReports
	e.s.mu.RUnlock()
	if r.order != k.source || r.contact == nil {
		return
	}
	i := r.contact
	age := time.Duration(i.Time) * time.Millisecond
	q := k.point(age)
	// The module rounded first contact up to the next millisecond. Accept
	// the measured surface within that physical chord and the floor tolerance.
	tolerance := .06 + (.001 * math.Hypot(float64(k.order.Horizontal), math.Max(61, math.Abs(float64(k.order.Vertical)))))
	if k.launch.Orientation != i.Orientation || !buddynav.BodyFlightInRange(age, float64(k.order.Horizontal), -float64(k.order.Vertical)) ||
		math.Hypot(math.Hypot(float64(i.X-q.X), float64(i.Y-q.Y)), float64(i.Z-q.Z)) > tolerance {
		return
	}
	k.contact = i
	k.next = time.After(0)
}

func (e *engine) advanceKnockback() error {
	k := e.impulse
	if k == nil || k.finished || k.contact == nil {
		return nil
	}
	k.next = nil
	age := time.Since(k.start).Truncate(time.Millisecond)
	end := time.Duration(k.contact.Time) * time.Millisecond
	i := k.launch
	op := uint32(opcode.MSGMoveHeartbeat)
	final := age >= end
	if final {
		i = *k.contact
		age = end
		op = opcode.MSGMoveFallLand
		if i.PitchPresent {
			op = opcode.MSGMoveStartSwim
		}
	} else {
		q := k.point(age)
		i.X, i.Y, i.Z = q.X, q.Y, q.Z
		i.FallTime = uint32(age.Milliseconds())
	}
	i.Time = k.launch.Time + uint32(age.Milliseconds())
	wire, err := movementinfo.MoverFrame(k.order.Mover, i)
	if err != nil {
		return err
	}
	ctx := context.WithValue(modsession.WithSelfBodyInput(context.Background()), knockbackFrameKey{}, k)
	_, _, err = e.writeContext(ctx, op, wire, k.lastScope)
	if err != nil {
		k.finished = true
		if modcb.PacketWasNotDispatched(err) {
			return nil
		}
		return err
	}
	k.lastScope = e.transport.snapshot()
	if final {
		k.finished = true
		e.s.mu.Lock()
		if r := &e.s.knockbackReports; r.order == k.source {
			r.land = &modv1.WorldPacket{Opcode: op, Payload: wire, TransportScope: k.lastScope}
		}
		e.s.mu.Unlock()
	} else {
		k.next = time.After(max(0, time.Until(k.start.Add(min(age+100*time.Millisecond, end)))))
	}
	return nil
}
