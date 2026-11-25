package buddyworld

import (
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"testing"
)

func TestOwnedAmmoFieldKnownZeroChangesAndPrivateScope(t *testing.T) {
	s := NewService(1)
	packet(t, s, opcode.SMSGLoginVerifyWorld, login())
	unknown := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{unitHealth: 100}))
	if unknown.SelfAmmoKnown {
		t.Fatal("missing own ammo baseline became known", unknown)
	}
	zero := packet(t, s, opcode.SMSGUpdateObject, createFixture(1, vitalFields()))
	if !zero.SelfAmmoKnown || zero.SelfAmmoEntry != 0 || zero.SelfAmmoRevision == 0 {
		t.Fatal(zero)
	}
	arrow := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{playerAmmo: 11285}))
	if !arrow.SelfAmmoKnown || arrow.SelfAmmoEntry != 11285 || arrow.SelfAmmoRevision <= zero.SelfAmmoRevision {
		t.Fatal(arrow)
	}
	foreign := packet(t, s, opcode.SMSGUpdateObject, createFixture(3, map[int]uint32{playerAmmo: 999, unitHealth: 100}))
	if s.state.players[1].ammoKnown || s.state.players[1].ammo != 0 {
		t.Fatal("foreign private ammo retained")
	}
	if foreign.SelfAmmoEntry != 11285 || foreign.SelfAmmoRevision != arrow.SelfAmmoRevision {
		t.Fatal("owner private field changed own ammo", foreign)
	}
	cleared := packet(t, s, opcode.SMSGUpdateObject, deltaFixture(1, map[int]uint32{playerAmmo: 0}))
	if !cleared.SelfAmmoKnown || cleared.SelfAmmoEntry != 0 || cleared.SelfAmmoRevision <= arrow.SelfAmmoRevision {
		t.Fatal(cleared)
	}
	moved := packet(t, s, opcode.SMSGNewWorld, login())
	if moved.SelfAmmoKnown {
		t.Fatal("new visit reused old private ammo baseline", moved)
	}
}
