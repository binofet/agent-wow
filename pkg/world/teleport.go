package world

import (
	"encoding/binary"
	"math"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// These acknowledge destinations commanded by the server; they never request
// a destination or supply client-invented coordinates. Source: Player.cpp
// SendTeleportAckPacket/TeleportTo and MovementHandler.cpp's corresponding
// HandleMoveTeleportAck/HandleMoveWorldportAckOpcode.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Player/Player.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/MovementHandler.cpp
func teleportAcknowledgement(op uint16, body []byte, selfGUID uint64, clock uint32) (uint32, []byte, bool) {
	if selfGUID == 0 || selfGUID>>48 != 0 {
		return 0, nil, false
	}
	if op == opcode.SMSGNewWorld {
		if len(body) != 20 {
			return 0, nil, false
		}
		c := teleportCursor{body: body[4:]}
		for i := 0; i < 4; i++ {
			c.coordinate()
		}
		if !c.complete() {
			return 0, nil, false
		}
		return opcode.MSGMoveWorldportAck, nil, true
	}
	if op != opcode.MSGMoveTeleportAck || len(body) > 1024 {
		return 0, nil, false
	}
	c := teleportCursor{body: body}
	guid := c.guid()
	guidEnd := c.offset
	order := c.u32()
	if guid != selfGUID || !c.movement() || !c.complete() {
		return 0, nil, false
	}
	// Client direction of MSG_MOVE_TELEPORT_ACK is packed GUID, uint32 order
	// counter (called flags by the handler), uint32 shared client timestamp.
	reply := append([]byte(nil), body[:guidEnd]...)
	reply = binary.LittleEndian.AppendUint32(reply, order)
	reply = binary.LittleEndian.AppendUint32(reply, clock)
	return opcode.MSGMoveTeleportAck, reply, true
}

type teleportCursor struct {
	body   []byte
	offset int
	bad    bool
}

func (c *teleportCursor) take(n int) []byte {
	if c.bad || n < 0 || n > len(c.body)-c.offset {
		c.bad = true
		return nil
	}
	b := c.body[c.offset : c.offset+n]
	c.offset += n
	return b
}
func (c *teleportCursor) u32() uint32 {
	b := c.take(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}
func (c *teleportCursor) guid() uint64 {
	b := c.take(1)
	if b == nil {
		return 0
	}
	mask := b[0]
	var guid uint64
	for i := uint(0); i < 8; i++ {
		if mask&(1<<i) != 0 {
			v := c.take(1)
			if v == nil {
				return 0
			}
			guid |= uint64(v[0]) << (i * 8)
		}
	}
	return guid
}
func (c *teleportCursor) coordinate() {
	v := math.Float32frombits(c.u32())
	if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || math.Abs(float64(v)) > 40000 {
		c.bad = true
	}
}
func (c *teleportCursor) complete() bool { return !c.bad && c.offset == len(c.body) }

// MovementInfo has no leading GUID here; the server packet supplied that
// before its order counter. Validate every optional tail before sending ACK.
func (c *teleportCursor) movement() bool {
	flags := c.u32()
	b := c.take(2)
	if b == nil {
		return false
	}
	extra := binary.LittleEndian.Uint16(b)
	c.u32() // server movement timestamp, not echoed as our client clock
	for i := 0; i < 4; i++ {
		c.coordinate()
	}
	if flags&0x200 != 0 {
		c.guid()
		for i := 0; i < 4; i++ {
			c.coordinate()
		}
		c.u32()
		c.take(1)
		if extra&0x400 != 0 {
			c.u32()
		}
	}
	if flags&0x02200000 != 0 || extra&0x20 != 0 {
		c.coordinate()
	}
	c.u32() // fallTime is always present
	if flags&0x1000 != 0 {
		for i := 0; i < 4; i++ {
			c.coordinate()
		}
	}
	if flags&0x04000000 != 0 {
		c.coordinate()
	}
	return !c.bad
}
