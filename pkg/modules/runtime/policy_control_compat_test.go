package modrt

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

type controlInputPeer struct {
	actionsv1.UnimplementedBuddyActionsServer
	sid    string
	direct atomic.Int32
}

func (p *controlInputPeer) Status(context.Context, *emptypb.Empty) (*actionsv1.State, error) {
	return &actionsv1.State{SessionId: p.sid, SelfGuid: 22}, nil
}
func (p *controlInputPeer) Polymorph(context.Context, *actionsv1.PolymorphRequest) (*actionsv1.State, error) {
	p.direct.Add(1)
	return &actionsv1.State{SessionId: p.sid, SelfGuid: 22, Reason: "exact_primitive_called"}, nil
}
func (p *controlInputPeer) Sap(context.Context, *actionsv1.SapRequest) (*actionsv1.State, error) {
	p.direct.Add(1)
	return &actionsv1.State{SessionId: p.sid, SelfGuid: 22, Reason: "exact_primitive_called"}, nil
}
func controlInputManager(t *testing.T) (*Manager, *controlInputPeer) {
	t.Helper()
	m := New(packagedPolicySpellRegistry(t), nil, nil)
	m.started, m.dir = true, "/private/aw-control"
	m.session.Identity = modsession.Identity{CharacterGUID: 22}
	for _, inst := range m.instances {
		inst.healthy = true
	}
	peer := &controlInputPeer{sid: "aw-control"}
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	actionsv1.RegisterBuddyActionsServer(server, peer)
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///control-input", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	m.instances[actionsModule].conn = conn
	t.Cleanup(func() { m.cancel(); conn.Close(); server.Stop(); listener.Close() })
	return m, peer
}
func controlInputSource(t *testing.T) string {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "behaviors", "encounter-commands.lua"))
	if err != nil {
		t.Fatal(err)
	}
	return "local commands=(function()\n" + string(script) + "\nend)()\n" + `return {on_event=commands.on_event,export_state=commands.export_state,import_state=commands.import_state,
 decide=function(frame)
  local obs=frame.input.obs
  commands.on_event({kind="encounter_observation",sequence=frame.input.observationSequence,value=obs})
  local action,status=commands.control_action(obs,frame,"2",false)
  return {action=action,status=status}
 end}`
}
func controlInputObservation() map[string]any {
	const target = "17379390962878382081"
	return map[string]any{"observationSequence": "1", "obs": map[string]any{
		"sessionId": "aw-control", "selfGuid": "22", "mapId": float64(0), "visitId": "1", "hostOwned": true,
		"players": map[string]any{"22": map[string]any{"present": true, "aliveKnown": true, "alive": true, "mapKnown": true, "mapId": float64(0), "positionKnown": true, "x": float64(0), "combatKnown": true}},
		"targets": map[string]any{target: map[string]any{"present": true, "aliveKnown": true, "alive": true, "mapKnown": true, "mapId": float64(0), "positionKnown": true, "x": float64(40), "unitFlagsKnown": true, "attackable": true}},
		"spells":  map[string]any{"118": map[string]any{"known": true, "capability": "polymorph", "ready": true, "available": true, "maxRange": float64(30)}}}}
}
func TestLegacyControlUsesExistingCurrentLuaLifecycleWithoutNativePrepare(t *testing.T) {
	m, peer := controlInputManager(t)
	source := controlInputSource(t)
	host, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	request := json.RawMessage(`{"sessionId":"aw-control","spellId":118,"targetGuid":"17379390962878382081","approach":true}`)
	raw, err := m.InvokeJSON(context.Background(), "buddy-actions.polymorph", request)
	if err != nil {
		t.Fatal(err)
	}
	var ack map[string]any
	if err = json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ack["reason"].(string), "lua_control_request_queued:") || peer.direct.Load() != 0 {
		t.Fatal("legacy preparation reached native controller", string(raw), peer.direct.Load())
	}
	input := controlInputObservation()
	first, err := host.vm.Decide(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	action, _ := first.Output["action"].(map[string]any)
	if action["capability"] != "approach_target" {
		t.Fatal(first.Output)
	}
	if _, err = host.vm.Reload(context.Background(), source+"\n-- same body while approaching", host.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	obs := input["obs"].(map[string]any)
	target := "17379390962878382081"
	obs["targets"].(map[string]any)[target].(map[string]any)["x"] = float64(20)
	obs["targetMovement"] = map[string]any{"targetGuid": target, "visitId": "1", "rangeYards": float64(30), "requireLos": true, "ready": true}
	input["observationSequence"] = "2"
	cast, err := host.vm.Decide(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	action, _ = cast.Output["action"].(map[string]any)
	if action["capability"] != "polymorph" || action["spellId"] != float64(118) {
		t.Fatal("current shared Lua did not choose exact CC", cast.Output)
	}
	actionID, err := host.vm.ReserveAction(cast)
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "aw-control", ActionID: actionID, Outcome: behavior.ActionSettledUnverified,
		Payload: map[string]any{"capability": "polymorph", "target_guid": target, "spell_id": float64(118), "phase": "outcome_unconfirmed_no_retry"}}})
	if err != nil {
		t.Fatal(err)
	}
	// The old prepare/commit retry is the SAME goal, not permission to erase an
	// unconfirmed cast's private no-replay state, including after a VM reload.
	if _, err = m.InvokeJSON(context.Background(), "buddy-actions.polymorph", request); err != nil {
		t.Fatal(err)
	}
	if _, err = host.vm.Reload(context.Background(), source+"\n-- preserve pending control", host.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	input["observationSequence"] = "3"
	again, err := host.vm.Decide(context.Background(), input)
	if err != nil || again.Output["action"] != nil || again.Output["status"].(map[string]any)["phase"] != "waiting_for_control_aura" {
		t.Fatal("duplicate legacy call replayed unknown control", again.Output, err)
	}
	// A reservation attributed by this VM bypasses the intent adapter and reaches
	// the exact native method. Public callers cannot set this private context.
	inst := m.instances[actionsModule]
	method, _ := inst.definition.RPC("polymorph")
	ctx := modsession.WithWriteOrigin(context.Background(), modsession.WriteOrigin{SessionID: "aw-control", Module: actionsModule, Writer: "lua", ActionID: "1"})
	if err = m.invoke(ctx, publicCall, "lua", actionsModule, inst, method, &actionsv1.PolymorphRequest{SessionId: "aw-control", SpellId: 118, TargetGuid: 17379390962878382081}, &actionsv1.State{}); err != nil || peer.direct.Load() != 1 {
		t.Fatal("exact VM action recursively queued instead of reaching native primitive", err, peer.direct.Load())
	}
}
func TestLegacyControlRejectsMissingPolicyAndForeignOrMalformedRequests(t *testing.T) {
	m, peer := controlInputManager(t)
	request := json.RawMessage(`{"sessionId":"aw-control","spellId":6770,"targetGuid":"17379390962878382081"}`)
	if _, err := m.InvokeJSON(context.Background(), "buddy-actions.sap", request); err == nil {
		t.Fatal("missing policy restored native preparation")
	}
	host, err := m.StartPolicy(context.Background(), controlInputSource(t))
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	for _, bad := range []string{`{"sessionId":"foreign","spellId":6770,"targetGuid":"17379390962878382081"}`, `{"sessionId":"aw-control","targetGuid":"17379390962878382081"}`, `{"sessionId":"aw-control","spellId":6770,"targetGuid":"3"}`, `{"sessionId":"aw-control","spellId":6770,"targetGuid":"17379390962878382081","approach":true,"cancel":true}`} {
		if _, err = m.InvokeJSON(context.Background(), "buddy-actions.sap", json.RawMessage(bad)); err == nil {
			t.Fatal("malformed/foreign intent admitted", bad)
		}
	}
	peer.sid = "replaced"
	if _, err = m.InvokeJSON(context.Background(), "buddy-actions.sap", request); err == nil {
		t.Fatal("foreign status admitted input")
	}
	m.dir = "/private/replaced"
	if _, err = m.InvokeJSON(context.Background(), "buddy-actions.sap", json.RawMessage(`{"sessionId":"replaced","spellId":6770,"targetGuid":"17379390962878382081"}`)); err == nil {
		t.Fatal("old VM accepted replacement body")
	}
	if host.vm.Status().Buffered != 0 || peer.direct.Load() != 0 {
		t.Fatal("rejected intent reached VM/native", host.vm.Status(), peer.direct.Load())
	}
}
