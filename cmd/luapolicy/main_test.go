package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
