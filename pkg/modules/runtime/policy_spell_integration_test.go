package modrt

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/modules/runner"
	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	policyMageGUID = uint64(11)
	policyWolfGUID = uint64(0xf13000022b000001)
	policySheepID  = uint32(118)
)

type policySpellWorld struct {
	worldv1.UnimplementedBuddyWorldServer
}

func (policySpellWorld) Snapshot(_ context.Context, req *worldv1.SnapshotRequest) (*worldv1.SnapshotResponse, error) {
	view := &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{
		Guid: policyMageGUID, Present: true, ClassKnown: true, ClassId: 8,
		MapKnown: true, PositionKnown: true, PositionRevision: 1, MovementFlagsKnown: true,
		AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, MaxHealth: 100,
		PowerKnown: true, Power: 128, MaxPower: 128, UnitFlagsKnown: true,
		ShapeshiftFormKnown: true, AuraStateKnown: true, SpeedsKnown: true, WalkSpeed: 2.5, RunSpeed: 7,
	}}
	if req.GetCreatureGuid() == policyWolfGUID {
		view.RequestedCreature = &worldv1.CreatureState{
			Guid: policyWolfGUID, Entry: 555, Present: true, MapKnown: true, PositionKnown: true,
			X: 15, AliveKnown: true, Alive: true, HealthKnown: true, Health: 100, UnitFlagsKnown: true,
		}
	}
	return view, nil
}

// World subscribes to SPELL_GO as well. Its observations remain fixed; only
// the packaged Actions callback is allowed to produce the spell operation result.
func (policySpellWorld) OnPacket(context.Context, *modv1.WorldPacket) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

type policySpellParty struct {
	partyv1.UnimplementedBuddyPartyServer
	sessionID string
	group     *partyv1.Group
}

func (p policySpellParty) State(context.Context, *emptypb.Empty) (*partyv1.StateResponse, error) {
	group := p.group
	if group == nil {
		group = &partyv1.Group{Known: true}
	}
	return &partyv1.StateResponse{SessionId: p.sessionID, Group: group}, nil
}

// The module manager starts its ordinary callback servers. Only Actions runs
// as a packaged executable. Its World and Party dependencies serve fixed input.
type policySpellRunner struct {
	binary            string
	action            *exec.Cmd
	peers             map[string]*grpc.Server
	world             worldv1.BuddyWorldServer
	group             *partyv1.Group
	hostOwnsSelfMana  bool
	hostOwnsEncounter bool
}

func (r *policySpellRunner) Up(ctx context.Context, launch modrunner.Launch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := launch.Manifest.Name
	socket := filepath.Join(launch.SocketDir, "module.sock")
	if name == "buddy-actions" {
		cmd := exec.Command(r.binary)
		cmd.Dir = launch.SocketDir
		cmd.Env = []string{"AGENT_WOW_MODULE_SOCKET=" + socket,
			"AGENT_WOW_SESSION_SOCKET=" + filepath.Join(launch.SocketDir, "session.sock"),
			"BUDDY_INDEPENDENT=false"}
		for key, value := range launch.Environment {
			if key != "AGENT_WOW_MODULE_SOCKET" && key != "AGENT_WOW_SESSION_SOCKET" {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
		}
		if r.hostOwnsSelfMana {
			cmd.Env = append(cmd.Env, "BUDDY_HOST_SELF_MANA_OWNED=true")
		}
		if r.hostOwnsEncounter {
			cmd.Env = append(cmd.Env, "BUDDY_HOST_ENCOUNTER_OWNED=true")
		}
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		r.action = cmd
		return nil
	}

	server := grpc.NewServer()
	switch name {
	case "buddy-world":
		world := r.world
		if world == nil {
			world = policySpellWorld{}
		}
		worldv1.RegisterBuddyWorldServer(server, world)
	case "buddy-party":
		partyv1.RegisterBuddyPartyServer(server, policySpellParty{sessionID: launch.Environment["AGENT_WOW_SESSION_ID"], group: r.group})
	default:
		return fmt.Errorf("unexpected fixture module %q", name)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	ready := health.NewServer()
	ready.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(server, ready)
	r.peers[name] = server
	go server.Serve(listener)
	return nil
}

func (r *policySpellRunner) Down(_ context.Context, launch modrunner.Launch) error {
	if peer := r.peers[launch.Manifest.Name]; peer != nil {
		peer.Stop()
	}
	if launch.Manifest.Name != "buddy-actions" || r.action == nil {
		return nil
	}
	if err := r.action.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	var stopped *exec.ExitError
	if err := r.action.Wait(); err != nil && !errors.As(err, &stopped) {
		return err
	}
	return nil
}

func packagedPolicySpellRegistry(t *testing.T) *moddisc.Registry {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"buddy-actions", "buddy-world", "buddy-party"} {
		dst := filepath.Join(root, name)
		if err := os.Mkdir(dst, 0700); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{"module.yaml", "module.pb", "compose.yaml"} {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "modules", name, file))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dst, file), raw, 0600); err != nil {
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

func spellFixturePackedGUID(guid uint64) []byte {
	body := []byte{0}
	for i := range 8 {
		if part := byte(guid >> (i * 8)); part != 0 {
			body[0] |= 1 << i
			body = append(body, part)
		}
	}
	return body
}

func spellFixtureGo(count byte) []byte {
	caster := spellFixturePackedGUID(policyMageGUID)
	body := append(caster, caster...)
	body = append(body, count)
	body = binary.LittleEndian.AppendUint32(body, policySheepID)
	body = binary.LittleEndian.AppendUint32(body, 0x900)
	body = binary.LittleEndian.AppendUint32(body, 0)
	body = append(body, 1)
	body = binary.LittleEndian.AppendUint64(body, policyWolfGUID)
	body = append(body, 0)
	body = binary.LittleEndian.AppendUint32(body, 2)
	body = append(body, spellFixturePackedGUID(policyWolfGUID)...)
	return binary.LittleEndian.AppendUint32(body, 75)
}

func spellFixtureAura(caster uint64) []byte {
	body := append(spellFixturePackedGUID(policyWolfGUID), 2)
	body = binary.LittleEndian.AppendUint32(body, policySheepID)
	body = append(body, 0xa3, 12, 1)
	body = append(body, spellFixturePackedGUID(caster)...)
	body = binary.LittleEndian.AppendUint32(body, 20000)
	return binary.LittleEndian.AppendUint32(body, 20000)
}

func spellFixturePolicy(label string) string {
	return fmt.Sprintf(`
local state = { results = "" }
return {
  on_event = function(event)
    if event.receipt then
      state.results = state.results .. "/" .. event.receipt.outcome .. ":" .. event.receipt.action_id
      state.last = event.receipt.payload
    end
    return true
  end,
  decide = function(frame)
    if frame.input.cast then
      return { action = { capability = "polymorph", sessionId = frame.session_id,
                          selfGuid = "%d", targetGuid = "%d", spellId = %d } }
    end
    return { results = state.results, revisionLabel = %q, last = state.last }
  end,
  export_state = function() return { version = 1, data = state } end,
  import_state = function(checkpoint) state = checkpoint.data; return true end,
}`, policyMageGUID, policyWolfGUID, policySheepID, label)
}

type policySpellWrite struct {
	opcode uint32
	body   []byte
	origin modsession.WriteOrigin
}

func startPackagedPolicySpell(t *testing.T) (*Manager, context.Context, <-chan policySpellWrite) {
	t.Helper()
	return startPackagedPolicySpellWith(t, &policySpellRunner{}, policySheepID)
}

func startPackagedPolicySpellWith(t *testing.T, runner *policySpellRunner, learnedSpell uint32) (*Manager, context.Context, <-chan policySpellWrite) {
	t.Helper()
	binDir := os.Getenv("BUDDY_ACTIONS_PACKAGED_BIN_DIR")
	if binDir == "" {
		t.Skip("set BUDDY_ACTIONS_PACKAGED_BIN_DIR to the directory containing a native buddy-actions build")
	}
	binaryPath, err := filepath.Abs(filepath.Join(binDir, "buddy-actions"))
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(binaryPath); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		t.Fatalf("packaged Actions executable %s is unavailable: %v", binaryPath, err)
	}
	runner.binary = binaryPath
	runner.peers = make(map[string]*grpc.Server)
	manager := New(packagedPolicySpellRegistry(t), runner, nil)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	writes := make(chan policySpellWrite, 16)
	err = manager.Start(ctx, modsession.Session{
		Identity: modsession.Identity{CharacterGUID: policyMageGUID, CharacterName: "Fixture"},
		Clock:    func() uint32 { return 12345 },
		SendPacket: func(ctx context.Context, code uint32, body []byte) error {
			select {
			case writes <- policySpellWrite{code, bytes.Clone(body), modsession.WriteOriginFromContext(ctx)}:
				return nil
			default:
				return errors.New("too many writes for offline fixture")
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Only the packet queue can establish the packaged module's learned spell.
	book := binary.LittleEndian.AppendUint32([]byte{0, 1, 0}, learnedSpell)
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGInitialSpells, append(book, 0, 0, 0, 0))
	return manager, ctx, writes
}

func deliverPolicySpellPacket(t *testing.T, ctx context.Context, manager *Manager, code uint16, body []byte) {
	t.Helper()
	if err := manager.Publish(code, body); err != nil {
		t.Fatal(err)
	}
	if err := manager.FlushPackets(ctx); err != nil {
		t.Fatal(err)
	}
}

func readPolicySpellCast(t *testing.T, ctx context.Context, writes <-chan policySpellWrite, origin modsession.WriteOrigin) policySpellWrite {
	t.Helper()
	var cast policySpellWrite
	select {
	case cast = <-writes:
	case <-ctx.Done():
		t.Fatal("no native cast reached the offline transport")
	}
	wantBody := []byte{1, 118, 0, 0, 0, 0, 2, 0, 0, 0}
	wantBody = append(wantBody, spellFixturePackedGUID(policyWolfGUID)...)
	if cast.opcode != opcode.CMSGCastSpell || !bytes.Equal(cast.body, wantBody) || cast.origin != origin {
		t.Fatalf("native cast = opcode %#x body %x origin %+v, want %#x %x %+v", cast.opcode, cast.body, cast.origin,
			opcode.CMSGCastSpell, wantBody, origin)
	}
	return cast
}

func TestPackagedActionsPolicySpellAttribution(t *testing.T) {
	manager, ctx, writes := startPackagedPolicySpell(t)
	nativeStatus := func() *actionsv1.State {
		t.Helper()
		raw, err := manager.InvokeJSON(ctx, "buddy-actions.status", nil)
		if err != nil {
			t.Fatal(err)
		}
		state := &actionsv1.State{}
		if err := protojson.Unmarshal(raw, state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	policy, err := manager.StartPolicy(ctx, spellFixturePolicy("before"))
	if err != nil {
		t.Fatal(err)
	}
	old := policy.vm.Status()
	_, receipt, err := policy.Step(ctx, map[string]any{"cast": true}, nil)
	if err != nil || receipt == nil || receipt.ActionID != 1 || receipt.SessionID != old.SessionID ||
		receipt.Outcome != behavior.ActionTransportAccepted {
		t.Fatalf("packaged Polymorph transport receipt: %+v, %v", receipt, err)
	}
	wantOrigin := modsession.WriteOrigin{SessionID: old.SessionID, Module: "buddy-actions", Writer: "lua",
		PolicyRevision: old.Revision, Generation: old.Epoch, ActionID: "1"}
	cast := readPolicySpellCast(t, ctx, writes, wantOrigin)

	revision, err := policy.Reload(ctx, spellFixturePolicy("after"), old.Revision)
	if err != nil {
		t.Fatal(err)
	}
	current := policy.vm.Status()
	if current.SessionID != old.SessionID || current.Revision != revision || current.Revision == old.Revision ||
		current.Epoch == old.Epoch || current.Pending != 1 {
		t.Fatalf("original cast reservation lost during reload: %+v", current)
	}
	checkResults := func(results string, pending int) map[string]any {
		t.Helper()
		out, extra, err := policy.Step(ctx, nil, nil)
		if err != nil || extra != nil || out["results"] != results || out["revisionLabel"] != "after" ||
			policy.vm.Status().Pending != pending {
			t.Fatalf("policy effect readback = %+v, %+v, %v, status %+v", out, extra, err, policy.vm.Status())
		}
		last, _ := out["last"].(map[string]any)
		return last
	}
	const transport = "/transport_accepted:1"
	checkResults(transport, 1)
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGSpellGo, spellFixtureGo(cast.body[0]))
	if op := nativeStatus().GetLastSpellOperation(); !op.GetServerCastGo() || op.GetEffectObserved() {
		t.Fatalf("native GO must be parsed without proving the aura: %v", op)
	}
	checkResults(transport, 1)

	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGAuraUpdate, spellFixtureAura(policyMageGUID+1))
	if op := nativeStatus().GetLastSpellOperation(); op.GetEffectObserved() {
		t.Fatalf("foreign aura settled the cast: %v", op)
	}
	checkResults(transport, 1)

	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGAuraUpdate, spellFixtureAura(policyMageGUID))
	if op := nativeStatus().GetLastSpellOperation(); !op.GetEffectObserved() || op.GetPhase() != "confirmed_aura" {
		t.Fatalf("matched native aura failed to settle the cast: %v", op)
	}
	effect := checkResults(transport+"/confirmed:1", 0)
	if effect["capability"] != "polymorph" || effect["spell_id"] != float64(policySheepID) ||
		effect["target_guid"] != fmt.Sprint(policyWolfGUID) || effect["phase"] != "confirmed_aura" ||
		effect["transport_written"] != true || effect["server_cast_go"] != true || effect["effect_observed"] != true {
		t.Fatalf("policy did not receive the typed Polymorph effect: %+v", effect)
	}
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGAuraUpdate, spellFixtureAura(policyMageGUID))
	checkResults(transport+"/confirmed:1", 0)

	select {
	case extra := <-writes:
		t.Fatalf("unexpected additional write: %+v", extra)
	default:
	}
}

func TestPackagedActionsTransferSettlesPriorVisitCast(t *testing.T) {
	manager, ctx, writes := startPackagedPolicySpell(t)
	policy, err := manager.StartPolicy(ctx, spellFixturePolicy("transfer"))
	if err != nil {
		t.Fatal(err)
	}
	current := policy.vm.Status()
	_, receipt, err := policy.Step(ctx, map[string]any{"cast": true}, nil)
	if err != nil || receipt == nil || receipt.Outcome != behavior.ActionTransportAccepted {
		t.Fatalf("prior visit cast = %+v, %v", receipt, err)
	}
	readPolicySpellCast(t, ctx, writes, modsession.WriteOrigin{
		SessionID: current.SessionID, Module: "buddy-actions", Writer: "lua",
		PolicyRevision: current.Revision, Generation: current.Epoch, ActionID: "1",
	})

	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGTransferPending, make([]byte, 4))
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGNewWorld, make([]byte, 20))
	out, other, err := policy.Step(ctx, nil, nil)
	last, _ := out["last"].(map[string]any)
	if err != nil || other != nil || out["results"] != "/transport_accepted:1/settled_unverified:1" ||
		last["phase"] != "world_transfer_outcome_unconfirmed" || last["effect_observed"] != false ||
		policy.vm.Status().Pending != 0 {
		t.Fatalf("retired visit left a pending cast or invented an effect: %+v %+v %v", out, other, err)
	}
	noPackagedWrites(t, writes)
}

func TestPackagedActionsPublicSpellHasModuleOrigin(t *testing.T) {
	manager, ctx, writes := startPackagedPolicySpell(t)
	policy, err := manager.StartPolicy(ctx, spellFixturePolicy("idle"))
	if err != nil {
		t.Fatal(err)
	}
	current := policy.vm.Status()
	params := []byte(fmt.Sprintf(`{"sessionId":%q,"targetGuid":%q,"spellId":%d}`,
		current.SessionID, fmt.Sprint(policyWolfGUID), policySheepID))
	raw, err := manager.InvokeJSON(ctx, "buddy-actions.polymorph", params)
	if err != nil {
		t.Fatal(err)
	}
	state := &actionsv1.State{}
	if err := protojson.Unmarshal(raw, state); err != nil {
		t.Fatal(err)
	}
	if op := state.GetLastSpellOperation(); !op.GetTransportWritten() || op.GetEffectObserved() {
		t.Fatalf("public Polymorph did not write an unsettled cast: %v", op)
	}
	origin := modsession.WriteOrigin{SessionID: current.SessionID, Module: "buddy-actions", Writer: "module"}
	readPolicySpellCast(t, ctx, writes, origin)
	if got := policy.vm.Status(); got.Pending != 0 || got.Buffered != 0 {
		t.Fatalf("public call entered the Lua action ledger: %+v", got)
	}
}
