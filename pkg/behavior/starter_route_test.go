package behavior

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStarterQuestRouteActualSourceCASChangesAllowedIDsInSameRuntime(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "starter-objectives.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	const gate = "if known and level>=2 then"
	if strings.Count(source, gate) != 1 {
		t.Fatal("actual starter route level gate is missing or ambiguous")
	}
	edited := strings.Replace(source, gate, "if known and level>=3 then", 1)
	frame := map[string]any{
		"stage": "quest_route", "selfGuid": "4", "autonomous": false,
		"activeQuestIds": []any{7}, "desiredQuestIds": []any{7, 18, 3903},
		"confirmedQuestIds": []any{33}, "wolfComplete": false,
		"levelKnown": true, "level": 2,
		"questRoute": map[string]any{
			"sessionId": "quest-session", "introProbeCurrent": false,
			"eaganProbeCurrent": false, "wolfProbeCurrent": false,
			"givers": []any{map[string]any{"guid": "123", "entry": 823}},
			"offer": map[string]any{
				"objectKnown": true, "questsKnown": true, "introQuestsKnown": true,
				"npcGuid": "0", "expiresInMs": "0", "quests": []any{},
			},
		},
	}
	before, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	// This is the actual Runtime and editable source with its default budgets.
	// Reload/CAS changes a pure choice; this fixture makes no live-session claim.
	r := New()
	first, err := r.Reload(source, "")
	if err != nil || first == "" {
		t.Fatal("actual starter route did not load", first, err)
	}
	evaluate := func(allowed any) map[string]any {
		t.Helper()
		out, err := r.Evaluate(frame)
		want := map[string]any{
			"status": "ready",
			"choice": map[string]any{
				"kind": "quest_route", "neededEntry": float64(823), "rewardQuestId": float64(0),
				"allowedQuestIds": allowed, "probes": map[string]any{},
			},
		}
		if err != nil || !reflect.DeepEqual(out, want) {
			t.Fatalf("actual starter route returned %v, want %v: %v; status %v", out, want, err, r.Status())
		}
		return out
	}
	original := evaluate([]any{float64(18), float64(3903)})
	if got, err := r.Reload(edited, ""); !errors.Is(err, ErrRevisionConflict) || got != first || r.Status().Revision != first {
		t.Fatal("stale CAS replaced the accepted starter route", got, err, r.Status())
	}
	if out := evaluate([]any{float64(18), float64(3903)}); !reflect.DeepEqual(out, original) {
		t.Fatal("stale CAS changed the route choice", out, original)
	}
	second, err := r.Reload(edited, first)
	if err != nil || second == "" || second == first || r.Status().Revision != second {
		t.Fatal("correct CAS did not accept the edited route", second, err, r.Status())
	}
	// An empty Lua table is the Runtime's empty JSON object, not an array.
	changed := evaluate(map[string]any{})
	if got, err := r.Reload("return {decide=function(", second); err == nil || got != second || r.Status().Revision != second {
		t.Fatal("invalid source replaced the accepted route revision", got, err, r.Status())
	}
	if out := evaluate(map[string]any{}); !reflect.DeepEqual(out, changed) {
		t.Fatal("invalid reload changed the accepted route choice", out, changed)
	}
	after, err := json.Marshal(frame)
	if err != nil || string(after) != string(before) {
		t.Fatal("reload/evaluation changed the original observed frame", err)
	}
}
