package modrt

import (
	"context"
	"encoding/binary"
	"encoding/json"
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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

type capacityResumeNative struct {
	*buddyloot.Service
	loseResponse bool
}

func (s *capacityResumeNative) ResumeSource(ctx context.Context, r *lootv1.ResumeSourceRequest) (*lootv1.ResumeSourceReceipt, error) {
	result, err := s.Service.ResumeSource(ctx, r)
	if err == nil && s.loseResponse {
		return nil, status.Error(codes.Unavailable, "metadata response unavailable")
	}
	return result, err
}

func TestPolicyContainerCapacityResumeUsesNativeRouteWithoutPhysicalPending(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed_metadata", true: "lost_metadata_reply"}[unknown], func(t *testing.T) {
			registry, err := moddisc.Load(filepath.Join("..", "..", "..", "modules"))
			if err != nil {
				t.Fatal(err)
			}
			m := New(registry, nil, nil)
			m.started, m.dir = true, "/private/corpse-body"
			m.session.Identity = modsession.Identity{CharacterGUID: 6}
			for _, inst := range m.instances {
				inst.healthy = true
			}
			writes := 0
			m.session.SendPacket = func(context.Context, uint32, []byte) error { writes++; return nil }
			const source = uint64(0xf110000000000000 | uint64(161557)<<24 | 42)
			w := &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{Guid: 6, Present: true, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, MaxHealth: 100, MapKnown: true, PositionKnown: true, PositionRevision: 1, UnitFlagsKnown: true, MovementFlagsKnown: true},
				InventoryKnown: true, InventoryRevision: 1, SelfQuestLogKnown: true, SelfQuestLogRevision: 1,
				QuestObjects: []*worldv1.GameObjectState{{Guid: source, Entry: 161557, Present: true, MapKnown: true, PositionKnown: true, PositionRevision: 1, X: 2, TypeKnown: true, GameObjectType: 3, FlagsKnown: true}}}
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
			callback := &corpseNativeCallback{manager: m, world: w, actions: &actionsv1.State{SessionId: "corpse-body", SelfGuid: 6, Position: &actionsv1.ClientPosition{Known: true, MapKnown: true, Revision: 1}}}
			native := buddyloot.NewService(callback, 6, "corpse-body")
			// Establish the capacity pause through actual public opening, window,
			// inventory rejection and release packets, without private state writes.
			native.Enable(context.Background(), &lootv1.EnableRequest{Enabled: true})
			native.Tick(context.Background())
			window := binary.LittleEndian.AppendUint64(nil, source)
			window = append(window, 6)
			window = binary.LittleEndian.AppendUint32(window, 0)
			window = append(window, 1)
			row := make([]byte, 22)
			row[0] = 2
			binary.LittleEndian.PutUint32(row[1:], 11119)
			binary.LittleEndian.PutUint32(row[5:], 1)
			row[21] = 4
			window = append(window, row...)
			native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLootResponse, Payload: window})
			native.Tick(context.Background())
			reject := make([]byte, 18)
			reject[0] = 50
			native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGInventoryChangeFailure, Payload: reject})
			native.Tick(context.Background())
			native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLootReleaseResponse, Payload: append(binary.LittleEndian.AppendUint64(nil, source), 1)})
			native.Tick(context.Background())
			paused, _ := native.GetState(context.Background(), &emptypb.Empty{})
			if paused.CapacityPause == nil || writes != 3 {
				t.Fatal("actual pause not established", paused, writes)
			}
			w.InventoryRevision++
			server := grpc.NewServer()
			lootv1.RegisterBuddyLootServer(server, &capacityResumeNative{Service: native, loseResponse: unknown})
			listener := bufconn.Listen(1 << 20)
			go server.Serve(listener)
			conn, err := grpc.NewClient("passthrough:///capacity-native", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
			if err != nil {
				t.Fatal(err)
			}
			m.instances["buddy-loot"].conn = conn
			defer func() { m.cancel(); conn.Close(); server.Stop(); listener.Close() }()
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "behaviors", "corpse-loot.lua"))
			if err != nil {
				t.Fatal(err)
			}
			policy := "local loot=(function()\n" + string(raw) + "\nend)()\nreturn {on_event=loot.on_event,export_state=loot.export_state,import_state=loot.import_state,decide=function(f)return loot.decide(f,{selfGuid='6',visitId='1'},true,false)end}"
			host, err := m.StartPolicyWithOptions(context.Background(), policy, behavior.SessionOptions{ExecutionLimit: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer host.close()
			observed, err := m.InvokeJSON(context.Background(), "buddy-loot.observe_corpses", nil)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err = json.Unmarshal(observed, &value); err != nil {
				t.Fatal(err)
			}
			seq, err := host.Enqueue(behavior.Event{Kind: "corpse_loot_observation", Value: value})
			if err != nil {
				t.Fatal(err)
			}
			n := strconv.FormatUint(seq, 10)
			_, receipt, err := host.Step(context.Background(), map[string]any{"observationSequence": n, "observationSequences": map[string]any{"corpse_loot": n}}, nil)
			want := behavior.ActionConfirmed
			if unknown {
				want = behavior.ActionSettledUnverified
			}
			if err != nil || receipt == nil || receipt.Outcome != want || host.vm.Status().Pending != 0 || writes != 3 {
				t.Fatal("metadata reply suppressed physical support or sent packet", receipt, err, host.vm.Status(), writes)
			}
			after, _ := native.GetState(context.Background(), &emptypb.Empty{})
			if after.InventoryPaused || after.CapacityPause != nil || after.LastGather.ItemPushObserved || after.LastGather.Status != lootv1.GatherStatus_GATHER_SERVER_REJECTED {
				t.Fatal("resume lost rejection evidence or invented pickup", after)
			}
		})
	}
}
