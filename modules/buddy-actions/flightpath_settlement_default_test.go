package buddyactions

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// The maintained CI builds this ordinary CLI before its race suite. This test
// compares the complete semantic outputs; it supplies no evaluator overrides.
func TestFlightpathSettlementDefaultCLIUsesExactNativeFlowFrames(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := os.Getenv("BUDDY_LUAPOLICY_TEST_BINARY")
	if binary == "" {
		binary = filepath.Join(root, "data", "bin", "luapolicy")
	} else if !filepath.IsAbs(binary) {
		binary = filepath.Join(root, binary)
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatalf("build the current clean checkout's evaluator first: go build -o data/bin/luapolicy ./cmd/luapolicy: %v", err)
	}
	head, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{}
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	if info.Path != "github.com/agent-wow/agent-wow/cmd/luapolicy" || settings["vcs.revision"] != strings.TrimSpace(string(head)) || settings["vcs.modified"] != "false" ||
		behavior.EvaluationLimit != 5*time.Millisecond || behavior.MaxJSONBytes != 32*1024 {
		t.Fatal("default CLI proof requires the exact clean source artifact and unchanged evaluation defaults", info.Path, settings)
	}
	p := newSettlementPolicyFixture(t, false)
	path := filepath.Join(t.TempDir(), "flight.lua")
	if err := os.WriteFile(path, []byte(p.f.source), 0600); err != nil {
		t.Fatal(err)
	}
	parity := func(input []byte, expected map[string]any) {
		t.Helper()
		if len(input) > behavior.MaxJSONBytes {
			t.Fatal("flow frame exceeds the default JSON bound", len(input))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, "-source", path)
		command.Dir, command.Stdin = root, bytes.NewReader(input)
		var diagnostic bytes.Buffer
		command.Stderr = &diagnostic
		out, err := command.Output()
		if err != nil {
			t.Fatal("default CLI failed this exact semantic frame", err, diagnostic.String())
		}
		var actual map[string]any
		if err := json.Unmarshal(out, &actual); err != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatal("full default CLI output differs from native-flow semantic evaluation", actual, expected, err)
		}
	}
	until := func(method string) {
		t.Helper()
		for i := 0; i < 1000; i++ {
			input, err := json.Marshal(p.f.input)
			if err != nil {
				t.Fatal(err)
			}
			out := p.step()
			if action, ok := out["action"].(map[string]any); ok {
				if action["method"] == method {
					parity(input, out)
					p.dispatch(action)
					return
				}
				if action["method"] != "buddy-actions.flightpath_reference" {
					t.Fatal("unexpected mutation before the selected flow stage", method, action)
				}
				p.dispatch(action)
			} else if out["done"] == true || out["resolve"] != nil {
				t.Fatal("flow completed or resolved before its expected call", method, out)
			}
		}
		t.Fatal("bounded fixture did not reach flow stage", method)
	}
	until("buddy-actions.activate_flightpath")
	activationPolicyNoEffect(t, p.step())
	p.scope = &modv1.TransportScope{ConnectionId: "settlement-policy-stream", SelfGuid: 1, VisitBound: true, VisitRevision: 9, MotionRevision: 10}
	flightpathSettlementPacket(t, p.f.base.x, opcode.SMSGActivatetaxireply, []byte{0, 0, 0, 0}, cloneFlightpathScope(p.scope))
	p.refresh()
	activationPolicyNoEffect(t, p.step())
	p.qualifySpline()
	until("buddy-world.complete_spline")
	p.ground()
	until("buddy-world.retire_spline_completion")
	until("buddy-actions.query_flightpath")
	p.arriveMenu(2)
	until("buddy-actions.settle_flightpath")
	input, err := json.Marshal(p.f.input)
	if err != nil {
		t.Fatal(err)
	}
	out := p.step()
	parity(input, out)
	if out["done"] != true || out["action"] != nil {
		t.Fatal("qualified final destination did not complete under defaults", out)
	}
}
