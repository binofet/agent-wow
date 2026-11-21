package modrt

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	inventoryv1 "github.com/agent-wow/agent-wow/modules/buddy-inventory/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"path/filepath"
)

type gearInputPeer struct {
	inventoryv1.UnimplementedBuddyInventoryServer
	mu     sync.Mutex
	state  *inventoryv1.State
	writes []*inventoryv1.EquipGearRequest
}

func (p *gearInputPeer) GetState(context.Context, *emptypb.Empty) (*inventoryv1.State, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return proto.Clone(p.state).(*inventoryv1.State), nil
}
func (p *gearInputPeer) EquipGear(_ context.Context, r *inventoryv1.EquipGearRequest) (*inventoryv1.State, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes = append(p.writes, proto.Clone(r).(*inventoryv1.EquipGearRequest))
	return proto.Clone(p.state).(*inventoryv1.State), nil
}
func gearInputManager(t *testing.T) (*Manager, *gearInputPeer) {
	t.Helper()
	registry, err := moddisc.Load(filepath.Join("..", "..", "..", "modules"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(registry, nil, nil)
	m.started, m.dir = true, "/private/gear-body"
	m.session.Identity = modsession.Identity{CharacterGUID: 6}
	for _, instance := range m.instances {
		instance.healthy = true
	}
	state := &inventoryv1.State{SessionId: "gear-body", SelfGuid: 6, Revision: 1, InventoryKnown: true, InventoryRevision: 2, EquipmentKnown: true, EquipmentRevision: 3,
		GearContext: &inventoryv1.GearContext{ClassKnown: true, ClassId: 4, Templates: map[string]*inventoryv1.GearTemplate{
			"100": {ItemId: 100, Available: true, ItemClass: 4, Subclass: 2, InventoryType: 7, Armor: 20, ArmorComparisonKnown: true},
			"99":  {ItemId: 99, Available: true, ItemClass: 4, Subclass: 2, InventoryType: 7, Armor: 10, ArmorComparisonKnown: true}}}}
	for slot := uint32(0); slot < 19; slot++ {
		state.Equipment = append(state.Equipment, &worldv1.EquipmentSlotState{Slot: slot, SlotKnown: true})
	}
	state.Equipment[6] = &worldv1.EquipmentSlotState{Slot: 6, SlotKnown: true, ItemGuid: 0x4000000000000002, Entry: 99, StackCount: 1, MetadataKnown: true, EnhancementsKnown: true}
	state.Items = []*inventoryv1.ItemEligibility{{Item: &inventoryv1.ItemRef{Slot: 2, ItemGuid: 0x4000000000000001, Entry: 100, StackCount: 1}, MetadataKnown: true, GearBlockedReason: "lua_gear_evaluation_required",
		GearFacts: &inventoryv1.GearItemFacts{EnhancementsKnown: true, HasEnhancements: true, Destinations: []*inventoryv1.GearDestination{{EquipmentSlot: 6, ExpectedEquippedItemGuid: 0x4000000000000002, Eligible: true}}}}}
	peer := &gearInputPeer{state: state}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	inventoryv1.RegisterBuddyInventoryServer(server, peer)
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///gear-input", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	m.instances["buddy-inventory"].conn = conn
	t.Cleanup(func() { m.cancel(); conn.Close(); server.Stop(); listener.Close() })
	return m, peer
}
func readGearState(t *testing.T, m *Manager) *inventoryv1.State {
	t.Helper()
	raw, err := m.InvokeJSON(context.Background(), "buddy-inventory.state", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := &inventoryv1.State{}
	if err = protojson.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
	return out
}
func requestGearState(t *testing.T, m *Manager, state *inventoryv1.State) error {
	t.Helper()
	r := &inventoryv1.EquipGearRequest{SessionId: state.SessionId, RequestId: "gear-test", ExpectedRevision: state.Revision, ExpectedInventoryRevision: state.InventoryRevision,
		Source: state.Items[0].Item, EquipmentSlot: 6, ExpectedEquipmentRevision: state.EquipmentRevision, ExpectedEquippedItemGuid: state.Equipment[6].ItemGuid, RequireConservativeUpgrade: true}
	raw, _ := protojson.Marshal(r)
	_, err := m.InvokeJSON(context.Background(), "buddy-inventory.equip_gear", raw)
	return err
}
func TestLegacyGearComparisonUsesCurrentLuaSourceWithoutAdvancingGameplay(t *testing.T) {
	m, peer := gearInputManager(t)
	if state := readGearState(t, m); state.Items[0].ConservativeUpgrade || state.GearContext.PolicyRevision != "" {
		t.Fatal("native state chose an upgrade", state)
	}
	source := followFallbackBundle(t)
	host, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	before, err := host.vm.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state := readGearState(t, m)
	if state.Items[0].ConservativeUpgrade || state.Items[0].GearBlockedReason != "enhanced_item_comparison_requires_policy_choice" || state.GearContext.PolicyRevision != host.vm.Status().Revision {
		t.Fatal("shared Lua default comparison missing", state)
	}
	if err = requestGearState(t, m, state); err == nil || len(peer.writes) != 0 {
		t.Fatal("legacy request bypassed Lua comparison", err)
	}
	assertFollowFallbackState(t, host, before)
	tuned := strings.Replace(source, "allow_enhanced_comparison=false", "allow_enhanced_comparison=true", 1)
	prior := host.vm.Status()
	if _, err = host.Reload(context.Background(), tuned, prior.Revision); err != nil {
		t.Fatal(err)
	}
	state = readGearState(t, m)
	if !state.Items[0].ConservativeUpgrade || state.Items[0].GearEquipmentSlot != 6 || state.GearContext.PolicyRevision == prior.Revision || host.vm.Status().SessionID != prior.SessionID {
		t.Fatal("same-body Lua gear tuning did not control public state", state)
	}
	if err = requestGearState(t, m, state); err != nil || len(peer.writes) != 1 || peer.writes[0].RequireConservativeUpgrade || !proto.Equal(peer.writes[0].Source, state.Items[0].Item) {
		t.Fatal("exact compatibility request did not preserve selection", err, peer.writes)
	}
}
func TestGearReadDoesNotEnterAdvanceAndRejectsUnpublishedRevisionsAndReplacedBodies(t *testing.T) {
	m, peer := gearInputManager(t)
	source := followFallbackBundle(t)
	host, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	host.advance.Lock()
	done := make(chan *inventoryv1.State, 1)
	go func() { done <- readGearState(t, m) }()
	select {
	case state := <-done:
		if state.GearContext.PolicyRevision == "" {
			host.advance.Unlock()
			t.Fatal("pure callback unavailable")
		}
	case <-time.After(time.Second):
		host.advance.Unlock()
		t.Fatal("gear callback waited on persistent dispatch")
	}
	host.advance.Unlock()
	// The VM commit-to-cache publication gap must never reuse the previous source.
	tuned := strings.Replace(source, "allow_enhanced_comparison=false", "allow_enhanced_comparison=true", 1)
	if _, err = host.vm.Reload(context.Background(), tuned, host.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	state := readGearState(t, m)
	if state.GearContext.PolicyRevision != "" || state.GearContext.PolicyError == "" || state.Items[0].ConservativeUpgrade {
		t.Fatal("stale gear cache survived VM commit", state)
	}
	if err = requestGearState(t, m, state); err == nil || len(peer.writes) != 0 {
		t.Fatal("unpublished source authorized an equip", err)
	}
	m.dir = "/private/replaced-gear-body"
	peer.state.SessionId = "replaced-gear-body"
	if state = readGearState(t, m); state.GearContext.PolicyRevision != "" || state.Items[0].ConservativeUpgrade {
		t.Fatal("old same-GUID policy decorated a new session", state)
	}
}
func TestGearEvaluatorFailureKeepsRawFactsReadableWithoutNativeFallback(t *testing.T) {
	m, peer := gearInputManager(t)
	source := `return {on_event=function(e)return true end,export_state=function()return {version=1,data={}}end,import_state=function(c)return true end,decide=function(f)return {}end,gear_evaluation=function(f)error("deliberate gear fault")end}`
	host, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	for range 2 {
		state := readGearState(t, m)
		if state.Items[0].GearFacts == nil || len(state.GearContext.Templates) != 2 || state.Items[0].ConservativeUpgrade || !strings.Contains(state.GearContext.PolicyError, "deliberate gear fault") {
			t.Fatal("gear failure hid facts or fell back to native scoring", state)
		}
		if err = requestGearState(t, m, state); err == nil || len(peer.writes) != 0 {
			t.Fatal("faulted gear policy authorized write", err)
		}
	}
}

func TestLegacyGearPinnedDescriptorKeepsItsExistingRPCDuringCoreOnlyUpgrade(t *testing.T) {
	m, peer := gearInputManager(t)
	root := t.TempDir()
	source := filepath.Join("..", "..", "..", "modules")
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err = os.Stat(filepath.Join(source, entry.Name(), "module.yaml")); err != nil {
			continue
		}
		dst := filepath.Join(root, entry.Name())
		if err = os.Mkdir(dst, 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"module.yaml", "module.pb", "compose.yaml"} {
			raw, err := os.ReadFile(filepath.Join(source, entry.Name(), name))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dst, name), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	path := filepath.Join(root, "buddy-inventory", "module.pb")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	set := &descriptorpb.FileDescriptorSet{}
	if err = proto.Unmarshal(raw, set); err != nil {
		t.Fatal(err)
	}
	for _, file := range set.File {
		if file.GetName() == "modules/buddy-inventory/inventory.proto" {
			for _, message := range file.MessageType {
				var fields []*descriptorpb.FieldDescriptorProto
				for _, field := range message.Field {
					if message.GetName() == "State" && field.GetNumber() == 42 || message.GetName() == "ItemEligibility" && field.GetNumber() == 15 {
						continue
					}
					fields = append(fields, field)
				}
				message.Field = fields
			}
		}
	}
	raw, err = proto.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := moddisc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := registry.Lookup("buddy-inventory")
	m.registry = registry
	m.instances["buddy-inventory"].definition = definition
	peer.state.GearContext = nil
	peer.state.Items[0].GearFacts = nil
	peer.state.Items[0].GearEquipEligible, peer.state.Items[0].ConservativeUpgrade = true, true
	peer.state.Items[0].GearEquipmentSlot, peer.state.Items[0].ExpectedEquippedItemGuid = 6, peer.state.Equipment[6].ItemGuid
	host, err := m.StartPolicy(context.Background(), followFallbackBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	state := readGearState(t, m)
	if !state.Items[0].ConservativeUpgrade || gearFactsAdvertised(m.instances["buddy-inventory"]) {
		t.Fatal("old pinned state comparison changed", state)
	}
	if err = requestGearState(t, m, state); err != nil || len(peer.writes) != 1 || !peer.writes[0].RequireConservativeUpgrade {
		t.Fatal("core-only upgrade replaced the old advertised RPC", err, peer.writes)
	}
	if host.vm.Status().Buffered != 0 {
		t.Fatal("legacy pinned RPC injected unsupported policy input", host.vm.Status())
	}
}

func TestGearReadUsesAcceptedSessionBudgetsForAFullFiveBagInventory(t *testing.T) {
	m, peer := gearInputManager(t)
	peer.state.Items = nil
	peer.state.InventoryBags = nil
	for bag, capacity := range []uint32{16, 8, 8, 8, 8} {
		row := &worldv1.InventoryBag{Bag: uint32(bag), SlotKnown: true, ContentsKnown: true, SlotCount: capacity}
		if bag != 0 {
			row.ContainerGuid = 0x4000000000100000 + uint64(bag)
			row.MetadataKnown = true
			row.Entry = 5000 + uint32(bag)
		}
		for slot := uint32(0); slot < capacity; slot++ {
			index := len(peer.state.Items)
			id := uint32(1000 + index)
			guid := uint64(0x4000000000000100) + uint64(index)
			owned := &worldv1.BackpackSlot{Bag: uint32(bag), Slot: slot, ItemGuid: guid, Entry: id, StackCount: 1, MetadataKnown: true, EnhancementsKnown: true}
			row.Slots = append(row.Slots, owned)
			peer.state.Items = append(peer.state.Items, &inventoryv1.ItemEligibility{Item: &inventoryv1.ItemRef{Bag: uint32(bag), Slot: slot, ItemGuid: guid, Entry: id, StackCount: 1}, MetadataKnown: true,
				GearFacts: &inventoryv1.GearItemFacts{EnhancementsKnown: true, Destinations: []*inventoryv1.GearDestination{{EquipmentSlot: 6, ExpectedEquippedItemGuid: peer.state.Equipment[6].ItemGuid, Eligible: true}}}})
			template := &inventoryv1.GearTemplate{ItemId: id, Available: true, Name: strings.Repeat("observed template ", 12), ItemClass: 4, Subclass: 2, InventoryType: 7, Armor: 20, ArmorComparisonKnown: true, Resistances: []uint32{0, 0, 0, 0, 0, 0}}
			for _, stat := range []uint32{3, 4, 5, 6, 7, 12, 13, 14, 15, 16} {
				template.Stats = append(template.Stats, &inventoryv1.GearStat{Type: stat, Value: 1})
			}
			peer.state.GearContext.Templates[strconv.FormatUint(uint64(id), 10)] = template
		}
		peer.state.InventoryBags = append(peer.state.InventoryBags, row)
	}
	encoded, err := protojson.Marshal(peer.state)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= behavior.MaxJSONBytes || len(peer.state.Items) != 48 {
		t.Fatal("fixture did not exceed old pure-read budget", len(encoded), len(peer.state.Items))
	}
	host, err := m.StartPolicyWithOptions(context.Background(), followFallbackBundle(t), behavior.SessionOptions{ValueBytes: 512 * 1024, ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer host.close()
	before, err := host.vm.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state := readGearState(t, m)
	if state.GearContext.PolicyError != "" || state.GearContext.PolicyRevision != host.vm.Status().Revision || len(state.Items) != 48 {
		t.Fatal("full inventory exceeded an inherited tiny read cap", len(encoded), state.GearContext, len(state.Items))
	}
	for _, item := range state.Items {
		if !item.ConservativeUpgrade || item.GearEquipmentSlot != 6 {
			t.Fatal("larger read lost a policy decision", item)
		}
	}
	assertFollowFallbackState(t, host, before)
}
