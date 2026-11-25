package behavior

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestStarterQuestRouteProjectionKeepsCompleteFramesAndExistingValidation(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "starter-objectives.lua"))
	if err != nil {
		t.Fatal(err)
	}
	// Keep complete validation under race instrumentation separate from the
	// identical ordinary Runtime CLI proof below. The original CAS test above
	// still exercises the same Runtime instance with its unchanged defaults.
	r, err := NewWithOptions(SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reload(string(source), ""); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"last_giver_823", "last_giver_196", "any_available_offer", "last_giver_invalid", "last_offer_invalid", "routine_invalid", "actions_invalid"} {
		t.Run(mode, func(t *testing.T) {
			givers, offers := []any{}, []any{}
			for i := 0; i < 128; i++ {
				givers = append(givers, map[string]any{"guid": strconv.Itoa(200 + i), "entry": 999})
			}
			for i := 0; i < 32; i++ {
				offers = append(offers, map[string]any{"questId": 1000 + i, "available": false})
			}
			givers[126] = map[string]any{"guid": "123", "entry": 196}
			givers[127] = map[string]any{"guid": "123", "entry": 823}
			frame := map[string]any{"stage": "quest_route", "selfGuid": "4", "autonomous": false,
				"activeQuestIds": []any{7}, "desiredQuestIds": []any{7, 18, 3903}, "confirmedQuestIds": []any{},
				"wolfComplete": false, "levelKnown": true, "level": 2,
				"questRoute": map[string]any{"sessionId": "complete-route", "introProbeCurrent": false,
					"eaganProbeCurrent": false, "wolfProbeCurrent": false, "givers": givers,
					"offer": map[string]any{"objectKnown": true, "questsKnown": true, "introQuestsKnown": true,
						"npcGuid": "123", "expiresInMs": "1", "quests": offers}}}
			needed, allowed, probes := float64(196), []any{float64(33)}, map[string]any{"eagan": true, "wolf": false}
			switch mode {
			case "last_giver_196":
				givers[126], givers[127] = givers[127], givers[126]
				needed, allowed, probes = 823, []any{float64(18), float64(3903), float64(5261)}, map[string]any{"wolf": true}
			case "any_available_offer":
				offers[0] = map[string]any{"questId": 5261, "available": true}
				offers[31] = map[string]any{"questId": 5261, "available": false}
				needed, allowed, probes = 823, []any{float64(18), float64(3903), float64(5261)}, map[string]any{}
			case "last_giver_invalid":
				// An unrelated final row still needs complete validation.
				givers[127] = map[string]any{"guid": "999", "entry": false}
			case "last_offer_invalid":
				offers[31] = map[string]any{"questId": 0.5, "available": false}
			case "routine_invalid":
				frame["routine"] = false
			case "actions_invalid":
				frame["actions"] = false
			}
			before, err := json.Marshal(frame)
			if err != nil || len(givers) != 128 || len(offers) != 32 {
				t.Fatal("full route fixture changed", err)
			}
			want := map[string]any{"status": "ready", "choice": map[string]any{"kind": "quest_route",
				"neededEntry": needed, "rewardQuestId": float64(0), "allowedQuestIds": allowed, "probes": probes}}
			if strings.HasSuffix(mode, "invalid") {
				want = map[string]any{"status": "invalid"}
			}
			// Every full frame must first return the complete expected decision
			// through Reload+Evaluate in the ordinary exact-head CLI at the real
			// 5ms/32KiB defaults. Errors, including resource exhaustion, fail.
			ordinary, err := evaluateDisposalProductionMode(t, string(source), frame, false, "evaluate-runtime")
			if err != nil || !reflect.DeepEqual(ordinary, want) {
				t.Fatal("full-frame/default ordinary Runtime contract changed", ordinary, want, err)
			}
			out, err := r.Evaluate(frame)
			if err != nil || !reflect.DeepEqual(out, want) || !reflect.DeepEqual(out, ordinary) {
				t.Fatal("full-frame semantic route contract changed", out, want, ordinary, err, r.Status())
			}
			after, err := json.Marshal(frame)
			if err != nil || string(before) != string(after) {
				t.Fatal("route projection changed the actual observed input", err)
			}
		})
	}
}
