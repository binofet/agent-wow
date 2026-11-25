package world

import (
	"context"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestEncounterRangedOnlyExpiryAndDuplicateUseOwnedClockAcrossReload(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "encounter-commands.lua"))
	if err != nil {
		t.Fatal(err)
	}
	// Pure command-lifecycle fixture supplies explicit clock readings. Production
	// encounter-session passes the host-owned frame clock, never command fields.
	source := "local c=(function()\n" + string(raw) + "\nend)()\n" + `return {on_event=c.on_event,export_state=c.export_state,import_state=c.import_state,decide=function(f)
 local cfg,status=c.ranged_stance(f.input.obs,{session_id=f.session_id,input={observationSequence=f.input.sequence},clock={monotonicMs=f.input.now,unixMs=f.input.unix}})
 return {config=cfg,status=status} end}`
	vm, err := behavior.NewSession(context.Background(), "range-clock", source)
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Close()
	obs := map[string]any{"sessionId": "range-clock", "selfGuid": "4", "mapId": float64(0), "visitId": "1"}
	seq, err := vm.Enqueue(behavior.Event{Kind: "encounter_observation", Value: obs})
	if err != nil {
		t.Fatal(err)
	}
	command := rangedDirective(765432)
	command["durationMs"] = float64(2000)
	enqueue := func() {
		t.Helper()
		if _, err := vm.Enqueue(behavior.Event{Kind: "command", Value: command}); err != nil {
			t.Fatal(err)
		}
	}
	decide := func(now, unix float64) map[string]any {
		t.Helper()
		d, err := vm.Decide(context.Background(), map[string]any{"obs": obs, "sequence": strconv.FormatUint(seq, 10), "now": now, "unix": unix})
		if err != nil {
			t.Fatal(err)
		}
		return d.Output["status"].(map[string]any)
	}
	enqueue()
	status := decide(0, 10000)
	if status["active"] != true || status["remainingMs"] != float64(2000) {
		t.Fatal(status)
	}
	enqueue()
	status = decide(1000, 11000)
	if status["remainingMs"] != float64(1000) {
		t.Fatal("duplicate extended owner deadline", status)
	}
	if _, err = vm.Reload(context.Background(), source, vm.Status().Revision); err != nil {
		t.Fatal(err)
	}
	status = decide(0, 12001)
	if status["active"] != false || status["phase"] != "expired" {
		t.Fatal("clock restart prolonged stance", status)
	}
}
