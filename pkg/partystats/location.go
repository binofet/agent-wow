// Package partystats decodes the ordinary 3.3.5a group member status packet.
package partystats

import (
	"bytes"
	"encoding/binary"
	"math/bits"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

type Member struct {
	GUID              uint64
	Flags             uint32
	Status, Zone      uint16
	Health, MaxHealth uint32
}

const (
	status     = 1 << 0
	health     = 1 << 1
	maxHealth  = 1 << 2
	zone       = 1 << 7
	fullFields = status | health | maxHealth | 1<<4 | 1<<5 | 1<<6 | zone | 1<<8 | 1<<9 | 1<<11 | 1<<12 | 1<<18
)

func (m Member) HasZone() bool { return m.Flags&zone != 0 }
func (m Member) Unavailable() bool {
	return m.Flags&status != 0 && (m.Status&1 == 0 || m.Status&12 != 0) || m.Flags&health != 0 && m.Health == 0
}
func (m Member) ConfirmsLife() bool {
	return m.Flags&(status|health) == status|health && !m.Unavailable()
}
func (m Member) LocationUpdate() bool { return m.HasZone() || m.Flags&status != 0 || m.Unavailable() }

// Decode consumes the entire member record, including the optional pet and
// aura blocks in GroupHandler.cpp. The wire's truncated XY is not a pose.
// On rejection GUID is retained when the subject itself was decodable.
func Decode(op uint32, b []byte) (m Member, ok bool) {
	full := op == opcode.SMSGPartyMemberStatsFull
	if !full && op != opcode.SMSGPartyMemberStats {
		return m, false
	}
	if full {
		if len(b) == 0 || b[0] != 0 {
			return m, false
		}
		b = b[1:]
	}
	if len(b) < 1 {
		return m, false
	}
	mask := b[0]
	b = b[1:]
	for i := uint(0); i < 8; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		if len(b) == 0 || b[0] == 0 {
			return Member{}, false
		}
		m.GUID |= uint64(b[0]) << (8 * i)
		b = b[1:]
	}
	if m.GUID == 0 || len(b) < 4 {
		return m, false
	}
	m.Flags = binary.LittleEndian.Uint32(b)
	b = b[4:]
	if m.Flags == 0 || m.Flags>>20 != 0 {
		return m, false
	}
	// Per-bit widths from GroupUpdateFlags. Auras and pet name are variable.
	widths := [20]int{2, 4, 4, 1, 2, 2, 2, 2, 4, 0, 8, 0, 2, 4, 4, 1, 2, 2, 0, 4}
	for bit, n := range widths {
		if m.Flags&(1<<bit) == 0 {
			continue
		}
		switch bit {
		case 9, 18:
			if len(b) < 8 {
				return m, false
			}
			n = 8 + 5*bits.OnesCount64(binary.LittleEndian.Uint64(b))
		case 11:
			end := bytes.IndexByte(b, 0)
			if end < 0 {
				return m, false
			}
			n = end + 1
		}
		if len(b) < n {
			return m, false
		}
		switch bit {
		case 0:
			m.Status = binary.LittleEndian.Uint16(b)
		case 1:
			m.Health = binary.LittleEndian.Uint32(b)
		case 2:
			m.MaxHealth = binary.LittleEndian.Uint32(b)
		case 7:
			m.Zone = binary.LittleEndian.Uint16(b)
		}
		b = b[n:]
	}
	if full && !(m.Flags == status && m.Status == 0) && m.Flags&fullFields != fullFields {
		return m, false
	}
	if m.Flags&status != 0 && m.Status&^0xff != 0 {
		return m, false
	}
	if m.Flags&(health|maxHealth) == health|maxHealth && (m.MaxHealth == 0 || m.Health > m.MaxHealth) {
		return m, false
	}
	return m, len(b) == 0
}
