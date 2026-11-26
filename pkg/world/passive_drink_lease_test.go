package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"

	"os"
	"testing"
)

func TestPassivePriorDoesNotStarveConfirmedOwnDrinkLease(t *testing.T) {
	for _, kind := range []string{"observed", "native_estimated", "priest_estimated", "near", "fresh_attack", "combat", "motion_unknown", "moving", "changed_visit", "expired_lease", "foreign_owner", "acquire_unknown"} {
		t.Run(kind, func(t *testing.T) {
			raw, e := os.ReadFile("../../behaviors/consumables.lua")
			if e != nil {
				t.Fatal(e)
			}
			h, e := behavior.NewSession(context.Background(), fallbackSession, "local encounter_consumables=(function()\n"+string(raw)+"\nend)()\n"+withStandingRange(readEncounterBundle(t)))
			if e != nil {
				t.Fatal(e)
			}
			defer h.Close()
			p := &fallbackPolicyTest{t: t, host: h}
			o := sludgePriorObservation()
			key := "4"
			if kind == "priest_estimated" {
				key = "1"
				o.SelfGuid = 1
				o.Players[key] = o.Players["4"]
				delete(o.Players, "4")
				o.Players[key].Guid = 1
			}
			self := o.Players[key]
			self.HealthKnown = true
			self.Health = 100
			self.MaxHealth = 100
			self.PowerKnown = true
			self.PowerType = 0
			self.Power = 2
			self.MaxPower = 100
			self.CombatKnown = true
			self.InCombat = false
			self.MovementKnown = true
			self.Moving = false
			self.X = 0
			self.Y = 0
			self.Z = 0
			for _, v := range o.Players {
				v.CombatKnown = true
				v.InCombat = false
				v.MovementKnown = true
				v.Moving = false
				v.HealthKnown = true
				v.Health = 100
				v.MaxHealth = 100
			}
			o.Spells = map[string]*actionsv1.EncounterSpell{}
			o.Navigation = &actionsv1.NavigationObservation{OwnerGuid: 3, MovementOwner: &actionsv1.MovementOwnerState{}}
			n := o.Targets[fallbackGUID(fallbackNPC)]
			n.X = 122
			n.Y = 0
			n.Z = 0
			n.Engaged = false
			n.AttackObservation = &actionsv1.EncounterAttackObservation{}
			view, _ := consumablePolicyFixture()
			view["sessionId"] = fallbackSession
			view["selfGuid"] = key
			step := func() behavior.Decision {
				t.Helper()
				seq, e := h.Enqueue(behavior.Event{Kind: "inventory_observation", Value: view})
				if e != nil {
					t.Fatal(e)
				}
				d, e := h.Decide(context.Background(), map[string]any{"kind": "encounter", "observationSequence": p.observe(o), "observationSequences": map[string]any{"inventory": fallbackGUID(seq)}})
				if e != nil {
					t.Fatal(e)
				}
				return d
			}
			d := step()
			a, _ := d.Output["action"].(map[string]any)
			if a["capability"] != "movement_owner" || a["mode"] != "acquire" {
				t.Fatal("no real consumable lease", d.Output)
			}
			owner := a["owner"].(string)
			id, e := h.ReserveAction(d)
			if e != nil {
				t.Fatal(e)
			}
			_, e = h.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": owner, "result": map[string]any{"token": "drink-token"}}}})
			if e != nil {
				t.Fatal(e)
			}
			o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: owner, RemainingMs: 2000}
			// A close hazard legitimately blocks consuming and records a tokenless prior
			// proposal. It must not borrow or release the consumable's different lease.
			n.X = 3
			d = step()
			if a, _ := d.Output["action"].(map[string]any); a["capability"] == "consume" {
				t.Fatal("near hazard allowed drink", d.Output)
			}
			n.X = 122
			self.PositionEstimated = kind != "observed"
			switch kind {
			case "near":
				n.X = 3
			case "fresh_attack":
				n.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3}
			case "combat":
				self.InCombat = true
			case "motion_unknown":
				o.Navigation.MotionUnconfirmed = true
			case "moving":
				self.Moving = true
			case "changed_visit":
				o.VisitId = 2
			case "expired_lease", "acquire_unknown":
				o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
			case "foreign_owner":
				o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "different-task", RemainingMs: 2000}
			}
			d = step()
			a, _ = d.Output["action"].(map[string]any)
			if kind == "expired_lease" || kind == "acquire_unknown" {
				if a["capability"] != "movement_owner" || a["mode"] != "acquire" || a["owner"] != owner {
					t.Fatal("expired own drink lease could not reacquire", d.Output)
				}
				id, e = h.ReserveAction(d)
				if e != nil {
					t.Fatal(e)
				}
				outcome := behavior.ActionConfirmed
				if kind == "acquire_unknown" {
					outcome = behavior.ActionUnknown
				}
				_, e = h.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: outcome, Payload: map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": owner, "result": map[string]any{"token": "renewed-drink-token"}}}})
				if e != nil {
					t.Fatal(e)
				}
				o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: owner, RemainingMs: 2000}
				d = step()
				a, _ = d.Output["action"].(map[string]any)
				if kind == "acquire_unknown" {
					if d.Output["action"] != nil || h.Status().Pending != 1 {
						t.Fatal("unknown reacquire replayed", d.Output, h.Status())
					}
					return
				}
			}
			if kind != "observed" && kind != "native_estimated" && kind != "priest_estimated" && kind != "expired_lease" {
				if a["capability"] == "consume" {
					t.Fatal("danger or uncertain state admitted drink", d.Output)
				}
				return
			}
			if a["capability"] != "consume" || a["spellId"] != float64(431) || a["expectedInventoryRevision"] != "10" {
				t.Fatal("passive stale prior starved exact own drink", d.Output)
			}
			if a["sessionId"] != fallbackSession || a["selfGuid"] != key {
				t.Fatal("wrong drink body", d.Output)
			}
			// Real action custody remains authoritative after admission, including reload.
			id, e = h.ReserveAction(d)
			if e != nil {
				t.Fatal(e)
			}
			_, e = h.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionUnknown, Payload: map[string]any{"capability": "consume", "requestId": a["requestId"]}}})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = h.Reload(context.Background(), "local encounter_consumables=(function()\n"+string(raw)+"\nend)()\n"+withStandingRange(readEncounterBundle(t)), h.Status().Revision); e != nil {
				t.Fatal(e)
			}
			d = step()
			if d.Output["action"] != nil || h.Status().Pending != 1 {
				t.Fatal("unknown drink replayed", d.Output, h.Status())
			}

		})
	}
}
