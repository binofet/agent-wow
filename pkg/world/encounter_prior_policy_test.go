package world

import (
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
	"testing"
)

func sludgePriorObservation() *actionsv1.EncounterObservationResponse {
	o := rangedObservation()
	o.MapId = proto.Uint32(349)
	o.Party = &actionsv1.EncounterParty{Group: fallbackRoster(true)}
	o.Players["3"] = fallbackPlayer(3)
	for _, p := range o.Players {
		p.MapId = proto.Uint32(349)
		p.PositionKnown = true
	}
	n := o.Targets[fallbackGUID(fallbackNPC)]
	n.Entry = 12222
	n.MapId = proto.Uint32(349)
	n.X = 22
	n.AttackObservation = &actionsv1.EncounterAttackObservation{}
	return o
}
func settlePriorLease(t *testing.T, p *fallbackPolicyTest, o *actionsv1.EncounterObservationResponse, modern bool) {
	t.Helper()
	d := p.decide(observeRanged(t, p, o, modern))
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] != "movement_owner" {
		t.Fatal("prior must establish real movement custody", d.Output)
	}
	id, err := p.host.ReserveAction(d)
	if err != nil {
		t.Fatal(err)
	}
	owner := "ranged:maraudon_sludge_backline"
	o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: owner}
	_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": owner, "result": map[string]any{"token": "prior-fixture", "state": map[string]any{"sessionId": fallbackSession, "selfGuid": "4", "movementOwner": map[string]any{"active": true, "owner": owner}}}}}})
	if err != nil {
		t.Fatal(err)
	}
}
func TestEncounterPriorAssistCannotOpenSludgePull(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "old_native", true: "band_native"}[modern], func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			o := sludgePriorObservation()
			_, err := p.host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"task": "engage", "targetGuid": fallbackGUID(fallbackNPC)}})
			if err != nil {
				t.Fatal(err)
			}
			settlePriorLease(t, p, o, modern)
			out := p.decide(observeRanged(t, p, o, modern)).Output
			if out["action"] != nil || out["followFormation"] != nil {
				t.Fatal("assist opened before tank", out)
			}
			prior, _ := out["encounterPrior"].(map[string]any)
			if prior["reason"] != "waiting_for_tank_attack_witness" {
				t.Fatal(out)
			}
			// Selection/combat hints and a legacy engagement cannot stand in for a hit.
			o.Targets[fallbackGUID(fallbackNPC)].AttackObservation.TargetKnown = true
			o.Targets[fallbackGUID(fallbackNPC)].AttackObservation.TargetGuid = 3
			out = p.decide(observeRanged(t, p, o, modern)).Output
			if out["action"] != nil {
				t.Fatal("selection manufactured tank engagement", out)
			}
		})
	}
}
func TestEncounterPriorWitnessIsPerCreatureAndCurrent(t *testing.T) {
	for _, mode := range []string{"tank", "self_defense", "stale", "contradicted", "other_guid"} {
		t.Run(mode, func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			o := sludgePriorObservation()
			n := o.Targets[fallbackGUID(fallbackNPC)]
			n.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3, AgeMs: 1}
			expected := true
			switch mode {
			case "self_defense":
				n.AttackObservation.VictimGuid = 4
			case "stale":
				n.AttackObservation.AgeMs = 5001
				expected = false
			case "contradicted":
				n.AttackObservation.TargetKnown = true
				n.AttackObservation.TargetGuid = 99
				expected = false
			case "other_guid":
				twin := proto.Clone(n).(*actionsv1.EncounterCreature)
				twin.Guid = fallbackNPC + 1
				o.Targets[fallbackGUID(twin.Guid)] = twin
				n.AttackObservation = &actionsv1.EncounterAttackObservation{}
				expected = true
			}
			_, _ = p.host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"task": "engage", "targetGuid": fallbackGUID(fallbackNPC)}})
			settlePriorLease(t, p, o, true)
			out := p.decide(observeRanged(t, p, o, true)).Output
			prior, _ := out["encounterPrior"].(map[string]any)
			if prior["allowed"] != expected {
				t.Fatal(mode, out)
			}
			if mode == "other_guid" && out["focusGuid"] == fallbackGUID(fallbackNPC) {
				t.Fatal("twin witness admitted original target", out)
			}
		})
	}
}

func TestEncounterPriorWithdrawalAndDeliberatePull(t *testing.T) {
	for _, mode := range []string{"withdraw", "no_mana", "deliberate_pull"} {
		t.Run(mode, func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			o := sludgePriorObservation()
			if mode == "withdraw" || mode == "no_mana" {
				o.Targets[fallbackGUID(fallbackNPC)].X = 3
			}
			if mode == "no_mana" {
				o.Players["4"].Power = 0
			}
			_, _ = p.host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"task": "engage", "targetGuid": fallbackGUID(fallbackNPC), "pull": mode == "deliberate_pull"}})
			settlePriorLease(t, p, o, true)
			out := p.decide(observeRanged(t, p, o, true)).Output
			a, _ := out["action"].(map[string]any)
			prior, _ := out["encounterPrior"].(map[string]any)
			if mode == "deliberate_pull" {
				if prior["reason"] != "explicit_pull_override" {
					t.Fatal(out)
				}
			} else if a["capability"] != "approach_target" || a["minRangeYards"] != float64(16) {
				t.Fatal("holding fire suppressed withdrawal", out)
			}
		})
	}
}

func TestEncounterPriorUnknownMovementCustodyCannotOpen(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "entered", true: "unknown"}[unknown], func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			o := sludgePriorObservation()
			d := p.decide(observeRanged(t, p, o, false))
			id, err := p.host.ReserveAction(d)
			if err != nil {
				t.Fatal(err)
			}
			if unknown {
				_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionUnknown, Payload: map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": "ranged:maraudon_sludge_backline"}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			o.Targets[fallbackGUID(fallbackNPC)].AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3, AgeMs: 1}
			for i := 0; i < 3; i++ {
				out := p.decide(observeRanged(t, p, o, false)).Output
				if out["action"] != nil {
					t.Fatal("prior replayed unsettled native action", out)
				}
			}
		})
	}
}

func TestEncounterPriorLegacyWithdrawalWithoutOpening(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	o := sludgePriorObservation()
	o.Navigation.BodyMovementSupported = true
	o.Targets[fallbackGUID(fallbackNPC)].X = 3
	_, _ = p.host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"task": "engage", "targetGuid": fallbackGUID(fallbackNPC)}})
	settlePriorLease(t, p, o, false)
	out := p.decide(observeRanged(t, p, o, false)).Output
	a, _ := out["action"].(map[string]any)
	status, _ := out["rangedStance"].(map[string]any)
	if a["capability"] != "move_body" || status["phase"] != "withdrawing_ground_input" {
		t.Fatal("old ABI must retreat without opening", out)
	}
}

func TestEncounterPriorKeepsUrgentStationaryFriendlyHealing(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	o := positionedHealObservation()
	o.MapId = proto.Uint32(349)
	for _, player := range o.Players {
		player.MapId = proto.Uint32(349)
	}
	o.Navigation = &actionsv1.NavigationObservation{MovementOwner: &actionsv1.MovementOwnerState{}}
	addFallbackTarget(o, fallbackNPC, 0, 900)
	n := o.Targets[fallbackGUID(fallbackNPC)]
	n.Entry = 12222
	n.MapId = proto.Uint32(349)
	n.X = 13
	n.Y = 10
	n.Z = 10
	n.AttackObservation = &actionsv1.EncounterAttackObservation{}
	settlePriorLease(t, p, o, false)
	out := p.decide(observeRanged(t, p, o, false)).Output
	a, _ := out["action"].(map[string]any)
	if a["capability"] != "heal" || a["targetGuid"] != "8" {
		t.Fatal("tank wait starved legitimate urgent support", out)
	}
}
