package world

import (
	"bytes"
	"math/bits"

	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// ROOT can immediately follow an UPDATE, before any gameplay subscriber runs.
// This classifier can only withdraw the existing ground image. A complete
// standalone own movement, actual client write or absolute world entry supplies
// its replacement. Other subjects never supply an origin for this body.
func (m *movementControl) objectUpdate(op uint16, body []byte, self uint64) {
	if m.info == nil {
		return
	}
	data, err := movementinfo.ObjectUpdatePayload(body, op == opcode.SMSGCompressedUpdateObject)
	if err != nil {
		m.info = nil
		return
	}
	c := teleportCursor{body: data}
	count := c.u32()
	if count > 4096 {
		m.info = nil
		return
	}
	current := m.info
	for range count {
		kind := c.take(1)
		if kind == nil {
			break
		}
		switch kind[0] {
		case 0, 1, 2, 3:
			guid := c.guid()
			if guid == 0 {
				c.bad = true
				break
			}
			if kind[0] >= 2 {
				t := c.take(1)
				if t == nil || t[0] > 7 || guid == self && t[0] != 4 {
					c.bad = true
					break
				}
			}
			if kind[0] != 0 {
				movement, n, err := movementinfo.DecodeObjectMovementPrefix(c.body[c.offset:])
				if err != nil {
					c.bad = true
					break
				}
				c.take(n)
				if guid == self {
					// Exclude the independent client timestamps. A complete
					// stationary ground image can corroborate the old source,
					// but this packet never establishes a new ROOT origin.
					raw := movement.LivingWire
					if !plainMovementInfo(current) || !plainMovementInfo(raw) || movement.Living.Flags&0x1ff != 0 ||
						!bytes.Equal(raw[:6], current[:6]) || !bytes.Equal(raw[10:], current[10:]) {
						m.info = nil
						return
					}
				}
			}
			if kind[0] != 1 {
				maskCount := c.take(1)
				if maskCount == nil {
					break
				}
				words := 0
				for range maskCount[0] {
					words += bits.OnesCount32(c.u32())
				}
				c.take(words * 4)
			}
		case 4, 5:
			n := c.u32()
			if n > 4096 {
				c.bad = true
				break
			}
			for range n {
				guid := c.guid()
				if guid == 0 {
					c.bad = true
					break
				}
				if guid == self && kind[0] == 4 {
					current = nil
				}
			}
		default:
			c.bad = true
		}
		if c.bad {
			break
		}
	}
	if !c.complete() {
		current = nil
	}
	m.info = current
}
