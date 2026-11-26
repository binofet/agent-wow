package world

import (
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"reflect"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
)

func sonicEncounter(class uint32) *actionsv1.EncounterObservationResponse {
	o := sludgePriorObservation()
	self, tank := o.Players["4"], o.Players["3"]
	self.ClassId = class
	self.GroundRunSpeed = proto.Float32(7)
	self.CombatKnown, tank.CombatKnown, tank.InCombat = true, true, true
	o.Navigation.OwnerGuid = 3
	tank.X = 11
	first := o.Targets[fallbackGUID(fallbackNPC)]
	first.Entry, first.X = 13323, 10
	first.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3}
	for index, entry := range []uint32{13323, 13323, 12207} {
		id := fallbackNPC + uint64(index+1)
		other := proto.Clone(first).(*actionsv1.EncounterCreature)
		other.Guid, other.Entry, other.X = id, entry, float32(15+index)
		if index == 1 {
			other.AttackObservation = &actionsv1.EncounterAttackObservation{}
		}
		o.Targets[fallbackGUID(id)] = other
	}
	if class == 5 {
		healing := positionedHealObservation()
		o.Healing, o.Spells = healing.Healing, healing.Spells
		tank.HealthKnown, tank.Health, tank.MaxHealth = true, 20, 100
		o.Healing.Patients[0].Guid = tank.Guid
		o.Healing.Patients[0].Distance = proto.Float64(11)
	}
	return o
}

func sonicLease(t *testing.T, p *fallbackPolicyTest, o *actionsv1.EncounterObservationResponse) {
	t.Helper()
	d := p.decide(observeRanged(t, p, o, true))
	a, _ := d.Output["action"].(map[string]any)
	owner := "ranged:maraudon_sonic_burst"
	if a["capability"] != "movement_owner" || a["mode"] != "acquire" || a["owner"] != owner {
		t.Fatalf("missing caster stance %v", d.Output)
	}
	id, err := p.host.ReserveAction(d)
	if err != nil {
		t.Fatal(err)
	}
	o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: owner}
	_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": owner, "result": map[string]any{"token": "caster-fixture", "state": map[string]any{"sessionId": fallbackSession, "selfGuid": "4", "movementOwner": map[string]any{"active": true, "owner": owner}}}}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSonicBurstCastersKeepLiveCarrierClearanceAndCastAgain(t *testing.T) {
	for _, class := range []uint32{5, 8, 9} {
		p, o := newFallbackPolicyTest(t, ""), sonicEncounter(class)
		sonicLease(t, p, o)
		step := func() map[string]any { return p.decide(observeRanged(t, p, o, true)).Output }
		target := fallbackGUID(fallbackNPC)
		wantRange := float64(22)
		if class == 5 {
			target = "3"
			wantRange = 40
		}
		out := step()
		a, _ := out["action"].(map[string]any)
		ids := []any{fallbackGUID(fallbackNPC), fallbackGUID(fallbackNPC + 1)}
		if a["capability"] != "approach_target" || a["targetGuid"] != target || a["clearanceYards"] != float64(14) ||
			a["rangeYards"] != wantRange || a["requireLos"] != true || !reflect.DeepEqual(a["clearanceTargetGuids"], ids) {
			t.Fatalf("class %d unsafe or misdirected route: %v", class, out)
		}
		if class != 5 && a["minRangeYards"] != float64(14) {
			t.Fatal(a)
		}
		self := o.Players["4"]
		self.X = -8
		targetID := fallbackNPC
		minimum := float32(14)
		if class == 5 {
			targetID = 3
			minimum = 0
			o.Healing.Patients[0].Distance = proto.Float64(19)
		}
		o.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: targetID, VisitId: 1, RangeYards: float32(wantRange), MinRangeYards: minimum, ClearanceTargetGuids: []uint64{fallbackNPC, fallbackNPC + 1}, ClearanceYards: 14, RequireLos: true, Ready: true}
		out = step()
		a, _ = out["action"].(map[string]any)
		if class == 5 && (a["capability"] != "heal" || a["targetGuid"] != "3") || class != 5 && a["capability"] != "offense" {
			t.Fatalf("class %d did not cast from safe geometry: %v", class, out)
		}
		// An already-acknowledged old band does not excuse the moving carrier.
		o.Targets[fallbackGUID(fallbackNPC+1)].Trajectory = &worldv1.CreatureTrajectory{Known: true, DestinationKnown: true, DestinationX: -6, DurationMs: 1000, AgeMs: 1000}
		out = step()
		a, _ = out["action"].(map[string]any)
		if a["capability"] != "approach_target" {
			t.Fatalf("class %d reused old safety after carrier moved: %v", class, out)
		}
		for _, n := range o.Targets {
			n.Alive = proto.Bool(false)
		}
		o.Players["3"].InCombat = false
		out = step()
		a, _ = out["action"].(map[string]any)
		if a["mode"] != "release" || a["owner"] != "ranged:maraudon_sonic_burst" {
			t.Fatalf("class %d held ordinary follow after combat: %v", class, out)
		}
	}
}

func TestSonicBurstLeavesOrdinaryRolesAndPassiveSightingsAlone(t *testing.T) {
	for _, mode := range []string{"rogue", "warrior", "tank_role", "passive", "foreign", "stale", "other_map", "other_species", "unknown_pose", "no_band"} {
		t.Run(mode, func(t *testing.T) {
			o := sonicEncounter(8)
			prefix := ""
			switch mode {
			case "rogue":
				o.Players["4"].ClassId = 4
			case "warrior":
				o.Players["4"].ClassId = 1
			case "tank_role":
				prefix = "local encounter_intent={role='tank'}\n"
			case "passive":
				o.Players["3"].InCombat = false
			case "foreign":
				for _, n := range o.Targets {
					n.AttackObservation.VictimGuid = 44
				}
			case "stale":
				for _, n := range o.Targets {
					n.AttackObservation.AgeMs = 6000
				}
			case "other_map":
				o.MapId = proto.Uint32(1)
			case "other_species":
				for _, n := range o.Targets {
					n.Entry = 12207
				}
			case "unknown_pose":
				o.Players["4"].PositionKnown = false
			}
			p := newFallbackPolicyTest(t, prefix)
			out := p.decide(observeRanged(t, p, o, mode != "no_band")).Output
			status, _ := out["rangedStance"].(map[string]any)
			if status["id"] == "maraudon_sonic_burst" {
				t.Fatal(mode, out)
			}
		})
	}
}

func TestSonicBurstGroundCustodyPreservesStationarySwimmingCare(t *testing.T) {
	for _, held := range []bool{false, true} {
		for _, body := range []string{"swimming", "unconfirmed", "ground_unavailable"} {
			p, o := newFallbackPolicyTest(t, ""), sonicEncounter(5)
			if held {
				sonicLease(t, p, o)
			}
			switch body {
			case "swimming":
				o.Players["4"].Swimming = true
				o.Players["4"].GroundRunSpeed = nil
			case "unconfirmed":
				o.Navigation.MotionUnconfirmed = true
			case "ground_unavailable":
				o.Players["4"].GroundRunSpeed = nil
			}
			d := p.decide(observeRanged(t, p, o, true))
			a, _ := d.Output["action"].(map[string]any)
			if a["capability"] == "approach_target" || a["capability"] == "movement_owner" && a["mode"] != "release" {
				t.Fatalf("%s, held %v requested new ground control: %v", body, held, d.Output)
			}
			if body == "unconfirmed" {
				continue
			}
			if held {
				if a["mode"] != "release" {
					t.Fatal("missing cleanup of owned ground lease", d.Output)
				}
				id, err := p.host.ReserveAction(d)
				if err != nil {
					t.Fatal(err)
				}
				o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
				_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "movement_owner", "owner": a["owner"], "mode": "release", "result": map[string]any{"sessionId": fallbackSession, "selfGuid": "4"}}}})
				if err != nil {
					t.Fatal(err)
				}
				d = p.decide(observeRanged(t, p, o, true))
				a, _ = d.Output["action"].(map[string]any)
			}
			if a["capability"] != "heal" || a["targetGuid"] != "3" {
				t.Fatal("stationary care displaced by dry-only positioning", d.Output)
			}
		}
	}
}
func TestSonicBurstUsesObservedTankAssignment(t *testing.T) {
	for _, assignment := range []string{"raid", "dungeon", "absent"} {
		p, o := newFallbackPolicyTest(t, ""), sonicEncounter(5)
		o.Party.Group.Members[1].Flags = 1
		m := &partyv1.Member{Guid: 7, Online: true, Flags: 2}
		if assignment == "dungeon" {
			m.Flags = 0
			m.Roles = 2
		}
		o.Party.Group.Members = append(o.Party.Group.Members, m)
		o.Players["7"] = proto.Clone(o.Players["3"]).(*actionsv1.EncounterPlayer)
		o.Players["7"].Guid = 7
		if assignment == "absent" {
			delete(o.Players, "7")
			out := p.decide(observeRanged(t, p, o, true)).Output
			if out["rangedStance"].(map[string]any)["active"] == true {
				t.Fatal("unlocated tank replaced by assistant", out)
			}
			continue
		}
		sonicLease(t, p, o)
		out := p.decide(observeRanged(t, p, o, true)).Output
		a, _ := out["action"].(map[string]any)
		if a["capability"] != "approach_target" || a["targetGuid"] != "7" {
			t.Fatal("lost assigned tank", assignment, out)
		}
	}
}
