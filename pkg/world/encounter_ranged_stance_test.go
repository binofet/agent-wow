package world

import (
	"context"
	"encoding/json"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/encoding/protojson"
	"testing"
)

func rangedDirective(entry uint32) map[string]any {
	return map[string]any{"task": "ranged_only", "id": "range-1", "creatureEntry": float64(entry), "mapId": float64(0), "visitId": "1", "minRangeYards": float64(15), "maxRangeYards": float64(25), "clearanceYards": float64(2), "durationMs": float64(60000), "enabled": true}
}
func queueRanged(t *testing.T, p *fallbackPolicyTest, o *actionsv1.EncounterObservationResponse, entry uint32) {
	t.Helper()
	if _, err := p.host.Enqueue(behavior.Event{Kind: "command", Value: rangedDirective(entry)}); err != nil {
		t.Fatal(err)
	}
	// Establish existing finite movement custody through an actual accepted
	// action/receipt before testing the new band mechanic. Legacy acquisition
	// and UNKNOWN behavior have a separate old-descriptor closed-loop cohort.
	if entry != 765432 {
		return
	}
	d := p.decide(observeRanged(t, p, o, true))
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] != "movement_owner" || a["mode"] != "acquire" {
		t.Fatal("fixture lease not selected", d.Output)
	}
	id, err := p.host.ReserveAction(d)
	if err != nil {
		t.Fatal(err)
	}
	o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "ranged:range-1"}
	_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": "ranged:range-1", "result": map[string]any{"token": "fixture-range-token", "state": map[string]any{"sessionId": fallbackSession, "selfGuid": fallbackGUID(fallbackSelf), "movementOwner": map[string]any{"active": true, "owner": "ranged:range-1"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
}
func rangedObservation() *actionsv1.EncounterObservationResponse {
	o := fallbackObservation()
	self := o.Players[fallbackGUID(fallbackSelf)]
	self.PositionKnown = true
	self.MovementKnown = true
	o.Navigation = &actionsv1.NavigationObservation{MovementOwner: &actionsv1.MovementOwnerState{}}
	self.ClassKnown = true
	self.ClassId = 8
	self.PowerKnown = true
	self.PowerType = 0
	self.Power = 100
	self.MaxPower = 100
	o.MeleeKnown = true
	o.Spells = map[string]*actionsv1.EncounterSpell{"133": {Known: true, Ready: true, Available: true, Capability: "offense", MetadataKnown: true, FamilyId: 133, Cost: 20, PowerType: 0, DirectDamage: 100, MaxRange: 30, CastTimeMs: 2000}}
	addFallbackTarget(o, fallbackNPC, fallbackSelf, 900)
	target := o.Targets[fallbackGUID(fallbackNPC)]
	target.Entry = 765432
	target.X = 3
	target.MeleeReady = true
	return o
}
func TestEncounterRangedOnlyAffordableCasterWithdrawsBeforeCasting(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	o := rangedObservation()
	queueRanged(t, p, o, 765432)
	out := p.decide(observeRanged(t, p, o, true)).Output
	a, _ := out["action"].(map[string]any)
	status, _ := out["rangedStance"].(map[string]any)
	if a["capability"] != "approach_target" || a["minRangeYards"] != float64(17) || a["rangeYards"] != float64(23) || status["applied"] != true || status["positionReady"] != false {
		t.Fatal("full-mana caster collapsed or cast too close", out)
	}
	o.Targets[fallbackGUID(fallbackNPC)].X = 20
	o.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: fallbackNPC, VisitId: 1, RangeYards: 23, MinRangeYards: 17, ClearanceYards: 17, ClearanceTargetGuids: []uint64{fallbackNPC}, RequireLos: true, Ready: true}
	out = p.decide(observeRanged(t, p, o, true)).Output
	a, _ = out["action"].(map[string]any)
	if a["capability"] != "offense" || a["spellId"] != float64(133) {
		t.Fatal("verified band suppressed actual affordable ranged cast", out)
	}
}
func TestEncounterRangedOnlyNoManaAndRogueNeverFallBackToSwing(t *testing.T) {
	for _, mode := range []string{"no_mana", "rogue"} {
		t.Run(mode, func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			o := rangedObservation()
			self := o.Players[fallbackGUID(fallbackSelf)]
			if mode == "no_mana" {
				self.Power = 0
			} else {
				self.ClassId = 4
				self.PowerType = 3
				o.Spells = map[string]*actionsv1.EncounterSpell{"1752": {Known: true, Ready: true, Available: true, Capability: "offense", MetadataKnown: true, FamilyId: 1752, Cost: 45, PowerType: 3, DirectDamage: 35, MaxRange: 5, WeaponStrike: true}}
			}
			queueRanged(t, p, o, 765432)
			o.ActiveMeleeTargetGuid = fallbackNPC
			out := p.decide(observeRanged(t, p, o, true)).Output
			a, _ := out["action"].(map[string]any)
			if a["capability"] != "melee_stop" {
				t.Fatal("ongoing swing escaped scoped order", out)
			}
			o.ActiveMeleeTargetGuid = 0
			o.Targets[fallbackGUID(fallbackNPC)].X = 20
			out = p.decide(observeRanged(t, p, o, true)).Output
			a, _ = out["action"].(map[string]any)
			if a["capability"] == "melee_start" || a["capability"] == "offense" || a["rangeYards"] == float64(3.5) {
				t.Fatal("unsupported ranged option became melee", out)
			}
		})
	}
}
func TestEncounterRangedOnlyScopeCancelAndCheckpointPreserveDefaults(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	o := rangedObservation()
	queueRanged(t, p, o, 765432)
	out := p.decide(observeRanged(t, p, o, true)).Output
	if _, err := p.host.Reload(context.Background(), readEncounterBundle(t), p.host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	out = p.decide(observeRanged(t, p, o, true)).Output
	if out["rangedStance"].(map[string]any)["active"] != true {
		t.Fatal("checkpoint lost stance", out)
	}
	if _, err := p.host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"task": "ranged_only", "id": "range-1", "cancel": true}}); err != nil {
		t.Fatal(err)
	}
	cancelDecision := p.decide(observeRanged(t, p, o, true))
	out = cancelDecision.Output
	if out["rangedStance"].(map[string]any)["active"] != false {
		t.Fatal("owner cancellation failed", out)
	}
	id, err := p.host.ReserveAction(cancelDecision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "movement_owner", "mode": "release", "owner": "ranged:range-1", "result": map[string]any{"sessionId": fallbackSession, "selfGuid": fallbackGUID(fallbackSelf), "movementOwner": map[string]any{}}}}})
	if err != nil {
		t.Fatal(err)
	}
	o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
	p.decide(observeRanged(t, p, o, true)) // consume confirmed release
	queueRanged(t, p, o, 765432)
	out = p.decide(observeRanged(t, p, o, true)).Output
	o.VisitId = 2
	out = p.decide(observeRanged(t, p, o, true)).Output
	if out["rangedStance"].(map[string]any)["active"] != false {
		t.Fatal("stance leaked across visit", out)
	}
}
func TestEncounterRangedOnlyTracksAllActualMatchingTargetsAndIgnoresUnrelated(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	o := rangedObservation()
	second := fallbackNPC + 1
	addFallbackTarget(o, second, fallbackSelf, 100)
	o.Targets[fallbackGUID(second)].Entry = 765432
	o.Targets[fallbackGUID(second)].X = 5
	queueRanged(t, p, o, 765432)
	out := p.decide(observeRanged(t, p, o, true)).Output
	a, _ := out["action"].(map[string]any)
	if ids, ok := a["clearanceTargetGuids"].([]any); !ok || len(ids) != 2 {
		t.Fatal("scope omitted matching visible peer", out)
	}
	p2 := newFallbackPolicyTest(t, "")
	o2 := rangedObservation()
	queueRanged(t, p2, o2, 999999)
	out = p2.decide(observeRanged(t, p2, o2, true)).Output
	a, _ = out["action"].(map[string]any)
	if a["minRangeYards"] != nil {
		t.Fatal("unrelated entry inherited stance", out)
	}
}

func TestEncounterRangedOnlyUnknownMotionAndEdgeDoNotPermitCloseFallback(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	o := rangedObservation()
	queueRanged(t, p, o, 765432)
	o.Targets[fallbackGUID(fallbackNPC)].X = 17
	o.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: fallbackNPC, VisitId: 1, RangeYards: 23, MinRangeYards: 17, ClearanceYards: 17, ClearanceTargetGuids: []uint64{fallbackNPC}, RequireLos: true, Ready: true}
	out := p.decide(observeRanged(t, p, o, true)).Output
	a, _ := out["action"].(map[string]any)
	if a["capability"] != "offense" {
		t.Fatal("valid inset edge prevented ranged cast", out)
	}
	o.Navigation.MotionUnconfirmed = true
	out = p.decide(observeRanged(t, p, o, true)).Output
	if out["action"] != nil || out["rangedStance"].(map[string]any)["phase"] != "waiting_for_native_custody" {
		t.Fatal("unknown motion became permission to cast", out)
	}
}

func observeRanged(t *testing.T, p *fallbackPolicyTest, o *actionsv1.EncounterObservationResponse, supported bool) string {
	t.Helper()
	raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if supported {
		value["hostTargetRangeBandSupported"] = true
	} else if movement, ok := value["targetMovement"].(map[string]any); ok {
		// Exact absence of the three additive fields in the older wire shape.
		delete(movement, "minRangeYards")
		delete(movement, "clearanceTargetGuids")
		delete(movement, "clearanceYards")
	}
	seq, err := p.host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: value})
	if err != nil {
		t.Fatal(err)
	}
	return fallbackGUID(seq)
}
