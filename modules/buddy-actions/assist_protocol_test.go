package buddyactions

import (
	"encoding/hex"
	"testing"
)

const assistCreatureGUID = uint64(0xf130000033000001)

func TestSmitePacketGoldenUsesExactObservedCreature(t *testing.T) {
	// CMSG_CAST_SPELL: cast-count7, spell585, flags0, UNIT mask2, packed NPC.
	got := hex.EncodeToString(spellPacket(7, 585, assistCreatureGUID))
	if got != "07490200000002000000c9013330f1" {
		t.Fatalf("Smite packet %s", got)
	}
	if got := hex.EncodeToString(castPacket(7)); got != "070208000000020000000103" {
		t.Fatalf("Lesser Heal wrapper changed: %s", got)
	}
}

func TestInitialKnownSpellsDiscriminateSmiteAndHeal(t *testing.T) {
	// Initial-spells payload with one spell585, slot0, no cooldowns.
	smiteOnly, err := hex.DecodeString("0001004902000000000000")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		spell uint32
		want  bool
	}{{585, true}, {2050, false}, {0, false}} {
		got, err := initialSpellKnown(smiteOnly, tc.spell)
		if err != nil || got != tc.want {
			t.Fatalf("spell%d: known%v err%v", tc.spell, got, err)
		}
	}
	if known, err := initialKnown(smiteOnly); err != nil || known {
		t.Fatal("heal wrapper recognized Smite as Lesser Heal", known, err)
	}
	for n := range len(smiteOnly) {
		if _, err := initialSpellKnown(smiteOnly[:n], 585); err == nil {
			t.Fatalf("accepted truncated spell list%d", n)
		}
	}
	if _, err := initialSpellKnown(append(smiteOnly, 0), 585); err == nil {
		t.Fatal("accepted trailing spell-list byte")
	}
}

func TestSmiteCastBodyBindsTargetAndRejectsTruncations(t *testing.T) {
	for _, tc := range []struct {
		name    string
		goEvent bool
		hex     string
	}{
		// Flags0, cast-time1500ms, unit-target mask2 and packed creatureGUID.
		{"start", false, "00000000dc05000002000000c9013330f1"},
		// Flags0, timestamp123, one hit(raw NPC GUID), no misses, unit target.
		{"go", true, "000000007b0000000101000033000030f10002000000c9013330f1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := hex.DecodeString(tc.hex)
			if err != nil {
				t.Fatal(err)
			}
			if !validCastTargetBody(body, 0, tc.goEvent, assistCreatureGUID) {
				t.Fatal("rejected complete target-bound body")
			}
			if validCastTargetBody(body, 0, tc.goEvent, ownerGUID) {
				t.Fatal("accepted body for a different target")
			}
			for n := range len(body) {
				if validCastTargetBody(body[:n], 0, tc.goEvent, assistCreatureGUID) {
					t.Fatalf("accepted truncated cast body%d", n)
				}
			}
			if validCastTargetBody(append(body, 0), 0, tc.goEvent, assistCreatureGUID) {
				t.Fatal("accepted trailing cast body byte")
			}
			prefixed := append([]byte{0xab, 0xcd}, body...)
			if !validCastTargetBody(prefixed, 2, tc.goEvent, assistCreatureGUID) {
				t.Fatal("rejected body after cast-event header offset")
			}
		})
	}
}
