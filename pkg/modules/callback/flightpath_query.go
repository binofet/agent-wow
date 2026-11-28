package modcb

import (
	"context"
	"encoding/binary"
	"fmt"
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

func FlightpathMenuPacket(op uint32, payload []byte) bool {
	return op == opcode.CMSGTaxiqueryavailablenodes && FlightpathQueryPacket(op, payload)
}

const FlightpathMaskWords = 14

// FlightpathMenuHeader validates the full build-12340 SHOWTAXINODES shape.
// A malformed prefix must not consume the one physically correlated response.
func FlightpathMenuHeader(payload []byte) (uint64, uint32, error) {
	const size = 16 + FlightpathMaskWords*4
	if len(payload) != size {
		return 0, 0, fmt.Errorf("taxi menu requires exactly %d bytes, got %d", size, len(payload))
	}
	if binary.LittleEndian.Uint32(payload) != 1 {
		return 0, 0, fmt.Errorf("taxi menu requires the show tag")
	}
	npc, source := binary.LittleEndian.Uint64(payload[4:]), binary.LittleEndian.Uint32(payload[12:])
	if npc == 0 || source == 0 || source > FlightpathMaskWords*32 {
		return 0, 0, fmt.Errorf("taxi menu requires a nonzero GUID and source node 1..%d", FlightpathMaskWords*32)
	}
	return npc, source, nil
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
