package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
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

func TestNativeFollowFiniteUnknownOwnershipCleanup(t *testing.T) {
	read := func(name string) string {
		b, e := os.ReadFile(filepath.Join("..", "..", "behaviors", name))
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	source := "local encounter_priors=(function()\n" + read("encounter-priors.lua") + "\nend)()\nlocal commands=(function()\n" + read("encounter-commands.lua") + `
end)()
return {on_event=function() end,export_state=function() return {version=1,data={}} end,import_state=function() return true end,decide=function(frame)
 local function scenario(kind)
  local obs={sessionId="fixture",selfGuid="4",visitId="1",mapId=349,navigation={following=true,ownerGuid="3",followRevision="1",movementOwner={active=false}},players={['4']={guid="4",present=true,aliveKnown=true,alive=true,combatKnown=true,mapKnown=true,mapId=349,positionKnown=true,movementKnown=true,moving=false},['3']={guid="3",present=true,aliveKnown=true,alive=true,combatKnown=true,mapKnown=true,mapId=349,positionKnown=true,x=5}},targets={['9']={guid="9",entry=12222,present=true,aliveKnown=true,alive=true,mapKnown=true,mapId=349,positionKnown=true,x=3}},party={group={members={{guid="3",online=true}}}},spells={['8']={known=true,metadataKnown=true,maxRange=40}}}
  assert(commands.import_state({version=1,data={requests={}}}))
  local seq=0
  local function step(now,action)
   seq=seq+1;commands.on_event({kind="encounter_observation",sequence=tostring(seq),value=obs})
   local f={session_id=obs.sessionId,input={observationSequence=tostring(seq)},clock={monotonicMs=now,unixMs=now},pending={}}
   if kind=="pending" and now>=600001 then f.pending={{action_id="99",outcome="unknown"}} end
   local row,status=commands.ranged_stance(obs,f);local out={action=action}
   commands.ranged_filter(obs,f,tostring(seq),out,row,status,nil);commands.ranged_finish(out.action)
   return out,status
  end
  local first=step(0)
  assert(first.action and first.action.mode=="acquire")
  commands.on_event({receipt={action_id="1",outcome="settled_unverified",payload={capability="movement_owner",owner="ranged:maraudon_sludge_backline",mode="acquire"}}})
  obs.targets['9'].x=170;obs.players['4'].positionEstimated=true
  local early,earlyStatus=step(100)
  local before=commands.export_state().data.rangedHold.uncertain
  if kind=="active" then obs.navigation.movementOwner={active=true,owner="foreign"}
  elseif kind=="body" then obs.sessionId="other"
  elseif kind=="visit" then obs.visitId="2"
  elseif kind=="motion" then obs.navigation.motionUnconfirmed=true
  elseif kind=="ground" then commands.export_state().data.rangedGround={operation={kind="move_body"},uncertain=true}
  end
  local action
  if kind=="friendly" then
   local h=commands.export_state().data.rangedHold;h.uncertain=nil;h.token="known-token"
   obs.navigation.movementOwner={active=true,owner=h.owner}
   obs.players['4'].positionEstimated=false
   commands.export_state().data.rangedGround={uncertain=true}
   action={capability="heal",targetGuid="3",spellId=8}
  end
  local later,laterStatus=step(600001,action)
  local remaining=commands.export_state().data.rangedHold
  return {early=early.action,earlyPhase=earlyStatus.phase,before=before,later=later.action,laterPhase=laterStatus.phase,held=remaining~=nil}
 end
 local out={}
 for _,kind in ipairs({'same','active','body','visit','motion','ground','friendly','pending'}) do out[kind]=scenario(kind) end
 return out
end}`
	h, e := behavior.NewSession(context.Background(), "finite-fixture", source)
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	d, e := h.Decide(context.Background(), map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"same", "active", "body", "visit", "motion", "ground", "friendly", "pending"} {
		row, _ := d.Output[kind].(map[string]any)
		if kind == "friendly" {
			a, _ := row["later"].(map[string]any)
			if a["capability"] != "heal" || row["laterPhase"] != "stationary_friendly_support" || row["held"] != true {
				t.Fatal(kind, row)
			}
			continue
		}
		if row["early"] != nil || row["before"] != true || row["later"] != nil {
			t.Fatal(kind, row)
		}
		if kind == "same" {
			if row["held"] != false || row["laterPhase"] != "finite_movement_hold_reconciled" {
				t.Fatal(kind, row)
			}
		} else if row["held"] != true {
			t.Fatal(kind, row)
		}
		if kind == "pending" && row["laterPhase"] != "waiting_for_native_custody" {
			t.Fatal("pending expiry falsely reported reconciliation", row)
		}
	}
}
