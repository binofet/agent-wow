package world

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func TestDeathRecoveryGivesAnObservedRescuerTime(t *testing.T) {
	module, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "death-recovery.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := "local r=(function()\n" + string(module) + `
end)()
r.tuning.trusted_offer_casters={["87"]=true}
return {on_event=r.on_event,export_state=r.export_state,import_state=r.import_state,decide=function(f)
    f.clock={monotonicMs=f.input.now}
    return r.decide(f,f.input.obs,"1")
end}`
	ctx := context.Background()
	host, err := behavior.NewSessionWithOptions(ctx, "rescue", source, behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	self := map[string]any{"guid": "1", "present": true, "mapKnown": true, "mapId": 349, "aliveKnown": true, "alive": false, "healthKnown": true, "health": 0}
	paladin := map[string]any{"guid": "87", "present": true, "mapKnown": true, "mapId": 349, "aliveKnown": true, "alive": true, "classKnown": true, "classId": 2}
	players := map[string]any{"1": self, "87": paladin}
	recovery := map[string]any{"deathEpoch": "1"}
	obs := map[string]any{"sessionId": "rescue", "selfGuid": "1", "players": players, "recovery": recovery}
	step := func(now int, phase, capability string) map[string]any {
		t.Helper()
		d, err := host.Decide(ctx, map[string]any{"now": now, "obs": obs, "observationSequence": "1"})
		if err != nil {
			t.Fatal(err)
		}
		action, _ := d.Output["action"].(map[string]any)
		if d.Output["phase"] != phase || (capability == "" && action != nil) || (capability != "" && action["capability"] != capability) {
			t.Fatalf("at %d expected %s/%s, got %v", now, phase, capability, d.Output)
		}
		return d.Output
	}
	const wait = "waiting_for_resurrection_offer"
	d := step(100, wait, "")
	if d["rescuerGuid"] != "87" || d["remainingMs"] != float64(60000) {
		t.Fatal(d)
	}
	recovery["resurrectionOffer"] = map[string]any{"deathEpoch": "1", "revision": "75", "casterGuid": "87", "ageMs": "0"}
	step(10100, "accepting_observed_resurrection_offer", "accept_resurrection")
	delete(recovery, "resurrectionOffer")
	// A request to recover and a source reload cannot restart this death's grace.
	if _, err := host.Enqueue(behavior.Event{Kind: "death_recovery_request"}); err != nil {
		t.Fatal(err)
	}
	step(20100, wait, "")
	if _, err := host.Reload(ctx, source, host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	step(60099, wait, "")
	step(60100, "release_spirit", "release_spirit")
	recovery["deathEpoch"] = "2"
	step(60400, wait, "")
	step(90400, wait, "")
	if got := step(100, wait, "")["remainingMs"]; got != float64(30000) {
		t.Fatal("clock rollback renewed the grace", got)
	}
	step(30100, "release_spirit", "release_spirit")

	recovery["deathEpoch"] = "3"
	for _, tc := range []struct {
		field string
		value any
	}{{"present", false}, {"alive", false}, {"mapId", 1}, {"mapKnown", false}, {"classKnown", false}, {"classId", 1}} {
		previous := paladin[tc.field]
		paladin[tc.field] = tc.value
		step(30200, "release_spirit", "release_spirit")
		paladin[tc.field] = previous
	}
	delete(players, "87")
	players["9"] = map[string]any{"guid": "9", "present": true, "mapKnown": true, "mapId": 349, "aliveKnown": true, "alive": true, "classKnown": true, "classId": 5, "inCombat": true}
	step(30300, "release_spirit", "release_spirit") // unrelated priest
	member := map[string]any{"guid": "9", "online": true}
	obs["party"] = map[string]any{"group": map[string]any{"known": true, "inGroup": true, "members": []any{member}}}
	step(30400, wait, "") // a living priest can finish combat before casting
	member["online"] = false
	step(30500, "release_spirit", "release_spirit")
	member["online"] = true
	recovery["releaseAttempted"] = true
	step(30600, "waiting_for_actual_ghost", "")
	recovery["ghostObserved"] = true
	step(30700, "query_corpse", "query_corpse")
}
