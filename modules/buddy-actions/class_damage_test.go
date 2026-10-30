package buddyactions

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Source-shaped ordinary Unit::SendAttackStateUpdate. Optional words follow
// all subdamage records; none of these synthetic packets claim live evidence.
func meleeDamageFixture(attacker, target uint64, damage, flags uint32) []byte {
	p := binary.LittleEndian.AppendUint32(nil, flags)
	p = append(p, packedGUID(attacker)...)
	p = append(p, packedGUID(target)...)
	p = binary.LittleEndian.AppendUint32(p, damage)
	p = append(p, 0, 0, 0, 0, 1) // overkill, one school
	p = binary.LittleEndian.AppendUint32(p, 1)
	p = binary.LittleEndian.AppendUint32(p, math.Float32bits(float32(damage)))
	p = binary.LittleEndian.AppendUint32(p, damage)
	if flags&0x60 != 0 {
		p = append(p, 0, 0, 0, 0)
	}
	if flags&0x180 != 0 {
		p = append(p, 0, 0, 0, 0)
	}
	p = append(p, 1, 0, 0, 0, 0, 0, 0, 0, 0) // victim hit, two zero words
	if flags&0x2000 != 0 {
		p = append(p, 0, 0, 0, 0)
	}
	if flags&0x800000 != 0 {
		p = append(p, 0, 0, 0, 0)
	}
	return p
}

func TestMeleeDamageFramingOptionalWordsAndMalformedAreAtomic(t *testing.T) {
	for _, flags := range []uint32{0, 0x20, 0x40, 0x80, 0x100, 0x2000, 0x800000, 0x8021e0} {
		p := meleeDamageFixture(1, 0xf130000033000001, 7, flags)
		a, target, damage, err := decodeMeleeDamage(p)
		if err != nil || a != 1 || target != 0xf130000033000001 || damage != 7 {
			t.Fatalf("flags%x: %x %x %d %v", flags, a, target, damage, err)
		}
		for n := 0; n < len(p); n++ {
			if a, target, damage, err := decodeMeleeDamage(p[:n]); err == nil || a != 0 || target != 0 || damage != 0 {
				t.Fatalf("accepted partial damage flags%x prefix%d", flags, n)
			}
		}
		if _, _, _, err := decodeMeleeDamage(append(bytes.Clone(p), 0)); err == nil {
			t.Fatal("accepted trailing melee damage")
		}
	}
	for _, flags := range []uint32{1, 0x80000000, 0x01000000, 0x10} {
		if _, _, _, err := decodeMeleeDamage(meleeDamageFixture(1, 2, 7, flags)); err == nil {
			t.Fatalf("unsupported/contradictory damage flags%x", flags)
		}
	}
}

func TestActualOwnDamageReceiptsOnlyUpdateClassAndGenericCounters(t *testing.T) {
	s, _, _ := assistRig(t)
	for _, spell := range []uint32{fireball, sinisterStrike} {
		p := damageGolden(t)
		// Literal golden consumes seven GUID bytes before the fixed tail.
		binary.LittleEndian.PutUint32(p[7:], spell)
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellnonmeleedamagelog, Payload: p})
		foreign := bytes.Clone(p)
		foreign[6] = 3
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellnonmeleedamagelog, Payload: foreign})
		zero := bytes.Clone(p)
		binary.LittleEndian.PutUint32(zero[11:], 0)
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSpellnonmeleedamagelog, Payload: zero})
	}
	for _, tc := range []struct {
		attacker uint64
		damage   uint32
	}{{1, 7}, {3, 7}, {1, 0}} {
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAttackerstateupdate, Payload: meleeDamageFixture(tc.attacker, 0xf130000033000001, tc.damage, 0)})
	}
	r := state(t, s)
	if r.FireballDamageEvents != 1 || r.FireballDamageTotal != 9 || r.SinisterStrikeDamageEvents != 1 || r.SinisterStrikeDamageTotal != 9 || r.MeleeDamageEvents != 1 || r.MeleeDamageTotal != 7 || r.AssistDamageEvents != 3 || r.AssistDamageTotal != 25 || r.SmiteDamageEvents != 0 {
		t.Fatal(r)
	}
	p := meleeDamageFixture(1, 0xf130000033000001, 7, 0)
	for n := 0; n < len(p); n++ {
		s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGAttackerstateupdate, Payload: p[:n]})
	}
	if after := state(t, s); after.AssistDamageEvents != 3 || after.AssistDamageTotal != 25 || after.MalformedPackets != uint64(len(p)) {
		t.Fatal("malformed prefix changed damage evidence", after)
	}
}
