package world

import (
	"context"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
)

// Start with the real shared automatic selector's fresh two-enemy defense.
func automaticControlFixture(t *testing.T) (*fallbackPolicyTest, *actionsv1.EncounterObservationResponse, behavior.Decision) {
	t.Helper()
	p := newFallbackPolicyTest(t, "")
	o := rawEngagementObservation(fallbackLeader, 900)
	o.Players[fallbackGUID(fallbackSelf)].PositionKnown = true
	addFallbackTarget(o, fallbackNPC+1, 0, 800)
	o.Targets[fallbackGUID(fallbackNPC+1)].AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: fallbackSelf, AgeMs: 1000}
	o.ObservedOwnerFocus = fallbackNPC
	o.Spells["118"] = &actionsv1.EncounterSpell{Known: true, Ready: true, Capability: "polymorph", Rank: 1, MaxRange: 30}
	d := p.decide(p.observe(o))
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] != "approach_target" || a["targetGuid"] != fallbackGUID(fallbackNPC+1) {
		t.Fatal("no initial automatic add selection", d.Output)
	}
	return p, o, d
}

func quietAutomaticControl(o *actionsv1.EncounterObservationResponse) {
	o.ObservedOwnerFocus = 0
	o.TargetMovement = nil
	o.Engagement = nil
	for _, player := range o.Players {
		player.CombatKnown = true
		player.InCombat = false
	}
	for _, npc := range o.Targets {
		npc.X = 80
		npc.Engaged = false
		npc.UnitFlagsKnown = true
		npc.UnitFlags = 0
		npc.Health = npc.MaxHealth
		npc.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, TargetKnown: true}
	}
}

func TestAutomaticControlDropsUnengagedStaleBindingAndAllowsCurrentDamage(t *testing.T) {
	p, o, _ := automaticControlFixture(t)
	quietAutomaticControl(o)
	out := p.decide(p.observe(o)).Output
	if out["action"] != nil {
		t.Fatal("automatic control repulled an old unengaged creature", out)
	}
	status, _ := out["controlStatus"].(map[string]any)
	if status["targetGuid"] != nil {
		t.Fatal("obsolete automatic target remained bound", status)
	}
	// A current party attacker remains eligible for ordinary damage even while
	// the standing optional automatic-control request has no suitable add.
	npc := o.Targets[fallbackGUID(fallbackNPC)]
	npc.X = 0
	npc.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: fallbackLeader, AgeMs: 10}
	o.ObservedOwnerFocus = fallbackNPC
	p.expectOffense(p.observe(o), "engaged_defense", fallbackNPC, 133)
	// The optional standing selector can select a NEW eligible add later; this
	// retires a binding, not automatic control or current party defense.
	addFallbackTarget(o, fallbackNPC+2, 0, 800)
	o.Targets[fallbackGUID(fallbackNPC+2)].AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: fallbackSelf, AgeMs: 10}
	out = p.decide(p.observe(o)).Output
	a, _ := out["action"].(map[string]any)
	if (a["capability"] != "approach_target" && a["capability"] != "polymorph") || a["targetGuid"] == fallbackGUID(fallbackNPC+1) || a["targetGuid"] == nil {
		t.Fatal("automatic defense did not select a current eligible add", out)
	}
}

func TestAutomaticControlRetainsObservedEffectAndUnsettledCast(t *testing.T) {
	for _, mode := range []string{"observed effect", "settled unverified cast"} {
		t.Run(mode, func(t *testing.T) {
			p, o, _ := automaticControlFixture(t)
			o.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: fallbackNPC + 1, VisitId: 1, RangeYards: 30, RequireLos: true, Ready: true, Phase: "in_position"}
			d := p.decide(p.observe(o))
			a, _ := d.Output["action"].(map[string]any)
			if a["capability"] != "polymorph" {
				t.Fatal("no current control cast", d.Output)
			}
			id, err := p.host.ReserveAction(d)
			if err != nil {
				t.Fatal(err)
			}
			outcome := behavior.ActionSettledUnverified
			if mode == "observed effect" {
				outcome = behavior.ActionConfirmed
			}
			_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: outcome,
				Payload: map[string]any{"capability": "polymorph", "target_guid": fallbackGUID(fallbackNPC + 1), "spell_id": 118}}})
			if err != nil {
				t.Fatal(err)
			}
			quietAutomaticControl(o)
			if mode == "observed effect" {
				o.ProtectedAuras = []*actionsv1.ProtectedTarget{{TargetGuid: fallbackNPC + 1, CasterGuid: fallbackSelf, Capability: "polymorph", DurationKnown: true, RemainingMs: 20000}}
			}
			out := p.decide(p.observe(o)).Output
			status, _ := out["controlStatus"].(map[string]any)
			if out["action"] != nil || status["targetGuid"] != fallbackGUID(fallbackNPC+1) {
				t.Fatal("effect/cast custody was discarded or retried", out)
			}
			want := "waiting_for_control_aura"
			if mode == "observed effect" {
				want = "control_observed"
			}
			if status["phase"] != want {
				t.Fatal(status)
			}
		})
	}
}

func TestAutomaticControlPendingUnknownReloadAndCancellationKeepExactStop(t *testing.T) {
	p, o, d := automaticControlFixture(t)
	id, err := p.host.ReserveAction(d)
	if err != nil {
		t.Fatal(err)
	}
	quietAutomaticControl(o)
	if out := p.decide(p.observe(o)).Output; out["action"] != nil || p.host.Status().Pending != 1 {
		t.Fatal("entered approach replaced", out)
	}
	_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionUnknown}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.host.Reload(context.Background(), readEncounterBundle(t)+"\n-- preserve automatic custody\n", p.host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	if out := p.decide(p.observe(o)).Output; out["action"] != nil || p.host.Status().Pending != 1 {
		t.Fatal("unknown approach cleared/replayed", out)
	}
	// Cancellation must still know which original approach it owes a STOP for;
	// the UNKNOWN reservation itself remains protected until its exact receipt.
	_, err = p.host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"task": "cancel_control", "id": "automatic:polymorph"}})
	if err != nil {
		t.Fatal(err)
	}
	if out := p.decide(p.observe(o)).Output; out["action"] != nil || p.host.Status().Pending != 1 {
		t.Fatal("cancel bypassed unknown", out)
	}
	_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionNotDispatched}})
	if err != nil {
		t.Fatal(err)
	}
	o.Players[fallbackGUID(fallbackSelf)].MovementKnown = true
	o.Players[fallbackGUID(fallbackSelf)].Moving = true
	out := p.decide(p.observe(o)).Output
	a, _ := out["action"].(map[string]any)
	if a["capability"] != "stop_movement" || a["targetGuid"] != fallbackGUID(fallbackNPC+1) {
		t.Fatal("cancel lost the original exact target STOP", out)
	}
}

func TestExplicitControlStillApproachesOwnerNamedUnengagedTarget(t *testing.T) {
	p, o, _ := automaticControlFixture(t)
	quietAutomaticControl(o)
	_, err := p.host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"task": "control", "id": "owner-control", "technique": "polymorph", "targetGuid": fallbackGUID(fallbackNPC + 1)}})
	if err != nil {
		t.Fatal(err)
	}
	out := p.decide(p.observe(o)).Output
	a, _ := out["action"].(map[string]any)
	if a["capability"] != "approach_target" || a["targetGuid"] != fallbackGUID(fallbackNPC+1) {
		t.Fatal("explicit owner control was treated as automatic", out)
	}
}
