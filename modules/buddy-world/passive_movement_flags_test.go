package buddyworld

import (
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestPassiveMovementFlagsLiteralWirePreservesHighBits(t *testing.T) {
	// Primary MovementInfo: GUID3, flags little-endian00000070, flags2=0,
	// time1234, XYZ1/2/3, orientation0.5, fallTime0. Passive status adds no tail.
	b, err := hex.DecodeString("0103000000700000d20400000000803f00000040000040400000003f00000000")
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	r := packet(t, s, opcode.MSGMoveHeartbeat, b)
	if r.MalformedPackets != 0 || !r.Owner.MovementFlagsKnown || r.Owner.MovementFlags != 0x70000000 || r.Owner.ExtraMovementFlags != 0 || r.Owner.X != 1 || r.Owner.Y != 2 || r.Owner.Z != 3 || r.Owner.MovementTime != 1234 {
		t.Fatal("literal high movement bits shifted or misframed", r)
	}
	for _, compressedPacket := range []bool{false, true} {
		s := NewService(1)
		packet(t, s, opcode.SMSGLoginVerifyWorld, login())
		create := createFixture(3, vitalFields())
		binary.LittleEndian.PutUint32(create[10:], 0x70000000)
		code := uint32(opcode.SMSGUpdateObject)
		if compressedPacket {
			create = compressed(t, create)
			code = opcode.SMSGCompressedUpdateObject
		}
		r := packet(t, s, code, create)
		if r.MalformedPackets != 0 || !r.Owner.MovementFlagsKnown || r.Owner.MovementFlags != 0x70000000 || r.Owner.ExtraMovementFlags != 0 || r.Owner.X != 1 || r.Owner.Y != 2 || r.Owner.Z != 3 {
			t.Fatal("CREATE high movement bits shifted or misframed", r)
		}
	}
}
