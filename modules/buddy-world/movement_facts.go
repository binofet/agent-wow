package buddyworld

import (
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// These are latest received own facts in a bound visit, not a current motion
// controller, an arrival receipt, or permission to send any packet.
type ownMovementObservation struct {
	known                 bool
	self, visit, revision uint64
	sourceOpcode          uint32
	at                    time.Time
	mapKnown              bool
	mapID                 uint32
	info                  movementinfo.Info
	wire                  []byte
}

type ownSplineObservation struct {
	known                 bool
	self, visit, revision uint64
	sourceOpcode          uint32
	at                    time.Time
	mapKnown              bool
	mapID                 uint32
	header                observedTrajectory
}

// Called only after the whole packet was validated. Pending values are copied
// into the packet clone and never escape a rejected packet or foreign mover.
func (s *observations) commitOwnMovementFacts(old observed, source uint32, now time.Time) {
	p := &s.players[0]
	bound := p.guid != 0 && p.present && p.mapKnown && s.visitRevision != 0
	visit := uint64(0)
	if bound {
		visit = s.visitRevision
	}
	boundary := source == opcode.SMSGLoginVerifyWorld || source == opcode.SMSGNewWorld || source == opcode.SMSGTransferPending ||
		old.guid != p.guid || old.present && !p.present || old.mapKnown && !p.mapKnown
	if boundary || p.receivedMovement {
		m := ownMovementObservation{self: p.guid, visit: visit, revision: s.ownMovement.revision + 1,
			sourceOpcode: source, at: now, mapKnown: bound, mapID: p.mapID}
		if p.receivedMovement && bound && p.receivedMovementInfo.AllFinite() {
			m.known, m.info = true, p.receivedMovementInfo
			m.wire = append([]byte(nil), p.receivedMovementWire[:p.receivedMovementWireLen]...)
		}
		s.ownMovement = m
	}
	if boundary || p.receivedSpline {
		t := ownSplineObservation{self: p.guid, visit: visit, revision: s.ownSpline.revision + 1,
			sourceOpcode: source, at: now, mapKnown: bound, mapID: p.mapID}
		if p.receivedSpline && bound && p.receivedSplineHeader.known && p.receivedSplineHeader.idKnown &&
			(!p.receivedSplineHeader.relative || p.receivedSplineHeader.transportKnown) {
			t.known, t.header = true, p.receivedSplineHeader
		}
		s.ownSpline = t
	}
	p.receivedMovement, p.receivedSpline = false, false
	p.receivedUnitFlags, p.receivedOwnCreate, p.receivedMotionInvalid = false, false, false
	p.receivedPosition = false
	p.receivedMovementInfo, p.receivedSplineHeader = movementinfo.Info{}, observedTrajectory{}
	clear(p.receivedMovementWire[:])
	p.receivedMovementWireLen = 0
}

func (m ownMovementObservation) snapshot(now time.Time) *worldv1.OwnMovementFacts {
	r := &worldv1.OwnMovementFacts{Known: m.known, SelfGuid: m.self, VisitRevision: m.visit,
		Revision: m.revision, SourceOpcode: m.sourceOpcode, AgeMs: age(now, m.at), MapKnown: m.mapKnown, MapId: m.mapID}
	if !m.known {
		return r
	}
	i := m.info
	r.Info = &worldv1.MovementInfoFacts{Flags: i.Flags, ExtraFlags: uint32(i.ExtraFlags), Time: i.Time,
		X: i.X, Y: i.Y, Z: i.Z, Orientation: i.Orientation, PitchKnown: i.PitchPresent, Pitch: i.Pitch,
		FallTime: i.FallTime, SplineElevationKnown: i.SplineElevationPresent, SplineElevation: i.SplineElevation}
	if i.TransportPresent {
		t := i.Transport
		r.Info.Transport = &worldv1.MovementTransportFacts{Guid: t.GUID, X: t.X, Y: t.Y, Z: t.Z,
			Orientation: t.Orientation, Time: t.Time, Seat: int32(t.Seat), Time2Known: t.Time2Present, Time2: t.Time2}
	}
	if i.JumpPresent {
		j := i.Jump
		r.Info.Jump = &worldv1.MovementJumpFacts{Velocity: j.Velocity, SinAngle: j.SinAngle, CosAngle: j.CosAngle, XySpeed: j.XYSpeed}
	}
	r.WireImage = append([]byte(nil), m.wire...)
	return r
}

func (s ownSplineObservation) snapshot(now time.Time) *worldv1.OwnSplineFacts {
	r := &worldv1.OwnSplineFacts{Known: s.known, SelfGuid: s.self, VisitRevision: s.visit,
		Revision: s.revision, SourceOpcode: s.sourceOpcode, AgeMs: age(now, s.at), MapKnown: s.mapKnown, MapId: s.mapID}
	if !s.known {
		return r
	}
	t := s.header
	r.IdKnown, r.SplineId, r.Flags = t.idKnown, t.id, t.flags
	r.DurationMs, r.ElapsedMs = t.duration, t.elapsed
	r.DestinationKnown = t.destinationKnown
	r.DestinationX, r.DestinationY, r.DestinationZ = t.destination[0], t.destination[1], t.destination[2]
	r.TransportRelative, r.TransportGuid, r.TransportSeat = t.relative, t.transport, int32(t.seat)
	r.Stopped, r.Cyclic = t.stopped, t.cyclic
	return r
}
