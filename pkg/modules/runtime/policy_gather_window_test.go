package modrt

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	buddyloot "github.com/agent-wow/agent-wow/modules/buddy-loot"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestPolicyGatherWindowUsesRealNativeGameObjectMoneySlotAndRelease(t *testing.T) {
	registry, err := moddisc.Load(filepath.Join("..", "..", "..", "modules"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(registry, nil, nil)
	m.started, m.dir = true, "/private/corpse-body"
	m.session.Identity = modsession.Identity{CharacterGUID: 6}
	for _, i := range m.instances {
		i.healthy = true
	}
	const source = uint64(0xf110000000000000 | uint64(161557)<<24 | 42)
	var writes []uint32
	var origins []modsession.WriteOrigin
	m.session.SendPacket = func(ctx context.Context, code uint32, p []byte) error {
		valid := code == opcode.CMSGCastSpell && len(writes) == 0 || code == opcode.CMSGLootMoney && len(p) == 0 ||
			code == opcode.CMSGAutostoreLootItem && len(p) == 1 && p[0] == 2 ||
			code == opcode.CMSGLootRelease && len(p) == 8 && binary.LittleEndian.Uint64(p) == source
		if !valid {
			return fmt.Errorf("foreign gather packet %x %v", code, p)
		}
		writes = append(writes, code)
		origins = append(origins, modsession.WriteOriginFromContext(ctx))
		return nil
	}
	w := &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{Guid: 6, Present: true, AliveKnown: true, Alive: true,
		HealthKnown: true, Health: 100, MaxHealth: 100, MapKnown: true, PositionKnown: true, PositionRevision: 1,
		UnitFlagsKnown: true, MovementFlagsKnown: true, SpeedsKnown: true, RunSpeed: 7}, InventoryKnown: true, InventoryRevision: 1,
		VisitRevision: 1, SelfQuestLogKnown: true, SelfQuestLogRevision: 1, QuestObjects: []*worldv1.GameObjectState{{Guid: source, Entry: 161557,
			Present: true, MapKnown: true, PositionKnown: true, PositionRevision: 1, X: 2, TypeKnown: true, GameObjectType: 3, FlagsKnown: true}}}
	for i := range 25 {
		w.SelfQuestLog = append(w.SelfQuestLog, &worldv1.QuestLogEntry{Slot: uint32(i)})
	}
	w.SelfQuestLog[0].QuestId = 3904
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
	callback := &corpseNativeCallback{manager: m, world: w, actions: &actionsv1.State{SessionId: "corpse-body", SelfGuid: 6,
		Position: &actionsv1.ClientPosition{Known: true, MapKnown: true, Revision: 1}}}
	native := buddyloot.NewAutomaticService(callback, 6, "corpse-body")
	server := grpc.NewServer()
	lootv1.RegisterBuddyLootServer(server, native)
	listener := bufconn.Listen(1 << 20)
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///native-gather", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	m.instances["buddy-loot"].conn = conn
	defer func() { m.cancel(); conn.Close(); server.Stop(); listener.Close() }()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "behaviors", "corpse-loot.lua"))
	if err != nil {
		t.Fatal(err)
	}
	policy := "local loot=(function()\n" + string(raw) + "\nend)()\nreturn {on_event=loot.on_event,export_state=loot.export_state,import_state=loot.import_state,decide=function(f)return loot.decide(f,{selfGuid='6',visitId='1'},true,f.input.urgent)end}"
	host, err := m.StartPolicyWithOptions(context.Background(), policy, behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	step := func(urgent bool) *behavior.ActionReceipt {
		t.Helper()
		encoded, e := m.InvokeJSON(context.Background(), "buddy-loot.observe_corpses", nil)
		if e != nil {
			t.Fatal(e)
		}
		var observation map[string]any
		if e = json.Unmarshal(encoded, &observation); e != nil {
			t.Fatal(e)
		}
		seq, e := host.Enqueue(behavior.Event{Kind: "corpse_loot_observation", Value: observation})
		if e != nil {
			t.Fatal(e)
		}
		n := strconv.FormatUint(seq, 10)
		_, receipt, e := host.Step(context.Background(), map[string]any{"urgent": urgent, "observationSequence": n,
			"observationSequences": map[string]any{"corpse_loot": n}}, nil)
		if e != nil {
			t.Fatal(e)
		}
		return receipt
	}
	// Native ticks no longer select a target or recipe. The actual accepted
	// Lua module chooses the observed object and routes the exact opening RPC.
	native.Enable(context.Background(), &lootv1.EnableRequest{Enabled: true})
	native.Tick(context.Background())
	if len(writes) != 0 {
		t.Fatal("native selected a gather target before Lua", writes)
	}
	if r := step(false); r == nil || r.Outcome != behavior.ActionSettledUnverified ||
		len(writes) != 1 || writes[0] != opcode.CMSGCastSpell || r.Payload["source_guid"] != strconv.FormatUint(source, 10) {
		t.Fatal("actual Lua-selected gather opening did not run", r, writes)
	}
	window := binary.LittleEndian.AppendUint64(nil, source)
	window = append(window, 6)
	window = binary.LittleEndian.AppendUint32(window, 5)
	window = append(window, 2)
	for _, item := range []struct {
		slot byte
		id   uint32
	}{{1, 42}, {2, 11119}} {
		row := make([]byte, 22)
		row[0] = item.slot
		binary.LittleEndian.PutUint32(row[1:], item.id)
		binary.LittleEndian.PutUint32(row[5:], 1)
		row[21] = 4
		window = append(window, row...)
	}
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLootResponse, Payload: window})
	native.Tick(context.Background())
	if len(writes) != 1 {
		t.Fatal("native selected window contents", writes)
	}
	callback.reject = true
	if r := step(false); r == nil || r.Outcome != behavior.ActionNotDispatched || len(writes) != 1 {
		t.Fatal("definite callback refusal consumed the money mutation", r, writes)
	}
	callback.reject = false
	if r := step(false); r == nil || len(writes) != 2 || writes[1] != opcode.CMSGLootMoney {
		t.Fatal("same Lua could not route game-object money", r, writes)
	}
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLootClearMoney})
	if r := step(false); r == nil || len(writes) != 3 || writes[2] != opcode.CMSGAutostoreLootItem {
		t.Fatal("same Lua did not choose bound quest item", r, writes)
	}
	if _, err = host.Reload(context.Background(), policy, host.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if r := step(false); r != nil || len(writes) != 3 || host.vm.Status().Pending != 0 {
		t.Fatal("reload replayed or monopolized support", r, writes)
	}
	push := make([]byte, 45)
	binary.LittleEndian.PutUint64(push, 6)
	binary.LittleEndian.PutUint32(push[25:], 11119)
	binary.LittleEndian.PutUint32(push[37:], 1)
	binary.LittleEndian.PutUint32(push[41:], 1)
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGItemPushResult, Payload: push})
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLootRemoved, Payload: []byte{2}})
	if r := step(false); r == nil || len(writes) != 4 || writes[3] != opcode.CMSGLootRelease || callback.actions.InteractionOwner != "" {
		t.Fatal("collected window did not yield its actual source", r, writes, callback.actions)
	}
	for _, origin := range origins {
		if origin.Writer != "lua" || origin.Module != "buddy-loot" || origin.ActionID == "" {
			t.Fatal("opening/window packet lost Lua attribution", origin)
		}
	}
	before, _ := native.GetState(context.Background(), &emptypb.Empty{})
	if before.LastGather.Status == lootv1.GatherStatus_GATHER_CONFIRMED {
		t.Fatal("write ACK invented release", before)
	}
	closed := append(binary.LittleEndian.AppendUint64(nil, source), 1)
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLootReleaseResponse, Payload: closed})
	native.Tick(context.Background())
	after, _ := native.GetState(context.Background(), &emptypb.Empty{})
	if after.LastGather.Status != lootv1.GatherStatus_GATHER_CONFIRMED || !after.LastGather.ReleaseObserved {
		t.Fatal("actual item/slot/source release did not settle gather", after)
	}
}
