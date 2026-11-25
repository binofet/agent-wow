package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"os"
	"testing"
)

func nativeFollowPrior(t *testing.T) (*fallbackPolicyTest, *actionsv1.EncounterObservationResponse, string) {
	t.Helper()
	source := readEncounterBundle(t)
	if path := os.Getenv("BUDDY_TEST_ENCOUNTER_BUNDLE"); path != "" {
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		source = string(b)
	}
	h, e := behavior.NewSession(context.Background(), fallbackSession, source)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(h.Close)
	p := &fallbackPolicyTest{t: t, host: h}
	o := sludgePriorObservation()
	o.Spells = map[string]*actionsv1.EncounterSpell{}
	o.Navigation.Following = false
	o.Navigation.OwnerGuid = 3
	o.Navigation.BodyMovementSupported = true
	self := o.Players["4"]
	self.CombatKnown = true
	self.X, self.Y, self.Z = 0, 0, 0
	o.Players["3"].CombatKnown = true
	o.Players["3"].X = 54
	n := o.Targets[fallbackGUID(fallbackNPC)]
	n.X, n.Y, n.Z = 80, 0, 0
	settlePriorLease(t, p, o, false)
	o.Navigation.MovementOwner.RemainingMs = 211000
	return p, o, source
}

func settleFollowPrior(t *testing.T, p *fallbackPolicyTest, d behavior.Decision, mode string, outcome behavior.ActionOutcome) uint64 {
	t.Helper()
	id, e := p.host.ReserveAction(d)
	if e != nil {
		t.Fatal(e)
	}
	owner := "ranged:maraudon_sludge_backline"
	state := map[string]any{"sessionId": fallbackSession, "selfGuid": "4", "movementOwner": map[string]any{"active": mode != "release", "owner": owner}}
	result := state
	if mode != "release" {
		result = map[string]any{"token": "follow-prior-token", "state": state}
	}
	_, e = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: outcome, Payload: map[string]any{"capability": "movement_owner", "mode": mode, "owner": owner, "result": result}}})
	if e != nil {
		t.Fatal(e)
	}
	return id
}

func TestLearnedPriorReleasesForNativeFollowWithoutRally(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_attack", true: "aged_attack"}[stale], func(t *testing.T) {
			p, o, _ := nativeFollowPrior(t)
			before := p.decide(observeRanged(t, p, o, false)).Output
			status, _ := before["rangedStance"].(map[string]any)
			if status["phase"] != "supported_ranged_option_unavailable" {
				t.Fatal("fixture did not reproduce measured idle hold", before)
			}
			o.Navigation.Following = true
			if stale {
				o.Targets[fallbackGUID(fallbackNPC)].AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3, AgeMs: 6000}
			}
			d := p.decide(observeRanged(t, p, o, false))
			a, _ := d.Output["action"].(map[string]any)
			if a["capability"] != "movement_owner" || a["mode"] != "release" || a["owner"] != "ranged:maraudon_sludge_backline" || a["token"] != "prior-fixture" {
				t.Fatal("safe native follow did not release exact old lease", d.Output)
			}
			settleFollowPrior(t, p, d, "release", behavior.ActionConfirmed)
			o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
			for _, x := range []float32{0, 43} {
				o.Players["4"].X = x
				out := p.decide(observeRanged(t, p, o, false)).Output
				if out["action"] != nil || !o.Navigation.Following {
					t.Fatal("default reacquired or fought continued native follow", out)
				}
			}
			// Current close danger resumes the existing finite lease and real old-ABI
			// withdrawal proposal even with no supported ranged offensive spell.
			o.Targets[fallbackGUID(fallbackNPC)].X = o.Players["4"].X + 3
			d = p.decide(observeRanged(t, p, o, false))
			a, _ = d.Output["action"].(map[string]any)
			if a["mode"] != "acquire" || a["owner"] != "ranged:maraudon_sludge_backline" {
				t.Fatal("near danger did not restore bounded defense custody", d.Output)
			}
			settleFollowPrior(t, p, d, "acquire", behavior.ActionConfirmed)
			o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "ranged:maraudon_sludge_backline"}
			out := p.decide(observeRanged(t, p, o, false)).Output
			a, _ = out["action"].(map[string]any)
			if a["capability"] != "move_body" {
				t.Fatal("native follow suppressed close-hazard withdrawal", out)
			}
		})
	}
}

func TestNativeFollowDoesNotOverrideExplicitDangerOrUnknownFacts(t *testing.T) {
	for _, kind := range []string{"explicit_order", "fresh_attack", "stale_victim_fighting", "self_combat", "unknown_pose", "estimated_self", "inferred_self", "near_hazard"} {
		t.Run(kind, func(t *testing.T) {
			p, o, _ := nativeFollowPrior(t)
			o.Navigation.Following = true
			n := o.Targets[fallbackGUID(fallbackNPC)]
			switch kind {
			case "explicit_order":
				waitTaskCommand(t, p, map[string]any{"task": "ranged_only", "id": "maraudon_sludge_backline", "creatureEntry": 12222, "mapId": 349, "visitId": "1", "minRangeYards": 14, "maxRangeYards": 36, "clearanceYards": 2, "durationMs": 600000})
			case "fresh_attack":
				n.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3}
			case "stale_victim_fighting":
				n.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3, AgeMs: 6000}
				o.Players["3"].InCombat = true
			case "self_combat":
				o.Players["4"].InCombat = true
			case "unknown_pose":
				n.PositionKnown = false
			case "estimated_self":
				o.Players["4"].PositionEstimated = true
			case "inferred_self":
				o.Players["4"].PositionInferred = true
			case "near_hazard":
				n.X = 3
			}
			out := p.decide(observeRanged(t, p, o, false)).Output
			a, _ := out["action"].(map[string]any)
			if a["capability"] == "movement_owner" && a["mode"] == "release" {
				t.Fatal("follow silently discarded explicit order/danger/unknown geometry", out)
			}
			if kind == "near_hazard" && a["capability"] != "move_body" {
				t.Fatal("close danger failed existing withdrawal", out)
			}
		})
	}
}

func TestNativeFollowLeaseReleaseUnknownSurvivesReloadAndInactiveSample(t *testing.T) {
	p, o, source := nativeFollowPrior(t)
	o.Navigation.Following = true
	d := p.decide(observeRanged(t, p, o, false))
	a, _ := d.Output["action"].(map[string]any)
	if a["mode"] != "release" {
		t.Fatal(d.Output)
	}
	id := settleFollowPrior(t, p, d, "release", behavior.ActionUnknown)
	if _, e := p.host.Reload(context.Background(), source+"\n-- retain unknown exact lease release", p.host.Status().Revision); e != nil {
		t.Fatal(e)
	}
	o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
	o.VisitId = 2
	for i := 0; i < 2; i++ {
		out := p.decide(observeRanged(t, p, o, false)).Output
		if out["action"] != nil {
			t.Fatal("reload/visit/inactive observation replayed UNKNOWN", out)
		}
	}
	pending := p.host.Outstanding()
	if len(pending) != 1 || pending[0].ActionID != id || pending[0].Outcome != behavior.ActionUnknown {
		t.Fatal("lost exact entered custody", pending)
	}
}
