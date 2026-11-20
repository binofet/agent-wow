package world

import (
	"encoding/json"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func positionedHealObservation() *actionsv1.EncounterObservationResponse {
	obs := standingHealObservation()
	const patient = uint64(8)
	obs.Party.Group.Members = append(obs.Party.Group.Members, &partyv1.Member{Guid: patient, Online: true})
	self := obs.Players[fallbackGUID(fallbackSelf)]
	self.PositionKnown, self.MovementKnown = true, true
	self.X, self.Y, self.Z = 10, 10, 10
	ally := fallbackPlayer(patient)
	ally.PositionKnown, ally.HealthKnown, ally.Health, ally.MaxHealth = true, true, 20, 100
	ally.X, ally.Y, ally.Z = 15, 10, 10
	obs.Players[fallbackGUID(patient)] = ally
	obs.Spells = map[string]*actionsv1.EncounterSpell{"6063": {Known: true, Ready: true, Available: true,
		Capability: "heal", MetadataKnown: true, FamilyId: 2054, MaxRange: 40}}
	obs.Healing.Patients = []*actionsv1.HealingPatient{{Guid: patient, Health: 20, MaxHealth: 100, Distance: proto.Float64(5)}}
	obs.Healing.Spells = []*actionsv1.HealingRank{{SpellId: 6063, Rank: proto.Uint32(2), Level: proto.Uint32(26),
		Cost: proto.Uint32(10), Amount: 400, CastMs: proto.Uint32(3000), MinRange: proto.Float64(0), MaxRange: proto.Float64(40)}}
	// A previous enemy approach is not a prerequisite for healing this ally.
	obs.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: fallbackNPC, VisitId: 1, RangeYards: 30, RequireLos: true}
	return obs
}

func observeSparseEncounter(t *testing.T, p *fallbackPolicyTest, obs *actionsv1.EncounterObservationResponse) string {
	t.Helper()
	raw, err := protojson.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	seq, err := p.host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: value})
	if err != nil {
		t.Fatal(err)
	}
	return fallbackGUID(seq)
}

func TestEncounterHealingApproachesOnlyWhenSelectedRankNeedsIt(t *testing.T) {
	for _, mode := range []string{"in_range", "out_of_range", "self_range_zero"} {
		t.Run(mode, func(t *testing.T) {
			p, obs := newFallbackPolicyTest(t, ""), positionedHealObservation()
			want, patient := "heal", uint64(8)
			switch mode {
			case "out_of_range":
				want = "approach_target"
				obs.Healing.Patients[0].Distance = proto.Float64(45)
				obs.Players["8"].X = 55
			case "self_range_zero":
				patient = fallbackSelf
				obs.Players[fallbackGUID(patient)].HealthKnown = true
				obs.Players[fallbackGUID(patient)].Health, obs.Players[fallbackGUID(patient)].MaxHealth = 20, 100
				obs.Healing.Patients[0].Guid, obs.Healing.Patients[0].Distance = patient, proto.Float64(0)
				obs.Healing.Spells[0].SelfOnly, obs.Healing.Spells[0].MaxRange = true, proto.Float64(0)
				obs.Spells["6063"].MaxRange = 0
			}
			action, _ := p.decide(observeSparseEncounter(t, p, obs)).Output["action"].(map[string]any)
			if action["capability"] != want || action["targetGuid"] != fallbackGUID(patient) ||
				want == "heal" && action["spellId"] != float64(6063) ||
				want == "approach_target" && action["rangeYards"] != float64(40) {
				t.Fatal("selected healing rank was replaced by unnecessary movement", action)
			}
		})
	}
}

func TestEncounterFailedHealRepositionsThenResumesOnFreshReadyFeed(t *testing.T) {
	p, obs := newFallbackPolicyTest(t, ""), positionedHealObservation()
	decide := func(want string) behavior.Decision {
		t.Helper()
		out := p.decide(observeSparseEncounter(t, p, obs))
		action, _ := out.Output["action"].(map[string]any)
		if action["capability"] != want || action["targetGuid"] != "8" {
			t.Fatal("wrong healing retry action", want, out.Output)
		}
		return out
	}
	settle := func(decision behavior.Decision, outcome behavior.ActionOutcome, capability string) {
		t.Helper()
		id, err := p.host.ReserveAction(decision)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
			SessionID: fallbackSession, ActionID: id, Outcome: outcome,
			Payload: map[string]any{"capability": capability, "target_guid": "8", "effect_observed": false},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	settle(decide("heal"), behavior.ActionFailed, "heal")
	settle(decide("approach_target"), behavior.ActionSettledUnverified, "approach_target")
	// The ordinary busy feed may briefly contain no ranks. It must not erase
	// the failed cast's need to recheck the patient's position.
	ranks := obs.Healing.Spells
	obs.Healing.Caster.Ready, obs.Spells["6063"].Ready = false, false
	obs.Healing.Spells = nil
	p.expectNoAction(observeSparseEncounter(t, p, obs))
	obs.Healing.Caster.Ready, obs.Spells["6063"].Ready = true, true
	obs.Healing.Spells = ranks
	obs.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: 8, VisitId: 1, RangeYards: 40, RequireLos: true}
	decide("approach_target")
	// A transport receipt alone is insufficient. The actual native ready
	// observation plus current geometry permits the next exact heal.
	obs.TargetMovement.Ready, obs.TargetMovement.Phase = true, "in_position"
	decide("heal")
}
