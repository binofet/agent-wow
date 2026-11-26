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
	guid := binary.LittleEndian.Uint64(payload)
	return guid>>48 == 0xf130 && guid&0xffffff != 0 && (guid>>24)&0xffffff != 0
}
