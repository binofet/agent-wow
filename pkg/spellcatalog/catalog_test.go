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
