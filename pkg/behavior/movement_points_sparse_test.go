package behavior

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestMovementPointSparseReferencePreservesFactsAndOverrides(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "movement-points.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := "local M=(function()\n" + string(raw) + "\nend)()\nreturn {decide=function(f) " +
		`if f.override=="numeric_false" then M.reference_spells[21070]=false
 elseif f.override=="string_false" then M.reference_spells["21070"]=false
 elseif f.override=="numeric_custom" then M.reference_spells={[42]=true}
 elseif f.override=="clear" then M.reference_spells={} end
 return {rows=M.ground_sightings(f.observation)} end}`
	for _, tc := range []struct {
		name, override string
		spell          any
		want           int
	}{
		{"default", "", 21070, 1}, {"different", "", 42, 0}, {"string_is_not_wire_number", "", "21070", 0},
		{"false_is_not_spell", "", false, 0}, {"fraction", "", 21070.5, 0},
		{"numeric_false", "numeric_false", 21070, 0}, {"string_false", "string_false", 21070, 0},
		{"numeric_custom", "numeric_custom", 42, 1}, {"clear", "clear", 21070, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewWithOptions(SessionOptions{ExecutionLimit: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.Reload(source, ""); err != nil {
				t.Fatal(err)
			}
			effect := map[string]any{"guid": "effect", "spellId": tc.spell, "casterGuid": "npc", "x": 2, "y": 3, "z": 4, "radiusYards": 5, "firstObservedAtUnixMs": "1000", "positionRevision": "7", "serverCastTimeMs": "9"}
			frame := map[string]any{"override": tc.override, "observation": map[string]any{"groundEffectsSupported": true, "visitId": "1", "groundEffects": []any{effect}}}
			out, err := r.Evaluate(frame)
			if err != nil {
				t.Fatal(err)
			}
			rows, ok := out["rows"].([]any)
			if tc.want == 0 {
				if !reflect.DeepEqual(out["rows"], map[string]any{}) {
					t.Fatal(out)
				}
			} else if !ok || len(rows) != tc.want {
				t.Fatal(out)
			}
			if tc.want == 1 {
				want := map[string]any{"id": "effect:1", "sourceGuid": "npc", "casterGuid": "npc", "groundEffectGuid": "effect", "spellId": float64(tc.spell.(int)), "x": float64(2), "y": float64(3), "z": float64(4), "radiusYards": float64(5), "observedAtMs": float64(1000), "positionRevision": "7", "serverCastTimeMs": "9", "estimated": false, "kind": "observed_ground_effect", "castObserved": false}
				if !reflect.DeepEqual(rows[0], want) {
					t.Fatal(rows[0], want)
				}
			}
			// Polling must not alter original time, radius, pose or source identity.
			again, err := r.Evaluate(frame)
			if err != nil || !reflect.DeepEqual(again, out) {
				t.Fatal(again, err)
			}
		})
	}
}
