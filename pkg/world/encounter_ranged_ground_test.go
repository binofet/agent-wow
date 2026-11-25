package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"math"
	"testing"
)

func legacyGround(t *testing.T) (*fallbackPolicyTest, *actionsv1.EncounterObservationResponse) {
	t.Helper()
	p := newFallbackPolicyTest(t, "")
	o := rangedObservation()
	o.Navigation.BodyMovementSupported = true
	queueRanged(t, p, o, 765432)
	return p, o
}
func groundAction(t *testing.T, p *fallbackPolicyTest, o *actionsv1.EncounterObservationResponse) (behavior.Decision, map[string]any) {
	t.Helper()
	d := p.decide(p.observe(o))
	a, _ := d.Output["action"].(map[string]any)
	return d, a
}
func groundReceipt(t *testing.T, p *fallbackPolicyTest, d behavior.Decision, a map[string]any, outcome behavior.ActionOutcome, phase string, written bool) {
	t.Helper()
	id, e := p.host.ReserveAction(d)
	if e != nil {
		t.Fatal(e)
	}
	_, e = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: outcome, Payload: map[string]any{
		"capability": a["capability"], "self_guid": fallbackGUID(fallbackSelf), "visit_id": "1", "sequence_id": a["sequenceId"], "phase": phase, "transport_written": written, "detail": "fixture exact rejection",
	}}})
	if e != nil {
		t.Fatal(e)
	}
}
func TestRangedLegacyGroundAffordableRetreatAndTargetedCast(t *testing.T) {
	p, o := legacyGround(t)
	d, a := groundAction(t, p, o)
	if a["capability"] != "move_body" || a["jump"] != false {
		t.Fatal(d.Output)
	}
	heading := a["bearingRadians"].(float64)
	if math.Cos(heading) > -.99 {
		t.Fatal("full-mana caster did not retreat", a)
	}
	groundReceipt(t, p, d, a, behavior.ActionSettledUnverified, "body_input_written", true)
	// Own motion is client-written, not server echoed. Correlate the exact
	// sequence and completed native STOP before using that estimated position.
	self := o.Players[fallbackGUID(fallbackSelf)]
	self.X = -1.5
	self.PositionEstimated = true
	seq := a["sequenceId"].(string)
	var n uint64
	for _, c := range seq {
		n = n*10 + uint64(c-'0')
	}
	o.Navigation.BodyMovement = &actionsv1.BodyMovementState{SequenceId: n, VisitId: 1, Phase: "body_input_written", TransportWritten: true}
	d, a = groundAction(t, p, o)
	if a["capability"] != "move_body" {
		t.Fatal("correlated client pose could not continue retreat", d.Output)
	}
	// A fresh independently observed safe firing pose needs no approach-to-center
	// or new band ABI and uses the actual actor's spell-ranking policy.
	p, o = legacyGround(t)
	o.Targets[fallbackGUID(fallbackNPC)].X = 20
	d, a = groundAction(t, p, o)
	targets, _ := a["targets"].(map[string]any)
	if a["capability"] != "cast_spell" || a["spellId"] != float64(133) || targets["mask"] != float64(2) || targets["objectGuid"] != fallbackGUID(fallbackNPC) {
		t.Fatal(d.Output)
	}
}
func TestRangedLegacyGroundFarApproachAndFacing(t *testing.T) {
	for _, mode := range []string{"far", "facing"} {
		t.Run(mode, func(t *testing.T) {
			p, o := legacyGround(t)
			o.Targets[fallbackGUID(fallbackNPC)].X = 40
			if mode == "facing" {
				o.Targets[fallbackGUID(fallbackNPC)].X = 20
				o.Players[fallbackGUID(fallbackSelf)].Orientation = math.Pi
			}
			d, a := groundAction(t, p, o)
			if a["capability"] != "move_body" {
				t.Fatal(d.Output)
			}
			heading := float64(o.Players[fallbackGUID(fallbackSelf)].Orientation) + a["bearingRadians"].(float64)
			if math.Cos(heading) < .98 {
				t.Fatal("not directed towards useful firing pose", a)
			}
			if mode == "facing" && a["durationMs"] != float64(1) {
				t.Fatal("facing used an unsafe large step", a)
			}
		})
	}
}
func TestRangedLegacyGroundAllHazardsAndNoMana(t *testing.T) {
	for _, mode := range []string{"two_hazards", "oom", "rogue", "unrelated", "unknown", "estimated", "no_abi"} {
		t.Run(mode, func(t *testing.T) {
			p, o := legacyGround(t)
			switch mode {
			case "two_hazards":
				addFallbackTarget(o, fallbackNPC+1, 0, 100)
				h := o.Targets[fallbackGUID(fallbackNPC+1)]
				h.Entry = 765432
				h.X = -18
			case "oom":
				o.Players[fallbackGUID(fallbackSelf)].Power = 0
			case "rogue":
				o.Spells = map[string]*actionsv1.EncounterSpell{"1752": {Known: true, Ready: true, Available: true, Capability: "offense", MetadataKnown: true, WeaponStrike: true, MaxRange: 5}}
			case "unrelated":
				o.Targets[fallbackGUID(fallbackNPC)].Entry = 42
			case "unknown":
				o.Targets[fallbackGUID(fallbackNPC)].PositionKnown = false
			case "estimated":
				o.Players[fallbackGUID(fallbackSelf)].PositionEstimated = true
			case "no_abi":
				o.Navigation.BodyMovementSupported = false
			}
			d, a := groundAction(t, p, o)
			if mode == "unrelated" {
				return
			} // Normal policy remains outside the scope.
			if mode == "unknown" || mode == "estimated" || mode == "no_abi" {
				if a != nil {
					t.Fatal("uncertain facts entered movement", d.Output)
				}
				return
			}
			if a["capability"] != "move_body" {
				t.Fatal(d.Output)
			}
			// Check the ENTIRE worst-case 14yd/s ray, not merely an endpoint at an
			// assumed 7yd/s speed. The second hazard constrains the westward retreat.
			angle := a["bearingRadians"].(float64)
			length := 14 * a["durationMs"].(float64) / 1000
			for _, h := range o.Targets {
				dx, dy := float64(h.X), float64(h.Y)
				initial := dx*dx + dy*dy
				projection := math.Max(0, math.Min(length, dx*math.Cos(angle)+dy*math.Sin(angle)))
				closest := math.Pow(dx-projection*math.Cos(angle), 2) + math.Pow(dy-projection*math.Sin(angle), 2)
				if initial >= 17*17 && closest < 17*17 {
					t.Fatal("crossed another hazard", a, h)
				}
				if initial < 17*17 && dx*math.Cos(angle)+dy*math.Sin(angle) > 0 {
					t.Fatal("closed on unsafe hazard", a)
				}
			}
		})
	}
}
func TestRangedLegacyGroundRefusalUnknownReloadAndVisit(t *testing.T) {
	for _, mode := range []string{"refused", "unknown", "pending", "written_no_progress", "visit"} {
		t.Run(mode, func(t *testing.T) {
			p, o := legacyGround(t)
			d, a := groundAction(t, p, o)
			if mode == "pending" {
				if _, e := p.host.ReserveAction(d); e != nil {
					t.Fatal(e)
				}
			} else if mode == "visit" {
				o.VisitId = 2
			} else {
				outcome, phase, written := behavior.ActionNotDispatched, "local_physics_refused", false
				if mode == "unknown" {
					outcome = behavior.ActionUnknown
				}
				if mode == "written_no_progress" {
					outcome = behavior.ActionSettledUnverified
					phase = "body_input_written"
					written = true
				}
				groundReceipt(t, p, d, a, outcome, phase, written)
				if mode == "written_no_progress" {
					seq := a["sequenceId"].(string)
					var n uint64
					for _, c := range seq {
						n = n*10 + uint64(c-'0')
					}
					o.Navigation.BodyMovement = &actionsv1.BodyMovementState{SequenceId: n, VisitId: 1, Phase: phase, TransportWritten: true}
				}
			}
			if _, e := p.host.Reload(context.Background(), readEncounterBundle(t)+"\n-- ground state retained", p.host.Status().Revision); e != nil {
				t.Fatal(e)
			}
			next, b := groundAction(t, p, o)
			if mode == "refused" || mode == "written_no_progress" {
				// A distinct safe ray may be attempted once; the refused ray must not repeat.
				if b != nil && b["capability"] == "move_body" && b["bearingRadians"] == a["bearingRadians"] {
					t.Fatal("retried unchanged refused ray", next.Output)
				}
			} else if b != nil && b["capability"] != "movement_owner" {
				t.Fatal("unsettled/old visit input replayed", next.Output)
			}
		})
	}
}

func TestRangedLegacyGroundActualGenericCastReceiptShape(t *testing.T) {
	for _, outcome := range []behavior.ActionOutcome{behavior.ActionConfirmed, behavior.ActionNotDispatched, behavior.ActionUnknown} {
		t.Run(string(outcome), func(t *testing.T) {
			p, o := legacyGround(t)
			o.Targets[fallbackGUID(fallbackNPC)].X = 20
			d, a := groundAction(t, p, o)
			if a["capability"] != "cast_spell" {
				t.Fatal(d.Output)
			}
			id, e := p.host.ReserveAction(d)
			if e != nil {
				t.Fatal(e)
			}
			// Exact maintained generic cast receipt: NO fabricated self_guid/visit_id.
			_, e = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: outcome, Payload: map[string]any{
				"capability": "cast_spell", "spell_id": uint32(133), "target_guid": fallbackGUID(fallbackNPC), "phase": "fixture_exact_cast_outcome", "transport_written": outcome != behavior.ActionNotDispatched, "server_cast_go": outcome == behavior.ActionConfirmed, "effect_observed": false,
			}}})
			if e != nil {
				t.Fatal(e)
			}
			next, b := groundAction(t, p, o)
			if outcome == behavior.ActionConfirmed {
				if b["capability"] != "cast_spell" {
					t.Fatal("settled cast permanently blocked next ordinary cast", next.Output)
				}
			} else if b != nil {
				t.Fatal("refused/unknown cast repeated", next.Output)
			}
		})
	}
}
func TestRangedLegacyGroundUnengagedTargetCannotCreatePull(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	o := rangedObservation()
	o.Navigation.BodyMovementSupported = true
	o.Targets[fallbackGUID(fallbackNPC)].X = 20
	o.Targets[fallbackGUID(fallbackNPC)].Engaged = false
	o.Engagement = map[string]*actionsv1.EncounterEngagement{}
	queueRanged(t, p, o, 765432)
	d, a := groundAction(t, p, o)
	if a != nil {
		t.Fatal("stance manufactured a new offensive target", d.Output)
	}
}
