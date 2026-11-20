package spellcatalog

import "testing"

func TestGeneratedSpellMechanicsAndConstraints(t *testing.T) {
	for id, kind := range map[uint32]Kind{2054: Heal, 2006: Resurrect, 1784: Stealth, 53: WeaponStrike, 585: Damage, 1243: Buff} {
		r, ok := Default().Lookup(id)
		if !ok || r.Capability().Kind != kind {
			t.Fatalf("%d: %+v %+v", id, r, r.Capability())
		}
	}
	r, _ := Default().Lookup(53)
	if !r.Behind || r.SubclassMask != 1<<15 || r.Attrs[0]&0x20000 != 0 {
		t.Fatal("Backstab constraints", r)
	}
	r, _ = Default().Lookup(8676)
	if !r.Behind || r.Attrs[0]&0x20000 == 0 {
		t.Fatal("Ambush constraints", r)
	}
	r, _ = Default().Lookup(1856)
	if r.Level != 22 || r.Capability().Blocked == "" {
		t.Fatal("unsupported trigger spell was silently usable", r)
	}
}

func TestUnfamiliarIDsUseMechanicsAndExposeUnsupportedEffects(t *testing.T) {
	c, err := New([]byte("{\"id\":987654,\"name\":\"New heal\",\"timingKnown\":true,\"rangeKnown\":true,\"itemClass\":-1,\"effects\":[{\"kind\":10,\"a\":21}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, ok := c.Lookup(987654)
	if !ok || r.Capability().Kind != Heal || r.Capability().Target != "ally" {
		t.Fatal(r)
	}
	r.Effects[0].Kind = 12345
	if r.Capability().Blocked != "effect_12345_requires_handler" {
		t.Fatal(r.Capability())
	}
	r.Effects[0].Kind = 10
	r.Effects[0].B = 15
	if r.Capability().Blocked == "" {
		t.Fatal("area accepted")
	}
	for _, raw := range []string{"{}", "{\"id\":0,\"name\":\"x\"}", "{\"id\":7,\"name\":\"x\"}\n{\"id\":7,\"name\":\"y\"}"} {
		if _, err := New([]byte(raw)); err == nil {
			t.Fatal("bad index accepted", raw)
		}
	}
}

func TestLearnedSingleTargetControlRanks(t *testing.T) {
	for _, tc := range []struct {
		kind Kind
		ids  []uint32
	}{
		{Polymorph, []uint32{118, 12824, 12825, 12826}},
		{Sap, []uint32{6770, 2070, 11297}},
	} {
		for _, id := range tc.ids {
			spell, ok := Default().Lookup(id)
			actual := spell.Capability()
			if !ok || actual != (Capability{Kind: tc.kind, Target: "enemy"}) || !actual.Kind.BreaksOnDamage() {
				t.Fatalf("spell %d has control %+v, want %s", id, actual, tc.kind)
			}
			spell.Effects[0].Radius = 1
			if spell.Capability().Kind == tc.kind {
				t.Fatalf("spell %d accepted area control as %s", id, tc.kind)
			}
		}
	}
}

func TestGeneratedConjurationOutput(t *testing.T) {
	for id, item := range map[uint32]uint32{587: 5349, 5504: 5350, 10140: 8079} {
		spell, ok := Default().Lookup(id)
		actual := spell.Capability()
		if !ok || spell.CreatedItem() != item || actual != (Capability{Kind: CreateItem, Target: "self"}) {
			t.Fatalf("spell %d produces %d with capability %+v, want %d", id, spell.CreatedItem(), actual, item)
		}
	}
}

func TestSelfItemCreationUsesVerifiedOutputAndNoReagent(t *testing.T) {
	catalog, err := New([]byte(`{"id":987654,"name":"New spell","timingKnown":true,"rangeKnown":true,"itemClass":-1,"effects":[{"kind":24,"a":1,"itemType":9876}]}`))
	if err != nil {
		t.Fatal(err)
	}
	spell, ok := catalog.Lookup(987654)
	if !ok || spell.CreatedItem() != 9876 || spell.Capability() != (Capability{Kind: CreateItem, Target: "self"}) {
		t.Fatal(spell)
	}
	spell.Reagents = []Reagent{{ID: 42, Count: 1}}
	if spell.Capability().Blocked != "material_or_focus_requirement" {
		t.Fatal(spell.Capability())
	}
	spell.Reagents = nil
	spell.Effects[0].ItemType = 0
	if spell.Capability().Blocked != "effect_24_requires_handler" {
		t.Fatal(spell.Capability())
	}
}

func TestInterruptSpellMetadata(t *testing.T) {
	for _, id := range []uint32{1766, 2139, 6552} {
		spell, ok := Default().Lookup(id)
		if !ok || !spell.InterruptsCasting() || spell.IsHealing() ||
			spell.Capability() != (Capability{Kind: Interrupt, Target: "enemy"}) {
			t.Fatalf("spell %d has incorrect interrupt metadata: %+v", id, spell.Capability())
		}
	}
	shieldBash, _ := Default().Lookup(72)
	if !shieldBash.InterruptsCasting() || shieldBash.Capability().Blocked == "" {
		t.Fatal("Shield Bash must report its interrupt and retain its special requirements")
	}
	fakeInterrupt := Spell{Effects: []Effect{{Kind: 6, Aura: 68}}}
	if fakeInterrupt.InterruptsCasting() {
		t.Fatal("an aura number is not an interrupt effect")
	}
}

func TestHealingSpellMetadata(t *testing.T) {
	for _, id := range []uint32{2054, 139, 11642, 12039, 12491, 16561} {
		spell, ok := Default().Lookup(id)
		if !ok || !spell.IsHealing() || spell.InterruptsCasting() {
			t.Fatalf("spell %d has incorrect healing metadata", id)
		}
	}
	for _, effect := range []Effect{{Kind: 136}, {Kind: 27, Aura: 8}} {
		spell := Spell{Effects: []Effect{effect}}
		if !spell.IsHealing() {
			t.Fatalf("effect %+v does not classify as healing", effect)
		}
	}
	for _, id := range []uint32{0, 585, 2006, 2645, 1766} {
		spell, _ := Default().Lookup(id)
		if spell.IsHealing() {
			t.Fatalf("spell %d spuriously classified as healing", id)
		}
	}
}
