package buddyactions_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/modules/runner"
	"github.com/agent-wow/agent-wow/pkg/modules/runtime"
	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
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
	if methods["buddy-actions.healing_options"].Effect != moddisc.EffectRead {
		t.Fatal("healing_options must be published as a read")
	}
	body, err := m.InvokeJSON(ctx, "buddy-actions.healing_options", nil)
	var healingView struct {
		SessionID string                `json:"sessionId"`
		Caster    struct{ Guid string } `json:"caster"`
		HostOwned bool                  `json:"hostOwned"`
	}
	if err != nil || json.Unmarshal(body, &healingView) != nil || healingView.SessionID != catalog.SessionID || healingView.Caster.Guid != "11" || healingView.HostOwned {
		t.Fatalf("healing_options returned wrong fixture identity or default authority: %s (%v)", body, err)
	}

	t.Run("encounter_observation", func(t *testing.T) {
		const alias = "buddy-actions.encounter_observation"
		if method := methods[alias]; method.Effect != moddisc.EffectRead ||
			method.Path != "/warcraftbuddy.actions.v1.BuddyActions/EncounterObservation" {
			t.Fatalf("wrong encounter catalog entry: %+v", method)
		}
		type observation struct {
			SessionID string  `json:"sessionId"`
			SelfGUID  string  `json:"selfGuid"`
			VisitID   string  `json:"visitId"`
			MapID     *uint32 `json:"mapId"`
			HostOwned *bool   `json:"hostOwned"`
			Reason    string  `json:"reason"`
		}
		read := func() observation {
			t.Helper()
			raw, err := m.InvokeJSON(ctx, alias, nil)
			var out observation
			if err != nil || json.Unmarshal(raw, &out) != nil || out.SessionID != catalog.SessionID ||
				out.SelfGUID != "11" || out.HostOwned == nil || *out.HostOwned {
				t.Fatalf("invalid encounter observation identity or custody: %s (%v)", raw, err)
			}
			return out
		}
		waitFor := func(visit, reason string) {
			t.Helper()
			deadline := time.Now().Add(2 * time.Second)
			for {
				out := read()
				mapMatches := (visit == "" && out.MapID == nil) || (visit != "" && out.MapID != nil && *out.MapID == 0)
				if out.VisitID == visit && out.Reason == reason && mapMatches {
					return
				}
				if !time.Now().Before(deadline) {
					t.Fatalf("encounter observation did not reach visit %q reason %q: %+v", visit, reason, out)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		waitFor("", "world_visit_unbound")
		if err := m.Publish(opcode.SMSGLoginVerifyWorld, make([]byte, 20)); err != nil {
			t.Fatal(err)
		}
		waitFor("1", "")
		if err := m.Publish(opcode.SMSGTransferPending, make([]byte, 4)); err != nil {
			t.Fatal(err)
		}
		waitFor("", "world_visit_unbound")
		if err := m.Publish(opcode.SMSGNewWorld, make([]byte, 20)); err != nil {
			t.Fatal(err)
		}
		waitFor("2", "")
		err := m.StartObservedPolicy(ctx, "", "encounter="+alias)
		if err == nil || !strings.Contains(err.Error(), "requires the session encounter lease") {
			t.Fatalf("encounter dispatch started before native custody was transferred: %v", err)
		}
	})

	t.Run("follow_formation", func(t *testing.T) {
		const alias = "buddy-actions.set_follow_formation"
		method := methods[alias]
		if method.Effect != moddisc.EffectMutation ||
			method.Path != "/warcraftbuddy.actions.v1.BuddyActions/SetFollowFormation" {
			t.Fatalf("incorrect formation goal route: %+v", method)
		}
		params := json.RawMessage(fmt.Sprintf(`{"sessionId":%q,"selfGuid":"11","visitId":"2","ownerGuid":"3","ownerPositionRevision":"1","followRevision":"1","x":1,"startDistance":2,"stopDistance":1,"ready":true}`, catalog.SessionID))
		_, err := m.InvokeJSON(ctx, alias, params)
		var call *modrt.CallError
		if !errors.As(err, &call) || call.Code != -32000 || status.Code(call.Err) != codes.PermissionDenied ||
			status.Convert(call.Err).Message() != "current attached encounter host required" {
			t.Fatalf("formation goal route did not preserve its private host boundary: %v", err)
		}
	})

	const service = "/warcraftbuddy.actions.v1.BuddyActions/"
	for _, tc := range []struct {
		alias, method, params, want string
	}{
		{"create_item", "CreateItem", `"selfGuid":"11","spellId":5505`, "exact learned item-creation rank required"},
		{"cast_self", "CastSelf", `"selfGuid":"11","spellId":12051`, "learned spell with a supported self target required"},
		{"cast_heal", "CastHeal", `"selfGuid":"99","targetGuid":"11","spellId":2050`, "current owned body required"},
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
