package behavior

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func tradeConsentSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "trade-consent.lua"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func tradeConsentFrame() map[string]any {
	return map[string]any{
		"sessionId": "consent-session", "selfGuid": "8",
		"trade": map[string]any{
			"sessionId": "consent-session", "revision": "21", "phase": "OPEN", "partnerGuid": "3",
			"partnerAccepted": true, "acceptTransportWritten": false,
			"ownRequestedItems": []any{}, "ownRequestedCopper": 0,
			"partnerItems": []any{}, "partnerCopper": 0,
		},
	}
}

func tradeConsentItem(slot int) map[string]any {
	return map[string]any{"slot": slot, "entry": 750, "count": 1, "itemGuid": "4611686018427387905",
		"name": "Observed item", "metadataKnown": true}
}

func tradeConsentResult(t *testing.T, r *Runtime, input map[string]any, want string) map[string]any {
	t.Helper()
	before, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Evaluate(input)
	if err != nil {
		t.Fatal(err, r.Status())
	}
	reason, ok := out["reason"].(string)
	if len(out) != 2 || out["kind"] != want || !ok || len(reason) == 0 || len(reason) > 128 {
		t.Fatalf("got %v, want bounded two-key %s decision", out, want)
	}
	after, err := json.Marshal(input)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("policy changed the observed input", err)
	}
	if r.Status().Failures != 0 || r.Status().QuarantinedRevision != "" {
		t.Fatal("decision used a failed policy", r.Status())
	}
	return out
}

func TestTradeConsentDefaultOwnerAndConsentFacts(t *testing.T) {
	source := tradeConsentSource(t)
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"owner_empty_window", func(map[string]any) {}, "accept"},
		{"gift", func(f map[string]any) {
			tr := f["trade"].(map[string]any)
			tr["partnerItems"], tr["partnerCopper"] = []any{tradeConsentItem(0)}, 123
		}, "accept"},
		{"explicit_outgoing", func(f map[string]any) {
			tr := f["trade"].(map[string]any)
			tr["ownRequestedItems"], tr["ownRequestedCopper"] = []any{tradeConsentItem(5)}, 123
		}, "accept"},
		{"observed_unknown_item_metadata", func(f map[string]any) {
			row := tradeConsentItem(0)
			row["metadataKnown"], row["name"], row["itemGuid"] = false, "", "0"
			f["trade"].(map[string]any)["partnerItems"] = []any{row}
		}, "accept"},
		{"other_partner", func(f map[string]any) { f["trade"].(map[string]any)["partnerGuid"] = "4" }, "wait"},
		{"unknown_partner", func(f map[string]any) { f["trade"].(map[string]any)["partnerGuid"] = "0" }, "wait"},
		{"partner_not_accepted", func(f map[string]any) { f["trade"].(map[string]any)["partnerAccepted"] = false }, "wait"},
		{"accept_written", func(f map[string]any) { f["trade"].(map[string]any)["acceptTransportWritten"] = true }, "wait"},
		{"other_session", func(f map[string]any) { f["trade"].(map[string]any)["sessionId"] = "replaced" }, "wait"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New() // Real default execution/input budgets, including under -race.
			load(t, r, source)
			frame := tradeConsentFrame()
			tc.edit(frame)
			tradeConsentResult(t, r, frame, tc.want)
		})
	}
	for _, phase := range []string{"IDLE", "REQUESTED", "INVITED", "ACCEPT_SENT", "COMPLETED", "unknown"} {
		t.Run(phase, func(t *testing.T) {
			r := New()
			load(t, r, source)
			frame := tradeConsentFrame()
			frame["trade"].(map[string]any)["phase"] = phase
			tradeConsentResult(t, r, frame, "wait")
		})
	}
}

func TestTradeConsentMalformedOrIncompleteFactsWait(t *testing.T) {
	source := tradeConsentSource(t)
	cases := map[string]func(map[string]any){
		"extra_input":        func(f map[string]any) { f["eligible"] = true },
		"missing_self":       func(f map[string]any) { delete(f, "selfGuid") },
		"numeric_self":       func(f map[string]any) { f["selfGuid"] = 8 },
		"zero_self":          func(f map[string]any) { f["selfGuid"] = "0" },
		"leading_zero_self":  func(f map[string]any) { f["selfGuid"] = "08" },
		"overflow_self":      func(f map[string]any) { f["selfGuid"] = "18446744073709551616" },
		"nondigit_self":      func(f map[string]any) { f["selfGuid"] = "8x" },
		"empty_session":      func(f map[string]any) { f["sessionId"] = "" },
		"oversize_session":   func(f map[string]any) { f["sessionId"] = strings.Repeat("s", 129) },
		"trade_not_object":   func(f map[string]any) { f["trade"] = false },
		"extra_trade":        func(f map[string]any) { f["trade"].(map[string]any)["eligible"] = true },
		"numeric_revision":   func(f map[string]any) { f["trade"].(map[string]any)["revision"] = 21 },
		"zero_revision":      func(f map[string]any) { f["trade"].(map[string]any)["revision"] = "0" },
		"negative_copper":    func(f map[string]any) { f["trade"].(map[string]any)["partnerCopper"] = -1 },
		"fraction_copper":    func(f map[string]any) { f["trade"].(map[string]any)["ownRequestedCopper"] = 0.5 },
		"overflow_copper":    func(f map[string]any) { f["trade"].(map[string]any)["ownRequestedCopper"] = float64(4294967296) },
		"unknown_acceptance": func(f map[string]any) { delete(f["trade"].(map[string]any), "partnerAccepted") },
		"unknown_written":    func(f map[string]any) { delete(f["trade"].(map[string]any), "acceptTransportWritten") },
		"numeric_acceptance": func(f map[string]any) { f["trade"].(map[string]any)["partnerAccepted"] = 1 },
		"string_written":     func(f map[string]any) { f["trade"].(map[string]any)["acceptTransportWritten"] = "false" },
		"missing_items":      func(f map[string]any) { delete(f["trade"].(map[string]any), "partnerItems") },
		"keyed_items": func(f map[string]any) {
			f["trade"].(map[string]any)["partnerItems"] = map[string]any{"1": tradeConsentItem(0)}
		},
		"duplicate_slots": func(f map[string]any) {
			f["trade"].(map[string]any)["partnerItems"] = []any{tradeConsentItem(0), tradeConsentItem(0)}
		},
		"own_service_slot": func(f map[string]any) { f["trade"].(map[string]any)["ownRequestedItems"] = []any{tradeConsentItem(6)} },
		"partner_slot_7":   func(f map[string]any) { f["trade"].(map[string]any)["partnerItems"] = []any{tradeConsentItem(7)} },
		"extra_item_fact": func(f map[string]any) {
			row := tradeConsentItem(0)
			row["eligible"] = true
			f["trade"].(map[string]any)["partnerItems"] = []any{row}
		},
		"missing_metadata_known": func(f map[string]any) {
			row := tradeConsentItem(0)
			delete(row, "metadataKnown")
			f["trade"].(map[string]any)["partnerItems"] = []any{row}
		},
		"oversize_item_name": func(f map[string]any) {
			row := tradeConsentItem(0)
			row["name"] = strings.Repeat("n", 256)
			f["trade"].(map[string]any)["partnerItems"] = []any{row}
		},
		"numeric_item_guid": func(f map[string]any) {
			row := tradeConsentItem(0)
			row["itemGuid"] = 8
			f["trade"].(map[string]any)["partnerItems"] = []any{row}
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			r := New()
			load(t, r, source)
			frame := tradeConsentFrame()
			edit(frame)
			tradeConsentResult(t, r, frame, "wait")
		})
	}
}

func TestTradeConsentFullActualFrameIncludesServiceSlot(t *testing.T) {
	r := New()
	load(t, r, tradeConsentSource(t))
	frame := tradeConsentFrame()
	frame["selfGuid"] = "18446744073709551615"
	tr := frame["trade"].(map[string]any)
	tr["revision"], tr["ownRequestedCopper"], tr["partnerCopper"] = "18446744073709551615", float64(4294967295), float64(4294967295)
	for _, side := range []struct {
		key  string
		size int
	}{{"ownRequestedItems", 6}, {"partnerItems", 7}} {
		rows := make([]any, side.size)
		for i := range rows {
			row := tradeConsentItem(i)
			row["entry"], row["count"], row["itemGuid"], row["name"] = float64(4294967295), float64(4294967295), "18446744073709551615", strings.Repeat("n", 255)
			rows[i] = row
		}
		tr[side.key] = rows
	}
	tradeConsentResult(t, r, frame, "accept")
	tr["partnerItems"] = append(tr["partnerItems"].([]any), tradeConsentItem(0))
	tradeConsentResult(t, r, frame, "wait")
}

func TestTradeConsentEditableTuningUsesCurrentSource(t *testing.T) {
	source := tradeConsentSource(t)
	r := New()
	revision := load(t, r, source)
	frame := tradeConsentFrame()
	tradeConsentResult(t, r, frame, "accept")
	const tuning = "local accept_owner_trades = true"
	if strings.Count(source, tuning) != 1 {
		t.Fatal("editable consent tuning is not unique")
	}
	disabled := strings.Replace(source, tuning, "local accept_owner_trades = false", 1)
	next, err := r.Reload(disabled, revision)
	if err != nil || next == revision {
		t.Fatal("consent edit was not accepted", next, err)
	}
	tradeConsentResult(t, r, frame, "wait")
	const owner = `local owner_guid = "3"`
	if strings.Count(source, owner) != 1 {
		t.Fatal("editable owner tuning is not unique")
	}
	large := "18446744073709551615"
	if _, err := r.Reload(strings.Replace(source, owner, `local owner_guid = "`+large+`"`, 1), next); err != nil {
		t.Fatal(err)
	}
	tradeConsentResult(t, r, frame, "wait")
	frame["trade"].(map[string]any)["partnerGuid"] = large
	tradeConsentResult(t, r, frame, "accept")
}
