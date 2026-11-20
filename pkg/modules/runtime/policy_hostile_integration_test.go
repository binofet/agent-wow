package modrt

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/modules/runner"
	"github.com/agent-wow/agent-wow/pkg/modules/session"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	fixtureSmite   = uint32(585)
	fixtureSap     = uint32(6770)
	fixtureStealth = uint32(1784)
	fixtureDagger  = uint32(2092)
)

type packagedHostileWorld struct {
	policySpellWorld
	rogue    bool
	attacker atomic.Bool
	stance   atomic.Uint32
}

func (w *packagedHostileWorld) Snapshot(ctx context.Context, req *worldv1.SnapshotRequest) (*worldv1.SnapshotResponse, error) {
	view, err := w.policySpellWorld.Snapshot(ctx, req)
	if err != nil {
		return nil, err
	}
	view.Self.Level, view.Self.LevelKnown = 30, true
	view.Self.Power, view.Self.MaxPower = 500, 500
	view.Self.ChannelSpellKnown, view.Self.ChannelObjectKnown = true, true
	if w.rogue {
		view.Self.ClassId, view.Self.PowerType = 4, 3
		view.Self.Power, view.Self.MaxPower = 100, 100
		view.Self.ShapeshiftForm = w.stance.Load()
		view.Self.Mainhand = &worldv1.EquippedWeapon{
			SlotKnown: true, ItemGuid: 0x4000000000000001, Entry: fixtureDagger, MetadataKnown: true, DurabilityKnown: true,
		}
	} else {
		view.Self.ClassId = 5
	}
	if target := view.RequestedCreature; target != nil {
		target.X, target.Level, target.LevelKnown, target.MaxHealth = 4, 10, true, 100
		if w.attacker.Load() {
			// A fresh attack relation binds hostility to the exact requested NPC.
			target.AttackVictimGuid, target.AttackVictimKnown, target.AttackAgeMs = policyMageGUID, true, 10
			target.TargetGuid, target.TargetKnown = policyMageGUID, true
		}
	}
	return view, nil
}

type packagedHostileRunner struct {
	policySpellRunner
	world *packagedHostileWorld
}

func (r *packagedHostileRunner) Up(ctx context.Context, launch modrunner.Launch) error {
	if launch.Manifest.Name != "buddy-world" {
		return r.policySpellRunner.Up(ctx, launch)
	}
	listener, err := net.Listen("unix", filepath.Join(launch.SocketDir, "module.sock"))
	if err != nil {
		return err
	}
	server := grpc.NewServer()
	worldv1.RegisterBuddyWorldServer(server, r.world)
	ready := health.NewServer()
	ready.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(server, ready)
	r.peers[launch.Manifest.Name] = server
	go server.Serve(listener)
	return nil
}

func startPackagedHostile(t *testing.T, rogue bool, spells ...uint32) (*Manager, context.Context, <-chan policySpellWrite, *packagedHostileWorld) {
	t.Helper()
	binDir := os.Getenv("BUDDY_ACTIONS_PACKAGED_BIN_DIR")
	if binDir == "" {
		t.Skip("set BUDDY_ACTIONS_PACKAGED_BIN_DIR to a native buddy-actions build with the hosted hostile spell bridge")
	}
	binaryPath, err := filepath.Abs(filepath.Join(binDir, "buddy-actions"))
	if err != nil {
		t.Fatal(err)
	}
	world := &packagedHostileWorld{rogue: rogue}
	runner := &packagedHostileRunner{
		policySpellRunner: policySpellRunner{binary: binaryPath, peers: make(map[string]*grpc.Server)},
		world:             world,
	}
	manager := New(packagedPolicySpellRegistry(t), runner, nil)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	writes := make(chan policySpellWrite, 16)
	if err := manager.Start(ctx, modsession.Session{
		Identity: modsession.Identity{CharacterGUID: policyMageGUID, CharacterName: "Fixture"},
		Clock:    func() uint32 { return 12345 },
		SendPacket: func(ctx context.Context, code uint32, body []byte) error {
			select {
			case writes <- policySpellWrite{code, bytes.Clone(body), modsession.WriteOriginFromContext(ctx)}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	book := binary.LittleEndian.AppendUint16([]byte{0}, uint16(len(spells)))
	for _, spell := range spells {
		book = binary.LittleEndian.AppendUint32(book, spell)
		book = append(book, 0, 0)
	}
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGInitialSpells, append(book, 0, 0))
	return manager, ctx, writes, world
}

func packagedHostilePolicy(capability string, spell uint32) string {
	return fmt.Sprintf(`
local receipts = ""
return {
  on_event = function(event)
    if event.receipt then
      receipts = receipts .. "/" .. event.receipt.outcome .. ":" .. event.receipt.action_id
    end
    return true
  end,
  decide = function(frame)
    if frame.input.cast then
      return { action = { capability = %q, sessionId = frame.session_id,
                          selfGuid = "%d", targetGuid = frame.input.targetGuid or "%d", spellId = %d } }
    end
    return { receipts = receipts }
  end,
  export_state = function() return { version = 1, data = { receipts = receipts } } end,
  import_state = function(checkpoint) receipts = checkpoint.data.receipts; return true end,
}`, capability, policyMageGUID, policyWolfGUID, spell)
}

func fixtureHostileGo(count byte, spell uint32) []byte {
	body := spellFixtureGo(count)
	spellOffset := 2*len(spellFixturePackedGUID(policyMageGUID)) + 1
	binary.LittleEndian.PutUint32(body[spellOffset:], spell)
	return body
}

func fixtureHostileDamage(target uint64) []byte {
	body := append(spellFixturePackedGUID(target), spellFixturePackedGUID(policyMageGUID)...)
	tail := make([]byte, 32)
	binary.LittleEndian.PutUint32(tail, fixtureSmite)
	binary.LittleEndian.PutUint32(tail[4:], 12)
	tail[12] = 2 // Holy school
	return append(body, tail...)
}

func fixtureSapAura(target uint64, spell uint32, flags byte) []byte {
	body := append(spellFixturePackedGUID(target), 2)
	body = binary.LittleEndian.AppendUint32(body, spell)
	body = append(body, flags, 30, 1)
	if flags&8 == 0 {
		body = append(body, spellFixturePackedGUID(policyMageGUID)...)
	}
	body = binary.LittleEndian.AppendUint32(body, 20000)
	return binary.LittleEndian.AppendUint32(body, 20000)
}

func checkPackagedWrite(t *testing.T, ctx context.Context, writes <-chan policySpellWrite, code uint32, body []byte, origin modsession.WriteOrigin) policySpellWrite {
	t.Helper()
	select {
	case got := <-writes:
		if got.opcode != code || !bytes.Equal(got.body, body) || got.origin != origin {
			t.Fatalf("native write = %#x %x %+v, want %#x %x %+v", got.opcode, got.body, got.origin, code, body, origin)
		}
		return got
	case <-ctx.Done():
		t.Fatalf("offline transport did not observe opcode %#x: %v", code, ctx.Err())
		return policySpellWrite{}
	}
}

func noPackagedWrites(t *testing.T, writes <-chan policySpellWrite) {
	t.Helper()
	select {
	case extra := <-writes:
		t.Fatalf("unexpected wire traffic: %#x %x %+v", extra.opcode, extra.body, extra.origin)
	default:
	}
}

func packagedHostileEffect(t *testing.T, ctx context.Context, manager *Manager) *actionsv1.SpellOperation {
	t.Helper()
	raw, err := manager.InvokeJSON(ctx, "buddy-actions.status", nil)
	if err != nil {
		t.Fatal(err)
	}
	state := &actionsv1.State{}
	if err := protojson.Unmarshal(raw, state); err != nil {
		t.Fatal(err)
	}
	return state.GetLastSpellOperation()
}

func checkHostileReceipt(t *testing.T, ctx context.Context, policy *SessionPolicy, want string, pending int) {
	t.Helper()
	got, extra, err := policy.Step(ctx, nil, nil)
	if err != nil || extra != nil || got["receipts"] != want || policy.vm.Status().Pending != pending {
		t.Fatalf("observed Lua receipt sequence: %+v %+v %v (status %+v)", got, extra, err, policy.vm.Status())
	}
}

func startExactHostileCast(t *testing.T, ctx context.Context, policy *SessionPolicy, writes <-chan policySpellWrite, spell uint32, id uint64) policySpellWrite {
	t.Helper()
	current := policy.vm.Status()
	_, receipt, err := policy.Step(ctx, map[string]any{"cast": true}, nil)
	if err != nil || receipt == nil || receipt.ActionID != id || receipt.SessionID != current.SessionID ||
		receipt.Outcome != behavior.ActionTransportAccepted {
		t.Fatalf("cast transport receipt: %+v %v", receipt, err)
	}
	body := binary.LittleEndian.AppendUint32([]byte{1}, spell)
	body = append(body, 0, 2, 0, 0, 0)
	body = append(body, spellFixturePackedGUID(policyWolfGUID)...)
	return checkPackagedWrite(t, ctx, writes, opcode.CMSGCastSpell, body, modsession.WriteOrigin{
		SessionID: current.SessionID, Module: "buddy-actions", Writer: "lua",
		PolicyRevision: current.Revision, Generation: current.Epoch, ActionID: strconv.FormatUint(id, 10),
	})
}

func TestPackagedActionsPolicyExactOffense(t *testing.T) {
	manager, ctx, writes, world := startPackagedHostile(t, false, fixtureSmite)
	policy, err := manager.StartPolicy(ctx, packagedHostilePolicy("offense", fixtureSmite))
	if err != nil {
		t.Fatal(err)
	}
	_, rejected, err := policy.Step(ctx, map[string]any{"cast": true, "targetGuid": fmt.Sprint(policyWolfGUID + 1)}, nil)
	if err != nil || rejected == nil || rejected.ActionID != 1 || rejected.Outcome != behavior.ActionNotDispatched {
		t.Fatalf("unobserved target was not definitely rejected: %+v %v", rejected, err)
	}
	_, rejected, err = policy.Step(ctx, map[string]any{"cast": true}, nil)
	if err != nil || rejected == nil || rejected.ActionID != 2 || rejected.Outcome != behavior.ActionNotDispatched {
		t.Fatalf("observed target without a hostile relation was not definitely rejected: %+v %v", rejected, err)
	}
	noPackagedWrites(t, writes)

	world.attacker.Store(true)
	cast := startExactHostileCast(t, ctx, policy, writes, fixtureSmite, 3)
	const sent = "/not_dispatched:1/not_dispatched:2/transport_accepted:3"
	checkHostileReceipt(t, ctx, policy, sent, 1)
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGSpellGo, fixtureHostileGo(cast.body[0], fixtureSmite))
	if op := packagedHostileEffect(t, ctx, manager); !op.GetServerCastGo() || op.GetEffectObserved() {
		t.Fatalf("exact offense GO must await damage: %v", op)
	}
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGSpellnonmeleedamagelog, fixtureHostileDamage(policyWolfGUID+1))
	checkHostileReceipt(t, ctx, policy, sent, 1)
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGSpellnonmeleedamagelog, fixtureHostileDamage(policyWolfGUID))
	checkHostileReceipt(t, ctx, policy, sent+"/confirmed:3", 0)
	if op := packagedHostileEffect(t, ctx, manager); op.GetCapability() != "offense" ||
		op.GetSpellId() != fixtureSmite || op.GetTargetGuid() != policyWolfGUID ||
		!op.GetEffectObserved() || op.GetPhase() != "confirmed_effect" {
		t.Fatalf("native exact damage did not settle the offense: %v", op)
	}
	noPackagedWrites(t, writes)
}

func primePackagedSap(t *testing.T, ctx context.Context, manager *Manager, writes <-chan policySpellWrite, sessionID string) {
	t.Helper()
	params := []byte(fmt.Sprintf(`{"sessionId":%q,"targetGuid":"%d","spellId":%d}`, sessionID, policyWolfGUID, fixtureSap))
	probe := func(reason string, code uint32, body []byte) {
		t.Helper()
		raw, err := manager.InvokeJSON(ctx, "buddy-actions.sap", params)
		if err != nil {
			t.Fatal(err)
		}
		state := &actionsv1.State{}
		if err := protojson.Unmarshal(raw, state); err != nil || state.GetReason() != reason || state.GetCastPending() {
			t.Fatalf("ordinary Sap query did not stop at %s: %s %v", reason, raw, err)
		}
		checkPackagedWrite(t, ctx, writes, code, body, modsession.WriteOrigin{
			SessionID: sessionID, Module: "buddy-actions", Writer: "module",
		})
	}
	probe("sap_weapon_not_confirmed", opcode.CMSGItemQuerySingle, binary.LittleEndian.AppendUint32(nil, fixtureDagger))
	weapon := binary.LittleEndian.AppendUint32(nil, fixtureDagger)
	weapon = binary.LittleEndian.AppendUint32(weapon, 2)  // Weapon class
	weapon = binary.LittleEndian.AppendUint32(weapon, 15) // Dagger subclass
	weapon = append(weapon, 0, 0, 0, 0)
	weapon = append(weapon, "Dagger\x00\x00\x00\x00"...)
	fields := make([]byte, 88)
	binary.LittleEndian.PutUint32(fields[24:], 13) // One-hand inventory type
	weapon = append(weapon, fields...)
	weapon = append(weapon, make([]byte, 8+24+28+12+120+4+1+116)...)
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGItemQuerySingleResponse, weapon)

	const entry = uint32(555)
	query := binary.LittleEndian.AppendUint32(nil, entry)
	query = binary.LittleEndian.AppendUint64(query, policyWolfGUID)
	probe("sap_target_type_pending", opcode.CMSGCreatureQuery, query)
	template := binary.LittleEndian.AppendUint32(nil, entry)
	template = append(template, "Creature\x00\x00\x00\x00\x00\x00"...)
	tail := make([]byte, 77)
	binary.LittleEndian.PutUint32(tail[4:], 7) // Humanoid
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGCreatureQueryResponse, append(template, tail...))
}

func TestPackagedActionsPolicySapRequiresObservedStealth(t *testing.T) {
	manager, ctx, writes, world := startPackagedHostile(t, true, fixtureSap, fixtureStealth)
	catalog, _, err := manager.observedSession()
	if err != nil {
		t.Fatal(err)
	}
	primePackagedSap(t, ctx, manager, writes, catalog.SessionID)
	policy, err := manager.StartPolicy(ctx, packagedHostilePolicy("sap", fixtureSap))
	if err != nil {
		t.Fatal(err)
	}
	_, rejected, err := policy.Step(ctx, map[string]any{"cast": true}, nil)
	if err != nil || rejected == nil || rejected.ActionID != 1 || rejected.Outcome != behavior.ActionNotDispatched {
		t.Fatalf("Sap without observed Stealth was not definitely rejected: %+v %v", rejected, err)
	}
	// In particular, the known ordinary preparation spell cannot be sent in
	// place of the requested Sap while the trusted invocation owns dispatch.
	noPackagedWrites(t, writes)

	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGAuraUpdate, fixtureSapAura(policyMageGUID, fixtureStealth, 0x39))
	world.stance.Store(30)
	cast := startExactHostileCast(t, ctx, policy, writes, fixtureSap, 2)
	const sent = "/not_dispatched:1/transport_accepted:2"
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGSpellGo, fixtureHostileGo(cast.body[0], fixtureSap))
	if op := packagedHostileEffect(t, ctx, manager); !op.GetServerCastGo() || op.GetEffectObserved() {
		t.Fatalf("exact Sap GO must await an aura: %v", op)
	}
	checkHostileReceipt(t, ctx, policy, sent, 1)
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGAuraUpdate, fixtureSapAura(policyWolfGUID+1, fixtureSap, 0xa3))
	checkHostileReceipt(t, ctx, policy, sent, 1)
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGAuraUpdate, fixtureSapAura(policyWolfGUID, fixtureSap, 0xa3))
	checkHostileReceipt(t, ctx, policy, sent+"/confirmed:2", 0)
	if op := packagedHostileEffect(t, ctx, manager); op.GetCapability() != "sap" ||
		op.GetSpellId() != fixtureSap || op.GetTargetGuid() != policyWolfGUID ||
		!op.GetEffectObserved() || op.GetPhase() != "confirmed_aura" {
		t.Fatalf("native exact aura did not settle Sap: %v", op)
	}
	noPackagedWrites(t, writes)
}
