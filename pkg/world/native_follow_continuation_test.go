package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
	"testing"
)

func admittedNativeFollow(t *testing.T) (*fallbackPolicyTest, *actionsv1.EncounterObservationResponse, string) {
	t.Helper()
	p, o, source := nativeFollowPrior(t)
	o.Navigation.Following = true
	o.Navigation.FollowRevision = 3
	d := p.decide(observeRanged(t, p, o, false))
	a, _ := d.Output["action"].(map[string]any)
	if a["mode"] != "release" || a["token"] != "prior-fixture" {
		t.Fatal("initial observed handoff did not release own exact token", d.Output)
	}
	settleFollowPrior(t, p, d, "release", behavior.ActionConfirmed)
	o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
	o.Players["4"].PositionEstimated = true
	out := p.decide(observeRanged(t, p, o, false)).Output
	if out["action"] != nil {
		t.Fatal("confirmed release did not settle before walking", out)
	}
	return p, o, source
}

func TestNativeFollowContinuesAcrossWrittenPoseEstimates(t *testing.T) {
	p, o, source := admittedNativeFollow(t)
	self := o.Players["4"]
	self.Moving = true
	o.Players["3"].X = 130
	o.Navigation.GoalKnown = true
	o.Navigation.GoalX = 130
	o.Navigation.RouteAvailable = true
	o.Navigation.Phase = "following_formation"
	first := o.Targets[fallbackGUID(fallbackNPC)]
	for i, x := range []float32{166.3, 167.8, 170.6} {
		n := proto.Clone(first).(*actionsv1.EncounterCreature)
		n.Guid = fallbackNPC + uint64(i)
		n.X = x
		o.Targets[fallbackGUID(n.Guid)] = n
	}
	// These are same-body typed native written estimates, not another player's
	// coordinates or fabricated server corrections. No confirmation flag is
	// invented in EncounterObservation: that flag belongs to Actions.State.
	for i := 0; i <= 65; i++ {
		self.X = float32(i * 2)
		out := p.decide(observeRanged(t, p, o, false)).Output
		status, _ := out["rangedStance"].(map[string]any)
		if out["action"] != nil || status["phase"] != "continuing_native_follow_estimate" || status["positionReady"] != false {
			t.Fatal("normal written walking reacquired optional ranged control", i, out)
		}
		if i == 30 {
			if _, e := p.host.Reload(context.Background(), source+"\n-- retain active native follow through policy edit", p.host.Status().Revision); e != nil {
				t.Fatal(e)
			}
		}
	}
}

func TestNativeFollowEstimateDoesNotReleaseAnyHeldLeaseOrOverrideDanger(t *testing.T) {
	for _, kind := range []string{"held_own", "foreign_owner", "missing_owner", "motion_unknown", "fresh_attack", "near_hazard", "explicit_hold"} {
		t.Run(kind, func(t *testing.T) {
			var p *fallbackPolicyTest
			var o *actionsv1.EncounterObservationResponse
			if kind == "held_own" {
				p, o, _ = nativeFollowPrior(t)
				o.Navigation.Following = true
				o.Players["4"].PositionEstimated = true
			} else {
				p, o, _ = admittedNativeFollow(t)
			}
			switch kind {
			case "foreign_owner":
				o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "another-task"}
			case "missing_owner":
				o.Navigation.MovementOwner = nil
			case "motion_unknown":
				o.Navigation.MotionUnconfirmed = true
			case "fresh_attack":
				o.Targets[fallbackGUID(fallbackNPC)].AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3}
			case "near_hazard":
				o.Targets[fallbackGUID(fallbackNPC)].X = 3
			case "explicit_hold":
				waitTaskCommand(t, p, map[string]any{"task": "ranged_only", "id": "maraudon_sludge_backline", "creatureEntry": 12222, "mapId": 349, "visitId": "1", "minRangeYards": 14, "maxRangeYards": 36, "clearanceYards": 2, "durationMs": 600000})
			}
			out := p.decide(observeRanged(t, p, o, true)).Output
			a, _ := out["action"].(map[string]any)
			status, _ := out["rangedStance"].(map[string]any)
			if status["phase"] == "continuing_native_follow_estimate" || a["mode"] == "release" {
				t.Fatal("continuation waived initial estimate/lease/danger guards", out)
			}
			if kind == "motion_unknown" || kind == "foreign_owner" || kind == "missing_owner" {
				if out["action"] != nil {
					t.Fatal("unknown/foreign custody entered another action", out)
				}
			}
			if kind == "fresh_attack" || kind == "near_hazard" || kind == "explicit_hold" {
				if a["capability"] != "movement_owner" || a["mode"] != "acquire" {
					t.Fatal("real danger or explicit hold lost priority", out)
				}
			}
		})
	}
}

func TestNativeFollowEstimatedNearDangerKeepsExistingBandDefense(t *testing.T) {
	p, o, _ := admittedNativeFollow(t)
	o.Targets[fallbackGUID(fallbackNPC)].X = 3
	d := p.decide(observeRanged(t, p, o, true))
	a, _ := d.Output["action"].(map[string]any)
	if a["mode"] != "acquire" {
		t.Fatal(d.Output)
	}
	settleFollowPrior(t, p, d, "acquire", behavior.ActionConfirmed)
	o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "ranged:maraudon_sludge_backline"}
	o.Players["4"].PositionEstimated = false // Existing withdrawal still requires its own trustworthy geometry.
	out := p.decide(observeRanged(t, p, o, true)).Output
	a, _ = out["action"].(map[string]any)
	if a["capability"] != "approach_target" || a["targetGuid"] != fallbackGUID(fallbackNPC) || a["minRangeYards"] != float64(16) {
		t.Fatal("near observed danger no longer reaches existing guarded band action", out)
	}
}

func TestNativeFollowProtectiveLeaseResumesOnlySameGoal(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_goal", true: "changed_goal"}[changed], func(t *testing.T) {
			p, o, _ := admittedNativeFollow(t)
			o.Targets[fallbackGUID(fallbackNPC)].X = 3
			d := p.decide(observeRanged(t, p, o, true))
			a, _ := d.Output["action"].(map[string]any)
			if a["mode"] != "acquire" {
				t.Fatal(d.Output)
			}
			settleFollowPrior(t, p, d, "acquire", behavior.ActionConfirmed)
			o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "ranged:maraudon_sludge_backline"}
			o.Targets[fallbackGUID(fallbackNPC)].X = 170
			if changed {
				o.Navigation.FollowRevision++
			}
			d = p.decide(observeRanged(t, p, o, false))
			a, _ = d.Output["action"].(map[string]any)
			if changed {
				if a["mode"] == "release" {
					t.Fatal("estimated pose reused another follow goal", d.Output)
				}
				return
			}
			if a["mode"] != "release" || a["token"] != "follow-prior-token" {
				t.Fatal("cleared danger stranded exact interrupted goal", d.Output)
			}
			settleFollowPrior(t, p, d, "release", behavior.ActionConfirmed)
			o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
			for i := 0; i < 3; i++ {
				out := p.decide(observeRanged(t, p, o, false)).Output
				if out["action"] != nil {
					t.Fatal(out)
				}
			}
		})
	}
}

func TestNativeFollowProtectiveReleaseUnknownNeverReplays(t *testing.T) {
	p, o, source := admittedNativeFollow(t)
	o.Targets[fallbackGUID(fallbackNPC)].X = 3
	d := p.decide(observeRanged(t, p, o, true))
	settleFollowPrior(t, p, d, "acquire", behavior.ActionConfirmed)
	o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "ranged:maraudon_sludge_backline"}
	o.Targets[fallbackGUID(fallbackNPC)].X = 170
	d = p.decide(observeRanged(t, p, o, false))
	a, _ := d.Output["action"].(map[string]any)
	if a["mode"] != "release" {
		t.Fatal(d.Output)
	}
	settleFollowPrior(t, p, d, "release", behavior.ActionUnknown)
	if _, e := p.host.Reload(context.Background(), source+"\n-- preserve unknown protective release", p.host.Status().Revision); e != nil {
		t.Fatal(e)
	}
	o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
	for i := 0; i < 3; i++ {
		out := p.decide(observeRanged(t, p, o, false)).Output
		if out["action"] != nil {
			t.Fatal("unknown release replayed or replaced", out)
		}
	}
}
