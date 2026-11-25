package modrt

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/types/known/emptypb"
)

func recoveryRuntimeSource(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "behaviors", "death-recovery.lua"))
	if err != nil {
		t.Fatal(err)
	}
	return "local r=(function()\n" + string(source) + "\nend)();local obs,seq\nreturn {on_event=function(e)r.on_event(e);if e.kind=='encounter_observation' then obs=e.value;seq=e.sequence end return true end,decide=function(f)return r.decide(f,obs,seq)end,export_state=r.export_state,import_state=r.import_state}"
}
func TestPolicyDeathRecoveryRealNativePacketsReloadAndUnknownCustody(t *testing.T) {
	m, native, cb := friendlyEncounterManager(t)
	cb.world.Self.Alive = false
	cb.world.Self.Health = 0
	if _, err := native.Recovery(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	source := recoveryRuntimeSource(t)
	policy, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer policy.close()
	step := func() (*behavior.ActionReceipt, map[string]any) {
		t.Helper()
		view, err := m.readPolicyObservation(context.Background(), friendlyEncounterSession, encounterObservationMethod)
		if err != nil {
			t.Fatal(err)
		}
		seq, err := policy.Enqueue(behavior.Event{Kind: "encounter_observation", Value: view})
		if err != nil {
			t.Fatal(err)
		}
		out, receipt, err := policy.Step(context.Background(), map[string]any{"observationSequence": strconv.FormatUint(seq, 10)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return receipt, out
	}
	receipt, out := step()
	if receipt == nil || receipt.Outcome != behavior.ActionSettledUnverified || receipt.Payload["transport_written"] != true || receipt.Payload["effect_observed"] != false || len(cb.writes) != 1 || cb.writes[0].Opcode != opcode.CMSGRepopRequest || len(cb.writes[0].Payload) != 1 || cb.writes[0].Payload[0] != 0 {
		t.Fatal(out, receipt, cb.writes)
	}
	if receipt, _ := step(); receipt != nil || policy.vm.Status().Pending != 0 {
		t.Fatal("release blocked unrelated global actions", policy.vm.Status())
	}
	if _, err := policy.vm.Reload(context.Background(), source+"\n-- same body edit", policy.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if receipt, _ := step(); receipt != nil || len(cb.writes) != 1 {
		t.Fatal("release replayed through reload", receipt, cb.writes)
	}
	cb.world.Self.Health = 1
	cb.world.Self.PlayerFlags = 0x10
	cb.world.Self.PositionRevision++
	// Actual callback entered but its outcome is unknown. The native per-death
	// query reservation survives while the global Lua spell ledger stays clear.
	send := m.session.SendPacket
	m.session.SendPacket = func(ctx context.Context, code uint32, p []byte) error {
		send(ctx, code, p)
		return errors.New("unknown socket result")
	}
	receipt, out = step()
	if receipt == nil || receipt.Outcome != behavior.ActionSettledUnverified || receipt.Payload["phase"] != "transport_unconfirmed_no_retry" || len(cb.writes) != 2 || cb.writes[1].Opcode != opcode.MSGCorpseQuery || len(cb.writes[1].Payload) != 0 {
		t.Fatal(out, receipt, cb.writes, policy.vm.Status())
	}
	if _, err := policy.vm.Reload(context.Background(), source+"\n-- unknown query retained", policy.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if receipt, _ := step(); receipt != nil || len(cb.writes) != 2 || policy.vm.Status().Pending != 0 {
		t.Fatal("unknown corpse query replayed", receipt, cb.writes)
	}
	state, _ := native.Status(context.Background(), &emptypb.Empty{})
	if state.Recovery == nil || !state.Recovery.CorpseQueryPending || state.Recovery.CorpseQueryKnown {
		t.Fatal("write invented corpse result", state)
	}
	cb.world.Self.Alive = true
	cb.world.Self.Health = 100
	cb.world.Self.PlayerFlags = 0
	step()
	m.session.SendPacket = send
	cb.world.Self.Alive = false
	cb.world.Self.Health = 0
	receipt, _ = step()
	if receipt == nil || len(cb.writes) != 3 || cb.writes[2].Opcode != opcode.CMSGRepopRequest {
		t.Fatal("new actual death remained bound to old query", receipt, cb.writes)
	}
}
func TestPolicyDeathRecoveryPacketFenceAndEpochValidation(t *testing.T) {
	for _, query := range []bool{false, true} {
		op := &policySpell{recovery: &policyRecovery{self: 22, epoch: 1, query: query}}
		code, body := uint32(opcode.CMSGRepopRequest), []byte{0}
		if query {
			code, body = opcode.MSGCorpseQuery, nil
		}
		if !op.matchesActionPacket(code, body) || op.matchesActionPacket(code, append(body, 1)) || op.matchesActionPacket(opcode.CMSGReclaimCorpse, body) {
			t.Fatal("death action packet fence lost", query)
		}
	}
	m, _, _ := friendlyEncounterManager(t)
	p, err := m.StartPolicy(context.Background(), `return {decide=function()return {}end,on_event=function()return true end,export_state=function()return {version=1,data={}}end,import_state=function()return true end}`)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	for _, action := range []map[string]any{{"sessionId": "foreign", "selfGuid": "22", "deathEpoch": "1"}, {"sessionId": friendlyEncounterSession, "selfGuid": "23", "deathEpoch": "1"}, {"sessionId": friendlyEncounterSession, "selfGuid": "22", "deathEpoch": "01"}} {
		if _, err := p.resolveDeathAction(action, "release_spirit"); err == nil {
			t.Fatal("foreign/noncanonical action admitted", action)
		}
	}
}

func TestPolicyDeathRecoveryDefiniteCoreRefusalAllowsSameDeathFreshRetry(t *testing.T) {
	m, native, cb := friendlyEncounterManager(t)
	cb.world.Self.Alive = false
	cb.world.Self.Health = 0
	if _, err := native.Recovery(context.Background(), &actionsv1.ToggleRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	policy, err := m.StartPolicy(context.Background(), recoveryRuntimeSource(t))
	if err != nil {
		t.Fatal(err)
	}
	defer policy.close()
	step := func() *behavior.ActionReceipt {
		t.Helper()
		view, err := m.readPolicyObservation(context.Background(), friendlyEncounterSession, encounterObservationMethod)
		if err != nil {
			t.Fatal(err)
		}
		seq, err := policy.Enqueue(behavior.Event{Kind: "encounter_observation", Value: view})
		if err != nil {
			t.Fatal(err)
		}
		_, r, err := policy.Step(context.Background(), map[string]any{"observationSequence": strconv.FormatUint(seq, 10)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, query := range []bool{false, true} {
		if query {
			cb.world.Self.Health = 1
			cb.world.Self.PlayerFlags = 0x10
			cb.world.Self.PositionRevision++
		}
		cb.reject = true
		r := step()
		state, err := native.Status(context.Background(), &emptypb.Empty{})
		if err != nil {
			t.Fatal(err)
		}
		if r == nil || r.Outcome != behavior.ActionNotDispatched || state.Recovery.ReleaseAttempted && !query || state.Recovery.CorpseQueryPending || state.LastTransportFailure == nil || state.LastTransportFailure.OutcomeUnknown {
			t.Fatal("definite core no-write poisoned the death", query, r, state)
		}
		before := len(cb.writes)
		cb.reject = false
		r = step()
		want := uint32(opcode.CMSGRepopRequest)
		if query {
			want = opcode.MSGCorpseQuery
		}
		if r == nil || r.Outcome != behavior.ActionSettledUnverified || len(cb.writes) != before+1 || cb.writes[before].Opcode != want {
			t.Fatal("same death fresh retry not dispatched", query, r, cb.writes)
		}
	}
}

func TestPolicyGhostRecoveryRealReclaimAndUnknownOutcomeWaitForActualLife(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(strconv.FormatBool(unknown), func(t *testing.T) {
			m, native, cb := friendlyEncounterManager(t)
			self := cb.world.Self
			self.Alive = false
			self.Health = 1
			self.PlayerFlags = 0x10
			self.PositionRevision = 2
			self.MovementFlags = 0x10000000
			self.SpeedsKnown = true
			self.RunSpeed = 8.75
			corpse := uint64(0xf101000000000001)
			cb.world.OwnCorpse = &worldv1.CorpseState{Guid: corpse, OwnerGuid: self.Guid, Present: true, MapKnown: true, PositionKnown: true, PositionRevision: 1, X: 10, FlagsKnown: true}
			source := recoveryRuntimeSource(t)
			p, err := m.StartPolicy(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			defer p.close()
			step := func() *behavior.ActionReceipt {
				t.Helper()
				view, err := m.readPolicyObservation(context.Background(), friendlyEncounterSession, encounterObservationMethod)
				if err != nil {
					t.Fatal(err)
				}
				seq, err := p.Enqueue(behavior.Event{Kind: "encounter_observation", Value: view})
				if err != nil {
					t.Fatal(err)
				}
				_, r, err := p.Step(context.Background(), map[string]any{"observationSequence": strconv.FormatUint(seq, 10)}, nil)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			r := step()
			if r == nil || len(cb.writes) != 1 || cb.writes[0].Opcode != opcode.MSGCorpseQuery {
				t.Fatal("default Lua did not query released ghost", r, cb.writes)
			}
			payload := binary.LittleEndian.AppendUint32([]byte{1}, 0)
			for _, v := range []float32{10, 0, 0} {
				payload = binary.LittleEndian.AppendUint32(payload, math.Float32bits(v))
			}
			payload = append(payload, make([]byte, 8)...)
			if _, err = native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.MSGCorpseQuery, Payload: payload}); err != nil {
				t.Fatal(err)
			}
			native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGCorpseReclaimDelay, Payload: make([]byte, 4)})
			send := m.session.SendPacket
			if unknown {
				m.session.SendPacket = func(ctx context.Context, code uint32, payload []byte) error {
					err := send(ctx, code, payload)
					if code == opcode.CMSGReclaimCorpse {
						return errors.New("actual reclaim write result unknown")
					}
					return err
				}
			}
			r = step()
			if r == nil || r.Outcome != behavior.ActionSettledUnverified || len(cb.writes) != 2 || cb.writes[1].Opcode != opcode.CMSGReclaimCorpse || len(cb.writes[1].Payload) != 8 || binary.LittleEndian.Uint64(cb.writes[1].Payload) != corpse || r.Payload["effect_observed"] != false {
				t.Fatal("exact typed reclaim did not reach real native wire", r, cb.writes)
			}
			if _, err = p.vm.Reload(context.Background(), source+"\n-- same-session death tuning", p.vm.Status().Revision); err != nil {
				t.Fatal(err)
			}
			if r = step(); r != nil || len(cb.writes) != 2 || p.vm.Status().Pending != 0 {
				t.Fatal("unconfirmed reclaim replayed or globally blocked", r, p.vm.Status(), cb.writes)
			}
			state, _ := native.Status(context.Background(), &emptypb.Empty{})
			if state.Recovery.AliveObserved || !state.Recovery.ReclaimAttempted || state.Recovery.ReclaimWritten == unknown {
				t.Fatal("transport invented resurrection or lost uncertainty", state)
			}
			self.Alive = true
			self.Health = 100
			self.PlayerFlags = 0
			self.MovementFlags = 0
			self.PositionRevision++
			step()
			state, _ = native.Status(context.Background(), &emptypb.Empty{})
			if !state.Recovery.AliveObserved {
				t.Fatal("actual life not observed", state)
			}
		})
	}
}

func TestPolicyGhostApproachAndStopRouteToActualNativeDescriptor(t *testing.T) {
	m, native, cb := friendlyEncounterManager(t)
	self := cb.world.Self
	self.Alive = false
	self.Health = 1
	self.PlayerFlags = 0x10
	self.PositionRevision = 2
	self.MovementFlags = 0x10000000
	self.SpeedsKnown = true
	self.RunSpeed = 8.75
	p, err := m.StartPolicy(context.Background(), recoveryRuntimeSource(t))
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	step := func() *behavior.ActionReceipt {
		t.Helper()
		v, e := m.readPolicyObservation(context.Background(), friendlyEncounterSession, encounterObservationMethod)
		if e != nil {
			t.Fatal(e)
		}
		seq, e := p.Enqueue(behavior.Event{Kind: "encounter_observation", Value: v})
		if e != nil {
			t.Fatal(e)
		}
		_, r, e := p.Step(context.Background(), map[string]any{"observationSequence": strconv.FormatUint(seq, 10)}, nil)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	if r := step(); r == nil || len(cb.writes) != 1 || cb.writes[0].Opcode != opcode.MSGCorpseQuery {
		t.Fatal(r, cb.writes)
	}
	payload := binary.LittleEndian.AppendUint32([]byte{1}, 0)
	for _, v := range []float32{10, 0, 0} {
		payload = binary.LittleEndian.AppendUint32(payload, math.Float32bits(v))
	}
	payload = append(payload, make([]byte, 8)...)
	if _, err = native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.MSGCorpseQuery, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	action := func(capability string) string {
		return `return {on_event=function()return true end,export_state=function()return {version=1,data={}}end,import_state=function()return true end,decide=function(f)return {action={capability="` + capability + `",sessionId=f.session_id,selfGuid="22",deathEpoch="1",expectedPositionRevision="2",corpseQueryRevision="1",rangeYards=5}}end}`
	}
	if _, err = p.Reload(context.Background(), action("approach_corpse"), p.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	r := step()
	// No private client assets are used. A real native geometry precondition is
	// the expected result, and is distinct from a missing/unimplemented RPC.
	if r == nil || r.Outcome != behavior.ActionNotDispatched || r.Payload["grpc_status"] != "FailedPrecondition" || r.Payload["grpc_message"] != "verified corpse route unavailable" || len(cb.writes) != 1 {
		t.Fatal("approach alias/descriptor did not reach native executor", r, cb.writes)
	}
	if _, err = p.Reload(context.Background(), action("stop_corpse"), p.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	r = step()
	if r == nil || r.Outcome != behavior.ActionSettledUnverified || r.Payload["native_phase"] != "stopped" || r.Payload["effect_observed"] != false || len(cb.writes) != 1 {
		t.Fatal("stop mode did not route through exact native method", r, cb.writes)
	}
}

func TestPolicyDeathDungeonEntryUsesExistingTypedRouteAndExactPacketFence(t *testing.T) {
	m, _, cb := friendlyEncounterManager(t)
	p, err := m.StartPolicy(context.Background(), `return {decide=function()return {}end,on_event=function()return true end,export_state=function()return {version=1,data={}}end,import_state=function()return true end}`)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	action := map[string]any{"sessionId": friendlyEncounterSession, "selfGuid": "22", "deathEpoch": "1", "expectedPositionRevision": "1", "corpseQueryRevision": "1", "rangeYards": .25, "triggerId": float64(123)}
	d, err := p.resolveDeathAction(action, "enter_corpse_instance")
	if err != nil || d.Send == nil {
		t.Fatal(d, err)
	}
	d.Send(context.Background()) // The actual live-body native guard refuses this synthetic living actor.
	if len(cb.writes) != 0 {
		t.Fatal("typed resolution bypassed native ghost guard", cb.writes)
	}
	delete(action, "triggerId")
	if _, err = p.resolveDeathAction(action, "enter_corpse_instance"); err == nil {
		t.Fatal("missing entrance accepted")
	}
	op := &policySpell{capability: "enter_corpse_instance", recovery: &policyRecovery{self: 22, epoch: 1, trigger: 123}, movement: &policyMovement{self: 22}}
	if !op.matchesActionPacket(opcode.CMSGAreatrigger, binary.LittleEndian.AppendUint32(nil, 123)) || op.matchesActionPacket(opcode.CMSGAreatrigger, binary.LittleEndian.AppendUint32(nil, 124)) || op.matchesActionPacket(opcode.CMSGReclaimCorpse, make([]byte, 8)) {
		t.Fatal("exact dungeon trigger fence lost")
	}
}
