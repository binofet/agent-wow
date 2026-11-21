package modrt

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modrunner "github.com/agent-wow/agent-wow/pkg/modules/runner"
	modsession "github.com/agent-wow/agent-wow/pkg/modules/session"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/types/known/emptypb"
)

const packagedRollGUID = uint64(0x400000000000bb21)

type packagedRollWorld struct {
	worldv1.UnimplementedBuddyWorldServer
	equipped bool
}

func (w packagedRollWorld) Snapshot(context.Context, *worldv1.SnapshotRequest) (*worldv1.SnapshotResponse, error) {
	out := &worldv1.SnapshotResponse{
		Self: &worldv1.PlayerState{Guid: policyMageGUID, Present: true, MapKnown: true,
			ClassKnown: true, ClassId: 8, RaceKnown: true, RaceId: 1, LevelKnown: true, Level: 10},
		VisitRevision: 1, SelfEquipmentKnown: true, SelfEquipmentRevision: 1,
		SelfSkillsKnown: true, SelfSkillsRevision: 1,
	}
	for i := range 19 {
		out.SelfEquipment = append(out.SelfEquipment, &worldv1.EquipmentSlotState{Slot: uint32(i), SlotKnown: true})
	}
	if w.equipped {
		out.SelfEquipment[4] = &worldv1.EquipmentSlotState{Slot: 4, SlotKnown: true,
			ItemGuid: 0x400000000000bb22, Entry: 5001, StackCount: 1, MetadataKnown: true,
			EnhancementsKnown: true, DurabilityKnown: true, Durability: 20, MaxDurability: 20}
	}
	for i := range 128 {
		row := &worldv1.SkillState{Slot: uint32(i)}
		if i == 0 {
			row.SkillId, row.Value, row.MaxValue = 415, 75, 75
		}
		out.SelfSkills = append(out.SelfSkills, row)
	}
	return out, nil
}

func (packagedRollWorld) OnPacket(context.Context, *modv1.WorldPacket) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}
func (packagedRollWorld) BeforeLogout(context.Context, *emptypb.Empty) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

type packagedRollTrade struct {
	tradev1.UnimplementedBuddyTradeServer
	session string
}

func (t packagedRollTrade) LookupItems(_ context.Context, req *tradev1.LookupItemsRequest) (*tradev1.LookupItemsResponse, error) {
	out := &tradev1.LookupItemsResponse{SessionId: t.session}
	for _, id := range req.ItemIds {
		out.Items = append(out.Items, &tradev1.ItemDetails{ItemId: id, Available: true,
			ItemClass: 4, Subclass: 1, InventoryType: 20, Quality: 2, ItemLevel: 10,
			RequiredLevel: 10, AllowableClass: 0xffffffff, AllowableRace: 0xffffffff,
			ArmorComparisonKnown: true, Armor: 10, Stats: []*tradev1.ItemStat{{Type: 5, Value: 2}}})
	}
	return out, nil
}
func (packagedRollTrade) OnPacket(context.Context, *modv1.WorldPacket) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}
func (packagedRollTrade) BeforeLogout(context.Context, *emptypb.Empty) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

type packagedRollRunner struct {
	binary   string
	process  *exec.Cmd
	peers    map[string]*grpc.Server
	equipped bool
}

func (r *packagedRollRunner) Up(_ context.Context, launch modrunner.Launch) error {
	socket := filepath.Join(launch.SocketDir, "module.sock")
	if launch.Manifest.Name == inventoryModule {
		cmd := exec.Command(r.binary)
		cmd.Dir, cmd.Stderr = launch.SocketDir, os.Stderr
		for key, value := range launch.Environment {
			if key == "AGENT_WOW_MODULE_SOCKET" {
				value = socket
			}
			if key == "AGENT_WOW_SESSION_SOCKET" {
				value = filepath.Join(launch.SocketDir, "session.sock")
			}
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		r.process = cmd
		return cmd.Start()
	}
	// Dependencies unused by Rolls still consume ordinary lifecycle and packet
	// delivery. No fake endpoint emits an Inventory vote or result.
	server := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		if err := stream.RecvMsg(&emptypb.Empty{}); err != nil {
			return err
		}
		return stream.SendMsg(&emptypb.Empty{})
	}))
	switch launch.Manifest.Name {
	case "buddy-world":
		worldv1.RegisterBuddyWorldServer(server, packagedRollWorld{equipped: r.equipped})
	case "buddy-trade":
		tradev1.RegisterBuddyTradeServer(server, packagedRollTrade{session: launch.Environment["AGENT_WOW_SESSION_ID"]})
	}
	ready := health.NewServer()
	ready.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(server, ready)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	r.peers[launch.Manifest.Name] = server
	go server.Serve(listener)
	return nil
}

func (r *packagedRollRunner) Down(_ context.Context, launch modrunner.Launch) error {
	if peer := r.peers[launch.Manifest.Name]; peer != nil {
		peer.Stop()
	}
	if launch.Manifest.Name != inventoryModule || r.process == nil {
		return nil
	}
	if err := r.process.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	var exit *exec.ExitError
	if err := r.process.Wait(); err != nil && !errors.As(err, &exit) {
		return err
	}
	return nil
}

func packagedRollRegistry(t *testing.T) *moddisc.Registry {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"buddy-inventory", "buddy-actions", "buddy-world", "buddy-trade", "buddy-party", "buddy-loot", "buddy-professions"} {
		dest := filepath.Join(root, name)
		if err := os.Mkdir(dest, 0700); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{"module.yaml", "module.pb", "compose.yaml"} {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "modules", name, file))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dest, file), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	registry, err := moddisc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func packagedRollPolicy(t *testing.T, label string) string {
	t.Helper()
	source, err := exec.Command("python3", "-B", filepath.Join("..", "..", "..", "dev", "encounter-bundle")).Output()
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`local policy=(function()
%s
end)()
local last
return {
 on_event=function(event) if event.receipt then last=event.receipt end return policy.on_event(event) end,
 decide=function(frame) if frame.input.inspect then return {last=last,label=%q} end return policy.decide(frame) end,
 export_state=function() return {version=1,data={roll=policy.export_state(),last=last}} end,
 import_state=function(saved) last=saved.data.last return policy.import_state(saved.data.roll) end
}`, source, label)
}

func TestPackagedInventoryObservedLuaRoll(t *testing.T) {
	binaryPath := os.Getenv("BUDDY_INVENTORY_PACKAGED_BINARY")
	if binaryPath == "" {
		t.Skip("set BUDDY_INVENTORY_PACKAGED_BINARY to the native Inventory executable")
	}
	for _, equipped := range []bool{false, true} {
		name, choice := "need useful armor", byte(1)
		if equipped {
			name, choice = "disenchant equal armor without personal enchanting", 3
		}
		t.Run(name, func(t *testing.T) {
			runner := &packagedRollRunner{binary: binaryPath, peers: make(map[string]*grpc.Server), equipped: equipped}
			manager := New(packagedRollRegistry(t), runner, nil)
			t.Cleanup(func() {
				if err := manager.Close(); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			writes := make(chan policySpellWrite, 4)
			if err := manager.Start(ctx, modsession.Session{
				Identity: modsession.Identity{CharacterGUID: policyMageGUID},
				Clock:    func() uint32 { return 1 },
				SendPacket: func(ctx context.Context, code uint32, body []byte) error {
					writes <- policySpellWrite{code, bytes.Clone(body), modsession.WriteOriginFromContext(ctx)}
					return nil
				},
			}); err != nil {
				t.Fatal(err)
			}
			deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGLoginVerifyWorld, make([]byte, 20))
			offer := binary.LittleEndian.AppendUint64(nil, packagedRollGUID)
			for _, field := range []uint32{0, 2, 5000, 0, 0, 1, 60000} {
				offer = binary.LittleEndian.AppendUint32(offer, field)
			}
			deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGLootStartRoll, append(offer, 15))
			if err := manager.StartObservedPolicy(ctx, packagedRollPolicy(t, "before"), "loot_roll=buddy-inventory.rolls"); err != nil {
				t.Fatal(err)
			}
			manager.mu.Lock()
			host := manager.policy
			manager.mu.Unlock()
			before := host.vm.Status()
			var cast policySpellWrite
			select {
			case cast = <-writes:
			case <-ctx.Done():
				t.Fatal("no Lua vote reached native callback")
			}
			want := binary.LittleEndian.AppendUint64(nil, packagedRollGUID)
			want = append(binary.LittleEndian.AppendUint32(want, 2), choice)
			if cast.opcode != opcode.CMSGLootRoll || !bytes.Equal(cast.body, want) || cast.origin.Writer != "lua" ||
				cast.origin.Module != inventoryModule || cast.origin.ActionID != "1" || cast.origin.PolicyRevision != before.Revision {
				t.Fatalf("native vote = %#x %x %+v", cast.opcode, cast.body, cast.origin)
			}
			for host.vm.Status().Pending != 1 {
				select {
				case <-ctx.Done():
					t.Fatal("vote reservation missing")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			params, _ := json.Marshal(map[string]string{"sessionId": before.SessionID, "characterGuid": fmt.Sprint(policyMageGUID),
				"expectedRevision": before.Revision, "source": packagedRollPolicy(t, "after")})
			if _, err := manager.InvokeJSON(ctx, "session.reload_policy", params); err != nil {
				t.Fatal(err)
			}
			echo := func(actor uint64) []byte {
				body := binary.LittleEndian.AppendUint64(nil, 0)
				body = binary.LittleEndian.AppendUint32(body, 2)
				body = binary.LittleEndian.AppendUint64(body, actor)
				for _, field := range []uint32{5000, 0, 0} {
					body = binary.LittleEndian.AppendUint32(body, field)
				}
				number, kind := byte(128), choice
				if choice == 1 {
					number, kind = 0, 0
				}
				return append(body, number, kind, 0)
			}
			deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGLootRoll, echo(policyMageGUID+1))
			if host.vm.Status().Pending != 1 {
				t.Fatal("foreign vote settled own request")
			}
			deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGLootRoll, echo(policyMageGUID))
			for host.vm.Status().Pending != 0 {
				select {
				case <-ctx.Done():
					t.Fatal("exact own vote was not consumed")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			out, receipt, err := host.Step(ctx, map[string]any{"inspect": true}, nil)
			last, _ := out["last"].(map[string]any)
			if err != nil || receipt != nil || out["label"] != "after" || last["action_id"] != "1" || last["outcome"] != string(behavior.ActionConfirmed) {
				t.Fatalf("Lua receipt after reload = %+v %+v %v", out, receipt, err)
			}
			select {
			case extra := <-writes:
				t.Fatalf("unexpected duplicate vote %+v", extra)
			default:
			}
		})
	}
}
