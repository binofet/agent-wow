package world

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func TestDeathRecoveryLuaOwnsOfferScopeAgeStopAndReloadCustody(t *testing.T) {
	module, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "death-recovery.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := "local r=(function()\n" + string(module) + "\nend)();return {on_event=r.on_event,export_state=r.export_state,import_state=r.import_state,decide=function(f)return r.decide(f,f.input.obs,'1')end}"
	vm, err := behavior.NewSessionWithOptions(context.Background(), "offer-policy", source, behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Close()
	self := map[string]any{"present": true, "aliveKnown": true, "alive": false, "healthKnown": true, "health": float64(0), "positionRevision": "1"}
	offer := map[string]any{"casterGuid": "3", "revision": "1", "deathEpoch": "1", "ageMs": "0"}
	recovery := map[string]any{"deathEpoch": "1", "resurrectionOffer": offer}
	obs := map[string]any{"sessionId": "offer-policy", "selfGuid": "1", "players": map[string]any{"1": self}, "recovery": recovery,
		"party": map[string]any{"group": map[string]any{"members": []any{map[string]any{"guid": "1", "online": true}, map[string]any{"guid": "3", "online": true}}}}}
	step := func() behavior.Decision {
		t.Helper()
		d, err := vm.Decide(context.Background(), map[string]any{"observationSequence": "1", "obs": obs})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	want := func(capability string) behavior.Decision {
		t.Helper()
		d := step()
		a, _ := d.Output["action"].(map[string]any)
		if a["capability"] != capability {
			t.Fatal("wrong current Lua choice", d.Output, capability)
		}
		return d
	}
	want("accept_resurrection")
	recovery["motionPending"] = true
	want("stop_corpse")
	recovery["motionPending"] = false
	offer["ageMs"] = "60001"
	want("release_spirit")
	edited := strings.Replace(source, "offer_max_age_ms=60000", "offer_max_age_ms=120000", 1)
	if _, err := vm.Reload(context.Background(), edited, vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	d := want("accept_resurrection")
	id, err := vm.ReserveAction(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vm.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "offer-policy", ActionID: id,
		Outcome: behavior.ActionSettledUnverified, Payload: map[string]any{"capability": "accept_resurrection", "self_guid": "1", "death_epoch": "1", "offer_revision": "1"}}}); err != nil {
		t.Fatal(err)
	}
	if d := step(); d.Output["action"] != nil || d.Output["phase"] != "waiting_for_actual_life_after_offer_acceptance" || vm.Status().Pending != 0 {
		t.Fatal("write receipt claimed life or lost private custody", d.Output, vm.Status())
	}
	if _, err := vm.Reload(context.Background(), source, vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if d := step(); d.Output["action"] != nil || d.Output["phase"] != "waiting_for_actual_life_after_offer_acceptance" {
		t.Fatal("reload/age expiry invented permission to replay", d.Output)
	}
}
