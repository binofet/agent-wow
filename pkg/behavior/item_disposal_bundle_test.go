package behavior

import (
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
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
			source := "local policy=(function()\n" + tc.source + "\nend)()\nreturn {decide=policy.disposal_evaluation}"
			if tc.name != "bundled_shared_score" {
				disposalArmorProductionResult(t, source, frame, tc.want)
				return
			}
			// The exact bundled case measured a decide resource refusal in CI.
			// Prove its full semantics separately, then invoke the ordinary
			// artifact-validated 5ms CLI once. A resource turn is NO decision;
			// it cannot substitute the semantic eligible result. Native accepted
			// exports separately require their actual default-budget decision.
			before := disposalArmorClone(t, frame)
			semantic := disposalArmorResult(t, disposalArmorSemanticRuntime(t, source), frame, tc.want)
			production, err := evaluateDisposalProduction(t, source, frame)
			var unavailable *disposalProductionResourceError
			if errors.As(err, &unavailable) && unavailable.phase == "decide" {
				if production != nil || !errors.Is(err, ErrExecutionBudget) {
					t.Fatal("resource refusal published a disposal decision", production, err)
				}
				t.Log("default standalone decide resource turn: NO decision; no retry or production-success claim", err)
			} else if err != nil || !reflect.DeepEqual(production, semantic) {
				t.Fatal("ordinary CLI returned a different decision or unsupported failure", production, semantic, err)
			}
			if !reflect.DeepEqual(frame, before) {
				t.Fatal("evaluation changed the observed frame")
			}
		})
	}
}
