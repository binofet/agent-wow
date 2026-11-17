package modrt

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
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
	policyPriestGUID = uint64(22)
	policyLesserHeal = uint32(2050)
)

type packagedHealWorld struct {
	policySpellWorld
	health atomic.Uint32
}

func (w *packagedHealWorld) Snapshot(context.Context, *worldv1.SnapshotRequest) (*worldv1.SnapshotResponse, error) {
	return &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{
		Guid: policyPriestGUID, Present: true, ClassKnown: true, ClassId: 5,
		MapKnown: true, PositionKnown: true, PositionRevision: 1, MovementFlagsKnown: true,
		AliveKnown: true, Alive: true, HealthKnown: true, Health: w.health.Load(), MaxHealth: 100,
		PowerKnown: true, Power: 128, MaxPower: 128, UnitFlagsKnown: true,
		ShapeshiftFormKnown: true, AuraStateKnown: true, SpeedsKnown: true, WalkSpeed: 2.5, RunSpeed: 7,
	}}, nil
}

type packagedHealRunner struct {
	policySpellRunner
	world *packagedHealWorld
}

func (r *packagedHealRunner) Up(ctx context.Context, launch modrunner.Launch) error {
	if launch.Manifest.Name == "buddy-actions" {
		launch.Environment = maps.Clone(launch.Environment)
		launch.Environment["BUDDY_HOST_HEALING_OWNED"] = "true"
	}
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

func startPackagedPolicyHeal(t *testing.T) (*Manager, context.Context, <-chan policySpellWrite, *packagedHealWorld) {
	t.Helper()
	binDir := os.Getenv("BUDDY_ACTIONS_PACKAGED_BIN_DIR")
	if binDir == "" {
		t.Skip("set BUDDY_ACTIONS_PACKAGED_BIN_DIR to the directory containing a native buddy-actions build with typed heal receipts")
	}
	binaryPath, err := filepath.Abs(filepath.Join(binDir, "buddy-actions"))
	if err != nil {
		t.Fatal(err)
	}
	world := &packagedHealWorld{}
	world.health.Store(60)
	runner := &packagedHealRunner{
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
	writes := make(chan policySpellWrite, 4)
	if err := manager.Start(ctx, modsession.Session{
		Identity: modsession.Identity{CharacterGUID: policyPriestGUID, CharacterName: "Fixture"},
		Clock:    func() uint32 { return 12345 },
		SendPacket: func(ctx context.Context, code uint32, body []byte) error {
			select {
			case writes <- policySpellWrite{code, bytes.Clone(body), modsession.WriteOriginFromContext(ctx)}:
				return nil
			default:
				return errors.New("too many heal writes for offline fixture")
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	book := binary.LittleEndian.AppendUint32([]byte{0, 1, 0}, policyLesserHeal)
	book = append(book, 0, 0, 0, 0)
	deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGInitialSpells, book)
	raw, err := manager.InvokeJSON(ctx, "buddy-actions.healing_options", nil)
	if err != nil {
		t.Fatal(err)
	}
	options := &actionsv1.HealingOptionsResponse{}
	if err := protojson.Unmarshal(raw, options); err != nil || !options.GetHostOwned() ||
		!options.GetCaster().GetReady() {
		t.Fatalf("fixture healer is not ready under host control: %s, %v", raw, err)
	}
	return manager, ctx, writes, world
}

func packagedHealPolicy(label string) string {
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
      return { action = { capability = "heal", sessionId = frame.session_id,
                          selfGuid = "%d", targetGuid = "%d", spellId = %d } }
    end
    return { results = state.results, revisionLabel = %q, last = state.last }
  end,
  export_state = function() return { version = 1, data = state } end,
  import_state = function(checkpoint) state = checkpoint.data; return true end,
}`, policyPriestGUID, policyPriestGUID, policyLesserHeal, label)
}

// Wraps the actual shipped heal policy to record receipts for assertions. The
// native observation driver and base Lua policy still select the action.
func packagedObservedHealPolicy(t *testing.T, label string) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "behaviors", "healing.lua"))
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`
local healing = (function()
%s
end)()
local results, last = "", nil
return {
  on_event = function(event)
    if event.receipt then
      results = results .. "/" .. event.receipt.outcome .. ":" .. event.receipt.action_id
      last = event.receipt.payload
    end
    return healing.on_event(event)
  end,
  decide = function(frame)
    if frame.input.fixtureRead == true then
      return {results = results, last = last, revisionLabel = %q}
    end
    return healing.decide(frame)
  end,
  export_state = function()
    return {version = 1, data = {base = healing.export_state(), results = results, last = last}}
  end,
  import_state = function(checkpoint)
    results, last = checkpoint.data.results, checkpoint.data.last
    return healing.import_state(checkpoint.data.base)
  end,
}`, source, label)
}

func waitForPackagedHealTransport(t *testing.T, ctx context.Context, policy *SessionPolicy) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		receipts := policy.vm.Outstanding()
		if len(receipts) == 1 && receipts[0].ActionID == 1 && receipts[0].Outcome == behavior.ActionTransportAccepted {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no transport acceptance for observed heal: %+v", receipts)
		case <-ticker.C:
		}
	}
}

func packagedHealSpellGo(count byte) []byte {
	caster := spellFixturePackedGUID(policyPriestGUID)
	body := append(caster, caster...)
	body = append(body, count)
	body = binary.LittleEndian.AppendUint32(body, policyLesserHeal)
	body = binary.LittleEndian.AppendUint32(body, 0x900)
	body = binary.LittleEndian.AppendUint32(body, 0)
	body = append(body, 1)
	body = binary.LittleEndian.AppendUint64(body, policyPriestGUID)
	body = append(body, 0)
	body = binary.LittleEndian.AppendUint32(body, 2)
	body = append(body, spellFixturePackedGUID(policyPriestGUID)...)
	return binary.LittleEndian.AppendUint32(body, 75)
}

func packagedHealLog(caster, target uint64, spell, healing, overheal uint32) []byte {
	body := append(spellFixturePackedGUID(target), spellFixturePackedGUID(caster)...)
	body = binary.LittleEndian.AppendUint32(body, spell)
	body = binary.LittleEndian.AppendUint32(body, healing)
	body = binary.LittleEndian.AppendUint32(body, overheal)
	body = binary.LittleEndian.AppendUint32(body, 0) // Absorb
	return append(body, 0, 0)                        // Noncritical, unused
}

func TestPackagedActionsPolicyExactHeal(t *testing.T) {
	for _, positive := range []bool{true, false} {
		name := "zero effective stays unconfirmed"
		if positive {
			name = "correlated positive effect across reload"
		}
		t.Run(name, func(t *testing.T) {
			manager, ctx, writes, world := startPackagedPolicyHeal(t)
			var policy *SessionPolicy
			if positive {
				if err := manager.StartObservedPolicy(ctx, packagedObservedHealPolicy(t, "before"),
					"healing=buddy-actions.healing_options"); err != nil {
					t.Fatal(err)
				}
				manager.mu.Lock()
				policy = manager.policy
				manager.mu.Unlock()
			} else {
				var err error
				policy, err = manager.StartPolicy(ctx, packagedHealPolicy("before"))
				if err != nil {
					t.Fatal(err)
				}
			}
			old := policy.vm.Status()
			if !positive {
				_, receipt, err := policy.Step(ctx, map[string]any{"cast": true}, nil)
				if err != nil || receipt == nil || receipt.ActionID != 1 || receipt.SessionID != old.SessionID ||
					receipt.Outcome != behavior.ActionTransportAccepted {
					t.Fatalf("packaged CastHeal transport receipt: %+v, %v", receipt, err)
				}
			}
			var cast policySpellWrite
			select {
			case cast = <-writes:
			case <-ctx.Done():
				t.Fatal("no native heal reached the offline transport")
			}
			want := binary.LittleEndian.AppendUint32([]byte{1}, policyLesserHeal)
			want = append(want, 0, 2, 0, 0, 0)
			want = append(want, spellFixturePackedGUID(policyPriestGUID)...)
			origin := modsession.WriteOrigin{SessionID: old.SessionID, Module: "buddy-actions", Writer: "lua",
				PolicyRevision: old.Revision, Generation: old.Epoch, ActionID: "1"}
			if cast.opcode != opcode.CMSGCastSpell || !bytes.Equal(cast.body, want) || cast.origin != origin {
				t.Fatalf("heal cast = %#x %x %+v, want %#x %x %+v", cast.opcode, cast.body, cast.origin,
					opcode.CMSGCastSpell, want, origin)
			}

			native := func() *actionsv1.SpellOperation {
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

			var revision string
			if positive {
				waitForPackagedHealTransport(t, ctx, policy)
				if op := native(); !op.GetTransportWritten() || op.GetServerCastGo() || op.GetEffectObserved() {
					t.Fatalf("observed policy cast was not in flight at reload: %v", op)
				}
				params, err := json.Marshal(map[string]string{
					"sessionId": old.SessionID, "characterGuid": fmt.Sprint(policyPriestGUID),
					"expectedRevision": old.Revision, "source": packagedObservedHealPolicy(t, "after"),
				})
				if err != nil {
					t.Fatal(err)
				}
				raw, err := manager.InvokeJSON(ctx, "session.reload_policy", params)
				if err != nil {
					t.Fatal(err)
				}
				var result struct {
					SessionID string `json:"sessionId"`
					Revision  string `json:"revision"`
				}
				if err := json.Unmarshal(raw, &result); err != nil || result.SessionID != old.SessionID {
					t.Fatalf("public reload response did not bind existing session: %s, %v", raw, err)
				}
				revision = result.Revision
			} else {
				var err error
				revision, err = policy.Reload(ctx, packagedHealPolicy("after"), old.Revision)
				if err != nil {
					t.Fatal(err)
				}
			}
			now := policy.vm.Status()
			if now.Revision != revision || now.Revision == old.Revision || now.Epoch == old.Epoch ||
				now.SessionID != old.SessionID || now.Pending != 1 {
				t.Fatalf("reload lost the exact heal reservation: %+v", now)
			}
			check := func(results string, pending int) map[string]any {
				t.Helper()
				out, extra, err := policy.Step(ctx, map[string]any{"fixtureRead": true}, nil)
				if err != nil || extra != nil || out["results"] != results || out["revisionLabel"] != "after" ||
					policy.vm.Status().Pending != pending {
					t.Fatalf("Lua heal readback = %+v, %+v, %v, status %+v", out, extra, err, policy.vm.Status())
				}
				last, _ := out["last"].(map[string]any)
				return last
			}

			const transport = "/transport_accepted:1"
			check(transport, 1)
			deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGSpellGo, packagedHealSpellGo(cast.body[0]))
			if op := native(); !op.GetServerCastGo() || op.GetEffectObserved() || op.GetSpellId() != policyLesserHeal ||
				op.GetTargetGuid() != policyPriestGUID {
				t.Fatalf("matching GO must retain the exact heal and await effect: %v", op)
			}
			check(transport, 1)
			for _, mismatch := range [][]byte{
				packagedHealLog(policyPriestGUID+1, policyPriestGUID, policyLesserHeal, 40, 0),
				packagedHealLog(policyPriestGUID, policyPriestGUID+1, policyLesserHeal, 40, 0),
				packagedHealLog(policyPriestGUID, policyPriestGUID, policyLesserHeal+1, 40, 0),
			} {
				deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGSpellheallog, mismatch)
				if op := native(); op.GetEffectObserved() {
					t.Fatalf("mismatched heal log settled the cast: %v", op)
				}
				check(transport, 1)
			}
			overheal := uint32(40)
			if positive {
				overheal = 5
				// The fixture now observes the restored health. The standing
				// policy sees no fresh injury once the exact receipt settles.
				world.health.Store(95)
			}
			log := packagedHealLog(policyPriestGUID, policyPriestGUID, policyLesserHeal, 40, overheal)
			deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGSpellheallog, log)
			if !positive {
				if op := native(); op.GetEffectObserved() || op.GetPhase() == "confirmed_heal" {
					t.Fatalf("zero effective healing confirmed the cast: %v", op)
				}
				check(transport+"/unknown:1", 1)
				// The first correlated zero-effective result consumed the native
				// receipt. A later same-tuple log cannot turn it into a success.
				deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGSpellheallog,
					packagedHealLog(policyPriestGUID, policyPriestGUID, policyLesserHeal, 40, 5))
				check(transport+"/unknown:1", 1)
				return
			}
			if op := native(); !op.GetEffectObserved() || op.GetPhase() != "confirmed_heal" {
				t.Fatalf("matching positive heal failed to settle the cast: %v", op)
			}
			effect := check(transport+"/confirmed:1", 0)
			if effect["capability"] != "heal" || effect["spell_id"] != float64(policyLesserHeal) ||
				effect["target_guid"] != fmt.Sprint(policyPriestGUID) || effect["phase"] != "confirmed_heal" ||
				effect["transport_written"] != true || effect["server_cast_go"] != true || effect["effect_observed"] != true {
				t.Fatalf("Lua did not receive the typed heal effect: %+v", effect)
			}
			deliverPolicySpellPacket(t, ctx, manager, opcode.SMSGSpellheallog, log)
			check(transport+"/confirmed:1", 0)
			select {
			case extra := <-writes:
				t.Fatalf("unexpected duplicate heal write: %+v", extra)
			default:
			}
		})
	}
}
