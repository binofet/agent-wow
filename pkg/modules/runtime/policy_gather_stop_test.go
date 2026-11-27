package modrt

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	buddyactions "github.com/agent-wow/agent-wow/modules/buddy-actions"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	buddyloot "github.com/agent-wow/agent-wow/modules/buddy-loot"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/o0olele/detour-go/detour"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const gatherStopSession = "gather-stop-body"
const gatherStopSelf = uint64(22)

type gatherStopEnvironment struct {
	mu               sync.Mutex
	manager          *Manager
	world            *worldv1.SnapshotResponse
	packets          []*modv1.SendPacketRequest
	origins          []modsession.WriteOrigin
	movements        []*worldv1.ClientMovementRequest
	clock            uint32
	acquisitions     int
	renewals         int
	acquiredRevision uint64
	request          *lootv1.OpenGatherRequest
	afterStop        func(*worldv1.SnapshotResponse)
}

type gatherStopCallback struct {
	env    *gatherStopEnvironment
	module string
}

func (f *gatherStopCallback) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if md, ok := metadata.FromOutgoingContext(ctx); ok {
		ctx = metadata.NewIncomingContext(ctx, md)
	}
	return &emptypb.Empty{}, f.env.manager.modulePacketWriter(gatherStopSession, f.module)(ctx, r.Opcode, r.Payload)
}

func (f *gatherStopCallback) GetClock(context.Context, *emptypb.Empty, ...grpc.CallOption) (*modv1.ClockResponse, error) {
	f.env.mu.Lock()
	defer f.env.mu.Unlock()
	f.env.clock += 200
	return &modv1.ClockResponse{ClientTimeMs: f.env.clock}, nil
}

func (f *gatherStopCallback) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, _ ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	e := f.env
	var value proto.Message
	switch r.Module + "." + r.Method {
	case "buddy-world.snapshot":
		e.mu.Lock()
		value = proto.Clone(e.world)
		e.mu.Unlock()
	case "buddy-world.observe_client_movement":
		movement := &worldv1.ClientMovementRequest{}
		if err := r.Request.UnmarshalTo(movement); err != nil {
			return nil, err
		}
		e.mu.Lock()
		e.movements = append(e.movements, proto.Clone(movement).(*worldv1.ClientMovementRequest))
		// This is the actual Actions post-write observation callback. No test
		// advances its private client-position counter or invents a STOP.
		if e.afterStop != nil && len(e.packets) != 0 && e.packets[len(e.packets)-1].Opcode == opcode.MSGMoveStop {
			e.afterStop(e.world)
			e.afterStop = nil
		}
		e.mu.Unlock()
		value = &emptypb.Empty{}
	case "buddy-party.state":
		value = &partyv1.StateResponse{SessionId: gatherStopSession, Group: &partyv1.Group{Known: true, InGroup: true,
			LeaderGuid: 3, GroupGuid: 7, Members: []*partyv1.Member{{Guid: gatherStopSelf, Self: true, Online: true}, {Guid: 3, Online: true}}}}
	default:
		if r.Module != actionsModule {
			return nil, fmt.Errorf("unexpected real gather/STOP dependency %s.%s", r.Module, r.Method)
		}
		var acquisition *actionsv1.InteractionRequest
		if r.Method == "acquire_interaction" {
			acquisition = &actionsv1.InteractionRequest{}
			if err := r.Request.UnmarshalTo(acquisition); err != nil {
				return nil, err
			}
			e.mu.Lock()
			if acquisition.Token == "" {
				e.acquisitions++
			} else {
				e.renewals++
			}
			e.mu.Unlock()
		}
		result, err := e.manager.invokeModule(ctx, f.module, r)
		if err == nil && acquisition != nil && acquisition.Token == "" {
			lease := &actionsv1.InteractionLease{}
			if result.GetResult().UnmarshalTo(lease) == nil {
				e.mu.Lock()
				e.acquiredRevision = lease.GetState().GetPosition().GetRevision()
				e.mu.Unlock()
			}
		}
		return result, err
	}
	packed, err := anypb.New(value)
	return &modv1.InvokeModuleResponse{Result: packed}, err
}

// Same tiny flat public Detour/terrain fixture used by buddynav's map tests.
// It exercises the production planner; it is not a mocked route or live asset.
func gatherStopNavigation(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"mmaps", "maps", "vmaps"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	const edge uint16 = 0xffff
	params := detour.DtNavMeshCreateParams{Verts: []uint16{0, 0, 0, 0, 0, 20, 20, 0, 20, 20, 0, 0}, VertCount: 4,
		Polys: []uint16{0, 1, 2, 3, edge, edge, edge, edge}, PolyFlags: []uint16{1}, PolyAreas: []uint8{1}, PolyCount: 1, Nvp: 4,
		Bmin: [3]float32{95, 30, 95}, Bmax: [3]float32{115, 32, 115}, WalkableHeight: 2, WalkableRadius: .5,
		WalkableClimb: .5, Cs: 1, Ch: 1, BuildBvTree: true}
	var data []byte
	var size int
	if !detour.DtCreateNavMeshData(&params, &data, &size) {
		t.Fatal("flat gather handoff navigation fixture failed")
	}
	mmap := make([]byte, 28)
	for i, value := range []float32{95, 30, 95, 533.3333, 533.3333} {
		binary.LittleEndian.PutUint32(mmap[4*i:], math.Float32bits(value))
	}
	binary.LittleEndian.PutUint32(mmap[20:], 1)
	binary.LittleEndian.PutUint32(mmap[24:], 128)
	tile := make([]byte, 56)
	for i, value := range []uint32{0x4d4d4150, 7, 20, uint32(size)} {
		binary.LittleEndian.PutUint32(tile[4*i:], value)
	}
	terrain := make([]byte, 60)
	for offset, value := range map[int]uint32{0: 0x5350414d, 4: 9, 20: 44, 24: 16, 44: 0x5447484d, 48: 1, 52: math.Float32bits(30), 56: math.Float32bits(30)} {
		binary.LittleEndian.PutUint32(terrain[offset:], value)
	}
	assets := map[string][]byte{"mmaps/000.mmap": mmap, "mmaps/0003131.mmtile": append(tile, data...),
		"maps/0003131.map": terrain, "vmaps/000_31_31.vmtile": append([]byte("VMAP_4.8"), 0, 0, 0, 0),
		"vmaps/000.vmtree": append([]byte("VMAP_4.8"), 1)}
	for name, raw := range assets {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func realGatherStopFixture(t *testing.T) (*gatherStopEnvironment, *buddyactions.Service, *buddyloot.Service, *grpc.ClientConn) {
	t.Helper()
	registry, err := moddisc.Load(filepath.Join("..", "..", "..", "modules"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(registry, nil, nil)
	m.started, m.dir = true, "/private/"+gatherStopSession
	m.session.Identity = modsession.Identity{CharacterGUID: gatherStopSelf}
	for _, inst := range m.instances {
		inst.healthy = true
	}
	player := func(guid uint64, x float32) *worldv1.PlayerState {
		return &worldv1.PlayerState{Guid: guid, Present: true, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, MaxHealth: 100,
			MapKnown: true, PositionKnown: true, PositionRevision: 1, X: x, Y: 100, Z: 30, UnitFlagsKnown: true,
			MovementFlagsKnown: true, SpeedsKnown: true, WalkSpeed: 2.5, RunSpeed: 7, ChannelSpellKnown: true, ChannelObjectKnown: true}
	}
	e := &gatherStopEnvironment{manager: m, world: &worldv1.SnapshotResponse{VisitRevision: 1, Self: player(gatherStopSelf, 100), Owner: player(3, 110),
		InventoryKnown: true, InventoryRevision: 1, SelfQuestLogKnown: true, SelfQuestLogRevision: 1,
		QuestObjects: []*worldv1.GameObjectState{{Guid: policyGatherSource, Entry: 161557, Present: true, MapKnown: true,
			PositionKnown: true, PositionRevision: 1, X: 102, Y: 100, Z: 30, TypeKnown: true, GameObjectType: 3, FlagsKnown: true}}}}
	for i := uint32(0); i < 25; i++ {
		e.world.SelfQuestLog = append(e.world.SelfQuestLog, &worldv1.QuestLogEntry{Slot: i})
	}
	e.world.SelfQuestLog[0].QuestId = 3904
	for b := uint32(0); b < 5; b++ {
		bag := &worldv1.InventoryBag{Bag: b, SlotKnown: true, ContentsKnown: true}
		if b == 0 {
			bag.SlotCount = 16
			for slot := uint32(0); slot < 16; slot++ {
				bag.Slots = append(bag.Slots, &worldv1.BackpackSlot{Slot: slot})
			}
		}
		e.world.InventoryBags = append(e.world.InventoryBags, bag)
	}
	m.session.SendPacket = func(ctx context.Context, code uint32, payload []byte) error {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.packets = append(e.packets, &modv1.SendPacketRequest{Opcode: code, Payload: bytes.Clone(payload)})
		e.origins = append(e.origins, modsession.WriteOriginFromContext(ctx))
		return nil
	}
	actions := buddyactions.NewService(&gatherStopCallback{env: e, module: actionsModule}, gatherStopSelf, gatherStopSession)
	actions.SetNavigationDataDir(gatherStopNavigation(t))
	key := strings.Repeat("a", 64)
	if err = actions.UseHostEncounterWriter(key); err != nil {
		t.Fatal(err)
	}
	entry := binary.LittleEndian.AppendUint32(nil, 0)
	for _, n := range []float32{100, 100, 30, 0} {
		entry = binary.LittleEndian.AppendUint32(entry, math.Float32bits(n))
	}
	if _, err = actions.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGLoginVerifyWorld, Payload: entry}); err != nil {
		t.Fatal(err)
	}
	loot := buddyloot.NewAutomaticService(&gatherStopCallback{env: e, module: "buddy-loot"}, gatherStopSelf, gatherStopSession)
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info.FullMethod == lootv1.BuddyLoot_OpenGather_FullMethodName {
			e.mu.Lock()
			e.request = proto.Clone(request.(*lootv1.OpenGatherRequest)).(*lootv1.OpenGatherRequest)
			e.mu.Unlock()
		}
		return handler(ctx, request)
	}))
	actionsv1.RegisterBuddyActionsServer(server, actions)
	lootv1.RegisterBuddyLootServer(server, loot)
	listener := bufconn.Listen(1 << 20)
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///real-actions-gather-stop", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	m.instances[actionsModule].conn, m.instances["buddy-loot"].conn = conn, conn
	t.Cleanup(func() { m.cancel(); conn.Close(); server.Stop(); listener.Close() })
	ctx := modcb.WithPolicyEncounterAccess(context.Background(), key, 1)
	md, _ := metadata.FromOutgoingContext(ctx)
	ctx = metadata.NewIncomingContext(context.Background(), md)
	goal := &actionsv1.FollowFormationRequest{SessionId: gatherStopSession, SelfGuid: gatherStopSelf, VisitId: 1,
		OwnerGuid: 3, OwnerPositionRevision: 1, X: 108, Y: 100, Z: 30, StartDistance: 1.9, StopDistance: .85,
		Ready: true, Phase: "following_formation"}
	for i := 0; i < 4; i++ {
		if _, err = actions.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(1)); err != nil {
			t.Fatal(err)
		}
		if _, err = actions.SetFollowFormation(ctx, goal); err != nil {
			t.Fatal(err)
		}
		actions.Tick(context.Background())
		state, err := actions.Status(context.Background(), &emptypb.Empty{})
		if err != nil {
			t.Fatal(err)
		}
		if state.Moving {
			return e, actions, loot, conn
		}
	}
	state, _ := actions.Status(context.Background(), &emptypb.Empty{})
	t.Fatal("actual finite follow input did not enter normal client motion", state)
	return nil, nil, nil, nil
}

func TestPolicyGatherRealActionsStopRevisionAndPostStopRefusal(t *testing.T) {
	for _, mode := range []string{"current", "object_changed", "quest_changed", "visit_changed"} {
		t.Run(mode, func(t *testing.T) {
			e, actions, loot, conn := realGatherStopFixture(t)
			before, err := actions.Status(context.Background(), &emptypb.Empty{})
			if err != nil || !before.Moving || before.GetPosition().GetRevision() == 0 {
				t.Fatal("fixture did not retain actual client motion", before, err)
			}
			if _, err = loot.Enable(context.Background(), &lootv1.EnableRequest{Enabled: true}); err != nil {
				t.Fatal(err)
			}
			if mode != "current" {
				e.mu.Lock()
				e.afterStop = func(w *worldv1.SnapshotResponse) {
					switch mode {
					case "object_changed":
						w.QuestObjects[0].PositionRevision++
					case "quest_changed":
						w.SelfQuestLogRevision++
						w.SelfQuestLog[0].Complete = true
					case "visit_changed":
						w.VisitRevision++
					}
				}
				e.mu.Unlock()
			}
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "behaviors", "corpse-loot.lua"))
			if err != nil {
				t.Fatal(err)
			}
			source := "local loot=(function()\n" + string(raw) + "\nend)()\nreturn {on_event=loot.on_event,export_state=loot.export_state,import_state=loot.import_state,decide=function(f)return loot.decide(f,{selfGuid='22',visitId='1'},true,false)end}"
			host, err := e.manager.StartPolicyWithOptions(context.Background(), source, behavior.SessionOptions{ExecutionLimit: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(host.close)
			observation, err := e.manager.InvokeJSON(context.Background(), "buddy-loot.observe_corpses", nil)
			if err != nil {
				t.Fatal(err)
			}
			var view map[string]any
			if err = json.Unmarshal(observation, &view); err != nil {
				t.Fatal(err)
			}
			seq, err := host.Enqueue(behavior.Event{Kind: "corpse_loot_observation", Value: view})
			if err != nil {
				t.Fatal(err)
			}
			n := strconv.FormatUint(seq, 10)
			out, receipt, err := host.Step(context.Background(), map[string]any{"observationSequence": n, "observationSequences": map[string]any{"corpse_loot": n}}, nil)
			if err != nil || receipt == nil || receipt.Outcome != behavior.ActionSettledUnverified {
				t.Fatal("actual Lua/Actions/Loot handoff did not preserve entered custody", out, receipt, err)
			}
			e.mu.Lock()
			var request *lootv1.OpenGatherRequest
			if e.request != nil {
				request = proto.Clone(e.request).(*lootv1.OpenGatherRequest)
			}
			acquiredRevision, acquisitions, renewals := e.acquiredRevision, e.acquisitions, e.renewals
			packets := append([]*modv1.SendPacketRequest(nil), e.packets...)
			origins := append([]modsession.WriteOrigin(nil), e.origins...)
			movements := append([]*worldv1.ClientMovementRequest(nil), e.movements...)
			e.mu.Unlock()
			if request == nil || request.ExpectedPositionRevision != before.Position.Revision || acquiredRevision <= request.ExpectedPositionRevision || acquisitions != 1 {
				t.Fatal("STOP did not advance the actual client pose independently of immutable Lua input", request, acquiredRevision, acquisitions, before)
			}
			var stopped, cast int
			for i, packet := range packets {
				switch packet.Opcode {
				case opcode.MSGMoveStop:
					stopped++
					if origins[i].Module != actionsModule {
						t.Fatal("STOP did not come from actual Actions", origins[i])
					}
				case opcode.CMSGCastSpell:
					cast++
					if origins[i].Writer != "lua" || origins[i].Module != "buddy-loot" || origins[i].ActionID == "" {
						t.Fatal("selected opening lost exact Lua attribution", origins[i])
					}
				}
			}
			if stopped != 1 || len(movements) < 2 || !movements[len(movements)-1].Valid || movements[len(movements)-1].ServerPositionRevision != 1 {
				t.Fatal("normal START/STOP client-pose publication was not used", stopped, movements)
			}
			encoded, _ := json.Marshal(receipt.Payload["result"])
			operation := &lootv1.CorpseOperation{}
			if err = protojson.Unmarshal(encoded, operation); err != nil || operation.SourceGuid != policyGatherSource || operation.RequestId != request.RequestId {
				t.Fatal("result lost immutable source/request binding", operation, err)
			}
			if mode == "current" {
				// The normal opener rechecks its exact same-token lease before
				// casting. That renewal is not another physical acquisition/STOP.
				if cast != 1 || renewals != 1 || !operation.OpeningTransportAttempted || !operation.OpeningTransportWritten {
					t.Fatal("valid STOP revision change rejected the selected opening", operation, cast)
				}
			} else if cast != 0 || renewals != 0 || operation.Phase != "acquired_not_opened" || !operation.Terminal || operation.OpeningTransportAttempted {
				t.Fatal("post-STOP fact change was replayable or mislabeled not dispatched", operation, cast)
			}
			immutable := proto.Clone(request).(*lootv1.OpenGatherRequest)
			repeated, err := lootv1.NewBuddyLootClient(conn).OpenGather(context.Background(), immutable)
			e.mu.Lock()
			afterPackets, afterAcquisitions, afterRenewals, sameRequest := len(e.packets), e.acquisitions, e.renewals, proto.Equal(e.request, immutable)
			e.mu.Unlock()
			if err != nil || !proto.Equal(repeated, operation) || afterPackets != len(packets) || afterAcquisitions != acquisitions || afterRenewals != renewals || !sameRequest {
				t.Fatal("same request reacquired custody, repeated STOP/opening or rewrote its pose", repeated, err, afterAcquisitions, afterPackets, len(packets))
			}
		})
	}
}
