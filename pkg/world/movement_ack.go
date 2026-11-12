package world

import (
	"encoding/binary"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// movementControl belongs only to the transport owner loop. It retains a
// bounded plain-ground MovementInfo from an actual server pose or a successful
// own movement write. It never supplies a new destination. Unsupported movement
// modes invalidate it rather than guessing transport, falling or swimming data.
type movementControl struct{ info []byte }

const plainMovementFlags = uint32(0x1ff | 0x800 | 0x10000000)

func plainMovementInfo(body []byte) bool {
	if len(body) != 30 || binary.LittleEndian.Uint32(body)&^plainMovementFlags != 0 || binary.LittleEndian.Uint16(body[4:]) != 0 {
		return false
	}
	c := teleportCursor{body: body}
	return c.movement() && c.complete()
}

func (m *movementControl) observed(op uint16, body []byte, self uint64, clock uint32) {
	switch op {
	case opcode.SMSGTransferPending:
		m.info = nil
	case opcode.SMSGLoginVerifyWorld, opcode.SMSGNewWorld:
		m.info = nil
		if _, _, valid := teleportAcknowledgement(opcode.SMSGNewWorld, body, self, clock); !valid {
			return
		}
		info := make([]byte, 30)
		binary.LittleEndian.PutUint32(info[6:], clock)
		copy(info[10:26], body[4:])
		m.info = info
	case opcode.MSGMoveTeleportAck:
		m.info = nil
		if _, _, valid := teleportAcknowledgement(op, body, self, clock); !valid {
			return
		}
		c := teleportCursor{body: body}
		c.guid()
		c.u32()
		if info := body[c.offset:]; plainMovementInfo(info) {
			m.info = append([]byte(nil), info...)
		}
	}
}

func (m *movementControl) sent(op uint32, body []byte, self uint64) {
	switch op {
	case opcode.MSGMoveStartForward, opcode.MSGMoveStop, opcode.MSGMoveHeartbeat:
		m.info = nil
		c := teleportCursor{body: body}
		if c.guid() != self || self == 0 || c.bad {
			return
		}
		if info := body[c.offset:]; plainMovementInfo(info) {
			m.info = append([]byte(nil), info...)
		}
	}
}

// Unit::SendMoveRoot sends packed GUID + order counter. HandleMoveRootAck
// reads packed GUID + the same counter + MovementInfo. In particular the
// ordinary resurrection/release path needs UNROOT acknowledged BEFORE the
// following graveyard teleport advances the server map-order counter. Handling
// it here synchronously avoids a delayed gameplay callback acknowledging an
// obsolete map. ROOT never accompanies moving flags in our response.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Unit/Unit.cpp
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Handlers/MovementHandler.cpp
func (m *movementControl) acknowledgement(op uint16, body []byte, self uint64, clock uint32) (uint32, []byte, []byte, bool) {
	if self == 0 || self>>48 != 0 || len(body) > 13 || !plainMovementInfo(m.info) {
		return 0, nil, nil, false
	}
	c := teleportCursor{body: body}
	if c.guid() != self {
		return 0, nil, nil, false
	}
	c.u32() // preserve the exact server order; never synthesize or advance it
	if !c.complete() {
		return 0, nil, nil, false
	}
	var replyOp uint32
	info := append([]byte(nil), m.info...)
	flags := binary.LittleEndian.Uint32(info) &^ 0x1ff
	switch op {
	case opcode.SMSGForceMoveRoot:
		replyOp, flags = opcode.CMSGForceMoveRootAck, flags|0x800
	case opcode.SMSGForceMoveUnroot:
		replyOp, flags = opcode.CMSGForceMoveUnrootAck, flags&^0x800
	default:
		return 0, nil, nil, false
	}
	binary.LittleEndian.PutUint32(info, flags)
	binary.LittleEndian.PutUint32(info[6:], clock)
	reply := append(append([]byte(nil), body...), info...)
	return replyOp, reply, info, true
}
