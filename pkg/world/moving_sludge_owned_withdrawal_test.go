package world

import (
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/protobuf/proto"
	"math"
	"testing"
)

func TestOwnedSludgeLeaseWithdrawsBeforeReadyHealing(t *testing.T) {
	for _, patient := range []string{"peer", "self"} {
		for _, geometry := range []string{"near_stopped", "near_spline", "far", "unknown_spline", "pending", "motion_unknown", "movement_unsupported"} {
			t.Run(patient+"/"+geometry, func(t *testing.T) {
				p := newFallbackPolicyTest(t, "")
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
				if patient == "self" {
					self.HealthKnown = true
					self.Health = 30
					self.MaxHealth = 100
					o.Players["8"].Health = 100
					o.Healing.Patients = []*actionsv1.HealingPatient{{Guid: 4, Health: 30, MaxHealth: 100, Distance: proto.Float64(0)}}
				}
				o.Navigation = &actionsv1.NavigationObservation{BodyMovementSupported: true, MovementOwner: &actionsv1.MovementOwnerState{}}
				for i := uint64(0); i < 3; i++ {
					n := proto.Clone(sludgePriorObservation().Targets[fallbackGUID(fallbackNPC)]).(*actionsv1.EncounterCreature)
					n.Guid = fallbackNPC + i
					n.X = self.X + 25 + float32(i)
					n.Y = self.Y
					n.Z = self.Z
					n.AttackObservation = &actionsv1.EncounterAttackObservation{}
					o.Targets[fallbackGUID(n.Guid)] = n
				}
				settlePriorLease(t, p, o, false)
				// All three remain current visible hazards. The final one closes in;
				// its raw origin and current spline pose can deliberately disagree.
				n := o.Targets[fallbackGUID(fallbackNPC+2)]
				switch geometry {
				case "near_stopped", "pending", "motion_unknown", "movement_unsupported":
					n.X = self.X + 3
				case "near_spline":
					n.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, AgeMs: 1000, DestinationKnown: true, DestinationX: self.X + 3, DestinationY: self.Y, DestinationZ: self.Z}
				case "unknown_spline":
					n.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, AgeMs: 500, DestinationKnown: false}
				}
				if geometry == "movement_unsupported" {
					o.Navigation.BodyMovementSupported = false
				}
				if geometry == "motion_unknown" {
					o.Navigation.MotionUnconfirmed = true
				}
				if geometry == "pending" {
					d := p.decide(observeRanged(t, p, o, false))
					if _, e := p.host.ReserveAction(d); e != nil {
						t.Fatal(e)
					}
				}
				out := p.decide(observeRanged(t, p, o, false)).Output
				a, _ := out["action"].(map[string]any)
				if geometry == "far" || geometry == "movement_unsupported" {
					if a["capability"] != "heal" {
						t.Fatal("safe stationary care lost", out)
					}
					return
				}
				if a["capability"] == "heal" {
					t.Fatal("ready healing bypassed current hazard or unresolved geometry/custody", out)
				}
				if geometry == "near_stopped" || geometry == "near_spline" {
					if a["capability"] != "move_body" {
						t.Fatal("safe outward input was not selected", out)
					}
					bearing, _ := a["bearingRadians"].(float64)
					if math.Cos(bearing) >= 0 {
						t.Fatal("withdrawal approached eastern moving pack", out)
					}
				} else if a["capability"] == "move_body" {
					t.Fatal("unknown/pending admitted another input", out)
				}
			})
		}
	}
}
