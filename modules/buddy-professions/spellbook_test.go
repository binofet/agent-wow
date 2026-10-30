package buddyprofessions

import (
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func initialBookFixture(ids ...uint32) []byte {
	p := make([]byte, 3+len(ids)*6+2)
	binary.LittleEndian.PutUint16(p[1:], uint16(len(ids)))
	for i, id := range ids {
		binary.LittleEndian.PutUint32(p[3+i*6:], id)
	}
	return p
}

func TestSpellBookNeedsCompleteBaselineAndOnlyActualRecipeIDs(t *testing.T) {
	b := spellBook{}
	// Literal SMSG_LEARNED_SPELL3275, followed by required uint16 zero.
	learned, _ := hex.DecodeString("cb0c00000000")
	if err := b.onPacket(opcode.SMSGLearnedSpell, learned); err != nil || b.known || b.has(3275) {
		t.Fatal("incremental event fabricated a baseline", b, err)
	}
	if err := b.onPacket(opcode.SMSGInitialSpells, initialBookFixture(3273, 2550, 7733, 585)); err != nil || !b.known {
		t.Fatal(b, err)
	}
	for _, id := range []uint32{3275, 2538, 2540, 7620, 3273, 2550, 7733, 585} {
		if b.has(id) {
			t.Fatal("wrapper or unrelated spell fabricated a recipe", id)
		}
	}
	if err := b.onPacket(opcode.SMSGLearnedSpell, learned); err != nil || !b.has(3275) || b.has(2538) || len(b.spells) != 1 {
		t.Fatal("actual recipe learning lost", b, err)
	}
	if err := b.onPacket(opcode.SMSGInitialSpells, initialBookFixture(2538, 2540, 7620, 585)); err != nil || b.has(3275) || !b.has(2538) || !b.has(2540) || !b.has(7620) || len(b.spells) != 3 {
		t.Fatal("replacement baseline retained absent or unrelated recipe", b, err)
	}
}

func TestSpellBookRemovalAndReplacementHonorBothSides(t *testing.T) {
	b := spellBook{}
	if err := b.onPacket(opcode.SMSGInitialSpells, initialBookFixture(3275, 2538)); err != nil {
		t.Fatal(err)
	}
	// Replace supported3275 by unsupported3276; it must revoke starter access.
	superseded, _ := hex.DecodeString("cb0c0000cc0c0000")
	if err := b.onPacket(opcode.SMSGSupercededSpell, superseded); err != nil || b.has(3275) || b.has(3276) || !b.has(2538) {
		t.Fatal(b, err)
	}
	// Replace that higher rank with actual supported3275, preserving knownness.
	superseded, _ = hex.DecodeString("cc0c0000cb0c0000")
	if err := b.onPacket(opcode.SMSGSupercededSpell, superseded); err != nil || !b.has(3275) {
		t.Fatal(b, err)
	}
	removed, _ := hex.DecodeString("cb0c0000")
	if err := b.onPacket(opcode.SMSGRemovedSpell, removed); err != nil || b.has(3275) || !b.known || !b.has(2538) {
		t.Fatal(b, err)
	}
}

func TestMalformedInitialSpellFramesRevokeBaselineAtomically(t *testing.T) {
	valid := initialBookFixture(3275, 2538)
	withCooldown := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint16(withCooldown[len(withCooldown)-2:], 1)
	withCooldown = append(withCooldown, make([]byte, 16)...)
	binary.LittleEndian.PutUint32(withCooldown[len(valid):], 3275)
	for _, wire := range [][]byte{valid, withCooldown} {
		for length := 0; length < len(wire); length++ {
			b := spellBook{}
			_ = b.onPacket(opcode.SMSGInitialSpells, valid)
			if err := b.onPacket(opcode.SMSGInitialSpells, wire[:length]); err == nil || b.known || b.has(3275) || len(b.spells) != 0 {
				t.Fatalf("prefix%d retained partial knowledge: %+v %v", length, b, err)
			}
		}
		b := spellBook{}
		if err := b.onPacket(opcode.SMSGInitialSpells, wire); err != nil || !b.has(3275) || !b.has(2538) {
			t.Fatal("complete cooldown frame rejected", b, err)
		}
		if err := b.onPacket(opcode.SMSGInitialSpells, append(wire, 0)); err == nil || b.known {
			t.Fatal("trailing byte retained baseline", b, err)
		}
	}
	for _, wire := range [][]byte{initialBookFixture(0), initialBookFixture(0x80000000), initialBookFixture(3275, 3275), {0, 1, 16, 0, 0}} {
		b := spellBook{}
		_ = b.onPacket(opcode.SMSGInitialSpells, valid)
		if err := b.onPacket(opcode.SMSGInitialSpells, wire); err == nil || b.known {
			t.Fatal("invalid complete baseline accepted", b, err)
		}
	}
}

func TestMalformedIncrementalKnowledgeCannotRetainUncertainRecipeAccess(t *testing.T) {
	for _, fixture := range []struct {
		op   uint32
		wire string
	}{
		{opcode.SMSGLearnedSpell, "cb0c00000000"},
		{opcode.SMSGRemovedSpell, "cb0c0000"},
		{opcode.SMSGSupercededSpell, "cb0c0000cc0c0000"},
	} {
		wire, _ := hex.DecodeString(fixture.wire)
		bad := [][]byte{append(append([]byte(nil), wire...), 0)}
		for length := 0; length < len(wire); length++ {
			bad = append(bad, wire[:length])
		}
		for _, payload := range bad {
			b := spellBook{}
			_ = b.onPacket(opcode.SMSGInitialSpells, initialBookFixture(3275))
			if err := b.onPacket(fixture.op, payload); err == nil || b.known || b.has(3275) {
				t.Fatal("malformed incremental retained uncertain access", fixture.op, b, err)
			}
		}
	}
	for _, fixture := range []struct {
		op   uint32
		wire string
	}{
		{opcode.SMSGLearnedSpell, "cb0c00000100"},
		{opcode.SMSGLearnedSpell, "000000000000"},
		{opcode.SMSGRemovedSpell, "00000080"},
		{opcode.SMSGSupercededSpell, "cb0c0000cb0c0000"},
		{opcode.SMSGSupercededSpell, "cb0c000000000000"},
	} {
		wire, _ := hex.DecodeString(fixture.wire)
		b := spellBook{}
		_ = b.onPacket(opcode.SMSGInitialSpells, initialBookFixture(3275))
		if err := b.onPacket(fixture.op, wire); err == nil || b.known {
			t.Fatal("invalid incremental frame accepted", fixture, b, err)
		}
	}
}

func TestEmptyBaselineAndUnrelatedPacketsCannotInventRecipes(t *testing.T) {
	b := spellBook{}
	if err := b.onPacket(opcode.SMSGInitialSpells, initialBookFixture()); err != nil || !b.known || len(b.spells) != 0 {
		t.Fatal("valid empty baseline rejected", b, err)
	}
	for _, op := range []uint32{opcode.SMSGTrainerBuySucceeded, opcode.SMSGUpdateObject, opcode.SMSGSpellGo} {
		if err := b.onPacket(op, []byte{0xff}); err != nil || !b.known || len(b.spells) != 0 {
			t.Fatal("unrelated packet changed spell knowledge", b, err)
		}
	}
}
