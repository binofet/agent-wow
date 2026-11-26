package behavior

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSparsePracticeConfigurationKeepsExactRecipesAndLegacyEdits(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "gathering.lua"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, edit, status string
		skill, spell       float64
	}{
		{"default", "", "selected", 129, 3275},
		{"legacy_numeric", "M.practice_tuning.spells={[3275]=129}", "selected", 129, 3275},
		{"canonical_string", "M.practice_tuning.spells={[\"3275\"]=129}", "selected", 129, 3275},
		{"numeric_override", "M.practice_tuning.spells[3275]=185", "selected", 185, 2538},
		{"all_numeric_overrides", "M.practice_tuning.spells[3275]=129;M.practice_tuning.spells[2538]=185;M.practice_tuning.spells[2540]=185;M.practice_tuning.spells[7620]=356", "selected", 129, 3275},
		{"cleared", "M.practice_tuning.spells={}", "blocked", 0, 0},
		{"seven_unique", `M.practice_tuning.spells={["3275"]=129,["2538"]=185,["2540"]=185,["7620"]=356,["42"]=129,["43"]=129,["44"]=129}`, "selected", 129, 3275},
		{"eight_unique", `M.practice_tuning.spells={["3275"]=129,["2538"]=185,["2540"]=185,["7620"]=356,["42"]=129,["43"]=129,["44"]=129,["45"]=129}`, "invalid", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "local M=(function()\n" + string(raw) + "\nend)()\n" + tc.edit + "\nreturn {decide=M.practice_choice}"
			r, err := NewWithOptions(SessionOptions{ExecutionLimit: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.Reload(source, ""); err != nil {
				t.Fatal(err)
			}
			frame := disposalArmorClone(t, sparsePracticeFrame())
			before := disposalArmorClone(t, frame)
			out, err := r.Evaluate(frame)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"status": tc.status}
			if tc.status == "selected" {
				want["skillId"], want["spellId"] = tc.skill, tc.spell
			} else if tc.status == "invalid" {
				want["status"] = "blocked"
				want["reason"] = "invalid_practice_tuning"
			} else {
				want["reason"] = "no_currently_ready_known_recipe"
			}
			if !reflect.DeepEqual(out, want) || !reflect.DeepEqual(frame, before) {
				t.Fatal(out, want)
			}
			if tc.status == "selected" {
				frame["mode"] = "validate"
				frame["selected"] = map[string]any{"skillId": tc.skill, "spellId": tc.spell}
				out, err = r.Evaluate(frame)
				want["status"] = "ready"
				if err != nil || !reflect.DeepEqual(out, want) {
					t.Fatal(out, err)
				}
			}
		})
	}
	for _, key := range []string{`"03275"`, `"3275.0"`, `"3.275e3"`, `"0"`, `"-1"`, `true`, `3275.5`, `2147483648`} {
		t.Run("invalid_"+key, func(t *testing.T) {
			source := "local M=(function()\n" + string(raw) + "\nend)()\nM.practice_tuning.spells={[" + key + "]=129};return {decide=M.practice_choice}"
			r, err := NewWithOptions(SessionOptions{ExecutionLimit: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.Reload(source, ""); err != nil {
				t.Fatal(err)
			}
			out, err := r.Evaluate(sparsePracticeFrame())
			if err != nil || !reflect.DeepEqual(out, map[string]any{"status": "blocked", "reason": "invalid_practice_tuning"}) {
				t.Fatal(out, err)
			}
		})
	}
}
func sparsePracticeFrame() map[string]any {
	skills, recipes := []any{}, []any{}
	for _, pair := range []struct{ skill, spell int }{{129, 3275}, {185, 2538}, {356, 7620}} {
		skills = append(skills, map[string]any{"skillId": pair.skill, "rank": 1, "maximum": 75})
		recipes = append(recipes, map[string]any{"skillId": pair.skill, "spellId": pair.spell, "known": true, "ready": true, "requirementsVerified": true})
	}
	return map[string]any{"sessionId": "practice-body", "selfGuid": "7", "mode": "choose", "requestedSkills": []any{129, 185, 356}, "attempts": []any{}, "professionState": map[string]any{"sessionId": "practice-body", "selfGuid": "7", "skillsKnown": true, "spellsKnown": true, "inventoryKnown": true, "revision": "1", "skillsRevision": "1", "inventoryRevision": "1", "backpackRevision": "1", "skills": skills, "recipes": recipes}}
}
