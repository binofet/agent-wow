package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
	"math"
	"os"
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
	p, o := cloudPointPolicy(t, "function()local p={} for i=1,960 do p[i]={x=100+i,y=0,z=0,radiusYards=5,expiresAtMs=120000} end p[961]={x=2,y=0,z=0,radiusYards=5,expiresAtMs=120000};return p end")
	d := cloudPointStep(t, p, o, 100000)
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
