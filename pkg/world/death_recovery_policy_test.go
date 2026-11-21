package world

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
)

func TestDeathRecoveryComposedPolicyTuningReloadAndPendingCustody(t *testing.T) {
	module, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "death-recovery.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := func(delay string) string {
		return "local encounter_death_recovery=(function()\n" + string(module) + "\nend)()\nencounter_death_recovery.tuning.release_delay_ms=" + delay + "\n" + readEncounterBundle(t)
	}
	host, err := behavior.NewSessionWithOptions(context.Background(), fallbackSession, source("60000"), behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	p := &fallbackPolicyTest{t: t, host: host}
	obs := positionedHealObservation()
	self := obs.Players[fallbackGUID(fallbackSelf)]
	self.Alive = proto.Bool(false)
	self.HealthKnown, self.Health, self.MaxHealth = true, 0, 100
	obs.Recovery = &actionsv1.RecoveryState{Enabled: true, DeathEpoch: 1}
	step := func() behavior.Decision {
		t.Helper()
		d, err := host.Decide(context.Background(), map[string]any{"observationSequence": p.observe(obs)})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if delayed := step(); delayed.Output["action"] != nil {
		t.Fatal("editable release delay ignored", delayed.Output)
	}
	if _, err = host.Reload(context.Background(), source("0"), host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	release := step()
	action, _ := release.Output["action"].(map[string]any)
	if action["capability"] != "release_spirit" || action["deathEpoch"] != "1" {
		t.Fatal(release.Output)
	}
	id, err := host.ReserveAction(release)
	if err != nil {
		t.Fatal(err)
	}
	// A real global pending operation is not erased by death recovery or reload.
	if _, err = host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionUnknown, Payload: map[string]any{"capability": "release_spirit", "self_guid": fallbackGUID(fallbackSelf), "death_epoch": "1"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = host.Reload(context.Background(), source("60000"), host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	obs.Recovery.GhostObserved = true
	if d := step(); d.Output["action"] != nil || host.Status().Pending != 1 {
		t.Fatal("unknown custody erased", d.Output, host.Status())
	}
	if _, err = host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionSettledUnverified, Payload: map[string]any{"capability": "release_spirit", "self_guid": fallbackGUID(fallbackSelf), "death_epoch": "1"}}}); err != nil {
		t.Fatal(err)
	}
	query := step()
	action, _ = query.Output["action"].(map[string]any)
	if action["capability"] != "query_corpse" {
		t.Fatal(query.Output)
	}
	// A proposal checkpoint already reserves the query, even if transport is
	// uncertain and a new policy revision changes timing while it is outstanding.
	if _, err = host.Reload(context.Background(), source("0"), host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if d := step(); d.Output["action"] != nil {
		t.Fatal("query replayed on reload", d.Output)
	}
	obs.Recovery.CorpseQueryKnown = true
	if d := step(); d.Output["action"] != nil {
		t.Fatal("corpse receipt became living action", d.Output)
	}
	// Positive life allows ordinary shared healing immediately; no new native
	// post-reclaim resource gate or a local release timer can veto that support.
	self.Alive = proto.Bool(true)
	self.Health = 20
	obs.Healing.Caster.Ready = true
	obs.Healing.Patients[0].Guid = fallbackSelf
	obs.Healing.Patients[0].Health = 20
	obs.Healing.Patients[0].Distance = proto.Float64(0)
	if d := step(); d.Output["action"].(map[string]any)["capability"] != "heal" {
		t.Fatal(d.Output)
	}
}

func TestDeathRecoveryDoesNotReserveReleaseBeforeActualLootWindowCleanup(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "..", "behaviors", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	source := "local encounter_death_recovery=(function()\n" + read("death-recovery.lua") + "\nend)()\nlocal encounter_corpse_loot=(function()\n" + read("corpse-loot.lua") + "\nend)()\n" + readEncounterBundle(t)
	host, err := behavior.NewSessionWithOptions(context.Background(), fallbackSession, source, behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	p := &fallbackPolicyTest{t: t, host: host}
	obs := positionedHealObservation()
	self := obs.Players[fallbackGUID(fallbackSelf)]
	self.Alive = proto.Bool(false)
	self.HealthKnown, self.Health, self.MaxHealth = true, 0, 100
	obs.Recovery = &actionsv1.RecoveryState{Enabled: true, DeathEpoch: 1}
	corpse := fallbackGUID(0xf130000001000001)
	loot := map[string]any{"sessionId": fallbackSession, "selfGuid": fallbackGUID(fallbackSelf),
		"self":      map[string]any{"present": true, "aliveKnown": true, "alive": false, "unitFlagsKnown": true},
		"state":     map[string]any{"pendingCorpseGuid": corpse, "phase": "waiting_open"},
		"operation": map[string]any{"corpseGuid": corpse, "requestId": "open", "openingTransportAttempted": true}}
	step := func() behavior.Decision {
		t.Helper()
		ls, err := host.Enqueue(behavior.Event{Kind: "corpse_loot_observation", Value: loot})
		if err != nil {
			t.Fatal(err)
		}
		out, err := host.Decide(context.Background(), map[string]any{"observationSequence": p.observe(obs), "observationSequences": map[string]any{"corpse_loot": fallbackGUID(ls)}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	close := step()
	a, _ := close.Output["action"].(map[string]any)
	if a["capability"] != "loot_close" {
		t.Fatal("death skipped current physical window cleanup", close.Output)
	}
	id, err := host.ReserveAction(close)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionSettledUnverified, Payload: map[string]any{"capability": "loot_close", "corpse_guid": corpse}}}); err != nil {
		t.Fatal(err)
	}
	loot["state"] = map[string]any{}
	loot["operation"].(map[string]any)["terminal"] = true
	release := step()
	a, _ = release.Output["action"].(map[string]any)
	if a["capability"] != "release_spirit" || host.Status().Pending != 0 {
		t.Fatal("unexecuted release was checkpoint-reserved during close", release.Output, host.Status())
	}
}
