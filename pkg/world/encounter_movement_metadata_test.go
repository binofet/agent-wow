package world

import (
	"context"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func TestEncounterUnknownMovementMetadataDoesNotKeepGlobalPendingOrDisableHealing(t *testing.T) {
	// An ordinary attached script requests finite custody once, keeps unknown
	// ownership as task data, and delegates standing healing to the real policy.
	source := `local original=(function()
` + readEncounterBundle(t) + `
end)()
local requested,unknown=false,false
return {on_event=function(event)
 if event.receipt and event.receipt.payload and event.receipt.payload.capability=="movement_owner" then
   unknown=event.receipt.payload.reconciliationRequired==true
 end
 return original.on_event(event)
end,
 decide=function(frame)
   if not requested then requested=true;return {action={capability="movement_owner",sessionId=frame.session_id,selfGuid="4",owner="wait:123",mode="acquire",leaseMs=2000}} end
   local out=original.decide(frame);out.taskStillUncertain=unknown;return out
 end,export_state=original.export_state,import_state=original.import_state}`
	host, err := behavior.NewSession(context.Background(), fallbackSession, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	p := &fallbackPolicyTest{t: t, host: host}
	observation := standingHealObservation()
	seq := p.observe(observation)
	first := p.decide(seq)
	if first.Output["action"].(map[string]any)["capability"] != "movement_owner" {
		t.Fatal(first.Output)
	}
	id, err := host.ReserveAction(first)
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionSettledUnverified, Payload: map[string]any{"capability": "movement_owner", "owner": "wait:123", "reconciliationRequired": true, "error": "RPC deadline exceeded; grant outcome unknown"}}})
	if err != nil {
		t.Fatal(err)
	}
	seq = p.observe(observation)
	next := p.decide(seq)
	action, _ := next.Output["action"].(map[string]any)
	if host.Status().Pending != 0 || next.Output["taskStillUncertain"] != true || action["capability"] != "heal" || action["targetGuid"] != fallbackGUID(fallbackLeader) {
		t.Fatal("unknown finite metadata disabled real standing healing or invented success", host.Status(), next.Output)
	}
}
