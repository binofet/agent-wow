package world

import (
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/proto"
	"testing"
)

// The moving creature's current pose remains the range witness. No ground
// object, persistent cloud point, or inferred pack link is supplied here.
func TestMovingSludgePriorKeepsStationaryCareAndTracksNearHazard(t *testing.T) {
	for _, modern := range []bool{false, true} {
		for _, kind := range []string{"distant_unrelated_combat", "distant_party_fight", "near", "unknown", "pending", "moving", "motion_unknown"} {
			t.Run(map[bool]string{false: "legacy", true: "band"}[modern]+"/"+kind, func(t *testing.T) {
				p := passivePriorPolicy(t, "")
				o := positionedHealObservation()
				o.MapId = proto.Uint32(349)
				for _, v := range o.Players {
					v.MapId = proto.Uint32(349)
					v.CombatKnown = true
					v.InCombat = false
				}
				self := o.Players["4"]
				self.MovementKnown = true
				self.Moving = false
				self.InCombat = true
				n := proto.Clone(sludgePriorObservation().Targets[fallbackGUID(fallbackNPC)]).(*actionsv1.EncounterCreature)
				n.X = self.X + 170
				n.Y = self.Y
				n.Z = self.Z
				n.Engaged = false
				n.AttackObservation = &actionsv1.EncounterAttackObservation{}
				o.Targets[fallbackGUID(fallbackNPC)] = n
				o.Navigation = &actionsv1.NavigationObservation{MovementOwner: &actionsv1.MovementOwnerState{}}
				switch kind {
				case "distant_party_fight":
					n.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3}
				case "near":
					n.X = self.X + 3
				case "unknown":
					n.PositionKnown = false
				case "moving":
					self.Moving = true
					n.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3}
				case "motion_unknown":
					o.Navigation.MotionUnconfirmed = true
					n.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3}
				}
				if kind == "pending" {
					d := p.decide(observeRanged(t, p, o, modern))
					if _, err := p.host.ReserveAction(d); err != nil {
						t.Fatal(err)
					}
				}
				out := p.decide(observeRanged(t, p, o, modern)).Output
				a, _ := out["action"].(map[string]any)
				if kind == "distant_unrelated_combat" || kind == "distant_party_fight" {
					if a["capability"] != "heal" || a["targetGuid"] != "8" {
						t.Fatal("stationary current care acquired unrelated range hold", out)
					}
				} else if a["capability"] == "heal" {
					t.Fatal("unsafe or protected observation bypassed custody/positioning", out)
				}
			})
		}
	}
}

func TestMovingSludgePriorRechecksEveryVisibleCreatureAcrossVisit(t *testing.T) {
	p := passivePriorPolicy(t, "")
	o := positionedHealObservation()
	o.MapId = proto.Uint32(349)
	for _, v := range o.Players {
		v.MapId = proto.Uint32(349)
		v.CombatKnown = true
		v.InCombat = false
	}
	self := o.Players["4"]
	self.MovementKnown = true
	self.Moving = false
	o.Navigation = &actionsv1.NavigationObservation{MovementOwner: &actionsv1.MovementOwnerState{}}
	for i := uint64(0); i < 3; i++ {
		n := proto.Clone(sludgePriorObservation().Targets[fallbackGUID(fallbackNPC)]).(*actionsv1.EncounterCreature)
		n.Guid = fallbackNPC + i
		n.X = self.X + 170 + float32(i)
		n.Y = self.Y
		n.Z = self.Z
		n.Engaged = false
		n.AttackObservation = &actionsv1.EncounterAttackObservation{}
		o.Targets[fallbackGUID(n.Guid)] = n
	}
	check := func(wantHeal bool) {
		t.Helper()
		out := p.decide(observeRanged(t, p, o, false)).Output
		a, _ := out["action"].(map[string]any)
		if (a["capability"] == "heal") != wantHeal {
			t.Fatal("moving hazard/current visit not rechecked", out)
		}
	}
	check(true)
	// The third creature moves into range; no static point or attack inference.
	n := o.Targets[fallbackGUID(fallbackNPC+2)]
	n.X = self.X + 3
	check(false)
	// A chosen acquire has already entered Lua custody even before the caller
	// reserves it. A visit change must not retire that unanswered operation.
	o.VisitId = 2
	n.X = self.X + 170
	check(false)
	// With no outstanding operation, a fresh body/visit remains free to care.
	p = passivePriorPolicy(t, "")
	check(true)
}
