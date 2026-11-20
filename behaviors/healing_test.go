package behaviors

import (
	"context"
	"errors"
	"maps"
	"os"
	"strconv"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func healingPolicy(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile("healing.lua")
	if err != nil {
		t.Fatal(err)
	}
	return string(source)
}

func newHealingSession(t *testing.T, source string) *behavior.Session {
	t.Helper()
	session, err := behavior.NewSession(context.Background(), "healer-session", source)
	// Cold VM initialization can lose the fixed 5ms budget on a race build.
	// Retry loading once. Observations and decisions keep their original budget.
	if errors.Is(err, context.DeadlineExceeded) {
		session, err = behavior.NewSession(context.Background(), "healer-session", source)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func patient(guid string, health, maxHealth int, distance float64, owner, attacked bool) map[string]any {
	return map[string]any{
		"guid": guid, "health": health, "maxHealth": maxHealth, "distance": distance,
		"isOwner": owner, "underAttack": attacked,
	}
}

func healRank(id, level, rank, amount, cost, castMs int, minRange, maxRange float64, selfOnly bool) map[string]any {
	return map[string]any{
		"spellId": id, "level": level, "rank": rank, "amount": amount, "cost": cost, "castMs": castMs,
		"minRange": minRange, "maxRange": maxRange, "selfOnly": selfOnly,
	}
}

func healingObservation(ready bool, mana int, patients, spells []any) map[string]any {
	return map[string]any{
		"caster":   map[string]any{"guid": "1", "ready": ready, "mana": mana},
		"patients": patients, "spells": spells,
	}
}

func enqueueHealing(t *testing.T, session *behavior.Session, view map[string]any) string {
	t.Helper()
	sequence, err := session.Enqueue(behavior.Event{Kind: "healing_observation", Value: view})
	if err != nil {
		t.Fatal(err)
	}
	return strconv.FormatUint(sequence, 10)
}

func decideHealing(t *testing.T, session *behavior.Session, sequence string) behavior.Decision {
	t.Helper()
	decision, err := session.Decide(context.Background(), map[string]any{
		"kind": "healing", "observationSequence": sequence,
	})
	if err != nil {
		t.Fatal(err)
	}
	return decision
}

func requireHeal(t *testing.T, decision behavior.Decision, target string, spell int) {
	t.Helper()
	if target == "" {
		if len(decision.Output) != 0 {
			t.Fatalf("wanted no heal, got %v", decision.Output)
		}
		return
	}
	want := map[string]any{
		"capability": "heal", "sessionId": "healer-session", "selfGuid": "1",
		"targetGuid": target, "spellId": float64(spell),
	}
	action, _ := decision.Output["action"].(map[string]any)
	if len(decision.Output) != 1 || !maps.Equal(action, want) {
		t.Fatalf("heal proposal = %v, want %v", decision.Output, want)
	}
}

func requireHealApproach(t *testing.T, decision behavior.Decision, target string, spell int, minRange, maxRange float64) {
	t.Helper()
	want := map[string]any{
		"capability": "heal", "sessionId": "healer-session", "selfGuid": "1",
		"targetGuid": target, "spellId": float64(spell), "needsApproach": true,
		"rangeYards": maxRange, "minRangeYards": minRange, "requireLos": true,
	}
	action, _ := decision.Output["action"].(map[string]any)
	if len(decision.Output) != 1 || !maps.Equal(action, want) {
		t.Fatalf("heal catch-up proposal = %v, want %v", decision.Output, want)
	}
}

func TestHealingPatientAndRankPolicy(t *testing.T) {
	session := newHealingSession(t, healingPolicy(t))
	lesserHeal := healRank(2050, 1, 1, 35, 30, 1500, 0, 40, false)
	heal := healRank(2054, 16, 1, 120, 85, 3000, 0, 40, false)
	flashHeal := healRank(2061, 20, 1, 90, 95, 1500, 0, 40, false)
	ordinary := []any{lesserHeal, heal, flashHeal}
	for _, test := range []struct {
		name     string
		patients []any
		spells   []any
		mana     int
		target   string
		spell    int
		approach float64
	}{
		{
			name: "standing party heal and mana efficiency",
			patients: []any{patient("1", 100, 100, 0, false, false),
				patient("3", 84, 100, 35, true, false)},
			mana: 100, target: "3", spell: 2050,
		},
		{
			name: "urgent self precedes the party",
			patients: []any{patient("3", 1, 100, 5, true, true),
				patient("1", 49, 100, 0, false, false)},
			mana: 100, target: "1", spell: 2050,
		},
		{
			name: "self at fifty yields to critically hurt ally",
			patients: []any{patient("1", 50, 100, 0, false, false),
				patient("3", 20, 100, 5, true, false)},
			mana: 100, target: "3", spell: 2061,
		},
		{
			name: "current attack breaks ordinary triage and favors speed",
			patients: []any{patient("4", 49, 100, 15, false, false),
				patient("3", 70, 140, 15, true, true)},
			mana: 100, target: "3", spell: 2061,
		},
		{
			name: "critical patient precedes less injured attacked patient",
			patients: []any{patient("3", 65, 100, 15, true, true),
				patient("4", 30, 100, 15, false, false)},
			mana: 100, target: "4", spell: 2061,
		},
		{
			name: "learned forty-yard heal reaches past the former thirty-five-yard cap",
			patients: []any{patient("3", 10, 100, 35.001, true, true),
				patient("4", 20, 100, 10, false, false)},
			mana: 100, target: "3", spell: 2061,
		},
		{
			name: "outside range selects catch-up instead of ignoring the injured patient",
			patients: []any{patient("3", 10, 100, 4.99, true, false),
				patient("4", 20, 100, 10.01, false, false),
				patient("5", 80, 100, 10, false, false)},
			spells: []any{healRank(2050, 1, 1, 35, 30, 1500, 5, 10, false)},
			mana:   30, target: "4", spell: 2050, approach: 10,
		},
		{
			name: "self-only spell remains available for caster",
			patients: []any{patient("3", 10, 100, 5, true, true),
				patient("1", 70, 100, 0, false, false)},
			spells: []any{healRank(2050, 1, 1, 35, 30, 1500, 0, 0, true)},
			mana:   30, target: "1", spell: 2050,
		},
		{
			name:     "faster rank excluded when one mana short",
			patients: []any{patient("4", 10, 100, 10, false, true)},
			mana:     94, target: "4", spell: 2054,
		},
		{
			name: "owner wins equal health percentage",
			patients: []any{patient("4", 60, 100, 10, false, false),
				patient("1", 60, 100, 0, false, false),
				patient("3", 30, 50, 15, true, false)},
			mana: 30, target: "3", spell: 2050,
		},
		{
			name: "tied party guids retain exact decimal order",
			patients: []any{patient("18446744073709551611", 65, 100, 10, false, false),
				patient("18446744073709551610", 65, 100, 10, false, false)},
			mana: 30, target: "18446744073709551610", spell: 2050,
		},
		{
			name:     "equal spell score ordered by level then rank then id",
			patients: []any{patient("3", 70, 100, 10, true, false)},
			spells: []any{healRank(2051, 5, 2, 35, 30, 1500, 0, 40, false),
				healRank(2053, 5, 3, 35, 30, 1500, 0, 40, false),
				healRank(2059, 4, 4, 35, 30, 1500, 0, 40, false),
				healRank(2052, 5, 3, 35, 30, 1500, 0, 40, false)},
			mana: 30, target: "3", spell: 2052,
		},
		{
			name: "standing threshold is strict even under attack",
			patients: []any{patient("3", 85, 100, 5, true, true),
				patient("4", 0, 100, 5, false, false)},
			mana: 100,
		},
		{
			name:     "empty learned ready ranks yields no action",
			patients: []any{patient("3", 20, 100, 5, true, true)},
			spells:   []any{},
			mana:     100,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			spells := test.spells
			if spells == nil {
				spells = ordinary
			}
			sequence := enqueueHealing(t, session, healingObservation(true, test.mana, test.patients, spells))
			decision := decideHealing(t, session, sequence)
			if test.approach > 0 {
				requireHealApproach(t, decision, test.target, test.spell, 5, test.approach)
			} else {
				requireHeal(t, decision, test.target, test.spell)
			}
		})
	}
}

func TestHealingCatchupTracksInjuredPatientAndThenHealsAfterMovement(t *testing.T) {
	session := newHealingSession(t, healingPolicy(t))
	spells := []any{healRank(2050, 1, 1, 35, 30, 1500, 0, 40, false)}
	ally := patient("4", 20, 100, 60, false, true)
	deadOwner := patient("3", 0, 100, 10, true, false)
	view := healingObservation(true, 30, []any{deadOwner, ally}, spells)
	requireHealApproach(t, decideHealing(t, session, enqueueHealing(t, session, view)), "4", 2050, 0, 40)
	ally["distance"] = 36
	requireHeal(t, decideHealing(t, session, enqueueHealing(t, session, view)), "4", 2050)
	ally["health"] = 100
	ally["distance"] = 60
	requireHeal(t, decideHealing(t, session, enqueueHealing(t, session, view)), "", 0)
}

func TestHealingCatchupUsesLongestLearnedEligibleRange(t *testing.T) {
	session := newHealingSession(t, healingPolicy(t))
	spells := []any{
		healRank(2050, 1, 1, 35, 30, 1500, 0, 40, false),
		healRank(2054, 16, 1, 120, 30, 1500, 0, 30, false),
	}
	view := healingObservation(true, 30, []any{patient("4", 20, 100, 60, false, true)}, spells)
	requireHealApproach(t, decideHealing(t, session, enqueueHealing(t, session, view)), "4", 2050, 0, 40)
}

func TestHealingOnlyProposesFromCurrentUnconsumedObservation(t *testing.T) {
	ctx := context.Background()
	source := healingPolicy(t)
	session := newHealingSession(t, source)
	spells := []any{healRank(2050, 1, 1, 35, 30, 1500, 0, 40, false)}
	patients := []any{patient("3", 50, 100, 10, true, true)}
	older := enqueueHealing(t, session, healingObservation(true, 30, patients, spells))
	current := enqueueHealing(t, session, healingObservation(true, 30, patients, spells))
	requireHeal(t, decideHealing(t, session, older), "", 0)
	decision := decideHealing(t, session, current)
	requireHeal(t, decision, "3", 2050)
	id, err := session.ReserveAction(decision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
		SessionID: "healer-session", ActionID: id, Outcome: behavior.ActionTransportAccepted,
	}})
	if err != nil {
		t.Fatal(err)
	}
	requireHeal(t, decideHealing(t, session, current), "", 0)
	blocked := enqueueHealing(t, session, healingObservation(false, 30, patients, spells))
	requireHeal(t, decideHealing(t, session, blocked), "", 0)
	disabled := healingObservation(true, 30, patients, spells)
	disabled["standing"] = false
	requireHeal(t, decideHealing(t, session, enqueueHealing(t, session, disabled)), "", 0)

	revision := session.Status().Revision
	newRevision, err := session.Reload(ctx, source+"\n-- reloaded", revision)
	if err != nil || newRevision == revision {
		t.Fatalf("reload revision=%q error=%v", newRevision, err)
	}
	requireHeal(t, decideHealing(t, session, current), "", 0)
	_, err = session.Enqueue(behavior.Event{Kind: "healing_effect", Receipt: &behavior.ActionReceipt{
		SessionID: "healer-session", ActionID: id, Outcome: behavior.ActionConfirmed,
	}})
	if err != nil {
		t.Fatal(err)
	}
	fresh := enqueueHealing(t, session, healingObservation(true, 30, patients, spells))
	requireHeal(t, decideHealing(t, session, fresh), "3", 2050)
}

func TestHealingConsidersTheEntireRaid(t *testing.T) {
	session := newHealingSession(t, healingPolicy(t))
	patients := make([]any, 40)
	for i := range patients {
		patients[i] = patient(strconv.Itoa(i+1), 84-i, 100, 15, false, false)
	}
	spells := make([]any, 25)
	for i := range spells {
		spells[i] = healRank(2000+i, i+1, i+1, 100+i, 10, 1500, 0, 40, false)
	}
	sequence := enqueueHealing(t, session, healingObservation(true, 100, patients, spells))
	requireHeal(t, decideHealing(t, session, sequence), "40", 2024)
}
