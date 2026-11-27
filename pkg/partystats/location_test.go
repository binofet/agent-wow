package partystats

import (
	"encoding/binary"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"testing"
)

func TestCompleteGroupMemberWire(t *testing.T) {
	// All twenty fields. Two player auras, a named pet with one aura and a seat.
	b := binary.LittleEndian.AppendUint32([]byte{0, 1, 3}, (1<<20)-1)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint32(b, 99)
	b = binary.LittleEndian.AppendUint32(b, 100)
	b = append(b, 1)
	for _, n := range []uint16{1, 50, 42, 1337, 932, 245} {
		b = binary.LittleEndian.AppendUint16(b, n)
	}
	b = binary.LittleEndian.AppendUint64(b, (1<<63)|1)
	b = append(b, make([]byte, 10)...)
	b = binary.LittleEndian.AppendUint64(b, 222)
	b = append(b, "Wolf"...)
	b = append(b, 0)
	b = append(b, make([]byte, 15)...)
	b = binary.LittleEndian.AppendUint64(b, 2)
	b = append(b, make([]byte, 9)...)
	m, ok := Decode(opcode.SMSGPartyMemberStatsFull, b)
	if !ok || m.GUID != 3 || m.Zone != 1337 || !m.ConfirmsLife() {
		t.Fatal("complete group member", m, ok)
	}
	for i := range len(b) {
		if _, ok := Decode(opcode.SMSGPartyMemberStatsFull, b[:i]); ok {
			t.Fatalf("accepted prefix %d of %d", i, len(b))
		}
	}
	if _, ok := Decode(opcode.SMSGPartyMemberStatsFull, append(b, 42)); ok {
		t.Fatal("accepted suffix")
	}
	small := binary.LittleEndian.AppendUint32([]byte{0, 1, 3}, status)
	if m, ok = Decode(opcode.SMSGPartyMemberStatsFull, append(small, 0, 0)); !ok || !m.Unavailable() {
		t.Fatal("valid ordinary offline reply", m, ok)
	}
	if _, ok = Decode(opcode.SMSGPartyMemberStatsFull, append(small, 1, 0)); ok {
		t.Fatal("online full lacked required fields")
	}
	delta := binary.LittleEndian.AppendUint32([]byte{1, 3}, 1<<4)
	if m, ok = Decode(opcode.SMSGPartyMemberStats, append(delta, 1, 0)); !ok || m.LocationUpdate() {
		t.Fatal("ordinary power change became a route barrier", m, ok)
	}
}
