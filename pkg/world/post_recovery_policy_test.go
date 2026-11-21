package world

import (
	"context"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
)

// Recuperation uses the existing complete policy, not a second resurrection
// resource selector or a native active-recovery gate after observed life.
func TestPostRecoverySharedLuaHealingDrinkAndDefenseKeepTheirCustody(t *testing.T) {
	p, obs := newFallbackPolicyTest(t, ""), positionedHealObservation()
	key := fallbackGUID(fallbackSelf)
	self := obs.Players[key]
	self.HealthKnown, self.Health, self.MaxHealth = true, 0, 100
	self.PowerKnown, self.PowerType, self.Power, self.MaxPower = true, 0, 60, 100
	self.ClassKnown, self.ClassId, self.LevelKnown, self.Level = true, 5, true, 30
	self.Alive = proto.Bool(false)
	obs.Healing.Caster.Ready = false
	patient := obs.Healing.Patients[0]
	patient.Guid, patient.Health, patient.Distance = fallbackSelf, 0, proto.Float64(0)
	obs.Navigation = &actionsv1.NavigationObservation{MovementOwner: &actionsv1.MovementOwnerState{}}
	view, _ := consumablePolicyFixture()
	view["sessionId"], view["selfGuid"] = fallbackSession, key
	observeInventory := func() {
		t.Helper()
		if _, err := p.host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: view}); err != nil {
			t.Fatal(err)
		}
	}
	step := func() behavior.Decision {
		t.Helper()
		observeInventory()
		return p.decide(observeSparseEncounter(t, p, obs))
	}
	receive := func(id uint64, outcome behavior.ActionOutcome, payload map[string]any) {
		t.Helper()
		if _, err := p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
			SessionID: fallbackSession, ActionID: id, Outcome: outcome, Payload: payload}}); err != nil {
			t.Fatal(err)
		}
	}
	if dead := step(); dead.Output["action"] != nil {
		t.Fatal("dead body authored a living resource action", dead.Output)
	}
	self.Alive, self.Health = proto.Bool(true), 20
	obs.Healing.Caster.Ready, patient.Health = true, 20
	heal := step()
	action, _ := heal.Output["action"].(map[string]any)
	if action["capability"] != "heal" || action["targetGuid"] != key {
		t.Fatal("actual life did not enter existing Lua self triage", heal.Output)
	}
	id, err := p.host.ReserveAction(heal)
	if err != nil {
		t.Fatal(err)
	}
	healPayload := map[string]any{"capability": "heal", "target_guid": key, "spell_id": action["spellId"]}
	receive(id, behavior.ActionUnknown, healPayload)
	if _, err = p.host.Reload(context.Background(), readEncounterBundle(t)+"\n-- same-body resource recovery", p.host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	self.Health, patient.Health = 100, 100
	if held := step(); held.Output["action"] != nil || p.host.Status().Pending != 1 {
		t.Fatal("actual life or reload erased unknown heal custody", held.Output, p.host.Status())
	}
	// Settle uncertainty without inventing our own healing effect. Fresh public
	// health is already full and the same Lua item policy now chooses the drink.
	receive(id, behavior.ActionSettledUnverified, healPayload)
	lease := step()
	request, _ := lease.Output["action"].(map[string]any)
	if request["capability"] != "movement_owner" || request["mode"] != "acquire" {
		t.Fatal("shared Lua mana policy did not begin its finite rest", lease.Output)
	}
	owner, _ := request["owner"].(string)
	leaseID, err := p.host.ReserveAction(lease)
	if err != nil {
		t.Fatal(err)
	}
	receive(leaseID, behavior.ActionConfirmed, map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": owner,
		"result": map[string]any{"token": "rest-after-life"}})
	obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: owner, RemainingMs: 2000}
	use := step()
	item, _ := use.Output["action"].(map[string]any)
	if item["capability"] != "consume" || item["spellId"] != float64(431) {
		t.Fatal("post-life resource recovery used a separate native chooser", use.Output)
	}
	useID, err := p.host.ReserveAction(use)
	if err != nil {
		t.Fatal(err)
	}
	receive(useID, behavior.ActionSettledUnverified, map[string]any{"capability": "consume", "requestId": item["requestId"], "reconciliationRequired": true})
	if _, err = p.host.Reload(context.Background(), readEncounterBundle(t)+"\n-- preserve uncertain item", p.host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	obs.Spells["585"] = &actionsv1.EncounterSpell{Known: true, Ready: true, Available: true, MetadataKnown: true,
		Capability: "offense", FamilyId: 585, Rank: 1, MaxRange: 30, DirectDamage: 30, PowerType: 0, Cost: 10}
	addFallbackTarget(obs, fallbackNPC, fallbackSelf, 100)
	self.CombatKnown, self.InCombat = true, true
	defense := step()
	attack, _ := defense.Output["action"].(map[string]any)
	if attack["capability"] != "offense" || attack["targetGuid"] != fallbackGUID(fallbackNPC) || p.host.Status().Pending != 0 {
		t.Fatal("resource recovery suppressed defense or replayed unknown drink", defense.Output, p.host.Status())
	}
}
