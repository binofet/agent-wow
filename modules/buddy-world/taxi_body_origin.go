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
	source        *modv1.TransportScope
	clear, origin uint64
	revision      uint64
}

func (s *Service) observeTaxiBodyOrigin(ctx context.Context, packet *modv1.WorldPacket, received observed, next *observations, now time.Time) {
	b, m, p, scope := &s.taxiBody, &s.motion, &next.players[0], packet.TransportScope
	f := &m.naturalTaxi
	if !scopeMatchesSelf(scope, received, next.visitRevision) || !snapshot(received, now).GetAlive() || received.receivedMotionInvalid {
		*b = taxiBodyObservation{}
		return
	}
	if f.source != nil && f.taxiOn != 0 && (b.source == nil || !sameMotionSource(b.source, f.source)) {
		*b = taxiBodyObservation{source: cloneMotionScope(f.source)}
	}
	if b.source == nil {
		return
	}
	if !sameMotionVisit(b.source, scope) || received.receivedOwnCreate || received.receivedSpline && !sameMotionSource(b.source, scope) {
		*b = taxiBodyObservation{}
		return
	}
	if b.clear != 0 && (received.receivedPosition && !received.receivedSpline || received.receivedMovement) {
		// A genuinely newer complete realm pose always supersedes a projection,
		// even when the correction's coordinates differ from the declared end.
		b.origin, b.revision = 0, 0
		i := received.receivedMovementInfo
		if received.receivedMovement && scope.MotionRevision > b.clear && received.positionKnown && !received.positionInferred &&
			m.unitFlagsKnown && m.unitFlags&movementflags.UnsafeUnitFlags == 0 && i.AllFinite() && movementflags.GroundCompatible(i.Flags, uint32(i.ExtraFlags)) && i.Flags&0xff == 0 && i.FallTime == 0 {
			b.origin, b.revision = scope.MotionRevision, p.positionRevision
		}
	} else if b.clear == 0 {
		if f.source == nil || !sameMotionSource(b.source, f.source) {
			*b = taxiBodyObservation{}
			return
		}
		// A stationary MovementInfo received before control returns may still
		// describe departure. It belongs in OwnMovementFacts only.
		if p.positionKnown {
			p.positionKnown, p.positionInferred, p.movementKnown = false, false, false
			p.positionRevision++
		}
		if f.cleared == 0 {
			return
		}
		b.clear = scope.MotionRevision
		h := next.ownSpline.header
		// An early dismount or unsupported spline has no witnessed absolute
		// endpoint. Its later actual realm correction can still restore motion.
		if !ordinaryFlyingSpline(h) || !h.endBearingKnown || splineRemaining(h, now) != 0 || m.unitFlags&movementflags.UnsafeUnitFlags != 0 {
			return
		}
		p.x, p.y, p.z, p.orientation = h.destination[0], h.destination[1], h.destination[2], h.endBearing
		p.positionKnown, p.positionInferred, p.movementKnown = true, true, true
		p.movementFlags, p.extraFlags, p.movementTime = 0, 0, 0
		p.positionAt, p.observedAt = now, now
		p.positionRevision++
		b.origin, b.revision = scope.MotionRevision, p.positionRevision
	}
	if b.origin == 0 || !p.positionKnown || !p.movementKnown || p.positionRevision != b.revision ||
		!m.unitFlagsKnown || m.unitFlags&movementflags.UnsafeUnitFlags != 0 || (p.positionInferred && (f.cleared == 0 || !sameMotionSource(f.source, scope))) {
		return
	}
	wire := binary.LittleEndian.AppendUint64(nil, b.origin)
	for _, v := range []float32{p.x, p.y, p.z} {
		wire = binary.LittleEndian.AppendUint32(wire, math.Float32bits(v))
	}
	_ = grpc.SetTrailer(ctx, metadata.Pairs(modcb.TaxiBodyOriginTrailer, string(wire)))
}
