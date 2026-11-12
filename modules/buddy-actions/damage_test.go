package buddyactions

import (
	"encoding/hex"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Source-shaped binary golden, not a captured live combat packet: packed
// creature target0xf130000033000001 then caster1, Smite585, damage9, holy2.
const smiteDamageGolden = "c9013330f101014902000009000000000000000200000000000000000000000000000000000000"

func damageGolden(t *testing.T) []byte {
	t.Helper()
	p, err := hex.DecodeString(smiteDamageGolden)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSpellDamageGoldenUsesTargetBeforeCaster(t *testing.T) {
	if opcode.SMSGSpellnonmeleedamagelog != 0x250 {
		t.Fatal("opcode catalog changed")
	}
	p := damageGolden(t)
	caster, target, spell, damage, err := decodeSpellDamage(p)
	if err != nil || caster != 1 || target != 0xf130000033000001 || spell != 585 || damage != 9 {
		t.Fatalf("caster%x target%x spell%d damage%d err%v", caster, target, spell, damage, err)
	}
	// Full absorb/resist is an ordinary report, never positive-damage proof.
	for i := 11; i < 15; i++ {
		p[i] = 0
	}
	if _, _, _, damage, err = decodeSpellDamage(p); err != nil || damage != 0 {
		t.Fatalf("zero damage: %d %v", damage, err)
	}
}

func TestSpellDamageRejectsEveryTruncationTrailingAndUnsupportedBody(t *testing.T) {
	p := damageGolden(t)
	for i := 0; i < len(p); i++ {
		if _, _, _, _, err := decodeSpellDamage(p[:i]); err == nil {
			t.Fatalf("accepted prefix%d", i)
		}
	}
	for _, extra := range [][]byte{{0}, make([]byte, 64)} {
		if _, _, _, _, err := decodeSpellDamage(append(append([]byte{}, p...), extra...)); err == nil {
			t.Fatal("accepted trailing data")
		}
	}
	// Both GUIDs consume7 bytes, so these indices address the fixed tail.
	for _, mutation := range []struct {
		index int
		value byte
	}{{7, 0}, {19, 0}, {19, 0x80}, {28, 2}, {29, 2}, {38, 1}} {
		bad := append([]byte{}, p...)
		bad[mutation.index] = mutation.value
		// Zero the full spell ID for the dedicated spell test.
		if mutation.index == 7 {
			for i := 7; i < 11; i++ {
				bad[i] = 0
			}
		}
		if _, _, _, _, err := decodeSpellDamage(bad); err == nil {
			t.Fatalf("accepted mutation %+v", mutation)
		}
	}
	for _, bad := range [][]byte{{0}, append([]byte{0}, p[5:]...), append([]byte{1, 0}, p[5:]...)} {
		if _, _, _, _, err := decodeSpellDamage(bad); err == nil {
			t.Fatal("accepted empty/noncanonical GUID")
		}
	}
}

func FuzzDecodeSpellDamage(f *testing.F) {
	p, _ := hex.DecodeString(smiteDamageGolden)
	f.Add(p)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, p []byte) {
		caster, target, spell, damage, err := decodeSpellDamage(p)
		if err != nil && (caster != 0 || target != 0 || spell != 0 || damage != 0) {
			t.Fatal("partial damage evidence on error")
		}
		if err == nil && (caster == 0 || target == 0 || spell == 0 || len(p) > 50) {
			t.Fatal("invalid accepted damage header")
		}
	})
}
