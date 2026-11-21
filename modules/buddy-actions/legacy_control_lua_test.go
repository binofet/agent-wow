package buddyactions

import (
	"context"
	"encoding/binary"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func TestLuaLegacySapKeepsSelectedStealthThroughDuplicateAndReload(t *testing.T) {
	host, err := behavior.NewSession(context.Background(), "commands-body", commandPolicySource(t))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	obs := commandObservation()
	target := encounterGUID(sheepTarget)
	obs.Spells = map[string]*actionsv1.EncounterSpell{
		"6770": {Known: true, Available: true, Ready: true, Capability: "sap", Rank: 1, MaxRange: 5},
		"1784": {Known: true, Available: true, Ready: true, Capability: "stealth", Rank: 1, SelfCastSupported: true},
		"1785": {Known: true, Available: true, Ready: true, Capability: "stealth", Rank: 2, SelfCastSupported: true}}
	request := map[string]any{"task": "control", "technique": "sap", "targetGuid": target, "spellId": float64(6770), "maintain": false, "id": "legacy:sap:exact"}
	sendPolicyCommand(t, host, request)
	seq := observeCommandPolicy(t, host, obs)
	decision, err := host.Decide(context.Background(), map[string]any{"observationSequence": seq, "focusGuid": "2"})
	if err != nil {
		t.Fatal(err)
	}
	expectControlAction(t, decision.Output, "cast_self", "1")
	action := decision.Output["action"].(map[string]any)
	if action["spellId"] != float64(1785) {
		t.Fatal("Lua did not choose learned Stealth rank", action)
	}
	// The selected Lua rank reaches the actual exact native primitive. No Sap
	// request may secretly choose or cast Stealth on its own.
	s, f, _ := policySapFixture(t)
	s.sessionID = "commands-body"
	learnSpells(s, 6770, 1784, 1785)
	emitPolymorph(s, opcode.SMSGAuraUpdate, buffAura(1, 1, 1, 0, 0))
	f.world.Self.ShapeshiftForm = 0
	st, err := s.CastSelf(context.Background(), &actionsv1.CastSelfRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, SpellId: uint32(action["spellId"].(float64))})
	if err != nil || !st.GetLastSpellOperation().GetTransportWritten() || lastCast(f) != 1785 {
		t.Fatal("actual native self cast changed selected rank", st, err, f.packets)
	}
	goPacket := polymorphGo(s.castCount, s.selfGUID, s.selfGUID, true)
	binary.LittleEndian.PutUint32(goPacket[2*len(packedGUID(s.selfGUID))+1:], 1785)
	emitPolymorph(s, opcode.SMSGSpellGo, goPacket)
	st = state(t, s)
	if !st.GetLastSpellOperation().GetServerCastGo() || st.GetLastSpellOperation().GetEffectObserved() {
		t.Fatal("native GO fabricated Stealth aura", st)
	}
	id, err := host.ReserveAction(decision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "commands-body", ActionID: id, Outcome: behavior.ActionSettledUnverified,
		Payload: map[string]any{"capability": "cast_self", "target_guid": "1", "spell_id": float64(1785), "phase": "server_cast_go_effect_unverified"}}})
	if err != nil {
		t.Fatal(err)
	}
	sendPolicyCommand(t, host, request)
	if _, err = host.Reload(context.Background(), commandPolicySource(t)+"\n-- preserve legacy Stealth custody", host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	out := commandDecision(t, host, observeCommandPolicy(t, host, obs), "2")
	if out["action"] != nil || out["control"].(map[string]any)["phase"] != "waiting_for_stealth_aura" {
		t.Fatal("duplicate retried unknown Stealth", out)
	}
	emitPolymorph(s, opcode.SMSGAuraUpdate, buffAura(s.selfGUID, s.selfGUID, 1, 1785, 20000))
	f.world.Self.ShapeshiftForm = 30
	obs.Spells["1785"].SelfAuraActive = s.stealthedLocked()
	if !obs.Spells["1785"].SelfAuraActive {
		t.Fatal("native aura parser did not observe Stealth")
	}

	expectControlAction(t, commandDecision(t, host, observeCommandPolicy(t, host, obs), "2"), "approach_target", target)
	obs.Targets[target].X = 4
	obs.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: sheepTarget, VisitId: 1, RangeYards: 5, RequireLos: true, Behind: true, Ready: true}
	expectControlAction(t, commandDecision(t, host, observeCommandPolicy(t, host, obs), "2"), "sap", target)
}
func TestLuaLegacyControlCancellationStopsOnceUntilPhysicalReconciliation(t *testing.T) {
	host, err := behavior.NewSession(context.Background(), "commands-body", commandPolicySource(t))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	obs := commandObservation()
	target := encounterGUID(sheepTarget)
	request := map[string]any{"task": "control", "technique": "polymorph", "targetGuid": target, "id": "legacy:polymorph:exact"}
	sendPolicyCommand(t, host, request)
	expectControlAction(t, commandDecision(t, host, observeCommandPolicy(t, host, obs), "2"), "approach_target", target)
	obs.Players["1"].Moving = true
	sendPolicyCommand(t, host, map[string]any{"task": "cancel_control", "technique": "polymorph", "targetGuid": target, "id": "legacy:polymorph:exact"})
	seq := observeCommandPolicy(t, host, obs)
	stop, err := host.Decide(context.Background(), map[string]any{"observationSequence": seq, "focusGuid": "2"})
	if err != nil {
		t.Fatal(err)
	}
	expectControlAction(t, stop.Output, "stop_movement", target)
	id, err := host.ReserveAction(stop)
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "commands-body", ActionID: id, Outcome: behavior.ActionSettledUnverified,
		Payload: map[string]any{"capability": "stop_movement", "target_guid": target, "phase": "transport_unconfirmed_no_retry"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Reload(context.Background(), commandPolicySource(t)+"\n-- uncertain STOP survives", host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	out := commandDecision(t, host, observeCommandPolicy(t, host, obs), "2")
	if out["action"] != nil || out["control"].(map[string]any)["phase"] != "waiting_for_control_stop" {
		t.Fatal("unknown STOP replayed or canceled intent forgotten", out)
	}
	obs.Players["1"].Moving = false
	obs.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: sheepTarget, VisitId: 1, Phase: "stopped", Ready: true}
	obs.Targets[target].Alive = proto.Bool(false)
	out = commandDecision(t, host, observeCommandPolicy(t, host, obs), "2")
	if out["action"] != nil || out["control"] != nil {
		t.Fatal("observed stop did not retire canceled task", out)
	}
}
