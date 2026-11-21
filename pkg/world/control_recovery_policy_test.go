package world

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func TestControlRecoverySharedPolicyRankTuningReloadAndHealingAfterControl(t *testing.T) {
	source := readEncounterBundle(t)
	host, err := behavior.NewSessionWithOptions(context.Background(), fallbackSession, source, behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	observer := &fallbackPolicyTest{t: t, host: host}
	obs := positionedHealObservation()
	obs.Support = &actionsv1.EncounterSupport{}
	obs.Support.ControlRecovery = &actionsv1.ControlRecoveryObservation{FleeingKnown: true, Fleeing: true,
		FearAuras: []*actionsv1.ObservedAura{{Slot: 5, SpellId: 7399, Revision: 1, MetadataKnown: true, Negative: true}},
		Options: []*actionsv1.ControlBreakOption{{SpellId: 59752, Ready: true, CategoryCooldownMs: 120000, Rank: 1}, {SpellId: 18499, Ready: true, CooldownMs: 30000, Rank: 1},
			{SpellId: 42292, ItemGuid: 0x4000000000000059, ItemSlot: 13, Ready: true, CooldownMs: 120000}}}
	step := func() behavior.Decision {
		t.Helper()
		d, err := host.Decide(context.Background(), map[string]any{"kind": "encounter", "observationSequence": observer.observe(obs)})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	d := step()
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] != "fear_break" || a["spellId"] != float64(18499) || a["targetGuid"] != fallbackGUID(obs.SelfGuid) {
		t.Fatal("native-like choice or healing displaced fear recovery", d.Output)
	}
	edited := strings.Replace(source, "spell_weights={}", "spell_weights={[59752]=10}", 1)
	if _, err := host.Reload(context.Background(), edited, host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	d = step()
	a, _ = d.Output["action"].(map[string]any)
	if a["spellId"] != float64(59752) || a["auraRevision"] != "1" {
		t.Fatal("same-session editable selection ignored", d.Output)
	}
	id, err := host.ReserveAction(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionUnknown,
		Payload: map[string]any{"capability": "fear_break", "target_guid": obs.SelfGuid, "spell_id": 59752, "aura_revision": "1"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Reload(context.Background(), edited+"\n-- retain own unknown source", host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if d := step(); d.Output["action"] != nil || host.Status().Pending != 1 {
		t.Fatal("unknown control break lost custody", d.Output, host.Status())
	}
	// Native positive clearance settles unverified, never as own spell success.
	if _, err := host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionSettledUnverified,
		Payload: map[string]any{"capability": "fear_break", "target_guid": obs.SelfGuid, "spell_id": 59752, "aura_revision": "1", "effect_observed": false}}}); err != nil {
		t.Fatal(err)
	}
	obs.Support.ControlRecovery.Fleeing = false
	obs.Support.ControlRecovery.FearAuras = nil
	d = step()
	a, _ = d.Output["action"].(map[string]any)
	if host.Status().Pending != 0 || a["capability"] != "heal" {
		t.Fatal("same policy did not resume normal support", d.Output, host.Status())
	}
}

func TestControlRecoverySharedPolicyNeedsCurrentReadyFearFacts(t *testing.T) {
	for _, mode := range []string{"no_aura", "no_fleeing", "unknown_fleeing", "busy_channel_or_cast", "attempted_application"} {
		t.Run(mode, func(t *testing.T) {
			host, err := behavior.NewSessionWithOptions(context.Background(), fallbackSession, readEncounterBundle(t), behavior.SessionOptions{ExecutionLimit: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer host.Close()
			observer := &fallbackPolicyTest{t: t, host: host}
			obs := positionedHealObservation()
			obs.Support = &actionsv1.EncounterSupport{}
			c := &actionsv1.ControlRecoveryObservation{FleeingKnown: true, Fleeing: true, FearAuras: []*actionsv1.ObservedAura{{Slot: 5, SpellId: 7399, Revision: 1, MetadataKnown: true, Negative: true}}, Options: []*actionsv1.ControlBreakOption{{SpellId: 59752, Ready: true}}}
			obs.Support.ControlRecovery = c
			switch mode {
			case "no_aura":
				c.FearAuras = nil
			case "no_fleeing":
				c.Fleeing = false
			case "unknown_fleeing":
				c.FleeingKnown = false
			case "busy_channel_or_cast":
				c.Options[0].Ready = false
			case "attempted_application":
				c.AttemptedAuraRevisions = []uint64{1}
			}
			d, err := host.Decide(context.Background(), map[string]any{"kind": "encounter", "observationSequence": observer.observe(obs)})
			if err != nil {
				t.Fatal(err)
			}
			a, _ := d.Output["action"].(map[string]any)
			if a["capability"] == "fear_break" {
				t.Fatal("unproven fear/source chosen", mode, d.Output)
			}
		})
	}
}

func TestControlRecoveryInstantBreakPrecedesHeldLootWindowCleanup(t *testing.T) {
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
	observer := &fallbackPolicyTest{t: t, host: host}
	obs := positionedHealObservation()
	obs.Interaction = &actionsv1.InteractionObservation{Owner: "loot", Token: "same-loot-token"}
	obs.Support = &actionsv1.EncounterSupport{ControlRecovery: &actionsv1.ControlRecoveryObservation{FleeingKnown: true, Fleeing: true,
		FearAuras: []*actionsv1.ObservedAura{{Slot: 5, SpellId: 7399, Revision: 1, MetadataKnown: true, Negative: true}}, Options: []*actionsv1.ControlBreakOption{{SpellId: 59752, Ready: true}}}}
	corpse := fallbackGUID(0xf130000001000001)
	loot := map[string]any{"sessionId": fallbackSession, "selfGuid": fallbackGUID(fallbackSelf), "self": map[string]any{"present": true, "aliveKnown": true, "alive": true},
		"actions": map[string]any{"interactionOwner": "loot"}, "state": map[string]any{"pendingCorpseGuid": corpse, "phase": "waiting_open"}, "operation": map[string]any{"corpseGuid": corpse, "requestId": "open", "openingTransportAttempted": true}}
	ls, err := host.Enqueue(behavior.Event{Kind: "corpse_loot_observation", Value: loot})
	if err != nil {
		t.Fatal(err)
	}
	d, err := host.Decide(context.Background(), map[string]any{"kind": "encounter", "observationSequence": observer.observe(obs), "observationSequences": map[string]any{"corpse_loot": fallbackGUID(ls)}})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] != "fear_break" {
		t.Fatal("loot close/STOP displaced instant legal control break", d.Output)
	}
}

func TestControlRecoveryInstantBreakPrecedesTrainingHandoffInCombinedPolicy(t *testing.T) {
	host, err := behavior.NewSessionWithOptions(context.Background(), fallbackSession, readEncounterBundle(t), behavior.SessionOptions{ExecutionLimit: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	observer := &fallbackPolicyTest{t: t, host: host}
	obs := positionedHealObservation()
	obs.Interaction = &actionsv1.InteractionObservation{Owner: "training", Token: "unchanged-private-training-token"}
	obs.Support = &actionsv1.EncounterSupport{ControlRecovery: &actionsv1.ControlRecoveryObservation{FleeingKnown: true, Fleeing: true,
		FearAuras: []*actionsv1.ObservedAura{{Slot: 5, SpellId: 7399, Revision: 1, MetadataKnown: true, Negative: true}}, Options: []*actionsv1.ControlBreakOption{{SpellId: 59752, Ready: true}}}}
	// The shared encounter's real engagement/healing facts normally request
	// interaction release. Instant self recovery must run without that STOP.
	obs.Targets[fallbackGUID(fallbackNPC)].Engaged = true
	d, err := host.Decide(context.Background(), map[string]any{"kind": "encounter", "observationSequence": observer.observe(obs)})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] != "fear_break" || d.Output["interaction"] != nil {
		t.Fatal("combined policy replaced self break with interaction STOP/release", d.Output)
	}
}
