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
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

type resurrectionInputPeer struct {
	actionsv1.UnimplementedBuddyActionsServer
	sid    string
	body   uint64
	direct atomic.Int32
	spell  atomic.Uint32
}

func (p *resurrectionInputPeer) Status(context.Context, *emptypb.Empty) (*actionsv1.State, error) {
	return &actionsv1.State{SessionId: p.sid, SelfGuid: p.body}, nil
}
func (p *resurrectionInputPeer) ResurrectOnce(_ context.Context, r *actionsv1.ResurrectOnceRequest) (*actionsv1.State, error) {
	p.direct.Add(1)
	p.spell.Store(r.SpellId)
	return &actionsv1.State{SessionId: p.sid, SelfGuid: p.body, Reason: "exact_native_request"}, nil
}
func resurrectionInputManager(t *testing.T) (*Manager, *resurrectionInputPeer) {
	t.Helper()
	m := New(packagedPolicySpellRegistry(t), nil, nil)
	m.started, m.dir = true, "/private/aw-priest"
	m.session.Identity = modsession.Identity{CharacterGUID: 22}
	for _, inst := range m.instances {
		inst.healthy = true
	}
	peer := &resurrectionInputPeer{sid: "aw-priest", body: 22}
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	actionsv1.RegisterBuddyActionsServer(server, peer)
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///res-input", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	m.instances["buddy-actions"].conn = conn
	t.Cleanup(func() { m.cancel(); conn.Close(); server.Stop(); listener.Close() })
	return m, peer
}
func TestLegacyResurrectionRankChoiceQueuesIntoCurrentSameBodyLua(t *testing.T) {
	m, peer := resurrectionInputManager(t)
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "behaviors", "support.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := "local support=(function()\n" + string(script) + "\nend)()\n" + `return {on_event=support.on_event,export_state=support.export_state,import_state=support.import_state,
 decide=function(frame) support.on_event({kind="encounter_observation",value=frame.input});return {action=support.resurrect(frame.input,frame)}end}`
	host, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	input := map[string]any{"sessionId": "aw-priest", "selfGuid": "22", "visitId": "1", "hostOwned": true,
		"players": map[string]any{"22": map[string]any{"present": true, "aliveKnown": true, "alive": true}},
		"party":   map[string]any{"group": map[string]any{"leaderGuid": "7"}},
		"spells":  map[string]any{"2006": map[string]any{"rank": float64(1)}, "2010": map[string]any{"rank": float64(2)}},
		"support": map[string]any{"resurrections": []any{
			map[string]any{"patientGuid": "7", "castTargetGuid": "7", "spellId": float64(2010), "ready": true},
			map[string]any{"patientGuid": "9", "castTargetGuid": "9", "spellId": float64(2006), "ready": true},
			map[string]any{"patientGuid": "9", "castTargetGuid": "9", "spellId": float64(2010), "ready": true}}}}
	// Change the actual rank chooser in this same running VM before the legacy
	// request. The native module must not choose a rank behind its back.
	edited := strings.Replace(source, "rank > best_rank", "rank < best_rank", 1)
	if edited == source {
		t.Fatal("test did not edit the actual Lua rank comparator")
	}
	if _, err = host.vm.Reload(context.Background(), edited, host.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	raw, err := m.InvokeJSON(context.Background(), "buddy-actions.resurrect_once", json.RawMessage(`{"sessionId":"aw-priest","selfGuid":"22","targetGuid":"9"}`))
	if err != nil {
		t.Fatal(err)
	}
	var ack map[string]any
	if err = json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ack["reason"].(string), "lua_resurrection_request_queued:") || peer.direct.Load() != 0 {
		t.Fatal("legacy rank was chosen natively", string(raw), peer.direct.Load())
	}
	out, _, err := host.Step(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	action, _ := out["action"].(map[string]any)
	if action["targetGuid"] != "9" || action["spellId"] != float64(2006) {
		t.Fatal("explicit patient/current edited rank chooser ignored", out)
	}
	// Explicit spell IDs remain an ordinary direct mechanical RPC.
	if _, err = m.InvokeJSON(context.Background(), "buddy-actions.resurrect_once", json.RawMessage(`{"sessionId":"aw-priest","selfGuid":"22","targetGuid":"9","spellId":2010}`)); err != nil || peer.direct.Load() != 1 || peer.spell.Load() != 2010 {
		t.Fatal("exact selected spell did not retain direct mechanics", err, peer.direct.Load(), peer.spell.Load())
	}
}
func TestLegacyResurrectionRejectsMissingPolicyMalformedAndForeignBody(t *testing.T) {
	m, peer := resurrectionInputManager(t)
	request := json.RawMessage(`{"sessionId":"aw-priest","selfGuid":"22","targetGuid":"9"}`)
	if _, err := m.InvokeJSON(context.Background(), "buddy-actions.resurrect_once", request); err == nil {
		t.Fatal("missing Lua restored a native rank selector")
	}
	host, err := m.StartPolicy(context.Background(), `return {on_event=function(e)return true end,decide=function(f)return {}end,export_state=function()return {version=1,data={}}end,import_state=function(c)return true end}`)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	for _, bad := range []string{`{"sessionId":"foreign","selfGuid":"22","targetGuid":"9"}`, `{"sessionId":"aw-priest","selfGuid":"23","targetGuid":"9"}`, `{"sessionId":"aw-priest","selfGuid":"22","targetGuid":"22"}`, `{"sessionId":"aw-priest","selfGuid":"22","targetGuid":"17379390962022744065"}`} {
		if _, err = m.InvokeJSON(context.Background(), "buddy-actions.resurrect_once", json.RawMessage(bad)); err == nil {
			t.Fatal("invalid request admitted", bad)
		}
	}
	peer.sid = "replaced"
	if _, err = m.InvokeJSON(context.Background(), "buddy-actions.resurrect_once", request); err == nil {
		t.Fatal("foreign module status accepted")
	}
	m.dir = "/private/replaced"
	request = json.RawMessage(`{"sessionId":"replaced","selfGuid":"22","targetGuid":"9"}`)
	if _, err = m.InvokeJSON(context.Background(), "buddy-actions.resurrect_once", request); err == nil {
		t.Fatal("same-GUID replacement authorized old VM")
	}
	if host.vm.Status().Buffered != 0 || peer.direct.Load() != 0 {
		t.Fatal("rejected input reached policy/native selector", host.vm.Status(), peer.direct.Load())
	}
}
