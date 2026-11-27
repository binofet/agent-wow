package modcb

import (
	"context"
	"encoding/binary"
	"strconv"

	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/metadata"
)

// The packet count comes from the exact World snapshot used for a ground
// arrival. The authenticated host compares it with the subscriber's input.
const flightpathWorldPackets = "agent-wow-flightpath-world-packets"

func WithFlightpathWorldPackets(ctx context.Context, count uint64) context.Context {
	return metadata.AppendToOutgoingContext(ctx, flightpathWorldPackets, strconv.FormatUint(count, 10))
}

func IncomingFlightpathWorldPackets(ctx context.Context) (count uint64, valid, supplied bool) {
	values := metadata.ValueFromIncomingContext(ctx, flightpathWorldPackets)
	if len(values) != 1 {
		return 0, false, len(values) != 0
	}
	n, err := strconv.ParseUint(values[0], 10, 64)
	return n, err == nil && n > 0 && strconv.FormatUint(n, 10) == values[0], true
}

func FlightpathQueryPacket(op uint32, payload []byte) bool {
	if (op != opcode.CMSGTaxinodeStatusQuery && op != opcode.CMSGTaxiqueryavailablenodes) || len(payload) != 8 {
		return false
	}
	return flightmasterGUID(binary.LittleEndian.Uint64(payload))
}

// FlightpathGroundPacket is the set of ordinary flightmaster packets that can
// bind to a proved ground STOP. Free Lua discovery retains its query-only check.
func FlightpathGroundPacket(op uint32, payload []byte) bool {
	if op != opcode.CMSGActivatetaxi {
		return FlightpathQueryPacket(op, payload)
	}
	if len(payload) != 16 || !flightmasterGUID(binary.LittleEndian.Uint64(payload)) {
		return false
	}
	from, to := binary.LittleEndian.Uint32(payload[8:]), binary.LittleEndian.Uint32(payload[12:])
	return from > 0 && from <= 448 && to > 0 && to <= 448 && from != to
}

func flightmasterGUID(guid uint64) bool {
	return guid>>48 == 0xf130 && guid&0xffffff != 0 && (guid>>24)&0xffffff != 0
}
