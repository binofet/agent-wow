package world

import (
	"math"
	"strconv"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
)

const dustOwner = "ranged:maraudon_princess_dust_field"

func princessEncounter(class uint32) *actionsv1.EncounterObservationResponse {
	o := sonicEncounter(class)
	for id, target := range o.Targets {
		if target.Guid != fallbackNPC {
			delete(o.Targets, id)
		}
	}
	boss := o.Targets[fallbackGUID(fallbackNPC)]
	boss.Entry, boss.X, boss.MeleeReady = 12201, 24, false
	o.Players["3"].X = 23
	if class == 5 {
		o.Healing.Patients[0].Distance = proto.Float64(23)
	}
	o.Navigation.BodyMovementSupported = true
	return o
}

func princessLease(t *testing.T, p *fallbackPolicyTest, o *actionsv1.EncounterObservationResponse, nativeBand bool) {
	t.Helper()
	d := p.decide(observeRanged(t, p, o, nativeBand))
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] != "movement_owner" || a["mode"] != "acquire" || a["owner"] != dustOwner {
		t.Fatal("missing owned Princess lease", d.Output)
	}
	id, err := p.host.ReserveAction(d)
	if err != nil {
		t.Fatal(err)
	}
	o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: dustOwner}
	_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
		SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionConfirmed,
		Payload: map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": dustOwner,
			"result": map[string]any{"token": "dust-fixture", "state": map[string]any{"sessionId": fallbackSession,
				"selfGuid": "4", "movementOwner": map[string]any{"active": true, "owner": dustOwner}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPrincessDustBodyOnlyWithdrawalAndRoleResumption(t *testing.T) {
	for _, class := range []uint32{5, 8, 9} {
		t.Run(strconv.Itoa(int(class)), func(t *testing.T) {
			p, o := newFallbackPolicyTest(t, ""), princessEncounter(class)
			self, boss := o.Players["4"], o.Targets[fallbackGUID(fallbackNPC)]
			self.PositionEstimated = true // stationary launch retained by native Actions
			princessLease(t, p, o, false)
			step := func() (behavior.Decision, map[string]any) {
				d := p.decide(observeRanged(t, p, o, false))
				a, _ := d.Output["action"].(map[string]any)
				return d, a
			}
			d, a := step()
			if a["capability"] != "move_body" || a["jump"] != false || math.Cos(a["bearingRadians"].(float64)) > -.98 {
				t.Fatal("failed to withdraw from the current boss on body-only host", d.Output)
			}
			seq, err := strconv.ParseUint(a["sequenceId"].(string), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			groundReceipt(t, p, d, a, behavior.ActionSettledUnverified, "body_input_written", true)
			self.X, self.Orientation = -1.5, math.Pi // a 214ms walk facing away from the boss
			if class == 5 {
				o.Healing.Patients[0].Distance = proto.Float64(24.5)
			}
			d, a = step()
			if a != nil {
				t.Fatal("used the new estimate before its exact native observation", d.Output)
			}
			o.Navigation.BodyMovement = &actionsv1.BodyMovementState{SequenceId: seq, VisitId: 1,
				Phase: "body_input_written", TransportWritten: true, PositionEstimated: true, X: self.X}
			d, a = step()
			if class != 5 {
				if a["capability"] != "approach_target" || a["targetGuid"] != fallbackGUID(fallbackNPC) ||
					a["minRangeYards"] != nil || a["clearanceYards"] != nil || a["requireLos"] != true {
					t.Fatal("withdrawing caster did not regain facing and LOS on the selected boss", d.Output)
				}
				self.Orientation = 0
				o.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: fallbackNPC, VisitId: 1, RangeYards: 28, RequireLos: true, Ready: true}
				d, a = step()
			}
			target, cap, spell := fallbackGUID(fallbackNPC), "offense", float64(133)
			if class == 5 {
				target, cap, spell = "3", "heal", 6063
			}
			if a["capability"] != cap || a["targetGuid"] != target || a["spellId"] != spell {
				t.Fatal("lost the role or rank at a safe native-written position", d.Output)
			}
			if stance := d.Output["rangedStance"].(map[string]any); stance["positionReady"] == true {
				t.Fatal("local geometry claimed a native LOS acknowledgement", stance)
			}
			boss.Trajectory = &worldv1.CreatureTrajectory{Known: true, DestinationKnown: true, DestinationX: 14, DurationMs: 1000, AgeMs: 1000}
			d, a = step()
			if a["capability"] != "move_body" {
				t.Fatal("previous safe pose obscured a moving boss", d.Output)
			}
			groundReceipt(t, p, d, a, behavior.ActionNotDispatched, "local_physics_refused", false)
			boss.Alive = proto.Bool(false)
			d, a = step()
			if a["mode"] != "release" || a["owner"] != dustOwner || a["token"] != "dust-fixture" {
				t.Fatal("boss death did not release exactly the owned lease", d.Output)
			}
		})
	}
}

func TestPrincessDustPreservesSelectedAddAndNativeLOS(t *testing.T) {
	p, o := newFallbackPolicyTest(t, ""), princessEncounter(8)
	boss := o.Targets[fallbackGUID(fallbackNPC)]
	princessLease(t, p, o, false)
	boss.X = 27
	addID := fallbackNPC + 8
	add := proto.Clone(boss).(*actionsv1.EncounterCreature)
	add.Guid, add.Entry, add.Health, add.X = addID, 12207, 2, 8
	add.AttackObservation.VictimGuid = fallbackSelf
	o.Targets[fallbackGUID(addID)] = add
	step := func() (behavior.Decision, map[string]any) {
		d := p.decide(observeRanged(t, p, o, false))
		a, _ := d.Output["action"].(map[string]any)
		return d, a
	}
	d, a := step()
	if d.Output["focusGuid"] != fallbackGUID(addID) || a["targetGuid"] != fallbackGUID(addID) ||
		a["capability"] != "approach_target" || a["requireLos"] != true || a["minRangeYards"] != nil || a["clearanceTargetGuids"] != nil {
		t.Fatal("lost selected add or submitted unsupported target-band fields", d.Output)
	}
	o.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: addID, VisitId: 1, RangeYards: 30, RequireLos: true, Ready: true}
	d, a = step()
	if a["capability"] != "offense" || a["targetGuid"] != fallbackGUID(addID) || a["spellId"] != float64(133) {
		t.Fatal("native LOS was ready but ordinary selected damage did not resume", d.Output)
	}
}

func TestPrincessDustHealerReachesSelectedPatientOutsideBoss(t *testing.T) {
	p, o := newFallbackPolicyTest(t, ""), princessEncounter(5)
	princessLease(t, p, o, false)
	o.Targets[fallbackGUID(fallbackNPC)].X = 26
	o.Players["3"].X, o.Players["3"].Y = 0, 55
	o.Healing.Patients[0].Distance = proto.Float64(55)
	d, a := groundAction(t, p, o)
	if a["capability"] != "move_body" || math.Sin(a["bearingRadians"].(float64)) < .98 {
		t.Fatal("healer did not approach the patient outside carrier clearance", d.Output)
	}
}

func TestPrincessDustCustodyAndPassiveSightings(t *testing.T) {
	for _, mode := range []string{"unengaged", "tank", "no_geometry", "no_actuator", "native_band", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			o := princessEncounter(8)
			intent := ""
			switch mode {
			case "unengaged":
				o.Players["3"].InCombat = false
			case "tank":
				intent = "local encounter_intent={role='tank'}\n"
			case "no_geometry":
				o.Players["4"].PositionKnown = false
			case "no_actuator":
				o.Navigation.BodyMovementSupported = false
			}
			p := newFallbackPolicyTest(t, intent)
			if mode == "native_band" || mode == "unknown" {
				princessLease(t, p, o, mode == "native_band")
			}
			d := p.decide(observeRanged(t, p, o, mode == "native_band"))
			a, _ := d.Output["action"].(map[string]any)
			switch mode {
			case "native_band":
				if a["capability"] != "approach_target" || a["minRangeYards"] != float64(25) || a["rangeYards"] != float64(28) ||
					a["requireLos"] != true {
					t.Fatal("full native range-band contract lost", d.Output)
				}
			case "unknown":
				groundReceipt(t, p, d, a, behavior.ActionUnknown, "body_input_unconfirmed", false)
				o.Targets[fallbackGUID(fallbackNPC)].Alive = proto.Bool(false)
				for i := 0; i < 2; i++ {
					d = p.decide(observeRanged(t, p, o, false))
					if d.Output["action"] != nil {
						t.Fatal("ended encounter replayed or released UNKNOWN movement", d.Output)
					}
				}
			default:
				if d.Output["rangedStance"].(map[string]any)["active"] == true {
					t.Fatal("unwarranted caster spacing", d.Output)
				}
			}
		})
	}
}
