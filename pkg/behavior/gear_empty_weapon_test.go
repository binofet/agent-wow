package behavior

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSharedGearEmptyMainhandRequiresObservedPhysicalWeaponDamage(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "gear.lua"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		delay   float64
		damages []any
		want    bool
	}{
		{"positive_physical", 2000, []any{map[string]any{"minimum": 4, "maximum": 6}}, true},
		{"missing_damage", 2000, []any{}, false},
		{"observed_zero_damage", 2000, []any{map[string]any{}}, false},
		{"missing_delay", 0, []any{map[string]any{"minimum": 4, "maximum": 6}}, false},
		{"nonphysical_damage", 2000, []any{map[string]any{"minimum": 4, "maximum": 6, "school": 1}}, false},
		{"inverted_damage", 2000, []any{map[string]any{"minimum": 6, "maximum": 4}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := New()
			load(t, r, "local gear=(function()\n"+string(source)+"\nend)()\nreturn {decide=gear.preview}")
			// The offered item is unowned: no private candidate GUID is supplied.
			input := map[string]any{"candidate": map[string]any{"itemId": 904001, "available": true,
				"itemClass": 2, "inventoryType": 13, "armorComparisonKnown": true, "delayMs": test.delay, "damages": test.damages},
				"candidateFacts": map[string]any{"enhancementsKnown": true},
				"currentFacts":   map[string]any{"slotKnown": true, "itemGuid": "0"}}
			out, err := r.Evaluate(input)
			if err != nil || out["upgrade"] != test.want {
				t.Fatal("unowned weapon preview violated physical damage coverage", out, err)
			}
			if !test.want && out["reason"] != "weapon_damage_unknown" {
				t.Fatal("missing physical damage acquired an empty-slot upgrade", out)
			}
		})
	}
}
