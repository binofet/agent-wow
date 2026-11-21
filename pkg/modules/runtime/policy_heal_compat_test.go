package modrt

import (
	"context"
	"encoding/json"
	"net"
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

type healInputPeer struct {
	actionsv1.UnimplementedBuddyActionsServer
	sid    string
	body   uint64
	direct atomic.Int32
}

func (p *healInputPeer) Status(context.Context, *emptypb.Empty) (*actionsv1.State, error) {
	return &actionsv1.State{SessionId: p.sid, SelfGuid: p.body}, nil
}
func (p *healInputPeer) HealOnce(context.Context, *emptypb.Empty) (*actionsv1.State, error) {
	p.direct.Add(1)
	return &actionsv1.State{}, nil
}

func healInputManager(t *testing.T) (*Manager, *healInputPeer) {
	t.Helper()
	m := New(packagedPolicySpellRegistry(t), nil, nil)
	m.started, m.dir = true, "/private/aw-healer"
	m.session.Identity = modsession.Identity{CharacterGUID: 22}
	for _, inst := range m.instances {
		inst.healthy = true
	}
	peer := &healInputPeer{sid: "aw-healer", body: 22}
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	actionsv1.RegisterBuddyActionsServer(server, peer)
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///heal-input", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	m.instances["buddy-actions"].conn = conn
	t.Cleanup(func() { m.cancel(); conn.Close(); server.Stop(); listener.Close() })
	return m, peer
}

func TestLegacyHealOnceQueuesIntoCurrentSameBodyLua(t *testing.T) {
	m, peer := healInputManager(t)
	source := `local s={requests=0};return {on_event=function(e)if e.kind=="healing_request" then s.requests=s.requests+1;s.source=e.value.source end return true end,
 decide=function(frame)return {requests=s.requests,source=s.source,version="first"}end,
 export_state=function()return {version=1,data=s}end,import_state=function(c)s=c.data;return true end}`
	host, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	// A code change is loaded into the existing VM/body before the legacy call.
	if _, err = host.vm.Reload(context.Background(), strings.Replace(source, `version="first"`, `version="edited"`, 1), host.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	raw, err := m.InvokeJSON(context.Background(), "buddy-actions.heal_once", nil)
	if err != nil {
		t.Fatal(err)
	}
	var ack map[string]any
	if err = json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ack["reason"].(string), "lua_healing_request_queued:") || peer.direct.Load() != 0 {
		t.Fatal("legacy request selected a native cast", string(raw), peer.direct.Load())
	}
	out, _, err := host.Step(context.Background(), nil, nil)
	if err != nil || out["requests"] != float64(1) || out["source"] != "heal_once" || out["version"] != "edited" {
		t.Fatal("current Lua did not receive ordered input", out, err)
	}
}

func TestLegacyHealOnceRejectsMissingPolicyAndForeignStatus(t *testing.T) {
	m, peer := healInputManager(t)
	if _, err := m.InvokeJSON(context.Background(), "buddy-actions.heal_once", nil); err == nil {
		t.Fatal("missing policy invented native healing")
	}
	host, err := m.StartPolicy(context.Background(), `return {on_event=function(e)return true end,decide=function(f)return {}end,export_state=function()return {version=1,data={}}end,import_state=function(s)return true end}`)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	peer.sid = "replaced"
	if _, err = m.InvokeJSON(context.Background(), "buddy-actions.heal_once", nil); err == nil {
		t.Fatal("foreign module status admitted input")
	}
	if host.vm.Status().Buffered != 0 || peer.direct.Load() != 0 {
		t.Fatal("rejected request reached policy or native selector")
	}
}
