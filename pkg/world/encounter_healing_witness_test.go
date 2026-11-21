package world

import (
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestEncounterHealingUrgencyUsesTheSharedRawAttackWitness(t *testing.T) {
	for _, age := range []uint64{0, 6000} {
		p, obs := newFallbackPolicyTest(t, ""), positionedHealObservation()
		patient := obs.Players["8"]
		patient.Health, patient.MaxHealth = 80, 100
		patient.CombatKnown, patient.InCombat = true, false
		owner := fallbackPlayer(fallbackLeader)
		owner.HealthKnown, owner.Health, owner.MaxHealth = true, 70, 100
		obs.Players[fallbackGUID(fallbackLeader)] = owner
		obs.Healing.Patients[0].Health = 80
		obs.Healing.Patients = append(obs.Healing.Patients, &actionsv1.HealingPatient{Guid: fallbackLeader, Health: 70, MaxHealth: 100, Distance: proto.Float64(5)})
		// A raw cached relation alone does not keep urgency alive indefinitely.
		obs.Healing.Patients[0].UnderAttack = true
		addFallbackTarget(obs, fallbackNPC, 0, 100)
		obs.Targets[fallbackGUID(fallbackNPC)].AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 8, AgeMs: age}
		want := "8"
		if age > 5000 {
			want = fallbackGUID(fallbackLeader)
		}
		action, _ := p.decide(observeSparseEncounter(t, p, obs)).Output["action"].(map[string]any)
		if action["capability"] != "heal" || action["targetGuid"] != want {
			t.Fatal("healing did not reuse Lua attack freshness", age, action)
		}
	}
}
