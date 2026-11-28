package buddyworld

import (
	"context"
	"encoding/binary"
	"math"
	"time"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// World keeps the received raw movement unchanged. The projection records
// the separate origin available to a new directional input after this ride.

type taxiBodyObservation struct {
	source                  *modv1.TransportScope
	departure               [3]float32
	departureKnown          bool
	clear, origin, revision uint64
}

func (s *Service) taxiBodyOriginRevision(p *observed, scope *modv1.TransportScope) uint64 {
	b, m := &s.taxiBody, &s.motion
	if b.origin == 0 || !p.positionKnown || !p.movementKnown || p.positionRevision != b.revision ||
		!m.unitFlagsKnown || m.unitFlags&movementflags.UnsafeUnitFlags != 0 || !sameMotionVisit(b.source, scope) ||
		p.positionInferred && (m.naturalTaxi.cleared == 0 || !sameMotionSource(m.naturalTaxi.source, scope)) {
		return 0
	}
	return b.origin
}

func (s *Service) observeTaxiBodyOrigin(ctx context.Context, packet *modv1.WorldPacket, received observed, next *observations, now time.Time) {
	b, m, p, scope := &s.taxiBody, &s.motion, &next.players[0], packet.TransportScope
	f := &m.naturalTaxi
	newVisitRaw := next.visitRevision > 1 && !scope.GetSplineActive() && received.receivedMovement
	if !newVisitRaw && b.source == nil && (f.source == nil || f.taxiOn == 0) {
		return
	}
	defer func() {
		if origin := s.taxiBodyOriginRevision(p, scope); origin != 0 {
			wire := binary.LittleEndian.AppendUint64(nil, origin)
			for _, v := range []float32{p.x, p.y, p.z} {
				wire = binary.LittleEndian.AppendUint32(wire, math.Float32bits(v))
			}
			_ = grpc.SetTrailer(ctx, metadata.Pairs(modcb.TaxiBodyOriginTrailer, string(wire)))
		}
	}()
	if !scopeMatchesSelf(scope, received, next.visitRevision) || !snapshot(received, now).GetAlive() || received.receivedMotionInvalid {
		*b = taxiBodyObservation{}
		return
	}
	// A separate World entry can interrupt a ride. Keep only a genuine
	// stationary image newly received in that visit, with current unit
	// state. A coordinate in NEW_WORLD or an old raw image is insufficient.
	// Core separately compares this visit against its physical booking.
	if newVisitRaw {
		*b = taxiBodyObservation{}
		i := received.receivedMovementInfo
		if received.positionKnown && !received.positionInferred && received.movementKnown && m.unitFlagsKnown && m.unitFlags&movementflags.UnsafeUnitFlags == 0 &&
			movementflags.GroundCompatible(i.Flags, uint32(i.ExtraFlags)) && i.Flags&0x1ff == 0 && i.FallTime == 0 && !i.TransportPresent {
			*b = taxiBodyObservation{source: cloneMotionScope(scope), origin: scope.MotionRevision, revision: received.positionRevision}
		}
		return
	}
	if b.source != nil && !b.source.SplineActive && (scope.SplineActive || !m.unitFlagsKnown || m.unitFlags&movementflags.UnsafeUnitFlags != 0) {
		*b = taxiBodyObservation{}
	}
	if f.source != nil && f.taxiOn != 0 && !sameMotionSource(b.source, f.source) {
		raw := next.ownMovement
		*b = taxiBodyObservation{source: cloneMotionScope(f.source), departureKnown: raw.known && raw.self == received.guid && raw.visit == next.visitRevision,
			departure: [3]float32{raw.info.X, raw.info.Y, raw.info.Z}}
	}
	if !sameMotionVisit(b.source, scope) || received.receivedOwnCreate || received.receivedSpline && !sameMotionSource(b.source, scope) {
		*b = taxiBodyObservation{}
		return
	}
	explicit := s.completion.operation
	if explicit != nil && (explicit.Pending || explicit.TransportUnknown && !explicit.Retired || sameMotionSource(explicit.TransportScope, b.source) && explicit.TransportWritten) {
		// The existing explicit completion needs its subsequent actual image.
		// A different unresolved completion never supplies a natural origin.
		b.origin = 0
		if explicit.Retired && !s.completion.retirementBlocked && sameMotionSource(explicit.RetirementScope, scope) && received.positionKnown && !received.positionInferred {
			b.origin, b.revision = explicit.RetirementScope.MotionRevision, p.positionRevision
		}
	} else if b.clear != 0 && (received.receivedPosition && !received.receivedSpline || received.receivedMovement) {
		// An actual MovementInfo received after clear supersedes the spline's
		// position even when the realm places the body somewhere else.
		b.origin = 0
		i := received.receivedMovementInfo
		if received.receivedMovement && scope.MotionRevision > b.clear && received.positionKnown && !received.positionInferred &&
			i.AllFinite() && movementflags.GroundCompatible(i.Flags, uint32(i.ExtraFlags)) && i.Flags&0xff == 0 && i.FallTime == 0 {
			b.origin, b.revision = scope.MotionRevision, p.positionRevision
		}
	} else if b.clear == 0 && f.cleared != 0 && sameMotionSource(b.source, f.source) {
		b.clear = scope.MotionRevision
		h, raw := next.ownSpline.header, next.ownMovement
		end := h.destination
		if !ordinaryFlyingSpline(h) || m.unitFlags&movementflags.UnsafeUnitFlags != 0 {
			return
		}
		if raw.known && sameMotionSource(raw.scope, scope) && raw.scope.MotionRevision > scope.SplineSourceRevision {
			seen := [3]float32{raw.info.X, raw.info.Y, raw.info.Z}
			if !p.positionInferred && p.positionKnown && seen == end {
				// A real stationary endpoint may precede the field-only dismount.
				b.origin, b.revision = scope.MotionRevision, p.positionRevision
			} else if !b.departureKnown || seen != b.departure {
				// The realm supplied a different position mid-flight. Require
				// another genuine post-clear body update to resolve it.
				return
			}
		}
		if b.origin == 0 {
			// Time alone is insufficient. The original complete flight and the
			// distinct terminal flag edge must both agree. Early dismounts keep
			// waiting for a new raw pose instead of later promoting a timer.
			if !h.endBearingKnown || splineRemaining(h, now) != 0 {
				return
			}
			p.x, p.y, p.z, p.orientation = end[0], end[1], end[2], h.endBearing
			p.positionKnown, p.positionInferred, p.movementKnown = true, true, true
			p.movementFlags, p.extraFlags, p.movementTime = 0, 0, 0
			p.positionAt, p.observedAt = now, now
			p.positionRevision++
			b.origin, b.revision = scope.MotionRevision, p.positionRevision
		}
	}
}
