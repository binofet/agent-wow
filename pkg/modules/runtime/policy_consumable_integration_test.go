package modrt

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	buddyinventory "github.com/agent-wow/agent-wow/modules/buddy-inventory"
	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type consumableNativeCallback struct {
	manager *Manager
	native  *buddyinventory.Service
	world   *worldv1.SnapshotResponse
	actions *actionsv1.State
	auras   *actionsv1.PlayerAuras
}

func consumableItemQueryFixture(entry uint32) []byte {
	p := []byte{}
	appendWord := func(v uint32) { p = binary.LittleEndian.AppendUint32(p, v) }
	for _, v := range []uint32{entry, 0, 5, 0} {
		appendWord(v)
	}
	p = append(p, []byte("Fixture\x00\x00\x00\x00")...)
	var header [22]uint32
	header[7], header[8], header[19] = ^uint32(0), ^uint32(0), 20
	for _, v := range header {
		appendWord(v)
	}
	for range (8 + 24 + 28 + 12) / 4 {
		appendWord(0)
	}
	for i := 0; i < 5; i++ {
		id := uint32(0)
		if i == 0 {
			id = 430
		}
		for _, v := range []uint32{id, 0, 0, 0, 0, 0} {
			appendWord(v)
		}
	}
	appendWord(0)
	p = append(p, 0)
	for range 29 {
		appendWord(0)
	}
	return p
}

func (f *consumableNativeCallback) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if r.Opcode == opcode.CMSGItemQuerySingle {
		return f.native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGItemQuerySingleResponse, Payload: consumableItemQueryFixture(binary.LittleEndian.Uint32(r.Payload))})
	}
	// This is the real callback's gRPC boundary: outgoing token metadata is
	// delivered as incoming metadata, then the production exact matcher runs.
	if md, ok := metadata.FromOutgoingContext(ctx); ok {
		ctx = metadata.NewIncomingContext(ctx, md)
	}
	err := f.manager.modulePacketWriter("consumable-body", consumableInventoryModule)(ctx, r.Opcode, r.Payload)
	if err == nil {
		f.world.InventoryBags[0].Slots[0].StackCount--
		f.world.InventoryRevision++
		f.auras = &actionsv1.PlayerAuras{TargetGuid: 6, Complete: true, Revision: 2, Auras: []*actionsv1.ObservedAura{{SpellId: 430, CasterGuid: 6, Positive: true, Revision: 2}}}
	}
	return &emptypb.Empty{}, err
}

func (f *consumableNativeCallback) InvokeModule(_ context.Context, r *modv1.InvokeModuleRequest, _ ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	var result proto.Message
	switch r.Module + "." + r.Method {
	case "buddy-world.snapshot":
		result = f.world
	case "buddy-actions.status":
		result = f.actions
	case "buddy-actions.spell_toolkit":
		result = &actionsv1.SpellToolkitState{SessionId: "consumable-body", SelfGuid: 6, PlayerAuras: []*actionsv1.PlayerAuras{f.auras}}
	case "buddy-trade.state":
		result = &tradev1.State{SessionId: "consumable-body", Phase: "IDLE"}
	case "buddy-loot.state":
		result = &lootv1.State{SessionId: "consumable-body"}
	case "buddy-professions.state":
		result = &professionsv1.State{SessionId: "consumable-body", SelfGuid: 6}
	case "buddy-actions.acquire_interaction":
		f.actions.InteractionOwner, f.actions.InteractionLeaseRemainingMs = "trade", 3000
		result = &actionsv1.InteractionLease{Token: "native-consumable-token", State: f.actions}
	case "buddy-actions.release_interaction":
		f.actions.InteractionOwner = ""
		result = f.actions
	default:
		return nil, fmt.Errorf("unexpected native dependency %s.%s", r.Module, r.Method)
	}
	packed, err := anypb.New(result)
	return &modv1.InvokeModuleResponse{Result: packed}, err
}

func TestConsumablePersistentLuaExecutesActualInventoryPrimitiveAndObservesItsEffects(t *testing.T) {
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
	var writes []modsession.WriteOrigin
	m.session.SendPacket = func(ctx context.Context, code uint32, p []byte) error {
		if code != opcode.CMSGUseItem || len(p) != 24 {
			t.Fatal("unrelated game write", code, p)
		}
		writes = append(writes, modsession.WriteOriginFromContext(ctx))
		return nil
	}
	w := &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{Guid: 6, Present: true, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, MaxHealth: 100,
		MapKnown: true, PositionKnown: true, UnitFlagsKnown: true, MovementFlagsKnown: true, LevelKnown: true, Level: 30, PowerKnown: true, Power: 60, MaxPower: 100}, InventoryKnown: true, InventoryRevision: 1, SelfCopperKnown: true, SelfCopperRevision: 1}
	for b := uint32(0); b < 5; b++ {
		bag := &worldv1.InventoryBag{Bag: b, SlotKnown: true, ContentsKnown: true}
		if b == 0 {
			bag.SlotCount = 16
			for i := uint32(0); i < 16; i++ {
				bag.Slots = append(bag.Slots, &worldv1.BackpackSlot{Slot: i})
			}
		}
		w.InventoryBags = append(w.InventoryBags, bag)
	}
	w.InventoryBags[0].Slots[0] = &worldv1.BackpackSlot{ItemGuid: 0x4000000000000001, Entry: 60001, StackCount: 3, MetadataKnown: true}
	callback := &consumableNativeCallback{manager: m, world: w, actions: &actionsv1.State{SessionId: "consumable-body", SelfGuid: 6, Following: true}, auras: &actionsv1.PlayerAuras{TargetGuid: 6, Complete: true, Revision: 1}}
	callback.native = buddyinventory.NewService(callback, "consumable-body", 6)
	server := grpc.NewServer()
	inventoryv1.RegisterBuddyInventoryServer(server, callback.native)
	ready := health.NewServer()
	ready.SetServingStatus(modcb.PolicyConsumableService, grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(server, ready)
	listener := bufconn.Listen(1 << 20)
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///native-consumable", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	m.instances[consumableInventoryModule].conn = conn
	defer func() { m.cancel(); conn.Close(); server.Stop(); listener.Close() }()
	module, err := os.ReadFile(filepath.Join("..", "..", "..", "behaviors", "consumables.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := "local m=(function()\n" + string(module) + "\nend)()\n" + `return {on_event=m.on_event,export_state=m.export_state,import_state=m.import_state,decide=function(f)
local c=m.choose(f.input.inventory,f.input.encounter);if not c then return {}end
return {action={capability="consume",sessionId=f.session_id,selfGuid="6",kind=c.kind,source=c.source,spellId=c.spellId,expectedInventoryRevision=c.inventoryRevision,requestId="native_integration"}}end}`
	host, err := m.StartPolicyWithOptions(context.Background(), source, behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	read := func() map[string]any {
		t.Helper()
		state, err := callback.native.GetState(context.Background(), &emptypb.Empty{})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := protojson.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err = json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	obs := map[string]any{"sessionId": "consumable-body", "selfGuid": "6", "players": map[string]any{"6": map[string]any{"present": true, "aliveKnown": true, "alive": true, "powerKnown": true, "powerType": float64(0), "power": float64(60), "maxPower": float64(100)}}}
	_, receipt, err := host.Step(context.Background(), map[string]any{"inventory": read(), "encounter": obs}, nil)
	if err != nil || receipt == nil || receipt.Outcome != behavior.ActionSettledUnverified || len(writes) != 1 || writes[0].Writer != "lua" || writes[0].ActionID == "" || writes[0].Module != consumableInventoryModule {
		t.Fatal("persistent Lua did not execute its exact native item use", receipt, writes, err)
	}
	view := read()
	effect := view["lastConsumableUse"].(map[string]any)
	if effect["phase"] != "confirmed_consumption_and_aura" || effect["inventoryConfirmed"] != true || effect["auraObserved"] != true {
		t.Fatal("actual native effects were lost", effect)
	}
	if _, err = host.Reload(context.Background(), strings.Replace(source, "drink_below_percent=70", "drink_below_percent=40", 1), host.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	out, _, err := host.Step(context.Background(), map[string]any{"inventory": view, "encounter": obs}, nil)
	if err != nil || out["action"] != nil || len(writes) != 1 || host.vm.Status().SessionID != "consumable-body" || host.vm.Status().Pending != 0 {
		t.Fatal("same-session tuning or settled item lane failed", out, err, host.vm.Status())
	}
}
