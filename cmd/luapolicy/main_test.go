package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func multiBodyObservationInput(t *testing.T) []byte {
	t.Helper()
	bodies := make([]any, 0, 4)
	for body := 1; body <= 4; body++ {
		bags := make([]any, 0, 5)
		for bag, capacity := range []int{16, 8, 8, 8, 8} {
			slots := make([]any, 0, capacity)
			for slot := 0; slot < capacity; slot++ {
				slots = append(slots, map[string]any{"bag": bag, "slot": slot,
					"itemGuid": fmt.Sprint(uint64(0x4000000000000000) + uint64(body*1000+bag*40+slot)),
					"entry":    100, "stackCount": 1, "metadataKnown": true,
					"durabilityKnown": true, "durability": 20, "maxDurability": 20, "enhancementsKnown": true})
			}
			bags = append(bags, map[string]any{"bag": bag, "slotCount": capacity, "contentsKnown": true, "slots": slots})
		}
		spells := make([]any, 0, 40)
		for spell := 1; spell <= 40; spell++ {
			spells = append(spells, map[string]any{"spellId": spell, "familyId": spell, "rank": 1, "metadataKnown": true})
		}
		bodies = append(bodies, map[string]any{"guid": strconv.Itoa(body), "mapId": 0,
			"inventoryBags": bags, "spells": spells})
	}
	raw, err := json.Marshal(map[string]any{"observations": bodies, "memory": map[string]any{"stage": "observe"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= behavior.MaxJSONBytes {
		t.Fatal("ordinary multi-body fixture did not exceed the legacy bound", len(raw))
	}
	return raw
}

func TestValueBudgetRoundTripsOrdinaryMultiBodyInputAndOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observe.lua")
	if err := os.WriteFile(path, []byte(`return {decide=function(frame)return frame end}`), 0600); err != nil {
		t.Fatal(err)
	}
	input := multiBodyObservationInput(t)
	for _, flags := range [][]string{{}, {"--value-bytes", "0"}} {
		var out bytes.Buffer
		if err := run(append([]string{"--source", path}, flags...), bytes.NewReader(input), &out); err == nil || out.Len() != 0 {
			t.Fatal("default standalone JSON bound changed", err, out.Len())
		}
	}
	for _, limit := range []int{128 * 1024, 256 * 1024} {
		var out bytes.Buffer
		if err := run([]string{"--source", path, "--value-bytes", strconv.Itoa(limit), "--execution-ms", "1000"}, bytes.NewReader(input), &out); err != nil {
			t.Fatal("configured multi-body frame rejected", len(input), limit, err)
		}
		if !bytes.Equal(bytes.TrimSpace(out.Bytes()), input) {
			t.Fatal("configured input/output lost facts", len(input), out.Len())
		}
	}
	// The encoded byte boundary is exact; the final JSON framing newline is
	// outside that value. Use few nodes so this checks bytes independently.
	flat := []byte(`{"text":"` + strings.Repeat("x", 40*1024) + `"}`)
	var exact bytes.Buffer
	if err := run([]string{"--source", path, "--value-bytes", strconv.Itoa(len(flat)), "--execution-ms", "1000"}, bytes.NewReader(flat), &exact); err != nil || !bytes.Equal(bytes.TrimSpace(exact.Bytes()), flat) {
		t.Fatal("exact configured JSON byte boundary rejected", err, exact.Len())
	}
	for _, limit := range []int{-1, len(input) - 1} {
		var out bytes.Buffer
		if err := run([]string{"--source", path, "--value-bytes", strconv.Itoa(limit)}, bytes.NewReader(input), &out); err == nil || out.Len() != 0 {
			t.Fatal("invalid or exceeded JSON budget produced output", limit, err)
		}
	}
	for _, budget := range []string{"-1", "9223372036855"} {
		if err := run([]string{"--source", path, "--execution-ms", budget}, strings.NewReader(`{}`), &bytes.Buffer{}); err == nil {
			t.Fatal("invalid execution duration accepted", budget)
		}
	}
}

func TestValueBudgetAlsoBoundsOutputAndTypedRequestValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.lua")
	if err := os.WriteFile(path, []byte(`return {decide=function(frame)return {text="`+strings.Repeat("x", 40*1024)+`"}end}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"--source", path, "--value-bytes", "16384"}, strings.NewReader(`{}`), &out); err == nil || out.Len() != 0 {
		t.Fatal("output escaped the configured value budget", err, out.Len())
	}
	request := `{"token":"` + strings.Repeat("x", 40*1024) + `","x":12}`
	args := []string{"--mode", "validate", "--modules", "../../modules", "--method", "buddy-actions.travel_to"}
	if err := run(args, strings.NewReader(request), &out); err == nil {
		t.Fatal("default typed request bound widened")
	}
	out.Reset()
	if err := run(append(args, "--value-bytes", "65536"), strings.NewReader(request), &out); err != nil {
		t.Fatal("configured typed request rejected", err)
	}
	var normalized map[string]any
	if err := json.Unmarshal(out.Bytes(), &normalized); err != nil || normalized["token"] != strings.Repeat("x", 40*1024) {
		t.Fatal("typed normalization lost the configured request", err)
	}
	if err := run(append(args, "--value-bytes", "65536"), strings.NewReader(`{"typo":1}`), &bytes.Buffer{}); err == nil {
		t.Fatal("configured bytes bypassed descriptor admission")
	}
}

func TestSourceReadCapInheritsCompositionLimitWithoutWideningJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "composed.lua")
	code := `local module=(function() local data="` + strings.Repeat("x", 33000) + `"; return {size=function() return #data end} end)(); return {decide=function(s) return {bytes=module.size()} end}`
	if err := os.WriteFile(path, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{"--source", path}, strings.NewReader(`{}`), &output); err != nil || !strings.Contains(output.String(), `"bytes":33000`) {
		t.Fatal("CLI truncated source at the old JSON-size cap", output.String(), err)
	}
	if err := run([]string{"--source", path}, strings.NewReader(`{"x":"`+strings.Repeat("x", 32*1024)+`"}`), &bytes.Buffer{}); err == nil {
		t.Fatal("larger source admitted an oversized JSON frame")
	}
	// An exact upper-bound file remains valid; one extra byte must fail the
	// CLI's bounded read before any policy output can be produced.
	code += "--" + strings.Repeat("x", behavior.MaxSourceBytes-len(code)-2)
	if err := os.WriteFile(path, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--source", path}, strings.NewReader(`{}`), &bytes.Buffer{}); err != nil {
		t.Fatal("exact source limit rejected", err)
	}
	if err := os.WriteFile(path, []byte(code+"x"), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	err := run([]string{"--source", path}, strings.NewReader(`{}`), &output)
	if err == nil || !strings.Contains(err.Error(), "bounded policy source required") || output.Len() != 0 {
		t.Fatal("CLI read beyond source cap", output.String(), err)
	}
}

func TestSourceBudgetFlagAllowsCompositionAndRejectsConfiguredOverflow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "composed.lua")
	code := `return {decide=function(s)return {ok=true} end}` + "\n--" + strings.Repeat("x", 256*1024)
	if err := os.WriteFile(path, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--source", path, "--source-bytes", "524288"}, strings.NewReader(`{}`), &bytes.Buffer{}); err != nil {
		t.Fatal("large configured source rejected", err)
	}
	if err := run([]string{"--source", path, "--source-bytes", "131072"}, strings.NewReader(`{}`), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "configured limit 131072") {
		t.Fatal("configured overflow was not explicit", err)
	}
	if err := run([]string{"--source", path, "--source-bytes", "-1"}, strings.NewReader(`{}`), &bytes.Buffer{}); err == nil {
		t.Fatal("negative source budget accepted")
	}
}

func TestDescriptorAdmissionRejectsUnknownFieldsAndWrongTypes(t *testing.T) {
	for _, tc := range []struct {
		method, input string
		valid         bool
	}{
		{"buddy-actions.status", `{}`, true},
		{"buddy-actions.travel_to", `{"token":"lease","x":12,"mapId":0}`, true},
		{"buddy-actions.travel_to", `{"token":"lease","x":"not-a-number"}`, false},
		{"buddy-actions.travel_to", `{"typo":12}`, false},
		{"buddy-actions.OnPacket", `{}`, false},
		{"buddy-chat.send", `{}`, false}, // No script admission metadata.
		{"buddy-actions.status", `[]`, false},
	} {
		t.Run(tc.method+tc.input, func(t *testing.T) {
			var output bytes.Buffer
			err := run([]string{"--mode", "validate", "--modules", "../../modules", "--method", tc.method}, strings.NewReader(tc.input), &output)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestPurePolicyCanResumeAndHotEditWithoutGameTransport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.lua")
	for _, phase := range []string{"travel", "turn_in"} {
		code := `return {decide=function(s) return {phase="` + phase + `", memory=s.memory, guid=s.guid} end}`
		if err := os.WriteFile(path, []byte(code), 0600); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		err := run([]string{"--source", path}, strings.NewReader(`{"memory":{"stack":[{"kind":"trip"}]},"guid":"17379391279565179449"}`), &output)
		if err != nil || !strings.Contains(output.String(), phase) || !strings.Contains(output.String(), "17379391279565179449") {
			t.Fatalf("%s %v", output.String(), err)
		}
	}
}

func TestPolicyRejectsAmbientIOAndMultipleInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.lua")
	if err := os.WriteFile(path, []byte(`return {decide=function(s) return {secret=os.getenv("HOME")} end}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--source", path}, strings.NewReader(`{}`), &bytes.Buffer{}); err == nil {
		t.Fatal("ambient IO admitted")
	}
	if err := os.WriteFile(path, []byte(`return {decide=function(s) return s end}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--source", path}, strings.NewReader(`{} {}`), &bytes.Buffer{}); err == nil {
		t.Fatal("second input admitted")
	}
}
