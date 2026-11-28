package buddyworld

import (
	"math"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Build 12340 Mo_Transport uses a global counter. Its template entry comes
// from OBJECT_FIELD_ENTRY and its type from GAMEOBJECT_BYTES_1.
func movingTransportGUID(guid uint64) bool {
	return movementinfo.MovingTransportGUID(guid)
}

// Unit::BuildMovementPacket names the actual transport or the vehicle base.
// Keep the complete raw own image separately, including unknown GUID types.
func passengerTransportGUID(guid uint64) bool {
	switch guid >> 48 {
	case 0x1fc0:
		return movingTransportGUID(guid)
	case 0xf110, 0xf120, 0xf130, 0xf140, 0xf150:
		return guid&0xffffff != 0
	case 0:
		return publicPlayerGUID(guid)
	}
	return false
}

func movingTransport(p *observed) bool {
	return movingTransportGUID(p.guid) && p.known[objectEntry] && p.fields[objectEntry] != 0 &&
		p.known[gameObjectBytes] && p.fields[gameObjectBytes]>>8&0xff == 15
}

type transportSource struct {
	visit, revision uint64
	opcode          uint32
}

func (s *observations) nextTransportSource(op uint32) transportSource {
	s.transportRevision++
	return transportSource{visit: s.visitRevision, revision: s.transportRevision, opcode: op}
}

type receivedPassenger struct {
	source transportSource
	at     time.Time
	info   movementinfo.Info
}

func (p *observed) passengerSnapshot(now time.Time) *worldv1.PassengerMovementFacts {
	r := p.passenger
	if r == nil || !p.present || !p.mapKnown || !p.positionKnown || p.positionInferred || !p.movementKnown {
		return nil
	}
	return &worldv1.PassengerMovementFacts{Info: movementFacts(r.info), VisitRevision: r.source.visit,
		Revision: r.source.revision, AgeMs: age(now, r.at), SourceOpcode: r.source.opcode}
}

type receivedHull struct {
	source           transportSource
	at               time.Time
	flags            uint16
	progress         uint32
	rotation         uint64
	positionRevision uint64
}

type receivedPathFraction struct {
	source transportSource
	at     time.Time
}

func (p *observed) transportSnapshot(now time.Time) *worldv1.MovingTransportFacts {
	if !movingTransport(p) || !p.present || !p.mapKnown {
		return nil
	}
	r := &worldv1.MovingTransportFacts{ObjectRevision: p.transportBirth}
	if q := p.shipQuery; q != nil {
		r.TemplateQueryPhase, r.Template = q.write, q.snapshot(p, now)
	}
	if p.known[0x08] {
		display := p.fields[0x08]
		r.DisplayId = &display
	}
	if p.known[0x04] {
		scale := math.Float32frombits(p.fields[0x04])
		if validFloat(scale) && scale > 0 {
			r.Scale = &scale
		}
	}
	if p.known[0x10] {
		period := p.fields[0x10]
		r.PeriodMs = &period
	}
	if f := p.transportFraction; f != nil {
		fraction := p.fields[gameObjectDynamic] >> 16
		r.PathFraction = &fraction
		r.PathFractionRevision, r.PathFractionAgeMs, r.PathFractionSourceOpcode = f.source.revision, age(now, f.at), f.source.opcode
	}
	if h := p.transport; h != nil {
		r.MovementRevision, r.MovementAgeMs, r.SourceOpcode = h.source.revision, age(now, h.at), h.source.opcode
		if p.positionKnown && p.positionRevision == h.positionRevision {
			r.PositionRevision = h.positionRevision
		}
		if h.flags&0x02 != 0 {
			v := h.progress
			r.PathProgressMs = &v
		}
		if h.flags&0x200 != 0 {
			v := h.rotation
			r.PackedRotation = &v
		}
	}
	return r
}

// A removed or replaced hull breaks the link to its prior passengers. Keep
// every actual rider's independent world pose and all unrelated vessels.
func (s *observations) clearPassengersOf(hull uint64) {
	matches := func(p *observed) bool {
		return p.passenger != nil && (hull == 0 || p.passenger.info.Transport.GUID == hull)
	}
	for i := range s.players {
		if matches(&s.players[i]) {
			s.players[i].passenger = nil
		}
	}
	for id, p := range s.nearbyPlayers {
		if matches(p) {
			fresh := *p
			fresh.passenger = nil
			s.nearbyPlayers[id] = &fresh
		}
	}
}

func (s *observations) forgetTransportScene() {
	s.clearPassengersOf(0)
	for id := range s.gameObjects {
		if movingTransportGUID(id) {
			delete(s.gameObjects, id)
		}
	}
}

func (s *observations) invalidateTransportPacket(op uint32, data []byte) {
	c := cursor{b: data}
	var guid uint64
	switch {
	case op == opcode.SMSGTransferPending:
		s.transportWorldPending = true
		s.forgetTransportScene()
		return
	case op == opcode.SMSGUpdateObject, op == opcode.SMSGCompressedUpdateObject, op == opcode.SMSGNewWorld,
		op == opcode.SMSGLoginVerifyWorld:
		s.forgetTransportScene()
		return
	case op == opcode.SMSGDestroyObject:
		guid = c.u64()
	case movementOpcode(op), op == opcode.MSGMoveTeleportAck, op == opcode.SMSGMoveKnockBack, op == opcode.MSGMoveKnockBack,
		op == opcode.SMSGMonsterMove, op == opcode.SMSGMonsterMoveTransport:
		guid = c.guid()
	default:
		return
	}
	if c.err != nil || guid == 0 || op == opcode.SMSGDestroyObject && guid == s.players[0].guid {
		s.forgetTransportScene()
	} else if movingTransportGUID(guid) {
		s.clearPassengersOf(guid)
		delete(s.gameObjects, guid)
	} else if p := player(&s.players, guid); p != nil {
		p.passenger = nil
	} else if p := s.nearbyPlayers[guid]; p != nil {
		p.passenger = nil
	}
}
