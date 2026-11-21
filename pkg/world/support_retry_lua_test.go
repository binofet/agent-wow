package world

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func supportRetrySource(t *testing.T) string {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "support.lua"))
	if err != nil {
		t.Fatal(err)
	}
	return "local module=(function()\n" + string(script) + "\nend)()\n" + `
 return {on_event=module.on_event,export_state=module.export_state,import_state=module.import_state,
 decide=function(frame)
   local input=frame.input
   module.resurrection_tuning.retry_ms=input.retryMs or 500
   module.on_event({kind="encounter_observation",value=input.obs})
   if #frame.pending~=0 then return {} end
   return {action=module.resurrect(input.obs,{clock=input.clock}),state=module.export_state()}
 end}`
}
func supportRetryInput() map[string]any {
	return map[string]any{"clock": map[string]any{"monotonicMs": float64(0)}, "obs": map[string]any{
		"sessionId": fallbackSession, "selfGuid": "4", "visitId": "1", "hostOwned": true,
		"players": map[string]any{"4": map[string]any{"present": true, "aliveKnown": true, "alive": true}, "3": map[string]any{"present": true, "aliveKnown": true, "alive": false}},
		"support": map[string]any{"resurrections": []any{map[string]any{"patientGuid": "3", "castTargetGuid": "17366161638117294133", "spellId": float64(2006), "ready": true, "canApproach": true, "rangeYards": float64(30)}}}}}
}
func supportRetryDecide(t *testing.T, host *behavior.Session, input map[string]any) behavior.Decision {
	t.Helper()
	out, err := host.Decide(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func supportRetryAction(t *testing.T, decision behavior.Decision, capability string) map[string]any {
	t.Helper()
	action, _ := decision.Output["action"].(map[string]any)
	if capability == "" {
		if len(action) != 0 {
			t.Fatal("unexpected replay", decision.Output)
		}
	} else if action["capability"] != capability {
		t.Fatal("wrong support decision", decision.Output)
	}
	return action
}
func supportRetryReceipt(t *testing.T, host *behavior.Session, decision behavior.Decision, outcome behavior.ActionOutcome, capability, target string) uint64 {
	t.Helper()
	id, err := host.ReserveAction(decision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: outcome,
		Payload: map[string]any{"capability": capability, "target_guid": target, "spell_id": float64(2006)}}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func TestLuaSupportRejectedResurrectionRepositionsThenRetriesAtScriptDeadline(t *testing.T) {
	source := supportRetrySource(t)
	host, err := behavior.NewSession(context.Background(), fallbackSession, source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	input := supportRetryInput()
	first := supportRetryDecide(t, host, input)
	action := supportRetryAction(t, first, "resurrect")
	if action["targetGuid"] != "3" || action["castTargetGuid"] != "17366161638117294133" {
		t.Fatal(action)
	}
	supportRetryReceipt(t, host, first, behavior.ActionFailed, "resurrect", "3")
	supportRetryAction(t, supportRetryDecide(t, host, input), "")
	if _, err = host.Reload(context.Background(), source+"\n-- same body retry tuning", host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	clock := input["clock"].(map[string]any)
	clock["monotonicMs"] = float64(499)
	supportRetryAction(t, supportRetryDecide(t, host, input), "")
	clock["monotonicMs"] = float64(500)
	reposition := supportRetryDecide(t, host, input)
	action = supportRetryAction(t, reposition, "approach_target")
	if action["targetGuid"] != "17366161638117294133" || action["requireLos"] != true {
		t.Fatal("reposition lost exact owned corpse", action)
	}
	supportRetryReceipt(t, host, reposition, behavior.ActionSettledUnverified, "approach_target", "17366161638117294133")
	// An approach write is not spatial readiness. Observe the native check.
	supportRetryAction(t, supportRetryDecide(t, host, input), "approach_target")
	obs := input["obs"].(map[string]any)
	obs["targetMovement"] = map[string]any{"targetGuid": "17366161638117294133", "visitId": "1", "ready": true, "requireLos": true, "rangeYards": float64(30)}
	supportRetryAction(t, supportRetryDecide(t, host, input), "resurrect")
}
func TestLuaSupportUncertainResurrectionKeepsSameDeathCustodyAcrossReload(t *testing.T) {
	for _, outcome := range []behavior.ActionOutcome{behavior.ActionTransportAccepted, behavior.ActionUnknown, behavior.ActionSettledUnverified} {
		t.Run(string(outcome), func(t *testing.T) {
			source := supportRetrySource(t)
			host, err := behavior.NewSession(context.Background(), fallbackSession, source)
			if err != nil {
				t.Fatal(err)
			}
			defer host.Close()
			input := supportRetryInput()
			first := supportRetryDecide(t, host, input)
			supportRetryAction(t, first, "resurrect")
			id := supportRetryReceipt(t, host, first, outcome, "resurrect", "3")
			if outcome == behavior.ActionUnknown || outcome == behavior.ActionTransportAccepted {
				// A later terminal, unverified receipt releases only the global
				// reservation; it must not rearm this private death binding.
				_, err = host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionSettledUnverified,
					Payload: map[string]any{"capability": "resurrect", "target_guid": "3", "spell_id": float64(2006)}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			supportRetryAction(t, supportRetryDecide(t, host, input), "")
			if _, err = host.Reload(context.Background(), source+"\n-- preserve death custody", host.Status().Revision); err != nil {
				t.Fatal(err)
			}
			input["clock"].(map[string]any)["monotonicMs"] = float64(1000000)
			supportRetryAction(t, supportRetryDecide(t, host, input), "")
			patient := input["obs"].(map[string]any)["players"].(map[string]any)["3"].(map[string]any)
			patient["alive"] = true
			supportRetryAction(t, supportRetryDecide(t, host, input), "")
			patient["alive"] = false
			supportRetryAction(t, supportRetryDecide(t, host, input), "resurrect")
		})
	}
}
func TestLuaSupportRetryTuningAndOtherPatientRemainIndependent(t *testing.T) {
	source := supportRetrySource(t)
	host, err := behavior.NewSession(context.Background(), fallbackSession, source)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	input := supportRetryInput()
	first := supportRetryDecide(t, host, input)
	supportRetryReceipt(t, host, first, behavior.ActionNotDispatched, "resurrect", "3")
	input["retryMs"] = float64(0)
	supportRetryAction(t, supportRetryDecide(t, host, input), "approach_target")
	obs := input["obs"].(map[string]any)
	obs["support"].(map[string]any)["resurrections"] = []any{map[string]any{"patientGuid": "9", "castTargetGuid": "9", "spellId": float64(2006), "ready": true}}
	action := supportRetryAction(t, supportRetryDecide(t, host, input), "resurrect")
	if action["targetGuid"] != "9" {
		t.Fatal("one rejected patient blocked other resurrection", action)
	}
}
