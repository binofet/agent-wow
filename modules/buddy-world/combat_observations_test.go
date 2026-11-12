package buddyworld

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Source-shaped Unit::SendAttackStateUpdate: attacker then victim, one school.
func incomingMeleeFixture(attacker, victim uint64, damage, flags uint32) []byte {
	p := binary.LittleEndian.AppendUint32(nil, flags)
	p = append(p, packedFixture(attacker)...)
	p = append(p, packedFixture(victim)...)
	p = binary.LittleEndian.AppendUint32(p, damage)
	p = append(p, 0, 0, 0, 0, 1)
	p = binary.LittleEndian.AppendUint32(p, 1)
	p = binary.LittleEndian.AppendUint32(p, math.Float32bits(float32(damage)))
	p = binary.LittleEndian.AppendUint32(p, damage)
	if flags&0x60 != 0 {
		p = append(p, 0, 0, 0, 0)
	}
	if flags&0x180 != 0 {
		p = append(p, 0, 0, 0, 0)
	}
	p = append(p, 1, 0, 0, 0, 0, 0, 0, 0, 0)
	if flags&0x2000 != 0 {
		p = append(p, 0, 0, 0, 0)
	}
	if flags&0x800000 != 0 {
		p = append(p, 0, 0, 0, 0)
	}
	return p
}

// Source-shaped SendSpellNonMeleeDamageLog: victim BEFORE attacker.
func incomingSpellFixture(attacker, victim uint64, damage uint32) []byte {
	p := append(packedFixture(victim), packedFixture(attacker)...)
	tail := make([]byte, 32)
	binary.LittleEndian.PutUint32(tail, 133)
	binary.LittleEndian.PutUint32(tail[4:], damage)
	tail[12] = 4
	return append(p, tail...)
}

func oldCombatRig(t *testing.T) (*Service, uint64) {
	t.Helper()
	s, _ := discoveryRig(t)
	p := discoveryNPC(s, 900, 202)
	start := binary.LittleEndian.AppendUint64(nil, p.guid)
	start = binary.LittleEndian.AppendUint64(start, 1)
	packet(t, s, opcode.SMSGAttackstart, start)
	s.now = func() time.Time { return time.Unix(1300, 0) }
	return s, p.guid
}

func combatView(t *testing.T, s *Service, guid uint64) *worldv1.CreatureState {
	t.Helper()
	w, err := s.Snapshot(context.Background(), &worldv1.SnapshotRequest{CreatureGuid: guid})
	if err != nil || w.RequestedCreature == nil {
		t.Fatal("missing cached attacker", err)
	}
	return w.RequestedCreature
}

func TestIncomingDamageRefreshesActualVictimEvidenceWithoutRefreshingPose(t *testing.T) {
	for _, op := range []uint32{opcode.SMSGAttackerstateupdate, opcode.SMSGSpellnonmeleedamagelog} {
		t.Run(opcode.WorldName(op), func(t *testing.T) {
			s, guid := oldCombatRig(t)
			before := combatView(t, s, guid)
			if before.AttackAgeMs != 300000 {
				t.Fatal("fixture age", before)
			}
			wire := incomingMeleeFixture(guid, 1, 7, 0)
			if op == opcode.SMSGSpellnonmeleedamagelog {
				wire = incomingSpellFixture(guid, 1, 7)
			}
			packet(t, s, op, wire)
			after := combatView(t, s, guid)
			if !after.AttackVictimKnown || after.AttackVictimGuid != 1 || after.AttackAgeMs != 0 || after.PositionAgeMs != before.PositionAgeMs || after.PositionRevision != before.PositionRevision || after.Health != before.Health {
				t.Fatal("damage did not refresh only actual combat evidence", after)
			}
			// An actual other player's CREATE permits public damage observations;
			// membership is intentionally not inferred here.
			packet(t, s, opcode.SMSGUpdateObject, createFixture(4, vitalFields()))
			if op == opcode.SMSGAttackerstateupdate {
				wire = incomingMeleeFixture(guid, 4, 7, 0)
			} else {
				wire = incomingSpellFixture(guid, 4, 7)
			}
			packet(t, s, op, wire)
			if got := combatView(t, s, guid); got.AttackVictimGuid != 4 || got.AttackAgeMs != 0 {
				t.Fatal("actual victim not updated", got)
			}
			stop := append(packedFixture(guid), packedFixture(4)...)
			stop = binary.LittleEndian.AppendUint32(stop, 0)
			packet(t, s, opcode.SMSGAttackstop, stop)
			if combatView(t, s, guid).AttackVictimKnown {
				t.Fatal("actual attack stop retained relation")
			}
		})
	}
}

func TestIncomingDamageMalformedZeroForeignAndUnsupportedCannotRefreshCombat(t *testing.T) {
	for _, op := range []uint32{opcode.SMSGAttackerstateupdate, opcode.SMSGSpellnonmeleedamagelog} {
		s, guid := oldCombatRig(t)
		encode := func(a, v uint64, d uint32) []byte {
			if op == opcode.SMSGSpellnonmeleedamagelog {
				return incomingSpellFixture(a, v, d)
			}
			return incomingMeleeFixture(a, v, d, 0)
		}
		wire := encode(guid, 1, 7)
		for n := 0; n < len(wire); n++ {
			packet(t, s, op, wire[:n])
		}
		packet(t, s, op, append(bytes.Clone(wire), 0))
		for _, p := range [][]byte{encode(guid, 1, 0), encode(1, guid, 7), encode(guid, 999, 7), encode(guid+1, 1, 7)} {
			packet(t, s, op, p)
		}
		if got := combatView(t, s, guid); got.AttackAgeMs != 300000 || got.AttackVictimGuid != 1 {
			t.Fatal("partial/foreign/zero damage refreshed evidence", op, got)
		}
	}
	for _, flags := range []uint32{0, 0x20, 0x40, 0x80, 0x100, 0x2000, 0x800000, 0x8021e0} {
		wire := incomingMeleeFixture(npcGUID, 1, 7, flags)
		a, v, d, e := decodeIncomingMeleeDamage(wire)
		if e != nil || a != npcGUID || v != 1 || d != 7 {
			t.Fatal("valid optional melee frame", flags, e)
		}
		for n := 0; n < len(wire); n++ {
			if _, _, _, e := decodeIncomingMeleeDamage(wire[:n]); e == nil {
				t.Fatal("optional truncated frame accepted", flags, n)
			}
		}
	}
	for _, flags := range []uint32{1, 0x80000000, 0x01000000, 0x10} {
		if _, _, _, e := decodeIncomingMeleeDamage(incomingMeleeFixture(npcGUID, 1, 7, flags)); e == nil {
			t.Fatal("unsupported damage", flags)
		}
	}
	for _, offset := range []int{0, 12, 21, 22, 31} {
		wire := incomingSpellFixture(npcGUID, 1, 7)
		at := len(wire) - 32
		if offset == 0 {
			binary.LittleEndian.PutUint32(wire[at:], 0)
		} else {
			wire[at+offset] = 0xff
		}
		if _, _, _, _, e := decodeIncomingSpellDamage(wire); e == nil {
			t.Fatal("unsupported spell frame", offset)
		}
	}
}

func FuzzIncomingCombatFrames(f *testing.F) {
	f.Add(uint8(0), incomingMeleeFixture(npcGUID, 1, 7, 0))
	f.Add(uint8(1), incomingSpellFixture(npcGUID, 1, 7))
	f.Add(uint8(0), []byte{})
	f.Fuzz(func(t *testing.T, kind uint8, p []byte) {
		if kind%2 == 0 {
			a, v, d, e := decodeIncomingMeleeDamage(p)
			if e != nil && (a != 0 || v != 0 || d != 0) {
				t.Fatal("partial evidence on malformed melee")
			}
		} else {
			a, v, s, d, e := decodeIncomingSpellDamage(p)
			if e != nil && (a != 0 || v != 0 || s != 0 || d != 0) {
				t.Fatal("partial evidence on malformed spell")
			}
		}
	})
}
