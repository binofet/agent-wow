package world

import (
	"context"
	"os"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
)

func TestEncounterUsesWaterOnlyOnceWhileObservedDrinkIsEffective(t *testing.T) {
	module, err := os.ReadFile("../../behaviors/consumables.lua")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"stocked_drink", "last_bottle", "aura_arrives_during_acquire"} {
		t.Run(mode, func(t *testing.T) {
			p := newFallbackPolicyTest(t, "local encounter_consumables=(function()\n"+string(module)+"\nend)()\n")
			obs := standingHealObservation()
			obs.SelfGuid, obs.Healing.Caster.Guid, obs.Party.Group.Members[0].Guid = 1, 1, 1
			self := obs.Players["4"]
			obs.Players["1"], self.Guid = self, 1
			delete(obs.Players, "4")
			self.PowerKnown, self.Power, self.MaxPower = true, 648, 3220
			owner := obs.Players["3"]
			owner.MovementKnown, owner.Health, owner.MaxHealth = true, 3601, 3601
			patient := obs.Healing.Patients[0]
			patient.Health, patient.MaxHealth = owner.Health, owner.MaxHealth
			obs.Healing.Caster.Mana = proto.Uint32(self.Power)
			obs.Navigation = &actionsv1.NavigationObservation{OwnerGuid: 3, Following: true, MovementOwner: &actionsv1.MovementOwnerState{}}
			view, _ := consumablePolicyFixture()
			view["sessionId"], view["selfGuid"] = fallbackSession, "1"
			items := view["items"].([]any)
			water := items[0].(map[string]any)
			item := water["item"].(map[string]any)
			item["entry"], item["stackCount"] = 3772, 15
			spell := water["consumable"].(map[string]any)["spells"].([]any)[0].(map[string]any)
			spell["spellId"] = 1133
			view["items"] = items[:1]
			if mode == "last_bottle" {
				view["items"] = items[1:] // Only a different drink remains in the bag.
				view["lastConsumableUse"] = map[string]any{"requestId": "previous-water", "kind": "drink", "spellId": 1133, "inventoryConfirmed": true, "auraObserved": true}
			}
			set := view["consumableContext"].(map[string]any)["selfAuras"].(map[string]any)
			set["targetGuid"], set["revision"] = "1", "20"
			row := map[string]any{"spellId": 1133, "casterGuid": "1", "positive": true, "durationKnown": true, "remainingMs": 12000, "revision": "20"}
			step := func() behavior.Decision {
				t.Helper()
				seq, err := p.host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: view})
				if err != nil {
					t.Fatal(err)
				}
				d, err := p.host.Decide(context.Background(), map[string]any{"kind": "encounter", "observationSequence": p.observe(obs), "observationSequences": map[string]any{"inventory": fallbackGUID(seq)}})
				if err != nil {
					t.Fatal(err)
				}
				return d
			}
			action := func(d behavior.Decision) map[string]any {
				a, _ := d.Output["action"].(map[string]any)
				return a
			}
			if mode == "aura_arrives_during_acquire" {
				d := step()
				a := action(d)
				if a["mode"] != "acquire" {
					t.Fatal("first quiet window did not begin rest", d.Output)
				}
				leaseOwner := a["owner"].(string)
				settleWaitOwnership(t, p, d, behavior.ActionConfirmed, "acquire", leaseOwner, "water-token")
				obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: leaseOwner, RemainingMs: 1900}
				set["auras"] = []any{row}
				d = step()
				a = action(d)
				if a["mode"] != "release" || a["owner"] != leaseOwner || a["token"] != "water-token" {
					t.Fatalf("newly observed drink should release only its unused lease, got %v", a)
				}
				settleWaitOwnership(t, p, d, behavior.ActionConfirmed, "release", leaseOwner, "")
				obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
			}
			set["auras"] = []any{row}
			for _, remaining := range []int{10000, 3000} {
				row["remainingMs"] = remaining
				d := step()
				status := d.Output["consumable"].(map[string]any)
				if action(d) != nil || status["hold"] == true || status["phase"] != "drink_aura_active" {
					t.Fatalf("current drink scheduled another action or held movement: %v", d.Output)
				}
			}
			// The same effective aura must let an affordable, useful between-pull heal through.
			patient.Health, owner.Health = 1868, 1868
			if d := step(); action(d)["capability"] != "heal" || action(d)["targetGuid"] != "3" {
				t.Fatal("active water displaced the injured tank's normal heal", d.Output)
			}
			patient.Health, owner.Health = 3601, 3601
			row["remainingMs"] = 0
			if mode == "last_bottle" {
				set["auras"] = []any{} // The successful historical receipt remains.
			}
			owner.Moving = true
			if d := step(); action(d)["capability"] == "consume" || action(d)["mode"] == "acquire" {
				t.Fatal("water displaced the advancing owner", d.Output)
			}
			owner.Moving = false
			d := step()
			if action(d)["mode"] != "acquire" {
				t.Fatal("expired drink or its old receipt starved the next quiet window", d.Output)
			}
			leaseOwner := action(d)["owner"].(string)
			settleWaitOwnership(t, p, d, behavior.ActionConfirmed, "acquire", leaseOwner, "new-water-token")
			obs.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: leaseOwner, RemainingMs: 1900}
			if d := step(); action(d)["capability"] != "consume" || action(d)["kind"] != "drink" {
				t.Fatal("quiet body did not use its next water", d.Output)
			}
		})
	}
}

func TestDrinkIdentityComesFromCurrentPositiveSelfAura(t *testing.T) {
	view, obs := consumablePolicyFixture()
	set := view["consumableContext"].(map[string]any)["selfAuras"].(map[string]any)
	host, err := behavior.NewSession(context.Background(), "consumable-body", consumablePolicySource(t, true))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	for _, tc := range []struct {
		name, target, caster string
		spell                int
		positive, active     bool
	}{
		{"self without duration field", "6", "6", 430, true, true},
		{"another body", "7", "6", 430, true, false},
		{"another caster", "6", "7", 430, true, false},
		{"harmful aura", "6", "6", 430, false, false},
		{"another buff", "6", "6", 999, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set["targetGuid"] = tc.target
			set["auras"] = []any{map[string]any{"spellId": tc.spell, "casterGuid": tc.caster, "positive": tc.positive}}
			out, err := host.Decide(context.Background(), map[string]any{"inventory": view, "encounter": obs, "requested": "drink"})
			if err != nil || (out.Output["choice"] == nil) != tc.active {
				t.Fatal("wrong eligibility for exact drink request", out.Output, err)
			}
		})
	}
}
