package behavior

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestItemDisposalActualEncounterBundleUsesItsSharedGear(t *testing.T) {
	bundle, err := exec.Command("python3", "-B", filepath.Join("..", "..", "dev", "encounter-bundle")).Output()
	if err != nil {
		t.Fatal(err)
	}
	// Use the production typed-facts projector and its real native fixture.
	// The bundle, rather than the caller's input, supplies the gear functions.
	projected := disposalArmorCapture(t, disposalArmorFrame(t))
	if projected.Frame == nil {
		t.Fatal("complete native facts did not reach Lua", projected.Result)
	}
	frame := projected.Frame
	frame["gear"] = map[string]any{"compare": "caller cannot supply functions"}
	const expression = "local gain=(candidate.armor or 0)-(current.armor or 0)"
	if strings.Count(string(bundle), expression) != 1 {
		t.Fatal("actual bundle must contain exactly one shared gear scorer")
	}
	for _, tc := range []struct {
		name, source, want string
	}{
		{"bundled_shared_score", string(bundle), "eligible"},
		{"edited_shared_score", strings.Replace(string(bundle), expression, expression+"-100", 1), "keep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			disposalArmorProductionResult(t, "local policy=(function()\n"+tc.source+"\nend)()\nreturn {decide=policy.disposal_evaluation}", frame, tc.want)
		})
	}
}
