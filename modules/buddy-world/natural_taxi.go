package buddyworld

import (
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// A receive-only observation. It owns no coordinates or completion operation.
type naturalTaxiFlight struct {
	source          *modv1.TransportScope
	taxiOn, cleared uint64
}

func ordinaryFlyingSpline(h observedTrajectory) bool {
	return h.known && h.idKnown && h.kind == 0 && !h.stopped && !h.relative && !h.cyclic &&
		h.destinationKnown && h.duration != 0 && h.flags&0x2000 != 0 &&
		h.flags&(unsafeEndpointFlags|0x200|0x800|0x200000|0x800000|0x1000000) == 0
}

func (m *ownMotionAuthority) observeNaturalTaxi(packet *modv1.WorldPacket, previous *modv1.TransportScope, p observed, next *observations, continuous bool) {
	f := &m.naturalTaxi
	scope := packet.TransportScope
	if !continuous || !scope.GetSplineActive() || !p.present || !p.known[unitHealth] || p.fields[unitHealth] == 0 ||
		p.receivedOwnCreate || p.receivedTaxiBarrier || p.receivedMotionInvalid || p.receivedPosition && !p.receivedSpline && !p.receivedMovement {
		*f = naturalTaxiFlight{}
		return
	}
	if p.receivedSpline {
		*f = naturalTaxiFlight{}
		if packet.Opcode != opcode.SMSGMonsterMove || !m.known || m.splineRevision != next.ownSpline.revision ||
			!ordinaryFlyingSpline(p.receivedSplineHeader) || scope.SplineSourceRevision != scope.MotionRevision ||
			previous.GetSplineRevision() == scope.SplineRevision {
			return
		}
		f.source = cloneMotionScope(scope)
	}
	if f.source == nil || !sameMotionSource(f.source, scope) || !m.unitFlagsKnown {
		*f = naturalTaxiFlight{}
		return
	}
	if p.receivedMovement {
		i := p.receivedMovementInfo
		if !i.AllFinite() || !movementflags.GroundCompatible(i.Flags, uint32(i.ExtraFlags)) || i.Flags&0xff != 0 || i.FallTime != 0 {
			*f = naturalTaxiFlight{}
			return
		}
	}
	if m.unitFlags&0x100000 != 0 {
		if f.cleared != 0 {
			*f = naturalTaxiFlight{}
		} else if f.taxiOn == 0 {
			f.taxiOn = m.unitFlagsMotion
		}
		return
	}
	if p.receivedUnitFlags && f.taxiOn != 0 && f.cleared == 0 &&
		m.unitFlagsMotion > f.taxiOn && m.unitFlagsMotion > f.source.SplineSourceRevision {
		f.cleared = m.unitFlagsRevision
	}
}
