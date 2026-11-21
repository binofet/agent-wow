package gamecatalog

import "testing"

func TestFearBreakItemCooldownUsesPinnedTemplateOverrides(t *testing.T) {
	for _, test := range []struct {
		entry uint32
		ms    int32
	}{{18854, 300000}, {18857, 300000}, {28234, 120000}, {37864, 120000}} {
		row, ok := FearBreakItemCooldown(test.entry)
		if !ok || row.CooldownMs != test.ms || row.Category != 1182 || row.CategoryCooldownMs != test.ms {
			t.Fatal(test, row, ok)
		}
	}
	if _, ok := FearBreakItemCooldown(19024); ok {
		t.Fatal("non-break trinket received cooldown capability")
	}
	if _, ok := FearBreakItemCooldown(0); ok {
		t.Fatal("unknown item received capability")
	}
}
