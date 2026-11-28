package modcb

import (
	"context"
	"encoding/binary"
	"math"

	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"google.golang.org/grpc/metadata"
)

const routeHullKey = "agentwow-route-hull-bin"
const RouteHullTrailer = "agentwow-observed-route-hull-bin"

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
