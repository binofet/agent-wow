package world

import (
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/proto"
)

func TestEncounterStandingHealingDoesNotRequireAnAuthoredPlan(t *testing.T) {
	for _, fighting := range []bool{false, true} {
		name := "idle"
		if fighting {
			name = "under_attack"
		}
		t.Run(name, func(t *testing.T) {
			policy := newFallbackPolicyTest(t, "")
			observation := standingHealObservation()
			if fighting {
				addFallbackTarget(observation, fallbackNPC, fallbackSelf, 100)
			}
			sequence := policy.observe(observation)
			decision := policy.decide(sequence)
			action, _ := decision.Output["action"].(map[string]any)
			if action["capability"] != "heal" || action["targetGuid"] != fallbackGUID(fallbackLeader) ||
				action["spellId"] != float64(2050) || action["observationSequence"] != sequence {
				t.Fatalf("standing healing did not take priority without a plan: %+v", decision.Output)
			}
		})
	}
}

func TestEncounterStandingHealingStillRequiresAnOnlineGroupPatient(t *testing.T) {
	policy := newFallbackPolicyTest(t, "")
	observation := standingHealObservation()
	observation.Party.Group = fallbackRoster(false)
	policy.expectNoAction(policy.observe(observation))
}

func standingHealObservation() *actionsv1.EncounterObservationResponse {
	observation := fallbackObservation()
	observation.Party = &actionsv1.EncounterParty{Group: fallbackRoster(true)}
	patient := fallbackPlayer(fallbackLeader)
	patient.HealthKnown, patient.Health, patient.MaxHealth = true, 20, 100
	observation.Players[fallbackGUID(fallbackLeader)] = patient
	observation.Spells["2050"] = &actionsv1.EncounterSpell{Known: true, Ready: true, Capability: "heal"}
	observation.Healing = &actionsv1.HealingOptionsResponse{
		SessionId: fallbackSession, HostOwned: true,
		Caster:   &actionsv1.HealingCaster{Guid: fallbackSelf, Ready: true, Mana: proto.Uint32(30)},
		Patients: []*actionsv1.HealingPatient{{Guid: fallbackLeader, Health: 20, MaxHealth: 100, Distance: proto.Float64(5)}},
		Spells: []*actionsv1.HealingRank{{SpellId: 2050, Rank: proto.Uint32(1), Level: proto.Uint32(1),
			Cost: proto.Uint32(10), Amount: 40, CastMs: proto.Uint32(1500), MinRange: proto.Float64(0), MaxRange: proto.Float64(40)}},
	}
	return observation
}
