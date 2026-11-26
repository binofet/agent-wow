package behavior

import (
	"github.com/agent-wow/agent-wow/behaviors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStarterSparseQuestTuningKeepsNumericOverridesAndObservedProfiles(t *testing.T) {
	// Semantic fixture only; existing ordinary5ms CLI proofs remain unchanged.
	source := behaviors.StarterSpellSource
	end := strings.LastIndex(source, "\nreturn M")
	if end < 0 {
		t.Fatal("shared starter adapter return missing")
	}
	source = source[:end] + `
return {decide=function(i)
 local m=encounter_starter_objectives
 local t=m.coldridge_practice
 if i.mode=="numeric" then
  t.profiles[170]={{entry=709,level=2,maxHealth=56,faction=36},{entry=724,level=2,maxHealth=55,faction=36}}
  t.objectives[170]={{entry=709,count=6},{entry=724,count=6}}
 elseif i.mode=="profiles_false" then t.profiles[170]=false
 elseif i.mode=="objectives_false" then t.objectives[170]=false end
 return {supported=m.coldridge_supports_entry(i.questId,i.entry),
  profile=m.coldridge_profile(i.creature,i.questId),lifecycle=m.coldridge_lifecycle(i.lifecycle)}
end}`
	r, err := NewWithOptions(SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Reload(source, ""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, mode                    string
		quest                         any
		entry, level, health, faction int
		want                          bool
		entries                       []any
	}{
		{"179", "", 179, 705, 1, 40, 0, true, []any{float64(707), float64(724)}},
		{"170", "", 170, 707, 2, 55, 36, true, []any{float64(707), float64(724)}},
		{"183", "", 183, 708, 3, 71, 189, true, []any{float64(707), float64(724)}},
		{"182", "", 182, 706, 4, 86, 190, true, []any{float64(707), float64(724)}},
		{"numeric override", "numeric", 170, 709, 2, 56, 36, true, []any{float64(709), float64(724)}},
		{"numeric false profile", "profiles_false", 170, 707, 2, 55, 36, false, nil},
		{"numeric false objectives", "objectives_false", 170, 707, 2, 55, 36, true, nil},
		{"unknown tuple", "", 170, 709, 2, 56, 36, false, []any{float64(707), float64(724)}},
		{"string ID remains unknown", "", "170", 707, 2, 55, 36, false, []any{float64(707), float64(724)}},
		{"fraction remains unknown", "", 170.5, 707, 2, 55, 36, false, []any{float64(707), float64(724)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := make([]any, 25)
			for j := range log {
				log[j] = map[string]any{"slot": j}
			}
			log[0] = map[string]any{"slot": 0, "questId": 170, "state": 0, "objectiveCounts": []any{0, 0, 0, 0}}
			lifecycle := map[string]any{"stage": "observe", "selfGuid": "1", "self": map[string]any{"guid": "1", "present": true, "classKnown": true, "classId": 9, "levelKnown": true, "level": 2}, "goal": map[string]any{"questId": 170, "counts": []any{0, 0}}, "nearby": []any{}, "clientPosition": map[string]any{}, "targetGuid": "0", "questLogKnown": true, "questLog": log}
			input := map[string]any{"mode": tc.mode, "questId": tc.quest, "entry": tc.entry, "creature": map[string]any{"mapKnown": true, "mapId": 0, "entry": tc.entry, "levelKnown": true, "level": tc.level, "maxHealth": tc.health, "factionTemplate": tc.faction}, "lifecycle": lifecycle}
			out, err := r.Evaluate(input)
			if err != nil {
				t.Fatal(err)
			}
			if out["supported"] != tc.want || out["profile"] != tc.want {
				t.Fatal("profile/override changed", out)
			}
			life, ok := out["lifecycle"].(map[string]any)
			if !ok {
				t.Fatal(out)
			}
			if tc.entries == nil {
				if life["active"] != false || life["needed"] != false {
					t.Fatal("false override fell through", life)
				}
			} else if !reflect.DeepEqual(life["neededEntries"], tc.entries) || life["active"] != true {
				t.Fatal("objective lookup changed", life)
			}
		})
	}
}
