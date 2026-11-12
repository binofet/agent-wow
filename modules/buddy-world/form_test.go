package buddyworld

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestOwnShapeshiftIsActualHighByteWithCompleteCreateZeroBaseline(t *testing.T) {
	s := NewService(1)
	r := packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	if r.Self.ShapeshiftFormKnown {
		t.Fatal("login position invented base form")
	}
	fields := vitalFields()
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, fields))
	if !r.Self.ShapeshiftFormKnown || r.Self.ShapeshiftForm != 0 {
		t.Fatal("full CREATE did not establish omitted base form zero", r.Self)
	}
	// Unit::GetShapeshiftForm reads byte3. Sheath/PvP/other lower bytes
	// remain unrelated even when all three are nonzero.
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x7a: 0x02030405}))
	if !r.Self.ShapeshiftFormKnown || r.Self.ShapeshiftForm != 2 {
		t.Fatal("form is not actual high byte", r.Self)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{0x7a: 0x00030405}))
	if !r.Self.ShapeshiftFormKnown || r.Self.ShapeshiftForm != 0 {
		t.Fatal("observed form removal was lost", r.Self)
	}
}

func TestOwnShapeshiftForeignPrivacyAndIndependentIdentity(t *testing.T) {
	for _, independent := range []bool{false, true} {
		s, self := NewService(1), byte(1)
		if independent {
			s, self = NewIndependentService(6), 6
		}
		fields := vitalFields()
		fields[0x7a] = 1 << 24
		packet(t, s, opcode.SMSGUpdateObject, createFixture(self, fields))
		fields[0x7a] = 2 << 24
		packet(t, s, opcode.SMSGUpdateObject, createFixture(3, fields))
		r := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{0x7a: 5 << 24}))
		if !r.Self.ShapeshiftFormKnown || r.Self.ShapeshiftForm != 1 || independent && r.Owner != nil || !independent && (r.Owner.ShapeshiftFormKnown || r.Owner.ShapeshiftForm != 0) {
			t.Fatal("foreign player form leaked into own form", r)
		}
	}
}

func TestOwnShapeshiftMalformedRollbackAndLifecycleInvalidation(t *testing.T) {
	for _, mode := range []string{"destroy", "transfer", "new_world"} {
		t.Run(mode, func(t *testing.T) {
			s := NewService(1)
			fields := vitalFields()
			fields[0x7a] = 2 << 24
			packet(t, s, opcode.SMSGUpdateObject, createFixture(1, fields))
			delta := deltaFixture(1, map[int]uint32{0x7a: 0})
			for n := range len(delta) {
				packet(t, s, opcode.SMSGUpdateObject, delta[:n])
			}
			bad := append(append([]byte(nil), delta...), 255)
			binary.LittleEndian.PutUint32(bad, 2)
			r := packet(t, s, opcode.SMSGUpdateObject, bad)
			if !r.Self.ShapeshiftFormKnown || r.Self.ShapeshiftForm != 2 {
				t.Fatal("malformed frame committed base form", r.Self)
			}
			switch mode {
			case "destroy":
				r = packet(t, s, opcode.SMSGDestroyObject, []byte{1, 0, 0, 0, 0, 0, 0, 0, 0})
			case "transfer":
				r = packet(t, s, opcode.SMSGTransferPending, []byte{0, 0, 0, 0})
			case "new_world":
				r = packet(t, s, opcode.SMSGNewWorld, login())
			}
			if r.Self.ShapeshiftFormKnown || r.Self.ShapeshiftForm != 0 {
				t.Fatal("stale shape survived world lifecycle", mode, r.Self)
			}
			r, _ = s.Snapshot(context.Background(), nil)
			if r.Self.ShapeshiftFormKnown {
				t.Fatal("snapshot inferred base form after invalidation")
			}
		})
	}
}
