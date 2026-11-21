package modrt

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

type consumableInputPeer struct {
	inventoryv1.UnimplementedBuddyInventoryServer
	sid    string
	direct atomic.Int32
}

func (p *consumableInputPeer) GetState(context.Context, *emptypb.Empty) (*inventoryv1.State, error) {
	return &inventoryv1.State{SessionId: p.sid, SelfGuid: 6}, nil
}
func (p *consumableInputPeer) Drink(context.Context, *inventoryv1.DrinkRequest) (*inventoryv1.DrinkReceipt, error) {
	p.direct.Add(1)
	return &inventoryv1.DrinkReceipt{}, nil
}
func (p *consumableInputPeer) EatBuffFood(context.Context, *inventoryv1.EatBuffFoodRequest) (*inventoryv1.EatBuffFoodReceipt, error) {
	p.direct.Add(1)
	return &inventoryv1.EatBuffFoodReceipt{}, nil
}

func consumableInputManager(t *testing.T) (*Manager, *consumableInputPeer) {
	t.Helper()
	registry, err := moddisc.Load(filepath.Join("..", "..", "..", "modules"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(registry, nil, nil)
	m.started, m.dir = true, "/private/consumable-body"
	m.session.Identity = modsession.Identity{CharacterGUID: 6}
	for _, instance := range m.instances {
		instance.healthy = true
	}
	peer := &consumableInputPeer{sid: "consumable-body"}
	listener := bufconn.Listen(64 << 10)
	server := grpc.NewServer()
	inventoryv1.RegisterBuddyInventoryServer(server, peer)
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///consumable-input", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	m.instances[consumableInventoryModule].conn = conn
	t.Cleanup(func() { m.cancel(); conn.Close(); server.Stop(); listener.Close() })
	return m, peer
}

func TestLegacyConsumablesQueueCurrentLuaWithoutClaimingItemEffect(t *testing.T) {
	m, peer := consumableInputManager(t)
	source := `local s={};return {on_event=function(e)if e.kind=="consumable_request" then s.kind=e.value.kind end return true end,
decide=function(f)return {kind=s.kind,version="first"}end,export_state=function()return {version=1,data=s}end,import_state=function(c)s=c.data;return true end}`
	host, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	if _, err = host.Reload(context.Background(), strings.Replace(source, `version="first"`, `version="edited"`, 1), host.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"drink", "eat_buff_food"} {
		raw, err := m.InvokeJSON(context.Background(), "buddy-inventory."+kind, json.RawMessage(`{"sessionId":"consumable-body","selfGuid":"6"}`))
		if err != nil {
			t.Fatal(err)
		}
		var ack map[string]any
		if err = json.Unmarshal(raw, &ack); err != nil {
			t.Fatal(err)
		}
		if ack["transportWritten"] == true || !strings.HasPrefix(ack["reason"].(string), "lua_consumable_request_queued:") || peer.direct.Load() != 0 {
			t.Fatal("queued input claimed item effect or called native selector", ack)
		}
		out, _, err := host.Step(context.Background(), nil, nil)
		if err != nil || out["kind"] != kind || out["version"] != "edited" {
			t.Fatal("current editable Lua did not receive request", out, err)
		}
	}
}

func TestLegacyConsumablesRejectAbsentOrStaleAttachedBody(t *testing.T) {
	m, peer := consumableInputManager(t)
	params := json.RawMessage(`{"sessionId":"consumable-body","selfGuid":"6"}`)
	if _, err := m.InvokeJSON(context.Background(), "buddy-inventory.drink", params); err == nil {
		t.Fatal("absent Lua used a native item selector")
	}
	host, err := m.StartPolicy(context.Background(), `return {on_event=function(e)return true end,decide=function(f)return {}end,export_state=function()return {version=1,data={}}end,import_state=function(c)return true end}`)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	m.dir = "/private/new-consumable-body"
	peer.sid = "new-consumable-body"
	if _, err = m.InvokeJSON(context.Background(), "buddy-inventory.drink", json.RawMessage(`{"sessionId":"new-consumable-body","selfGuid":"6"}`)); err == nil || host.vm.Status().Buffered != 0 || peer.direct.Load() != 0 {
		t.Fatal("stale same-GUID VM accepted new session input", err)
	}
}

func TestConsumablePolicyCallbackPermitsOnlyExactSelectedItemPacket(t *testing.T) {
	item := &inventoryv1.ItemRef{ItemGuid: 0x4000000000000001, Bag: 0, Slot: 3, Entry: 60001, StackCount: 2}
	op := &policySpell{spell: 430, consumable: &policyConsumable{source: item}}
	p := make([]byte, 24)
	p[0], p[1] = 255, 26
	binary.LittleEndian.PutUint32(p[3:], 430)
	binary.LittleEndian.PutUint64(p[7:], item.ItemGuid)
	if !op.matchesActionPacket(opcode.CMSGUseItem, p) {
		t.Fatal("exact normal item-use packet denied")
	}
	for _, index := range []int{0, 1, 3, 7, 15, 19, 20} {
		changed := append([]byte(nil), p...)
		changed[index]++
		if op.matchesActionPacket(opcode.CMSGUseItem, changed) {
			t.Fatal("foreign item, target or flags admitted", index)
		}
	}
	if op.matchesActionPacket(opcode.CMSGCastSpell, p) || op.matchesActionPacket(opcode.CMSGUseItem, append(p, 0)) {
		t.Fatal("unrelated or trailing packet bytes admitted")
	}
}
