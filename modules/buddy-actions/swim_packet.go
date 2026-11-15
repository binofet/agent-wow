package buddyactions

import (
	"encoding/binary"
	"math"

	"github.com/agent-wow/agent-wow/pkg/movementflags"
)

const (
	swimMovementFlag   = 0x00200000
	swimAscendingFlag  = 0x00400000
	swimDescendingFlag = 0x00800000
)

// swimMovementPacket is ordinary Wrath MovementInfo for movement in water.
// Swimming inserts a pitch in radians after XYZO, before the mandatory
// fallTime. Even a stationary swimmer has this extra field.
// https://github.com/azerothcore/azerothcore-wotlk/blob/49bdb66ce2fe00771c9e27a9859afd344b7b1344/src/server/game/Server/WorldSession.cpp#L1099
func swimMovementPacket(guid uint64, clock uint32, p position, pitch float32, flags uint32) []byte {
	const allowed = 0x1ff | swimMovementFlag | swimAscendingFlag | swimDescendingFlag | movementflags.PassiveModifiers
	if guid == 0 || guid>>48 != 0 || !finite(p) || math.IsNaN(float64(pitch)) ||
		pitch < -float32(math.Pi/2) || pitch > float32(math.Pi/2) || flags&swimMovementFlag == 0 || flags&^allowed != 0 {
		return nil
	}
	for _, opposing := range []uint32{0x3, 0xc, 0x30, 0xc0, swimAscendingFlag | swimDescendingFlag} {
		if flags&opposing == opposing {
			return nil
		}
	}
	packet := movementPacket(guid, clock, p, false)
	body := packet[len(packet)-30:]
	binary.LittleEndian.PutUint32(body, flags)
	binary.LittleEndian.PutUint32(body[26:], math.Float32bits(pitch))
	return binary.LittleEndian.AppendUint32(packet, 0)
}
