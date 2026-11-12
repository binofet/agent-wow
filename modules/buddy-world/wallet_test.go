package buddyworld

import (
	"context"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestOwnCoinageCreateZeroBaselineAndMonotonicValueRevision(t *testing.T) {
	s := NewService(1)
	r := packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	if r.SelfCopperKnown || r.SelfCopperRevision != 0 {
		t.Fatal("login position invented wallet")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	if !r.SelfCopperKnown || r.SelfCopper != 0 || r.SelfCopperRevision != 1 {
		t.Fatal(r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	if r.SelfCopperRevision != 1 {
		t.Fatal("unchangedCREATE advanced wallet revision")
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{playerCoinage: 12345}))
	if r.SelfCopper != 12345 || r.SelfCopperRevision != 2 {
		t.Fatal(r)
	}
	r = packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{playerCoinage: 12345, unitHealth: 50}))
	if r.SelfCopperRevision != 2 {
		t.Fatal("unchangedwallet advanced from other unit field")
	}
	fields := vitalFields()
	fields[playerCoinage] = 12345
	r = packet(t, s, opcode.SMSGUpdateObject, createFixture(1, fields))
	if r.SelfCopper != 12345 || r.SelfCopperRevision != 2 {
		t.Fatal("samewallet reconnect looked like change")
	}
	r = packet(t, s, opcode.SMSGCompressedUpdateObject, compressed(t, deltaFixture(1, map[int]uint32{playerCoinage: 12295})))
	if r.SelfCopper != 12295 || r.SelfCopperRevision != 3 {
		t.Fatal(r)
	}
}

func TestOwnCoinageIgnoresForeignAndMalformedPackets(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	fields := vitalFields()
	fields[playerCoinage] = 10000
	packet(t, s, opcode.SMSGUpdateObject, createFixture(1, fields))
	packet(t, s, opcode.SMSGUpdateObject, createFixture(3, fields))
	packet(t, s, opcode.SMSGUpdateObject, deltaFixture(3, map[int]uint32{playerCoinage: 0}))
	packet(t, s, opcode.SMSGUpdateObject, deltaFixture(2, map[int]uint32{playerCoinage: 999999}))
	delta := deltaFixture(1, map[int]uint32{playerCoinage: 0})
	for n := 0; n < len(delta); n++ {
		packet(t, s, opcode.SMSGUpdateObject, delta[:n])
	}
	packet(t, s, opcode.SMSGUpdateObject, append(delta, 255))
	r, _ := s.Snapshot(context.Background(), nil)
	if !r.SelfCopperKnown || r.SelfCopper != 10000 || r.SelfCopperRevision != 1 {
		t.Fatal("foreign or malformed packet mutated own wallet", r)
	}
}
