package modrt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
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
	if policy.vm.Status().Pending != 0 {
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
	if receipt == nil || receipt.Outcome != behavior.ActionSettledUnverified || receipt.Payload["phase"] != "transport_unconfirmed_no_retry" || len(cb.writes) != 2 || cb.writes[1].Opcode != opcode.MSGCorpseQuery || len(cb.writes[1].Payload) != 0 || policy.vm.Status().Pending != 0 {
		t.Fatal(out, receipt, cb.writes, policy.vm.Status())
	}
	if _, err := policy.vm.Reload(context.Background(), source+"\n-- unknown query retained", policy.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if receipt, _ := step(); receipt != nil || len(cb.writes) != 2 {
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
