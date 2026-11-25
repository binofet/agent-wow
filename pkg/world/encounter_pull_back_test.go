package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"testing"
)

func pullBackObservation() *actionsv1.EncounterObservationResponse {
	obs := waitTaskObservation()
	addFallbackTarget(obs, fallbackNPC, 0, 1000)
	npc := obs.Targets[fallbackGUID(fallbackNPC)]
	npc.X, npc.Y, npc.Z = 30, 10, 10
	npc.AttackObservation = &actionsv1.EncounterAttackObservation{}
	obs.Spells["585"] = &actionsv1.EncounterSpell{Known: true, Ready: true, Available: true, MetadataKnown: true, Capability: "offense", DirectDamage: 10, MaxRange: 30, CastTimeMs: 1000}
	return obs
}
func beginPullBack(t *testing.T) (*fallbackPolicyTest, *actionsv1.EncounterObservationResponse) {
	t.Helper()
	p, obs := newFallbackPolicyTest(t, ""), pullBackObservation()
	waitTaskCommand(t, p, map[string]any{"task": "pull_back", "id": "back", "targetGuid": fallbackGUID(fallbackNPC), "tankGuid": "3", "spellId": 585, "deadlineMonotonicMs": 999999999999})
	return p, obs
}
func pullAction(t *testing.T, p *fallbackPolicyTest, obs *actionsv1.EncounterObservationResponse, capability string) behavior.Decision {
	t.Helper()
	d := p.decide(observeSparseEncounter(t, p, obs))
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] != capability {
		t.Fatalf("want %s, got %+v", capability, d.Output)
	}
	return d
}
func preparePullCast(t *testing.T, p *fallbackPolicyTest, obs *actionsv1.EncounterObservationResponse) behavior.Decision {
	t.Helper()
	d := pullAction(t, p, obs, "movement_owner")
	settleWaitOwnership(t, p, d, behavior.ActionConfirmed, "acquire", "task:back", "aim-token")
	obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "task:back", RemainingMs: 3000}
	pullAction(t, p, obs, "approach_target")
	obs.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: fallbackNPC, VisitId: 1, RangeYards: 28, RequireLos: true, Ready: true}
	d = pullAction(t, p, obs, "movement_owner")
	if d.Output["action"].(map[string]any)["mode"] != "release" {
		t.Fatal(d.Output)
	}
	settleWaitOwnership(t, p, d, behavior.ActionConfirmed, "release", "task:back", "")
	obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
	d = pullAction(t, p, obs, "movement_owner")
	if d.Output["action"].(map[string]any)["owner"] != "spell:pull:back" {
		t.Fatal("did not reuse exact spell request", d.Output)
	}
	settleWaitOwnership(t, p, d, behavior.ActionConfirmed, "acquire", "spell:pull:back", "spell-token")
	obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "spell:pull:back", RemainingMs: 3000}
	return pullAction(t, p, obs, "cast_spell")
}
func settlePullCast(t *testing.T, p *fallbackPolicyTest, d behavior.Decision, outcome behavior.ActionOutcome, effect bool, target uint64) {
	t.Helper()
	id, err := p.host.ReserveAction(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: outcome,
		Payload: map[string]any{"capability": "cast_spell", "spell_id": 585, "target_guid": fallbackGUID(target), "server_cast_go": outcome == behavior.ActionConfirmed, "transport_written": outcome != behavior.ActionNotDispatched, "effect_observed": effect, "phase": "fixture"}}}); err != nil {
		t.Fatal(err)
	}
}
func releasePullSpell(t *testing.T, p *fallbackPolicyTest, obs *actionsv1.EncounterObservationResponse) {
	t.Helper()
	d := pullAction(t, p, obs, "movement_owner")
	if d.Output["action"].(map[string]any)["owner"] != "spell:pull:back" || d.Output["action"].(map[string]any)["mode"] != "release" {
		t.Fatal(d.Output)
	}
	settleWaitOwnership(t, p, d, behavior.ActionConfirmed, "release", "spell:pull:back", "")
	obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
	p.decide(observeSparseEncounter(t, p, obs)) // Existing spell lease cleanup observes the release.
}
func TestPullBackOneActualCastThenInCombatReturnAndTankHandoff(t *testing.T) {
	p, obs := beginPullBack(t)
	d := preparePullCast(t, p, obs)
	id, err := p.host.ReserveAction(d)
	if err != nil {
		t.Fatal(err)
	}
	// Pending cast is not another cast or movement.
	held := p.decide(observeSparseEncounter(t, p, obs)).Output
	if held["action"] != nil {
		t.Fatal("pending cast repeated", held)
	}
	if _, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionConfirmed,
		Payload: map[string]any{"capability": "cast_spell", "spell_id": 585, "target_guid": fallbackGUID(fallbackNPC), "effect_observed": true, "server_cast_go": true, "transport_written": true}}}); err != nil {
		t.Fatal(err)
	}
	obs.Targets[fallbackGUID(fallbackNPC)].AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: fallbackSelf}
	releasePullSpell(t, p, obs)
	d = pullAction(t, p, obs, "movement_owner")
	settleWaitOwnership(t, p, d, behavior.ActionConfirmed, "acquire", "task:back", "return-token")
	obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "task:back", RemainingMs: 3000}
	d = pullAction(t, p, obs, "approach_target")
	if d.Output["action"].(map[string]any)["targetGuid"] != "3" {
		t.Fatal("not returning to bound tank", d.Output)
	}
	obs.Targets[fallbackGUID(fallbackNPC)].AttackObservation.VictimGuid = 3
	d = pullAction(t, p, obs, "movement_owner")
	if d.Output["task"].(map[string]any)["reason"] != "tank_handoff_observed" {
		t.Fatal(d.Output)
	}
	settleWaitOwnership(t, p, d, behavior.ActionConfirmed, "release", "task:back", "")
	obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
	out := p.decide(observeSparseEncounter(t, p, obs)).Output
	if out["task"].(map[string]any)["phase"] != "complete" {
		t.Fatal(out)
	}
	if _, err = p.host.Reload(context.Background(), readEncounterBundle(t)+"\n-- retain exact pull", p.host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	waitTaskCommand(t, p, map[string]any{"task": "pull_back", "id": "back", "targetGuid": fallbackGUID(fallbackNPC), "tankGuid": "3", "spellId": 585, "deadlineMonotonicMs": 999999999999})
	if out = p.decide(observeSparseEncounter(t, p, obs)).Output; out["action"] != nil && out["action"].(map[string]any)["capability"] == "cast_spell" {
		t.Fatal("retired pull replayed", out)
	}
}

func TestPullBackUnknownCastPersistsWithoutRetry(t *testing.T) {
	p, obs := beginPullBack(t)
	d := preparePullCast(t, p, obs)
	settlePullCast(t, p, d, behavior.ActionUnknown, false, fallbackNPC)
	for i := 0; i < 3; i++ {
		out := p.decide(observeSparseEncounter(t, p, obs)).Output
		if out["action"] != nil {
			t.Fatal("UNKNOWN generated another action", out)
		}
	}
	if p.host.Status().Pending != 1 {
		t.Fatal("UNKNOWN was cleared", p.host.Status())
	}
	if _, err := p.host.Reload(context.Background(), readEncounterBundle(t)+"\n-- unknown remains", p.host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	waitTaskCommand(t, p, map[string]any{"task": "cancel_task", "id": "back"})
	out := p.decide(observeSparseEncounter(t, p, obs)).Output
	if out["action"] != nil || p.host.Status().Pending != 1 {
		t.Fatal("cancel/reload replayed or cleared UNKNOWN", out, p.host.Status())
	}
}

func TestPullBackGoAndWrongTargetEffectAreNotContact(t *testing.T) {
	for _, target := range []uint64{fallbackNPC, fallbackNPC + 1} {
		t.Run(fallbackGUID(target), func(t *testing.T) {
			p, obs := beginPullBack(t)
			d := preparePullCast(t, p, obs)
			settlePullCast(t, p, d, behavior.ActionConfirmed, target != fallbackNPC, target)
			releasePullSpell(t, p, obs)
			for i := 0; i < 3; i++ {
				out := p.decide(observeSparseEncounter(t, p, obs)).Output
				if out["action"] != nil {
					t.Fatal("GO or other spawn authorized action", out)
				}
				task := out["task"].(map[string]any)
				if task["phase"] != "waiting_for_real_pull_contact" {
					t.Fatal(out)
				}
			}
		})
	}
}

func TestPullBackForeignLeaseAndInvalidSpellDoNotCast(t *testing.T) {
	t.Run("foreign lease", func(t *testing.T) {
		p, obs := beginPullBack(t)
		obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "foreign", RemainingMs: 3000}
		out := p.decide(observeSparseEncounter(t, p, obs)).Output
		if out["action"] != nil || out["task"].(map[string]any)["phase"] != "movement_owned_elsewhere" {
			t.Fatal(out)
		}
	})
	for _, kind := range []string{"unlearned", "weapon", "protected", "engaged"} {
		t.Run(kind, func(t *testing.T) {
			p, obs := beginPullBack(t)
			switch kind {
			case "unlearned":
				obs.Spells["585"].Known = false
			case "weapon":
				obs.Spells["585"].WeaponStrike = true
			case "protected":
				obs.Targets[fallbackGUID(fallbackNPC)].Protected = true
			case "engaged":
				obs.Targets[fallbackGUID(fallbackNPC)].AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3}
			}
			out := p.decide(observeSparseEncounter(t, p, obs)).Output
			if a, _ := out["action"].(map[string]any); a["spellId"] == float64(585) || out["task"].(map[string]any)["phase"] != "complete" {
				t.Fatal(out)
			}
		})
	}
}

func TestPullBackExplicitTaskYieldsOnlyExistingPriorLease(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "band"}[modern], func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			obs := sludgePriorObservation()
			obs.Targets[fallbackGUID(fallbackNPC)].X = 30
			obs.Spells["585"] = &actionsv1.EncounterSpell{Known: true, Ready: true, Available: true, MetadataKnown: true, Capability: "offense", DirectDamage: 10, MaxRange: 30, CastTimeMs: 1000}
			settlePriorLease(t, p, obs, modern)
			waitTaskCommand(t, p, map[string]any{"task": "pull_back", "id": "back", "targetGuid": fallbackGUID(fallbackNPC), "tankGuid": "3", "spellId": 585, "deadlineMonotonicMs": 999999999999})
			d := p.decide(observeRanged(t, p, obs, modern))
			a, _ := d.Output["action"].(map[string]any)
			if a["capability"] != "movement_owner" || a["mode"] != "release" || a["owner"] != "ranged:maraudon_sludge_backline" {
				t.Fatal("did not cooperatively release exact prior hold", d.Output)
			}
			id, err := p.host.ReserveAction(d)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "movement_owner", "mode": "release", "owner": "ranged:maraudon_sludge_backline", "result": map[string]any{"sessionId": fallbackSession, "selfGuid": "4"}}}})
			if err != nil {
				t.Fatal(err)
			}
			obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
			d = p.decide(observeRanged(t, p, obs, modern))
			a, _ = d.Output["action"].(map[string]any)
			if a["capability"] != "movement_owner" || a["owner"] != "task:back" || a["mode"] != "acquire" {
				t.Fatal("task failed normal lease acquisition", d.Output)
			}
			settleWaitOwnership(t, p, d, behavior.ActionConfirmed, "acquire", "task:back", "own-task")
			obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "task:back", RemainingMs: 3000}
			d = p.decide(observeRanged(t, p, obs, modern))
			a, _ = d.Output["action"].(map[string]any)
			if a["capability"] != "approach_target" || d.Output["task"].(map[string]any)["phase"] == "complete" {
				t.Fatal("foreign hold release retired task", d.Output)
			}

		})
	}
}

func TestPullBackCancellationStopsOnlyOwnReturnThenWaitsForWitness(t *testing.T) {
	p, obs := beginPullBack(t)
	d := preparePullCast(t, p, obs)
	settlePullCast(t, p, d, behavior.ActionConfirmed, true, fallbackNPC)
	releasePullSpell(t, p, obs)
	d = pullAction(t, p, obs, "movement_owner")
	settleWaitOwnership(t, p, d, behavior.ActionConfirmed, "acquire", "task:back", "return-token")
	obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "task:back", RemainingMs: 3000}
	pullAction(t, p, obs, "approach_target")
	obs.Players["4"].Moving = true
	obs.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: 3, VisitId: 1}
	waitTaskCommand(t, p, map[string]any{"task": "cancel_task", "id": "back"})
	d = pullAction(t, p, obs, "stop_movement")
	// Merely choosing STOP is not a stopped witness; never repeat it or release.
	out := p.decide(observeSparseEncounter(t, p, obs)).Output
	if out["action"] != nil {
		t.Fatal("repeated STOP or released moving body", out)
	}
	obs.Players["4"].MovementKnown = false
	out = p.decide(observeSparseEncounter(t, p, obs)).Output
	if out["action"] != nil {
		t.Fatal("unknown motion released lease", out)
	}
	obs.Players["4"].MovementKnown = true
	obs.Players["4"].Moving = false
	d = pullAction(t, p, obs, "movement_owner")
	if d.Output["action"].(map[string]any)["mode"] != "release" {
		t.Fatal(d.Output)
	}
}

func TestPullBackKnownCastRefusalDoesNotRetry(t *testing.T) {
	p, obs := beginPullBack(t)
	d := preparePullCast(t, p, obs)
	settlePullCast(t, p, d, behavior.ActionNotDispatched, false, fallbackNPC)
	releasePullSpell(t, p, obs)
	for i := 0; i < 3; i++ {
		out := p.decide(observeSparseEncounter(t, p, obs)).Output
		if a, _ := out["action"].(map[string]any); a["capability"] == "cast_spell" {
			t.Fatal("refusal retried", out)
		}
		if out["task"].(map[string]any)["reason"] != "pull_cast_refused" {
			t.Fatal(out)
		}
	}
}
