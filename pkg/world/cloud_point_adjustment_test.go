package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

func cloudPointPolicy(t *testing.T, provider string) (*fallbackPolicyTest, *actionsv1.EncounterObservationResponse) {
	t.Helper()
	source := "local encounter_intent={cloud_points=" + provider + "}\n" + readEncounterBundle(t)
	source = strings.Replace(source, "local function decide(frame)", "local function decide(frame)\nframe.clock={monotonicMs=frame.input.now or 100000,unixMs=frame.input.now or 100000}", 1)
	source = strings.Replace(source, "local points=intent.cloud_points(frame.clock.unixMs,scope,sightings)", "local points=intent.cloud_points(frame.clock.unixMs,scope,sightings)\nout.testSightings=sightings;out.testScope=scope", 1)
	h, e := behavior.NewSession(context.Background(), fallbackSession, source)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(h.Close)
	o := fallbackObservation()
	o.MapId = proto.Uint32(349)
	o.MeleeKnown = true
	o.Spells = map[string]*actionsv1.EncounterSpell{}
	self := o.Players["4"]
	self.MapId = proto.Uint32(349)
	self.PositionKnown = true
	self.PositionRevision = 1
	self.MovementKnown = true
	self.CombatKnown = true
	o.Navigation = &actionsv1.NavigationObservation{BodyMovementSupported: true, MovementOwner: &actionsv1.MovementOwnerState{}}
	return &fallbackPolicyTest{t: t, host: h}, o
}
func cloudPointStep(t *testing.T, p *fallbackPolicyTest, o *actionsv1.EncounterObservationResponse, now int) behavior.Decision {
	t.Helper()
	d, e := p.host.Decide(context.Background(), map[string]any{"kind": "encounter", "observationSequence": p.observe(o), "now": now})
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func TestCloudPointLocalAdjustmentInsideDenseAndOutside(t *testing.T) {
	for _, kind := range []string{"inside", "coincident", "overlap", "outside", "expired", "foreign", "pending", "unknown_motion", "wrong_radius", "vertical"} {
		t.Run(kind, func(t *testing.T) {
			points := "{{x=2,y=0,z=0,radiusYards=5,expiresAtMs=120000}}"
			if kind == "coincident" {
				points = "{{x=0,y=0,z=0,radiusYards=5,expiresAtMs=120000}}"
			}
			if kind == "overlap" {
				points = "{{x=1,y=0,z=0,radiusYards=5,expiresAtMs=120000},{x=-1,y=0,z=0,radiusYards=5,expiresAtMs=120000},{x=0,y=1,z=0,radiusYards=5,expiresAtMs=120000},{x=0,y=-1,z=0,radiusYards=5,expiresAtMs=120000}}"
			}
			if kind == "outside" {
				points = "{{x=20,y=0,z=0,radiusYards=5,expiresAtMs=120000}}"
			}
			if kind == "wrong_radius" {
				points = "{{x=2,y=0,z=0,radiusYards=5.5,expiresAtMs=120000}}"
			}
			if kind == "vertical" {
				points = "{{x=2,y=0,z=10,radiusYards=5,expiresAtMs=120000}}"
			}
			p, o := cloudPointPolicy(t, "function()return "+points+" end")
			now := 100000
			if kind == "expired" {
				now = 120001
			}
			if kind == "foreign" {
				o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "consumable:own-drink", RemainingMs: 2000}
			}
			if kind == "unknown_motion" {
				o.Navigation.MotionUnconfirmed = true
			}
			if kind == "pending" {
				d := cloudPointStep(t, p, o, now)
				if _, e := p.host.ReserveAction(d); e != nil {
					t.Fatal(e)
				}
			}
			d := cloudPointStep(t, p, o, now)
			a, _ := d.Output["action"].(map[string]any)
			active := kind == "inside" || kind == "coincident" || kind == "overlap"
			if active {
				if a["capability"] != "move_body" || a["durationMs"] != float64(200) || a["jump"] != false {
					t.Fatal("no ordinary bounded correction", d.Output)
				}
				if kind == "inside" && math.Cos(a["bearingRadians"].(float64)) > -.99 {
					t.Fatal("did not leave inside point", a)
				}
				if kind == "inside" {
					status := d.Output["cloudAdjustment"].(map[string]any)
					point := status["point"].(map[string]any)
					if point["x"] != float64(2) || point["expiresAtMs"] != float64(120000) || status["distanceYards"] != float64(2) || status["clearanceYards"] != float64(-3) {
						t.Fatal("incorrect contributing point evidence", status)
					}
				}
			} else if d.Output["action"] != nil {
				t.Fatal("points changed unrelated or protected movement", d.Output)
			}
		})
	}
}
func TestCloudPointRealEncounterSightingsAreStableEstimatedAndScoped(t *testing.T) {
	p, o := cloudPointPolicy(t, "function()return {} end")
	addFallbackTarget(o, fallbackNPC, 0, 100)
	n := o.Targets[fallbackGUID(fallbackNPC)]
	n.Entry = 12221
	n.MapId = proto.Uint32(349)
	n.PositionKnown = true
	n.PositionRevision = 7
	n.X = 2
	first := cloudPointStep(t, p, o, 100000).Output["testSightings"].([]any)[0].(map[string]any)
	n.PositionRevision = 8 // sameXYZ is not a new cloud or a new expiry
	second := cloudPointStep(t, p, o, 110000).Output["testSightings"].([]any)[0].(map[string]any)
	if first["id"] != second["id"] || first["observedAtMs"] != second["observedAtMs"] || second["estimated"] != true || second["castObserved"] != false {
		t.Fatal("poll renewed or misattributed sighting", first, second)
	}
	n.X = 3
	n.PositionRevision = 9
	third := cloudPointStep(t, p, o, 111000).Output["testSightings"].([]any)[0].(map[string]any)
	if first["id"] == third["id"] || third["observedAtMs"] != float64(111000) {
		t.Fatal("new observed movement did not reach actor emitter", third)
	}
	o.VisitId = 2
	fourth := cloudPointStep(t, p, o, 112000).Output["testSightings"].([]any)[0].(map[string]any)
	if fourth["observedAtMs"] != float64(112000) {
		t.Fatal("old epoch observation reused", fourth)
	}
}
func TestCloudPointDefaultRouteAndFollowExpiry(t *testing.T) {
	raw, e := os.ReadFile("../../behaviors/follow-formation.lua")
	if e != nil {
		t.Fatal(e)
	}
	p, o := cloudPointPolicy(t, "function()return {{x=2,y=0,z=0,radiusYards=5,expiresAtMs=120000}} end")
	source := "local encounter_formation=(function()\n" + string(raw) + "\nend)()\nlocal encounter_intent={cloud_points=function()return {{x=2,y=0,z=0,radiusYards=5,expiresAtMs=120000}} end}\n" + readEncounterBundle(t)
	source = strings.Replace(source, "local function decide(frame)", "local function decide(frame)\nframe.clock={monotonicMs=frame.input.now,unixMs=frame.input.now}", 1)
	h, e := behavior.NewSession(context.Background(), fallbackSession, source)
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	p.host = h
	owner := fallbackPlayer(3)
	owner.MapId = proto.Uint32(349)
	owner.PositionKnown = true
	owner.MovementKnown = true
	owner.X = 30
	o.Players["3"] = owner
	o.Navigation.FollowOwner = owner
	o.Navigation.OwnerGuid = 3
	o.Navigation.Following = true
	o.Navigation.FollowFormationSupported = true
	o.Navigation.FollowRevision = 1
	d := cloudPointStep(t, p, o, 120001)
	if d.Output["action"] != nil || d.Output["followFormation"] == nil {
		t.Fatal("expired point changed normal follow", d.Output)
	}
}

func TestCloudPointDenseInputKeepsLateUnderfootPoint(t *testing.T) {
	// Real emitters retain their20s points; do not charge construction of
	// every historical table to each decision. Grow64 points per callback,
	// then exercise the complete cached961-point consumer under normal limits.
	p, o := cloudPointPolicy(t, "(function()local p,n={},0;return function() if n==960 then p[961]=p[961] or {x=2,y=0,z=0,radiusYards=5,expiresAtMs=120000};return p end local stop=math.min(960,n+64);for i=n+1,stop do p[i]={x=100+i,y=0,z=0,radiusYards=5,expiresAtMs=120000} end n=stop;return p end end)()")
	for i := 0; i < 15; i++ {
		d := cloudPointStep(t, p, o, 100000+i)
		if d.Output["action"] != nil {
			t.Fatal("irrelevant retained points changed default action", d.Output)
		}
	}
	d := cloudPointStep(t, p, o, 100016)
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] != "move_body" {
		t.Fatal("dense emitter output disabled useful late point", d.Output)
	}
}
func TestCloudPointIntersectingNextLegUsesSideOffsetAndUnknownIsNotReplayed(t *testing.T) {
	p, o := cloudPointPolicy(t, "function(now)if now==100000 then return {} end return {{x=7,y=0,z=0,radiusYards=5,expiresAtMs=120000}} end")
	o.Players["4"].InCombat = true
	addFallbackTarget(o, fallbackNPC, fallbackSelf, 100)
	n := o.Targets[fallbackGUID(fallbackNPC)]
	n.MapId = proto.Uint32(349)
	n.X = 30
	n.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: fallbackSelf}
	o.Spells["133"] = &actionsv1.EncounterSpell{Known: true, Ready: true, Available: true, MetadataKnown: true, Capability: "offense", FamilyId: 133, Rank: 1, MaxRange: 20, DirectDamage: 10}
	baseline := cloudPointStep(t, p, o, 100000)
	if a, _ := baseline.Output["action"].(map[string]any); a["capability"] != "approach_target" {
		t.Fatal("fixture did not enter actual ordinary approach", baseline.Output)
	}
	d := cloudPointStep(t, p, o, 100100)
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] != "move_body" || math.Sin(a["bearingRadians"].(float64)) < .99 {
		t.Fatal("intersecting ordinary approach was not sidestepped", d.Output)
	}
	id, e := p.host.ReserveAction(d)
	if e != nil {
		t.Fatal(e)
	}
	_, e = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionUnknown, Payload: map[string]any{"capability": "move_body", "self_guid": "4", "visit_id": "1", "sequence_id": a["sequenceId"]}}})
	if e != nil {
		t.Fatal(e)
	}
	d = cloudPointStep(t, p, o, 100100)
	if d.Output["action"] != nil || p.host.Status().Pending != 1 {
		t.Fatal("unknown input replayed by point consumer", d.Output)
	}
}
func TestCloudPointTrajectoryInputIsEstimatedAndPollStable(t *testing.T) {
	p, o := cloudPointPolicy(t, "function()return {} end")
	addFallbackTarget(o, fallbackNPC, 0, 100)
	n := o.Targets[fallbackGUID(fallbackNPC)]
	n.Entry = 12221
	n.MapId = proto.Uint32(349)
	n.PositionRevision = 1
	n.X = 2
	// The ordinary pose helper supports this public moving-creature trajectory.
	n.Trajectory = &worldv1.CreatureTrajectory{Known: true, DestinationKnown: true, DestinationX: 12, DurationMs: 2000, AgeMs: 1000}
	first := cloudPointStep(t, p, o, 100000).Output["testSightings"].([]any)[0].(map[string]any)
	if first["x"] != float64(7) || first["estimated"] != true {
		t.Fatal("actual trajectory was not available as estimated input", first)
	}
	next := cloudPointStep(t, p, o, 100250).Output["testSightings"].([]any)[0].(map[string]any)
	if first["id"] != next["id"] || first["observedAtMs"] != next["observedAtMs"] {
		t.Fatal("poll renewed unchanged trajectory sample", first, next)
	}
}

func TestCloudPointInputRemainsAvailableUnderExistingRangedLease(t *testing.T) {
	p, o := cloudPointPolicy(t, "function()return {{x=2,y=0,z=0,radiusYards=5,expiresAtMs=120000}} end")
	addFallbackTarget(o, fallbackNPC, 0, 100)
	n := o.Targets[fallbackGUID(fallbackNPC)]
	n.Entry = 12221
	n.MapId = proto.Uint32(349)
	n.PositionRevision = 1
	n.X = 2
	o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "ranged:maraudon_sludge_backline", RemainingMs: 2000}
	d := cloudPointStep(t, p, o, 100000)
	if d.Output["action"] != nil || len(d.Output["testSightings"].([]any)) != 1 || d.Output["cloudPointInput"].(map[string]any)["activeMovementOwner"] != "ranged:maraudon_sludge_backline" {
		t.Fatal("input hidden or existing role lease overridden", d.Output)
	}
}
func TestCloudPointPreservesUrgentHealing(t *testing.T) {
	p, _ := cloudPointPolicy(t, "function()return {{x=12,y=10,z=10,radiusYards=5,expiresAtMs=120000}} end")
	o := positionedHealObservation()
	o.MapId = proto.Uint32(349)
	for _, v := range o.Players {
		v.MapId = proto.Uint32(349)
	}
	o.Navigation = &actionsv1.NavigationObservation{BodyMovementSupported: true, MovementOwner: &actionsv1.MovementOwnerState{}}
	d := cloudPointStep(t, p, o, 100000)
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] != "heal" || a["targetGuid"] != "8" {
		t.Fatal("point consumer stole urgent healing", d.Output)
	}
}

func TestCloudPointPassiveSludgeDoesNotBlanketVetoLocalEscape(t *testing.T) {
	for _, kind := range []string{"distant_passive", "protected_clearance", "active_owner"} {
		t.Run(kind, func(t *testing.T) {
			p, o := cloudPointPolicy(t, "function()return {{x=2,y=0,z=0,radiusYards=5,expiresAtMs=120000}} end")
			addFallbackTarget(o, fallbackNPC, 0, 100)
			n := o.Targets[fallbackGUID(fallbackNPC)]
			n.Entry = 12222
			n.MapId = proto.Uint32(349)
			n.X = 120
			n.PositionKnown = true
			n.AttackObservation = &actionsv1.EncounterAttackObservation{}
			if kind == "protected_clearance" {
				n.X = -17
			}
			if kind == "active_owner" {
				o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "ranged:maraudon_sludge_backline", RemainingMs: 2000}
			}
			d := cloudPointStep(t, p, o, 100000)
			a, _ := d.Output["action"].(map[string]any)
			stance := d.Output["rangedStance"].(map[string]any)
			if stance["phase"] != "passive_distant_prior" || stance["active"] != false {
				t.Fatal("not actual passive prior fixture", d.Output)
			}
			if kind == "distant_passive" {
				if a["capability"] != "move_body" {
					t.Fatal("inert passive prior starved correction", d.Output)
				}
			} else if d.Output["action"] != nil {
				t.Fatal("cloud pulse bypassed active custody or existing clearance", d.Output)
			}
		})
	}
}

func TestCloudPointPreservesUrgentInterruptApproach(t *testing.T) {
	p, _ := cloudPointPolicy(t, "function()return {{x=2,y=0,z=0,radiusYards=5,expiresAtMs=120000}} end")
	o := supportResObservation()
	o.MapId = proto.Uint32(349)
	for _, v := range o.Players {
		v.MapId = proto.Uint32(349)
		v.PositionKnown = true
		v.MovementKnown = true
	}
	o.Navigation = &actionsv1.NavigationObservation{BodyMovementSupported: true, MovementOwner: &actionsv1.MovementOwnerState{}}
	addFallbackTarget(o, fallbackNPC, fallbackSelf, 100)
	n := o.Targets[fallbackGUID(fallbackNPC)]
	n.MapId = proto.Uint32(349)
	n.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: fallbackSelf}
	o.Support.CasterInCombat = true
	o.Support.Interrupts = []*actionsv1.InterruptOption{{TargetGuid: fallbackNPC, SpellId: 1766, CastRevision: 8, RemainingMs: 1500, Healing: true, CanApproach: true, RangeYards: 5}}
	d := cloudPointStep(t, p, o, 100000)
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] != "approach_target" || a["targetGuid"] != fallbackGUID(fallbackNPC) {
		t.Fatal("point pulse stole urgent interrupt approach", d.Output)
	}
}

func TestCloudPointUsesOnlyAcknowledgedExistingRoleGroundWriter(t *testing.T) {
	for _, kind := range []string{"owned", "unknown_hold", "foreign", "changed_visit", "matching_estimate", "unrelated_estimate"} {
		t.Run(kind, func(t *testing.T) {
			p, o := cloudPointPolicy(t, "function()return {{x=2,y=0,z=0,radiusYards=5,expiresAtMs=120000}} end")
			addFallbackTarget(o, fallbackNPC, 0, 100)
			n := o.Targets[fallbackGUID(fallbackNPC)]
			n.Entry = 12222
			n.MapId = proto.Uint32(349)
			n.PositionKnown = true
			n.X = 123
			n.AttackObservation = &actionsv1.EncounterAttackObservation{}
			// Unknown combat status keeps the prior active, matching an admitted role
			// rather than the separately tested inert passive branch.
			o.Players["4"].CombatKnown = false
			d := cloudPointStep(t, p, o, 100000)
			a, _ := d.Output["action"].(map[string]any)
			if a["capability"] != "movement_owner" || a["mode"] != "acquire" {
				t.Fatal("role did not acquire through original path", d.Output)
			}
			id, e := p.host.ReserveAction(d)
			if e != nil {
				t.Fatal(e)
			}
			outcome := behavior.ActionConfirmed
			if kind == "unknown_hold" {
				outcome = behavior.ActionSettledUnverified
			}
			_, e = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: outcome, Payload: map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": "ranged:maraudon_sludge_backline", "result": map[string]any{"token": "actual-role-token", "state": map[string]any{"sessionId": fallbackSession, "selfGuid": "4", "movementOwner": map[string]any{"active": true, "owner": "ranged:maraudon_sludge_backline"}}}}}})
			if e != nil {
				t.Fatal(e)
			}
			o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "ranged:maraudon_sludge_backline", RemainingMs: 2000}
			if kind == "foreign" {
				o.Navigation.MovementOwner.Owner = "another-role"
			}
			if kind == "changed_visit" {
				o.VisitId = 2
			}
			d = cloudPointStep(t, p, o, 100100)
			a, _ = d.Output["action"].(map[string]any)
			if kind == "owned" || kind == "matching_estimate" || kind == "unrelated_estimate" {
				if a["capability"] != "move_body" || d.Output["cloudAdjustment"].(map[string]any)["existingRoleWriter"] != true {
					t.Fatal("known own role could not use original ground writer", d.Output)
				}
				if kind == "matching_estimate" || kind == "unrelated_estimate" {
					first := a
					id, e = p.host.ReserveAction(d)
					if e != nil {
						t.Fatal(e)
					}
					_, e = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionSettledUnverified, Payload: map[string]any{"capability": "move_body", "self_guid": "4", "visit_id": "1", "sequence_id": first["sequenceId"], "phase": "body_input_written", "transport_written": true}}})
					if e != nil {
						t.Fatal(e)
					}
					sequence, _ := strconv.ParseUint(first["sequenceId"].(string), 10, 64)
					o.Players["4"].PositionEstimated = true
					o.Players["4"].X = -1.4
					o.Navigation.BodyMovement = &actionsv1.BodyMovementState{SequenceId: sequence, VisitId: 1, Phase: "body_input_written", TransportWritten: true, PositionEstimated: true, X: -1.4}
					if kind == "unrelated_estimate" {
						o.Navigation.BodyMovement.SequenceId++
					}
					d = cloudPointStep(t, p, o, 100200)
					a, _ = d.Output["action"].(map[string]any)
					if kind == "unrelated_estimate" {
						if d.Output["action"] != nil {
							t.Fatal("borrowed unrelated estimated body witness", d.Output)
						}
						return
					}
					if a["capability"] != "move_body" || a["sequenceId"] == first["sequenceId"] {
						t.Fatal("correlated own body could not continue exiting disk", d.Output)
					}
				}
				id, e = p.host.ReserveAction(d)
				if e != nil {
					t.Fatal(e)
				}
				_, e = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionUnknown, Payload: map[string]any{"capability": "move_body", "self_guid": "4", "visit_id": "1", "sequence_id": a["sequenceId"]}}})
				if e != nil {
					t.Fatal(e)
				}
				d = cloudPointStep(t, p, o, 100200)
				if d.Output["action"] != nil || p.host.Status().Pending != 1 {
					t.Fatal("owned writer replayed unknown cloud pulse", d.Output)
				}
			} else if a["capability"] == "move_body" {
				t.Fatal("cloud step borrowed uncertain/foreign/prior-visit custody", d.Output)
			}
		})
	}
}
