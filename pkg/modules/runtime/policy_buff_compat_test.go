package modrt

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func (p *healInputPeer) Buffs(_ context.Context, r *actionsv1.ToggleRequest) (*actionsv1.State, error) {
	p.direct.Add(1)
	return &actionsv1.State{SessionId: p.sid, SelfGuid: p.body, Buffing: r.Enabled, BuffReason: "lua_policy_owns_buff_choices"}, nil
}

func buffEnsureSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "behaviors", "buffs.lua"))
	if err != nil {
		t.Fatal(err)
	}
	return "local buffs=(function()\n" + string(raw) + "\nend)()\n" + `return {on_event=buffs.on_event,export_state=buffs.export_state,import_state=buffs.import_state,
 decide=function(frame)return {action=buffs.choose(frame.input,frame),requestPending=buffs.export_state().data.ensureRequested}end}`
}

func buffEnsureInput() map[string]any {
	return map[string]any{"sessionId": "aw-healer", "selfGuid": "22", "mapId": float64(0), "hostOwned": true, "spellbookKnown": true,
		"players": map[string]any{"22": map[string]any{"present": true, "aliveKnown": true, "alive": true, "mapKnown": true, "mapId": float64(0), "positionKnown": true,
			"combatKnown": true, "powerKnown": true, "powerType": float64(0), "power": float64(100), "maxPower": float64(100), "healthKnown": true, "health": float64(100), "maxHealth": float64(100)}},
		"spells": map[string]any{
			"1243": map[string]any{"known": true, "metadataKnown": true, "available": true, "ready": true, "capability": "buff", "targetRule": "self", "familyId": float64(1243), "rank": float64(1), "powerType": float64(0)},
			"1244": map[string]any{"known": true, "metadataKnown": true, "available": true, "ready": true, "capability": "buff", "targetRule": "self", "familyId": float64(1243), "rank": float64(2), "powerType": float64(0)}}}
}

func TestLegacyBuffEnsureReachesEditedCurrentLuaWithoutClaimingCast(t *testing.T) {
	m, peer := healInputManager(t)
	source := buffEnsureSource(t)
	host, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	raw, err := m.InvokeJSON(context.Background(), "buddy-actions.buffs", json.RawMessage(`{"enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var ack map[string]any
	if err = json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ack["reason"].(string), "lua_buff_request_queued:") || ack["buffing"] == true || peer.direct.Load() != 0 {
		t.Fatal("request claimed native toggle/cast success", string(raw), peer.direct.Load())
	}
	// Unknown spell/body facts cannot be interpreted in Python or the bridge.
	first, err := host.vm.Decide(context.Background(), map[string]any{})
	if err != nil || first.Output["action"] != nil || first.Output["requestPending"] != true {
		t.Fatal("request invented a buff without current observations", first, err)
	}
	edited := strings.Replace(source, "return (a.rank or 0)>(b.rank or 0)", "return (a.rank or 0)<(b.rank or 0)", 1)
	if edited == source {
		t.Fatal("actual shared comparator was not edited")
	}
	before := host.vm.Status()
	if _, err = host.Reload(context.Background(), edited, before.Revision); err != nil {
		t.Fatal(err)
	}
	out, err := host.vm.Decide(context.Background(), buffEnsureInput())
	if err != nil {
		t.Fatal(err)
	}
	action, _ := out.Output["action"].(map[string]any)
	if action["spellId"] != float64(1243) || out.Output["requestPending"] != nil || host.vm.Status().SessionID != before.SessionID || peer.direct.Load() != 0 {
		t.Fatal("ensure check bypassed edited Lua or became sticky", out.Output, peer.direct.Load())
	}
	// Clearing the native legacy status cannot introduce a Lua support off state.
	if _, err = m.InvokeJSON(context.Background(), "buddy-actions.buffs", json.RawMessage(`{"enabled":false}`)); err != nil || peer.direct.Load() != 1 {
		t.Fatal("legacy status clear did not retain its RPC", err)
	}
	out, err = host.vm.Decide(context.Background(), buffEnsureInput())
	if err != nil || out.Output["action"] == nil {
		t.Fatal("legacy disable turned off shared maintenance", out, err)
	}
}

func TestLegacyBuffEnsureRejectsAbsentOrReplacedCurrentPolicy(t *testing.T) {
	m, peer := healInputManager(t)
	request := json.RawMessage(`{"enabled":true}`)
	if _, err := m.InvokeJSON(context.Background(), "buddy-actions.buffs", request); err == nil {
		t.Fatal("new module invented a native buff selector without Lua")
	}
	host, err := m.StartPolicy(context.Background(), buffEnsureSource(t))
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	peer.sid = "replacement"
	if _, err = m.InvokeJSON(context.Background(), "buddy-actions.buffs", request); err == nil {
		t.Fatal("foreign native status admitted input")
	}
	m.dir = "/private/replacement"
	if _, err = m.InvokeJSON(context.Background(), "buddy-actions.buffs", request); err == nil {
		t.Fatal("same GUID authorized a stale VM")
	}
	if host.vm.Status().Buffered != 0 || peer.direct.Load() != 0 {
		t.Fatal("rejected request reached Lua or native toggle")
	}
}

func TestBuffCoreUpgradePreservesPinnedPreExactBuffDescriptor(t *testing.T) {
	m, peer := healInputManager(t)
	root := t.TempDir()
	for _, name := range []string{"buddy-actions", "buddy-party", "buddy-world"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{"module.yaml", "module.pb", "compose.yaml"} {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "modules", name, file))
			if err != nil {
				t.Fatal(err)
			}
			if name == "buddy-actions" && file == "module.yaml" {
				var lines []string
				for _, line := range strings.Split(string(raw), "\n") {
					if !strings.HasPrefix(line, "  cast_buff:") {
						lines = append(lines, line)
					}
				}
				raw = []byte(strings.Join(lines, "\n"))
			}
			if name == "buddy-actions" && file == "module.pb" {
				set := &descriptorpb.FileDescriptorSet{}
				if err = proto.Unmarshal(raw, set); err != nil {
					t.Fatal(err)
				}
				for _, descriptor := range set.File {
					for _, service := range descriptor.Service {
						var methods []*descriptorpb.MethodDescriptorProto
						for _, method := range service.Method {
							if method.GetName() != "CastBuff" {
								methods = append(methods, method)
							}
						}
						service.Method = methods
					}
				}
				raw, err = proto.Marshal(set)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err = os.WriteFile(filepath.Join(dir, file), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	registry, err := moddisc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	m.registry = registry
	m.instances["buddy-actions"].definition, _ = registry.Lookup("buddy-actions")
	for _, enabled := range []bool{true, false} {
		raw, _ := json.Marshal(map[string]any{"enabled": enabled})
		if _, err = m.InvokeJSON(context.Background(), "buddy-actions.buffs", raw); err != nil {
			t.Fatal("core-only update broke old pinned toggle", enabled, err)
		}
	}
	if peer.direct.Load() != 2 {
		t.Fatal("old descriptor was redirected away from its installed RPC", peer.direct.Load())
	}
}
