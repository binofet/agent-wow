package buddyactions_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/modules/runner"
	"github.com/agent-wow/agent-wow/pkg/modules/runtime"
	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type packagedRunner struct {
	binDir string
	procs  map[string]*exec.Cmd
}

func (r *packagedRunner) Up(ctx context.Context, launch modrunner.Launch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.Command(filepath.Join(r.binDir, launch.Manifest.Name))
	cmd.Dir = launch.SocketDir
	cmd.Env = []string{
		"AGENT_WOW_MODULE_SOCKET=" + filepath.Join(launch.SocketDir, "module.sock"),
		"AGENT_WOW_SESSION_SOCKET=" + filepath.Join(launch.SocketDir, "session.sock"),
		"BUDDY_INDEPENDENT=false",
	}
	for key, value := range launch.Environment {
		if key != "AGENT_WOW_MODULE_SOCKET" && key != "AGENT_WOW_SESSION_SOCKET" {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	r.procs[launch.Manifest.Name] = cmd
	return nil
}

func (r *packagedRunner) Down(_ context.Context, launch modrunner.Launch) error {
	cmd := r.procs[launch.Manifest.Name]
	if cmd == nil {
		return nil
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	// The fixture owns these children. Reap each after deliberately stopping it.
	var killed *exec.ExitError
	if err := cmd.Wait(); err != nil && !errors.As(err, &killed) {
		return err
	}
	return nil
}

func TestPackagedActionsRegistryRoutes(t *testing.T) {
	binDir := os.Getenv("BUDDY_ACTIONS_PACKAGED_BIN_DIR")
	if binDir == "" {
		t.Skip("set BUDDY_ACTIONS_PACKAGED_BIN_DIR to the directory of native buddy-actions, buddy-world, and buddy-party binaries")
	}
	binDir, err := filepath.Abs(binDir)
	if err != nil {
		t.Fatal(err)
	}
	moduleDir := t.TempDir()
	for _, name := range []string{"buddy-actions", "buddy-world", "buddy-party"} {
		if info, err := os.Stat(filepath.Join(binDir, name)); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("native executable %s is unavailable: %v", name, err)
		}
		dst := filepath.Join(moduleDir, name)
		if err := os.Mkdir(dst, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{"module.yaml", "module.pb", "compose.yaml"} {
			data, err := os.ReadFile(filepath.Join("..", name, file))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dst, file), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	registry, err := moddisc.Load(moduleDir)
	if err != nil {
		t.Fatal(err)
	}
	m := modrt.New(registry, &packagedRunner{binDir: binDir, procs: make(map[string]*exec.Cmd)}, nil)
	var writes atomic.Uint32
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
		if count := writes.Load(); count != 0 {
			t.Errorf("packaged registry probes attempted %d game packet writes", count)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = m.Start(ctx, modsession.Session{
		Identity: modsession.Identity{CharacterGUID: 11, CharacterName: "Fixture"},
		Clock:    func() uint32 { return 12345 },
		SendPacket: func(_ context.Context, opcode uint32, _ []byte) error {
			writes.Add(1)
			return fmt.Errorf("packaged registry fixture rejects game packet %#x", opcode)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := m.InvokeJSON(ctx, "session.describe", nil)
	if err != nil {
		t.Fatal(err)
	}
	var catalog modrt.SessionCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil || catalog.SessionID == "" || catalog.CharacterGUID != "11" ||
		catalog.Revision != registry.Catalog().Revision {
		t.Fatalf("packaged session catalog does not bind the fixture body and registry: %v", err)
	}
	methods := make(map[string]moddisc.CatalogMethod)
	for _, method := range catalog.Methods {
		methods[method.Name] = method
	}
	for _, alias := range []string{"status", "behavior", "spell_toolkit"} {
		full := "buddy-actions." + alias
		if methods[full].Effect != moddisc.EffectRead {
			t.Errorf("%s must be published as a read", full)
		}
		body, err := m.InvokeJSON(ctx, full, nil)
		if err != nil {
			t.Fatal(err)
		}
		var identity struct {
			SessionID string `json:"sessionId"`
			SelfGUID  string `json:"selfGuid"`
		}
		if err := json.Unmarshal(body, &identity); err != nil || identity.SessionID != catalog.SessionID || identity.SelfGUID != "11" {
			t.Fatalf("%s returned wrong fixture identity: %s (%v)", full, body, err)
		}
	}

	const service = "/warcraftbuddy.actions.v1.BuddyActions/"
	for _, tc := range []struct {
		alias, method, params, want string
	}{
		{"create_item", "CreateItem", `"selfGuid":"11","spellId":5505`, "exact learned item-creation rank required"},
		{"polymorph", "Polymorph", `"targetGuid":"17379390962022744065","spellId":118`, "exact learned crowd-control rank required"},
		{"sap", "Sap", `"targetGuid":"17379390962022744065","spellId":6770`, "exact learned crowd-control rank required"},
		{"resurrect_once", "ResurrectOnce", `"selfGuid":"11","targetGuid":"12"`, "current living caster observation required"},
		{"accept_resurrection", "AcceptResurrection", `"selfGuid":"11","casterGuid":"12"`, "current recipient session and pending offer from that caster required"},
	} {
		t.Run(tc.alias, func(t *testing.T) {
			full := "buddy-actions." + tc.alias
			method := methods[full]
			if method.Effect != moddisc.EffectMutation || method.Path != service+tc.method {
				t.Fatalf("incorrect published method: %+v", method)
			}
			params := json.RawMessage(fmt.Sprintf(`{"sessionId":%q,%s}`, catalog.SessionID, tc.params))
			_, err := m.InvokeJSON(ctx, full, params)
			var call *modrt.CallError
			if !errors.As(err, &call) {
				t.Fatalf("routed method must return a typed error: %v", err)
			}
			s := status.Convert(call.Err)
			if call.Code == -32601 || s.Code() == codes.Unimplemented {
				t.Fatalf("packaged method unavailable (registry code %d, module %s): %s", call.Code, s.Code(), s.Message())
			}
			if call.Code != -32000 || s.Code() != codes.FailedPrecondition || s.Message() != tc.want {
				t.Fatalf("expected exact benign precondition, got registry code %d, module %s: %s", call.Code, s.Code(), s.Message())
			}
		})
	}
	var missing *modrt.CallError
	if _, err := m.InvokeJSON(ctx, "buddy-actions.packaged_fixture_missing", nil); !errors.As(err, &missing) ||
		missing.Code != -32601 || status.Code(missing.Err) != codes.NotFound {
		t.Fatalf("missing alias did not return method not found: %v", err)
	}
}
