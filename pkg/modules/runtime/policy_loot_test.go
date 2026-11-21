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
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type corpseNativeCallback struct {
	manager *Manager
	world   *worldv1.SnapshotResponse
	actions *actionsv1.State
	reject  bool
}

func (f *corpseNativeCallback) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if md, ok := metadata.FromOutgoingContext(ctx); ok {
		ctx = metadata.NewIncomingContext(ctx, md)
	}
	sid := "corpse-body"
	if f.reject {
		sid = "replaced-callback-session"
	}
	return &emptypb.Empty{}, f.manager.modulePacketWriter(sid, "buddy-loot")(ctx, r.Opcode, r.Payload)
}
func (f *corpseNativeCallback) InvokeModule(_ context.Context, r *modv1.InvokeModuleRequest, _ ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	var result proto.Message
	switch r.Module + "." + r.Method {
	case "buddy-world.snapshot":
		result = f.world
	case "buddy-actions.status":
		result = f.actions
	case "buddy-actions.acquire_interaction":
		f.actions.InteractionOwner, f.actions.InteractionLeaseRemainingMs = "loot", 15000
		result = &actionsv1.InteractionLease{Token: "exact-corpse-token", State: f.actions}
	case "buddy-actions.release_interaction":
		f.actions.InteractionOwner = ""
		result = f.actions
	default:
		return nil, fmt.Errorf("unexpected corpse dependency %s.%s", r.Module, r.Method)
	}
	a, err := anypb.New(result)
	return &modv1.InvokeModuleResponse{Result: a}, err
}

func TestPolicyCorpseLootUsesActualNativeOpenAndSourceReceiptWithoutBlockingDefense(t *testing.T) {
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
	var writes []uint32
	var origins []modsession.WriteOrigin
	const corpse = uint64(0xf130000001000001)
	m.session.SendPacket = func(ctx context.Context, code uint32, p []byte) error {
		valid := (code == opcode.CMSGLoot || code == opcode.CMSGLootRelease) && len(p) == 8 && binary.LittleEndian.Uint64(p) == corpse ||
			code == opcode.CMSGLootMoney && len(p) == 0 || code == opcode.CMSGAutostoreLootItem && len(p) == 1 && p[0] == 2
		if !valid {
			return fmt.Errorf("foreign loot write %x %v", code, p)
		}
		writes = append(writes, code)
		origins = append(origins, modsession.WriteOriginFromContext(ctx))
		return nil
	}
	w := &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{Guid: 6, Present: true, AliveKnown: true, Alive: true, MapKnown: true, PositionKnown: true, PositionRevision: 1, UnitFlagsKnown: true}, InventoryKnown: true, InventoryRevision: 1,
		LootableCorpses: []*worldv1.CreatureState{{Guid: corpse, Present: true, AliveKnown: true, HealthKnown: true, MapKnown: true, PositionKnown: true, PositionRevision: 1, X: 2, DynamicFlags: 1}}}
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
	callback := &corpseNativeCallback{manager: m, world: w, actions: &actionsv1.State{SessionId: "corpse-body", SelfGuid: 6, Position: &actionsv1.ClientPosition{Known: true, MapKnown: true, Revision: 1}}}
	native := buddyloot.NewAutomaticService(callback, 6, "corpse-body")
	server := grpc.NewServer()
	lootv1.RegisterBuddyLootServer(server, native)
	listener := bufconn.Listen(1 << 20)
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///native-corpse", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	m.instances["buddy-loot"].conn = conn
	defer func() { m.cancel(); conn.Close(); server.Stop(); listener.Close() }()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "behaviors", "corpse-loot.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := "local loot=(function()\n" + string(raw) + "\nend)()\nreturn {on_event=loot.on_event,export_state=loot.export_state,import_state=loot.import_state,decide=function(f)return loot.decide(f,{selfGuid='6',visitId='1'},true,f.input.urgent)end}"
	host, err := m.StartPolicyWithOptions(context.Background(), source, behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	step := func(urgent bool) (map[string]any, *behavior.ActionReceipt) {
		t.Helper()
		encoded, e := m.InvokeJSON(context.Background(), "buddy-loot.observe_corpses", nil)
		if e != nil {
			t.Fatal(e)
		}
		var o map[string]any
		if e = json.Unmarshal(encoded, &o); e != nil {
			t.Fatal(e)
		}
		seq, e := host.Enqueue(behavior.Event{Kind: "corpse_loot_observation", Value: o})
		if e != nil {
			t.Fatal(e)
		}
		n := strconv.FormatUint(seq, 10)
		out, r, e := host.Step(context.Background(), map[string]any{"urgent": urgent, "observationSequence": n, "observationSequences": map[string]any{"corpse_loot": n}}, nil)
		if e != nil {
			t.Fatal(e)
		}
		return out, r
	}
	callback.reject = true
	_, r := step(false)
	if r == nil || r.Outcome != behavior.ActionNotDispatched || len(writes) != 0 {
		t.Fatal("known core callback rejection invented a transport attempt", r, writes)
	}
	view, e := native.ObserveCorpses(context.Background(), &emptypb.Empty{})
	if e != nil || view.Operation.OpeningTransportAttempted || len(view.RetiredSources) != 0 {
		t.Fatal("known no-write poisoned native source receipt", view, e)
	}
	callback.reject = false
	_, r = step(false)
	if r == nil || r.Outcome != behavior.ActionSettledUnverified || len(writes) != 1 || origins[0].Writer != "lua" || origins[0].Module != "buddy-loot" || origins[0].ActionID == "" {
		t.Fatal("shared policy did not emit one attributed exact OPEN", r, writes, origins)
	}
	// A transport return is not a loot window, and cannot justify reopening.
	o, err := native.ObserveCorpses(context.Background(), &emptypb.Empty{})
	if err != nil || o.Operation.WindowObserved {
		t.Fatal(o, err)
	}
	callback.reject = true
	_, r = step(true)
	if r == nil || r.Outcome != behavior.ActionNotDispatched || len(writes) != 1 {
		t.Fatal("known no-write close consumed UI release", r, writes)
	}
	callback.reject = false
	_, r = step(true)
	if r == nil || len(writes) != 2 || writes[1] != opcode.CMSGLootRelease || callback.actions.InteractionOwner != "" {
		t.Fatal("incoming defense did not yield loot custody immediately", r, writes, callback.actions)
	}
	if _, err = host.Reload(context.Background(), source, host.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	out, r := step(true)
	if r != nil || out["action"] != nil || len(writes) != 2 || host.vm.Status().Pending != 0 {
		t.Fatal("unknown release became sticky core pending or replayed after reload", out, r, host.vm.Status())
	}
	// Only the source-bound server window/release establishes effects.
	closed := binary.LittleEndian.AppendUint64(nil, corpse)
	closed = append(closed, 1)
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLootReleaseResponse, Payload: closed})
	native.Tick(context.Background())
	o, _ = native.ObserveCorpses(context.Background(), &emptypb.Empty{})
	if !o.Operation.Terminal || !o.Operation.ReleaseObserved || o.Operation.WindowObserved {
		t.Fatal("release invented an unobserved opening window", o.Operation)
	}
	// A positively observed new source life permits a new window. Exercise the
	// same VM/native callback path for exact money and item selection, not a
	// mock receipt that assumes native Tick processed the offered window.
	dead := w.LootableCorpses[0]
	alive := proto.Clone(dead).(*worldv1.CreatureState)
	alive.Alive = true
	alive.Health = 1
	alive.MaxHealth = 1
	w.NearbyCreatures = []*worldv1.CreatureState{alive}
	w.RequestedCreature = alive
	w.LootableCorpses = nil
	native.Tick(context.Background())
	step(false)
	w.NearbyCreatures = nil
	w.RequestedCreature = dead
	w.LootableCorpses = []*worldv1.CreatureState{dead}
	step(false)
	window := binary.LittleEndian.AppendUint64(nil, corpse)
	window = append(window, 1)
	window = binary.LittleEndian.AppendUint32(window, 5)
	window = append(window, 1)
	row := make([]byte, 22)
	row[0] = 2
	binary.LittleEndian.PutUint32(row[1:], 42)
	binary.LittleEndian.PutUint32(row[5:], 1)
	window = append(window, row...)
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLootResponse, Payload: window})
	native.Tick(context.Background())
	if len(writes) != 3 {
		t.Fatal("native selected window contents", writes)
	}
	callback.reject = true
	_, r = step(false)
	if r == nil || r.Outcome != behavior.ActionNotDispatched || len(writes) != 3 {
		t.Fatal("known no-write money refusal was lost", r, writes)
	}
	callback.reject = false
	_, r = step(false)
	if r == nil || len(writes) != 4 || writes[3] != opcode.CMSGLootMoney {
		t.Fatal("Lua did not choose money after definite refusal", r, writes)
	}
	host.Reload(context.Background(), source, host.vm.Status().Revision)
	_, r = step(false)
	if r != nil || len(writes) != 4 || host.vm.Status().Pending != 0 {
		t.Fatal("reload replayed money or blocked support ledger", r, writes)
	}
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLootClearMoney})
	_, r = step(false)
	if r == nil || len(writes) != 5 || writes[4] != opcode.CMSGAutostoreLootItem {
		t.Fatal("actual clear did not advance Lua to exact slot", r, writes)
	}
	push := make([]byte, 45)
	binary.LittleEndian.PutUint64(push, 6)
	binary.LittleEndian.PutUint32(push[25:], 42)
	binary.LittleEndian.PutUint32(push[37:], 1)
	binary.LittleEndian.PutUint32(push[41:], 1)
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGItemPushResult, Payload: push})
	_, r = step(false)
	if r != nil {
		t.Fatal("push without removed slot advanced window", r)
	}
	native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLootRemoved, Payload: []byte{2}})
	_, r = step(false)
	if r == nil || len(writes) != 6 || writes[5] != opcode.CMSGLootRelease {
		t.Fatal("Lua did not release actually collected window", r, writes)
	}
}
