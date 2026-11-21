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

func TestGhostRecoveryLuaOwnsDefaultContinuationDeadlineAndObservedReclaimDelay(t *testing.T) {
	module, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "death-recovery.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := "local r=(function()\n" + string(module) + "\nend)();return {on_event=r.on_event,export_state=r.export_state,import_state=r.import_state,decide=function(f)f.clock={monotonicMs=f.input.now};return r.decide(f,f.input.obs,'1')end}"
	host, err := behavior.NewSessionWithOptions(context.Background(), "dead-policy", source, behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	self := map[string]any{"present": true, "aliveKnown": true, "alive": false, "healthKnown": true, "health": float64(1), "positionKnown": true, "mapKnown": true, "mapId": float64(0), "positionRevision": "1", "x": float64(0)}
	corpse := map[string]any{"guid": "17366161638117343233", "ownerGuid": "1", "present": true, "flagsKnown": true, "position": map[string]any{"known": true, "mapKnown": true, "mapId": float64(0), "x": float64(60), "revision": "1"}}
	recovery := map[string]any{"enabled": false, "deathEpoch": "1", "ghostObserved": true, "ghostMovementSupported": true, "corpseQueryKnown": true, "corpseFound": true, "corpseQueryRevision": "1", "corpseMapId": float64(0), "corpseLocation": map[string]any{"known": true, "mapKnown": true, "mapId": float64(0), "x": float64(60)}, "ownCorpse": corpse}
	obs := map[string]any{"sessionId": "dead-policy", "selfGuid": "1", "players": map[string]any{"1": self}, "recovery": recovery}
	step := func(now float64) behavior.Decision {
		t.Helper()
		out, err := host.Decide(context.Background(), map[string]any{"observationSequence": "1", "now": now, "obs": obs})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, now := range []float64{0, 700000} {
		d := step(now)
		a, _ := d.Output["action"].(map[string]any)
		if a["capability"] != "approach_corpse" || a["rangeYards"] != float64(35) {
			t.Fatal("old enable/ten-minute veto remains", d.Output)
		}
	}
	limited := strings.Replace(source, "deadline_ms=0", "deadline_ms=600000", 1)
	if _, err = host.Reload(context.Background(), limited, host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if d := step(700000); d.Output["action"] != nil || d.Output["phase"] != "configured_deadline_reached" {
		t.Fatal("Lua deadline not authoritative", d.Output)
	}
	if _, err = host.Reload(context.Background(), source, host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if d := step(700000); d.Output["action"] == nil {
		t.Fatal("edited Lua deadline could not resume same body", d.Output)
	}
	self["x"] = float64(50)
	recovery["reclaimDelayKnown"] = true
	recovery["reclaimDelayRemainingMs"] = "5000"
	if d := step(700000); d.Output["action"] != nil || d.Output["phase"] != "waiting_for_observed_reclaim_delay" {
		t.Fatal("actual server timer ignored", d.Output)
	}
	recovery["reclaimDelayRemainingMs"] = "0"
	reclaim := step(700001)
	a, _ := reclaim.Output["action"].(map[string]any)
	if a["capability"] != "reclaim_corpse" || a["corpseGuid"] != corpse["guid"] {
		t.Fatal(reclaim.Output)
	}
	id, err := host.ReserveAction(reclaim)
	if err != nil {
		t.Fatal(err)
	}
	host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: "dead-policy", ActionID: id, Outcome: behavior.ActionSettledUnverified, Payload: map[string]any{"capability": "reclaim_corpse", "self_guid": "1", "death_epoch": "1"}}})
	if _, err = host.Reload(context.Background(), source, host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if d := step(700002); d.Output["action"] != nil || d.Output["phase"] != "waiting_for_actual_life_after_reclaim" || host.Status().Pending != 0 {
		t.Fatal("unknown reclaim replayed/blocked globally", d.Output, host.Status())
	}
	self["alive"] = true
	self["health"] = float64(100)
	step(700003)
	self["alive"] = false
	self["health"] = float64(0)
	recovery["deathEpoch"] = "2"
	recovery["ghostObserved"] = false
	if d := step(700004); d.Output["action"].(map[string]any)["capability"] != "release_spirit" {
		t.Fatal("actual new life/death lost ordinary support", d.Output)
	}
}
