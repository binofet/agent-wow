package buddyactions

import (
	"encoding/binary"
	"fmt"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
)

const (
	flightpathMaskWords = modcb.FlightpathMaskWords
	flightpathNodeLimit = flightpathMaskWords * 32
)

// Wrath 3.3.5a/build 12340 uses an unpacked GUID and one boolean byte for
// SMSG_TAXINODE_STATUS, and a show tag, GUID, source node and fourteen mask
// words for SMSG_SHOWTAXINODES. These layouts follow the public TaxiHandler:
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/TaxiHandler.cpp
// The client build fixes this wire contract; no server source revision is
// inferred from a received packet. NPC identity and operation attribution are
// separate from these pure payload decoders.
func decodeFlightpathStatus(payload []byte) (npc uint64, known bool, err error) {
	if len(payload) != 9 {
		return 0, false, fmt.Errorf("taxi status requires exactly 9 bytes, got %d", len(payload))
	}
	npc = binary.LittleEndian.Uint64(payload)
	if npc == 0 || payload[8] > 1 {
		return 0, false, fmt.Errorf("taxi status requires a nonzero GUID and boolean known byte")
	}
	return npc, payload[8] == 1, nil
}

func decodeFlightpathMenu(payload []byte) (npc uint64, source uint32, knownNodes []uint32, err error) {
	npc, source, err = modcb.FlightpathMenuHeader(payload)
	if err != nil {
		return 0, 0, nil, err
	}
	knownNodes = make([]uint32, 0)
	for word := 0; word < flightpathMaskWords; word++ {
		mask := binary.LittleEndian.Uint32(payload[16+word*4:])
		for bit := 0; bit < 32; bit++ {
			if mask&(uint32(1)<<bit) != 0 {
				knownNodes = append(knownNodes, uint32(word*32+bit+1))
			}
		}
	}
	// Preserve the server's exact mask, including an absent source-node bit.
	// A mask is not a route, fare, permission to activate, or action receipt.
	return npc, source, knownNodes, nil
}
