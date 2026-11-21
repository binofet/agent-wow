package modrt

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"testing"

	buddyactions "github.com/agent-wow/agent-wow/modules/buddy-actions"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func runtimeResurrectionOffer(t *testing.T, native *buddyactions.Service, caster uint64) {
	t.Helper()
	p := binary.LittleEndian.AppendUint64(nil, caster)
	p = binary.LittleEndian.AppendUint32(p, 1)
	p = append(p, 0, 0, 0)
	if _, err := native.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGResurrectRequest, Payload: p}); err != nil {
		t.Fatal(err)
	}
}

func offerPolicyStep(t *testing.T, m *Manager, p *SessionPolicy) (*behavior.ActionReceipt, map[string]any) {
	t.Helper()
	view, err := m.readPolicyObservation(context.Background(), friendlyEncounterSession, encounterObservationMethod)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := p.Enqueue(behavior.Event{Kind: "encounter_observation", Value: view})
	if err != nil {
		t.Fatal(err)
	}
	out, receipt, err := p.Step(context.Background(), map[string]any{"observationSequence": strconv.FormatUint(seq, 10)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return receipt, out
}

func TestPolicyResurrectionOfferCurrentLuaChoosesAndUnknownResponseSurvivesReload(t *testing.T) {
	m, native, cb := friendlyEncounterManager(t)
	cb.world.Self.Alive, cb.world.Self.Health = false, 0
	runtimeResurrectionOffer(t, native, 7)
	// The default policy does not select an unsolicited nonparty offer. Keep
	// this fixture's corpse release delayed while the agent edits that choice.
	source := strings.Replace(recoveryRuntimeSource(t), "release_delay_ms=0", "release_delay_ms=3600000", 1)
	policy, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer policy.close()
	if r, out := offerPolicyStep(t, m, policy); r != nil || len(cb.writes) != 0 || out["phase"] != "release_delay" {
		t.Fatal("native or old policy selected nonparty offer", r, out, cb.writes)
	}
	edited := strings.Replace(source, "offer_scope='party'", "offer_scope='any_offer'", 1)
	if _, err := policy.vm.Reload(context.Background(), edited, policy.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	send := m.session.SendPacket
	m.session.SendPacket = func(ctx context.Context, code uint32, p []byte) error {
		send(ctx, code, p)
		return errors.New("actual response transport outcome unknown")
	}
	r, out := offerPolicyStep(t, m, policy)
	if r == nil || r.Outcome != behavior.ActionSettledUnverified || r.Payload["phase"] != "transport_unconfirmed_no_retry" ||
		r.Payload["effect_observed"] != false || r.Payload["caster_guid"] != "7" || r.Payload["offer_revision"] != "1" ||
		len(cb.writes) != 1 || cb.writes[0].Opcode != opcode.CMSGResurrectResponse || len(cb.writes[0].Payload) != 9 ||
		binary.LittleEndian.Uint64(cb.writes[0].Payload) != 7 || cb.writes[0].Payload[8] != 1 {
		t.Fatal("real current VM did not reach exact native response", out, r, cb.writes)
	}
	if _, err := policy.vm.Reload(context.Background(), edited+"\n-- preserve offer custody", policy.vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if r, out := offerPolicyStep(t, m, policy); r != nil || len(cb.writes) != 1 || policy.vm.Status().Pending != 0 || out["phase"] != "waiting_for_actual_life_after_offer_acceptance" {
		t.Fatal("unknown acceptance replayed or blocked global ledger", r, out, cb.writes, policy.vm.Status())
	}
	cb.world.Self.Alive, cb.world.Self.Health = true, 100
	offerPolicyStep(t, m, policy)
	cb.world.Self.Alive, cb.world.Self.Health = false, 0
	runtimeResurrectionOffer(t, native, 7)
	m.session.SendPacket = send
	r, out = offerPolicyStep(t, m, policy)
	if r == nil || r.Outcome != behavior.ActionSettledUnverified || r.Payload["transport_written"] != true ||
		r.Payload["effect_observed"] != false || r.Payload["death_epoch"] != "2" || r.Payload["offer_revision"] != "2" || len(cb.writes) != 2 {
		t.Fatal("positive life/new death did not retire prior offer custody", out, r, cb.writes)
	}
}

func TestPolicyResurrectionOfferCoreNoWriteAllowsSameOfferFreshDecision(t *testing.T) {
	m, native, cb := friendlyEncounterManager(t)
	cb.world.Self.Alive, cb.world.Self.Health = false, 0
	runtimeResurrectionOffer(t, native, 7)
	source := strings.Replace(recoveryRuntimeSource(t), "offer_scope='party'", "offer_scope='any_offer'", 1)
	p, err := m.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	cb.reject = true
	if r, out := offerPolicyStep(t, m, p); r == nil || r.Outcome != behavior.ActionNotDispatched || len(cb.writes) != 0 {
		t.Fatal("definite core refusal became ambiguous response", r, out, cb.writes)
	}
	cb.reject = false
	if r, out := offerPolicyStep(t, m, p); r == nil || r.Outcome != behavior.ActionSettledUnverified || r.Payload["transport_written"] != true || len(cb.writes) != 1 {
		t.Fatal("known no-write stranded same offer", r, out, cb.writes)
	}
	op := &policySpell{capability: "accept_resurrection", recovery: &policyRecovery{caster: 7}}
	body := binary.LittleEndian.AppendUint64(nil, 7)
	body = append(body, 1)
	if !op.matchesActionPacket(opcode.CMSGResurrectResponse, body) {
		t.Fatal("exact response rejected")
	}
	for _, changed := range [][]byte{append([]byte{8}, body[1:]...), append(body[:8:8], 0), append(body, 0)} {
		if op.matchesActionPacket(opcode.CMSGResurrectResponse, changed) {
			t.Fatal("foreign/reject/malformed response escaped exact packet fence", changed)
		}
	}
}
