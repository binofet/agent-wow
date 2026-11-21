package spellcatalog

import "testing"

func TestGenericLearnedCastDoesNotDependOnEffectClassifier(t *testing.T) {
	// These are different wire targeting/lifecycle forms, not separate handlers.
	for _, id := range []uint32{2120, 2121, 8422, 8423, 10, 6141, 8427, 1449, 8437, 8438, 8439} {
		spell, ok := Default().Lookup(id)
		if !ok || !spell.GenericCastSupported() || spell.Passive() {
			t.Fatalf("learned active spell %d must have normal cast transport: %+v", id, spell)
		}
	}
	for _, id := range []uint32{10, 6141, 8427} {
		spell, _ := Default().Lookup(id)
		if !spell.Channeled() || spell.ClassificationReason() != "automatic_selection_unclassified" {
			t.Fatalf("Blizzard's channel is a lifecycle fact, not a cast prohibition: %+v", spell)
		}
	}
	// A new effect, targets, material/rune requirement or incomplete catalog
	// metadata cannot require a native gameplay handler before attempting a cast.
	spell := Spell{ID: 987654, ItemClass: 2, Focus: 42, RuneCost: 7, Reagents: []Reagent{{42, 2}},
		Effects: []Effect{{Kind: 12345, A: 987, Radius: 999}}}
	if !spell.GenericCastSupported() || spell.ClassificationReason() != "automatic_selection_unclassified" {
		t.Fatal("unfamiliar learned metadata blocked generic casting", spell)
	}
	spell.Attrs[0] |= 0x40
	if !spell.Passive() || spell.GenericCastSupported() {
		t.Fatal("passive was advertised as an active ability")
	}
	if (Spell{}).GenericCastSupported() {
		t.Fatal("zero spell ID is not a learned ability")
	}
}
