package world

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func TestEncounterCorpseLootYieldsCustodyForHealingAndPreservesSameSessionState(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "corpse-loot.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := "local encounter_corpse_loot=(function()\n" + string(raw) + "\nend)()\n" + readEncounterBundle(t)
	host, err := behavior.NewSessionWithOptions(context.Background(), fallbackSession, source, behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	p := &fallbackPolicyTest{t: t, host: host}
	obs := positionedHealObservation()
	patients := obs.Healing.Patients
	obs.Healing.Patients = nil
	corpse := fallbackGUID(0xf130000001000001)
	loot := map[string]any{"sessionId": fallbackSession, "selfGuid": fallbackGUID(fallbackSelf),
		"self":           map[string]any{"present": true, "aliveKnown": true, "alive": true},
		"actions":        map[string]any{"position": map[string]any{"known": true, "mapKnown": true, "mapId": float64(0), "x": float64(10), "y": float64(10), "z": float64(10), "revision": "1"}},
		"inventoryKnown": true, "inventoryRevision": "1", "generalFreeSlots": float64(5),
		"corpses": []any{map[string]any{"guid": corpse, "present": true, "mapKnown": true, "mapId": float64(0), "positionKnown": true, "positionRevision": "1", "x": float64(12), "y": float64(10), "z": float64(10), "aliveKnown": true, "healthKnown": true, "dynamicFlags": float64(1)}},
		"state":   map[string]any{}}
	decide := func() behavior.Decision {
		t.Helper()
		ls, e := host.Enqueue(behavior.Event{Kind: "corpse_loot_observation", Value: loot})
		if e != nil {
			t.Fatal(e)
		}
		es := observeSparseEncounter(t, p, obs)
		d, e := host.Decide(context.Background(), map[string]any{"kind": "encounter", "observationSequence": es, "observationSequences": map[string]any{"corpse_loot": fallbackGUID(ls)}})
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	settle := func(d behavior.Decision, capability string) {
		t.Helper()
		id, e := host.ReserveAction(d)
		if e != nil {
			t.Fatal(e)
		}
		_, e = host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionSettledUnverified, Payload: map[string]any{"capability": capability, "corpse_guid": corpse, "opening_transport_attempted": true}}})
		if e != nil {
			t.Fatal(e)
		}
	}
	first := decide()
	a, _ := first.Output["action"].(map[string]any)
	if a["capability"] != "loot_open" {
		t.Fatal("quiet encounter did not use shared Lua corpse policy", first.Output)
	}
	settle(first, "loot_open")
	loot["state"] = map[string]any{"pendingCorpseGuid": corpse, "phase": "waiting_open"}
	loot["operation"] = map[string]any{"corpseGuid": corpse, "requestId": "open", "openingTransportAttempted": true}
	obs.Healing.Patients = patients
	close := decide()
	a, _ = close.Output["action"].(map[string]any)
	if a["capability"] != "loot_close" {
		t.Fatal("needed allied heal did not release exact loot custody", close.Output)
	}
	settle(close, "loot_close")
	loot["state"].(map[string]any)["phase"] = "waiting_release"
	if _, err = host.Reload(context.Background(), source, host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	a, _ = decide().Output["action"].(map[string]any)
	if a["capability"] != "heal" || a["targetGuid"] != "8" || host.Status().Pending != 0 || host.Status().SessionID != fallbackSession {
		t.Fatal("unknown UI release blocked healing/replayed OPEN across same-session reload", a, host.Status())
	}
}
