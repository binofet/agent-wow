package modcb

import (
	"context"
	"encoding/binary"
	"math"

	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"google.golang.org/grpc/metadata"
)

const routeHullKey = "agentwow-route-hull-bin"
const surfaceBodySelectionKey = "agentwow-surface-body-selection-bin"
const RouteHullTrailer = "agentwow-observed-route-hull-bin"

type RouteHullRider struct{ Player, Ship uint64 }

// This header precedes World's hull images. It ties the complete result to
// the physical input order even when a foreign rider's packet leaves the
// controlled character's independent motion revision unchanged.
func RouteHullHeader(sequence uint64, riders []RouteHullRider) string {
	b := binary.LittleEndian.AppendUint64(nil, sequence)
	for _, r := range riders {
		b = binary.LittleEndian.AppendUint64(b, r.Player)
		b = binary.LittleEndian.AppendUint64(b, r.Ship)
	}
	return string(b)
}

func ReadRouteHullHeader(wire string) (uint64, map[uint64]uint64, bool) {
	// World retains self, owner and up to 64 public nearby players.
	if len(wire) < 8 || (len(wire)-8)%16 != 0 || len(wire) > 8+66*16 {
		return 0, nil, false
	}
	b := []byte(wire)
	seq := binary.LittleEndian.Uint64(b)
	var riders map[uint64]uint64
	if len(b) > 8 {
		riders = make(map[uint64]uint64, (len(b)-8)/16)
	}
	for b = b[8:]; len(b) > 0; b = b[16:] {
		player, ship := binary.LittleEndian.Uint64(b), binary.LittleEndian.Uint64(b[8:])
		if player>>48 != 0 || uint32(player) == 0 || !movementinfo.MovingTransportGUID(ship) || riders[player] != 0 {
			return 0, nil, false
		}
		riders[player] = ship
	}
	return seq, riders, seq != 0 && seq != math.MaxUint64
}

// RouteHull binds a selected moving GameObject lifetime to the World read
// that chose it. In a World trailer, Packets is the last hull-changing read.
type RouteHull struct {
	GUID, Birth, Packets uint64
}

func (h RouteHull) Valid() bool {
	return movementinfo.MovingTransportGUID(h.GUID) && h.Birth != 0 && h.Packets != 0 && h.Packets < math.MaxUint64
}

func (h RouteHull) Wire() string {
	var b [24]byte
	binary.LittleEndian.PutUint64(b[:], h.GUID)
	binary.LittleEndian.PutUint64(b[8:], h.Birth)
	binary.LittleEndian.PutUint64(b[16:], h.Packets)
	return string(b[:])
}

func ReadRouteHull(row string) (RouteHull, bool) {
	if len(row) != 24 {
		return RouteHull{}, false
	}
	b := []byte(row)
	h := RouteHull{binary.LittleEndian.Uint64(b), binary.LittleEndian.Uint64(b[8:]), binary.LittleEndian.Uint64(b[16:])}
	return h, h.Valid()
}

func WithRouteHull(ctx context.Context, guid, birth, packets uint64) context.Context {
	return metadata.AppendToOutgoingContext(ctx, routeHullKey, (RouteHull{guid, birth, packets}).Wire())
}

func IncomingRouteHull(ctx context.Context) (guid, birth, packets uint64, valid, supplied bool) {
	rows := metadata.ValueFromIncomingContext(ctx, routeHullKey)
	if len(rows) != 1 {
		return 0, 0, 0, false, len(rows) != 0
	}
	h, ok := ReadRouteHull(rows[0])
	return h.GUID, h.Birth, h.Packets, ok, true
}

// The authenticated MoveBody callback keeps its policy visit and sequence.
// These are independent of the physical transport's connection and visit.
func WithSurfaceBodySelection(ctx context.Context, visit, sequence uint64) context.Context {
	var b [16]byte
	binary.LittleEndian.PutUint64(b[:], visit)
	binary.LittleEndian.PutUint64(b[8:], sequence)
	return metadata.AppendToOutgoingContext(ctx, surfaceBodySelectionKey, string(b[:]))
}

func IncomingSurfaceBodySelection(ctx context.Context) (visit, sequence uint64, supplied bool) {
	rows := metadata.ValueFromIncomingContext(ctx, surfaceBodySelectionKey)
	if len(rows) != 1 || len(rows[0]) != 16 {
		return 0, 0, len(rows) != 0
	}
	b := []byte(rows[0])
	return binary.LittleEndian.Uint64(b), binary.LittleEndian.Uint64(b[8:]), true
}
