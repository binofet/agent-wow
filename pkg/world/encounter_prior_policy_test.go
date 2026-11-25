package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
	"strings"
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
	for _, mode := range []string{"tank", "self_defense", "party_defense", "unknown_roster", "not_in_group", "stale", "contradicted", "other_guid"} {
		t.Run(mode, func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			o := sludgePriorObservation()
			n := o.Targets[fallbackGUID(fallbackNPC)]
			n.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3, AgeMs: 1}
			expected := true
			switch mode {
			case "self_defense":
				n.AttackObservation.VictimGuid = 4
			case "party_defense", "unknown_roster", "not_in_group":
				n.AttackObservation.VictimGuid = 8
				o.Players["8"] = fallbackPlayer(8)
				o.Players["8"].MapId = proto.Uint32(349)
				o.Party.Group.Members = append(o.Party.Group.Members, &partyv1.Member{Guid: 8, Online: true})
				if mode == "unknown_roster" {
					o.Party.Group.Known = false
					expected = false
				}
				if mode == "not_in_group" {
					o.Party.Group.InGroup = false
					expected = false
				}
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

func TestEncounterPriorStaleObservationAndCancellationScope(t *testing.T) {
	read := func(name string) string {
		b, e := os.ReadFile(filepath.Join("..", "..", "behaviors", name))
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	source := "local encounter_priors=(function()\n" + read("encounter-priors.lua") + "\nend)()\nlocal commands=(function()\n" + read("encounter-commands.lua") + `
end)()
return {on_event=commands.on_event,export_state=commands.export_state,import_state=commands.import_state,decide=function(frame)
 local obs={sessionId="fixture",selfGuid="4",mapId=349,visitId="1",targets={a={guid="9",entry=12222,present=true,aliveKnown=true,alive=true}}}
 local function capture() commands.on_event({kind="encounter_observation",sequence="1",value=obs}) end
 local function active(now)
  local cfg,status=commands.ranged_stance(obs,{session_id=obs.sessionId,input={observationSequence="1"},clock={monotonicMs=now,unixMs=now}})
  return cfg~=nil
 end
 capture()
 local first=active(0)
 local checkpoint=commands.import_state(commands.export_state())
 local expired=active(600001)
 local later=active(600002)
 commands.on_event({kind="command",value={task="ranged_only",id="maraudon_sludge_backline",cancel=true}})
 local cancelled=active(600003)
 obs.visitId="2";capture();local visitTransition=active(600004)
 commands.on_event({kind="command",value={task="ranged_only",id="maraudon_sludge_backline",cancel=true}})
 local visitCancelled=active(600005)
 obs.sessionId="new-body";obs.selfGuid="5";capture();local bodyTransition=active(600006)
 return {first=first,checkpoint=checkpoint,expired=expired,later=later,cancelled=cancelled,visitTransition=visitTransition,visitCancelled=visitCancelled,bodyTransition=bodyTransition}
end}`
	host, e := behavior.NewSession(context.Background(), "prior-lifecycle", source)
	if e != nil {
		t.Fatal(e)
	}
	defer host.Close()
	d, e := host.Decide(context.Background(), map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"first", "checkpoint", "visitTransition", "bodyTransition"} {
		if d.Output[key] != true {
			t.Fatal(key, d.Output)
		}
	}
	for _, key := range []string{"expired", "later", "cancelled", "visitCancelled"} {
		if d.Output[key] != false {
			t.Fatal(key, d.Output)
		}
	}
}

func TestEncounterPriorExactExplicitSpellWinsWithoutAuthorizingAutomaticPull(t *testing.T) {
	raw, e := os.ReadFile(filepath.Join("..", "..", "behaviors", "spell-invocation.lua"))
	if e != nil {
		t.Fatal(e)
	}
	p := newFallbackPolicyTest(t, "local encounter_spell_invocation=(function()\n"+string(raw)+"\nend)()\n")
	o := sludgePriorObservation()
	settlePriorLease(t, p, o, true)
	_, e = p.host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"task": "cast_spell", "id": "owner-explicit-cast", "spellId": float64(133), "targets": map[string]any{"mask": float64(2), "objectGuid": fallbackGUID(fallbackNPC)}, "requirements": map[string]any{"stationary": false}}})
	if e != nil {
		t.Fatal(e)
	}
	out := p.decide(observeRanged(t, p, o, true)).Output
	a, _ := out["action"].(map[string]any)
	prior, _ := out["encounterPrior"].(map[string]any)
	if a["capability"] != "cast_spell" || a["spellId"] != float64(133) || prior["reason"] != "explicit_spell_request" {
		t.Fatal("explicit requested spell was mistaken for broad assist", out)
	}
}

func TestEncounterPriorReacquireUnknownRetainsSelectedLeaseHorizon(t *testing.T) {
	read := func(name string) string {
		b, e := os.ReadFile(filepath.Join("..", "..", "behaviors", name))
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	source := "local encounter_priors=(function()\n" + read("encounter-priors.lua") + "\nend)()\nlocal commands=(function()\n" + read("encounter-commands.lua") + `
end)()
return {on_event=commands.on_event,export_state=commands.export_state,import_state=commands.import_state,decide=function(frame)
 local owner="ranged:maraudon_sludge_backline"
 local obs={sessionId="fixture",selfGuid="4",mapId=349,visitId="1",navigation={movementOwner={active=false}},players={['4']={guid="4",present=true,aliveKnown=true,alive=true,mapKnown=true,mapId=349,positionKnown=true,x=0,y=0,z=0,movementKnown=true,moving=false}},targets={['9']={guid="9",entry=12222,present=true,aliveKnown=true,alive=true,mapKnown=true,mapId=349,positionKnown=true,x=22,y=0,z=0}}}
 commands.on_event({kind="encounter_observation",sequence="1",value=obs})
 local function step(now,select)
  local f={session_id="fixture",input={observationSequence="1"},pending={},clock={monotonicMs=now,unixMs=now}}
  local row,status=commands.ranged_stance(obs,f);local out={}
  commands.ranged_filter(obs,f,"1",out,row,status,nil)
  commands.ranged_finish(select and out.action or nil)
  return out,status
 end
 step(0,true)
 commands.on_event({receipt={action_id="1",outcome="confirmed",payload={capability="movement_owner",mode="acquire",owner=owner,result={token="old-token",state={sessionId="fixture",selfGuid="4",movementOwner={active=true,owner=owner}}}}}})
 obs.navigation.movementOwner={active=true,owner=owner}
 commands.on_event({kind="command",value={task="ranged_only",id="maraudon_sludge_backline",creatureEntry=12222,mapId=349,visitId="1",minRangeYards=14,maxRangeYards=36,clearanceYards=2,durationMs=1200000}})
 step(1,true)
 obs.navigation.movementOwner={active=false}
 local unselected=step(600001,false)
 local oldHorizon=commands.export_state().data.rangedHold.untilUnixMs
 local reacquire=step(600002,true)
 local newHorizon=commands.export_state().data.rangedHold.untilUnixMs
 commands.on_event({receipt={action_id="2",outcome="unknown",payload={capability="movement_owner",mode="acquire",owner=owner}}})
 local later,status=step(600003,true)
 return {oldHorizon=oldHorizon,newHorizon=newHorizon,unselected=unselected.action,reacquire=reacquire.action,later=later.action,phase=status.phase}
end}`
	host, e := behavior.NewSession(context.Background(), "prior-renewal", source)
	if e != nil {
		t.Fatal(e)
	}
	defer host.Close()
	d, e := host.Decide(context.Background(), map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	if d.Output["oldHorizon"] != float64(600000) || d.Output["newHorizon"] != float64(1200001) || d.Output["later"] != nil || d.Output["phase"] != "movement_hold_unknown" {
		t.Fatal("new selected lease reused an old UNKNOWN horizon", d.Output)
	}
	for _, key := range []string{"unselected", "reacquire"} {
		a, _ := d.Output[key].(map[string]any)
		if a["capability"] != "movement_owner" || a["mode"] != "acquire" {
			t.Fatal(key, d.Output)
		}
	}
}

func TestStandingPriorFreshObservationsTuningAndExplicitOverlay(t *testing.T) {
	read := func(name string) string {
		b, e := os.ReadFile(filepath.Join("..", "..", "behaviors", name))
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	source := "local encounter_priors=(function()\n" + read("encounter-priors.lua") + "\nend)()\nlocal commands=(function()\n" + read("encounter-commands.lua") + `
end)()
return {on_event=commands.on_event,export_state=commands.export_state,import_state=commands.import_state,decide=function(frame)
 local obs={sessionId="fixture",selfGuid="4",mapId=349,visitId="1",targets={a={guid="9",entry=12222,present=true,aliveKnown=true,alive=true}}}
 local seq=0
 local function active(now,fresh)
  if fresh then seq=seq+1;commands.on_event({kind="encounter_observation",sequence=tostring(seq),value=obs}) end
  local row,status=commands.ranged_stance(obs,{session_id=obs.sessionId,input={observationSequence=tostring(seq)},clock={monotonicMs=now,unixMs=now}})
  return row,status
 end
 local first,status=active(0,true)
 local stale=active(600001,false)
 local standing=active(600002,true)
 encounter_priors.rules[1].minRangeYards=20
 local tuned=active(600003,true)
 commands.on_event({kind="command",value={task="ranged_only",id="user-finite",creatureEntry=12222,mapId=349,visitId="1",minRangeYards=8,maxRangeYards=30,clearanceYards=1,durationMs=100}})
 local explicit=active(600004,true)
 encounter_priors.rules[1].minRangeYards=22
 local unchanged=active(600050,true)
 local resumed,resumedStatus=active(600105,true)
 commands.on_event({kind="command",value={task="ranged_only",id="maraudon_sludge_backline",cancel=true}})
 local cancelled=active(600106,true)
 local stillcancelled=active(1200107,true)
 obs.visitId="2";local nextVisit=active(1200108,true)
 commands.on_event({kind="command",value={task="ranged_only",id="maraudon_sludge_backline",cancel=true}})
 active(1200109,true)
 obs.selfGuid="5";obs.sessionId="new-body";local nextBody=active(1200110,true)
 return {first=first~=nil,stale=stale~=nil,standing=standing~=nil,tuned=tuned.minRangeYards,
  explicit=explicit.origin,explicitMin=explicit.minRangeYards,explicitDeadline=explicit.expiresUnixMs,
  unchangedMin=unchanged.minRangeYards,unchangedDeadline=unchanged.expiresUnixMs,
  resumed=resumed.origin,resumedMin=resumed.minRangeYards,standingStatus=resumedStatus.standing,
  cancelled=cancelled~=nil,stillcancelled=stillcancelled~=nil,nextVisit=nextVisit~=nil,nextBody=nextBody~=nil,
  checkpoint=commands.import_state(commands.export_state())}
end}`
	host, e := behavior.NewSession(context.Background(), "standing-prior", source)
	if e != nil {
		t.Fatal(e)
	}
	defer host.Close()
	d, e := host.Decide(context.Background(), map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"first", "standing", "standingStatus", "nextVisit", "nextBody", "checkpoint"} {
		if d.Output[key] != true {
			t.Fatal(key, d.Output)
		}
	}
	for _, key := range []string{"stale", "cancelled", "stillcancelled"} {
		if d.Output[key] != false {
			t.Fatal(key, d.Output)
		}
	}
	if d.Output["tuned"] != float64(20) || d.Output["explicit"] != "explicit" || d.Output["explicitMin"] != float64(8) || d.Output["unchangedMin"] != float64(8) || d.Output["explicitDeadline"] != float64(600104) || d.Output["unchangedDeadline"] != float64(600104) || d.Output["resumed"] != "learned_prior" || d.Output["resumedMin"] != float64(22) {
		t.Fatal(d.Output)
	}
}

func TestStandingPriorRefreshCannotMoveCommittedUnknownLeaseHorizon(t *testing.T) {
	read := func(name string) string {
		b, e := os.ReadFile(filepath.Join("..", "..", "behaviors", name))
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	source := "local encounter_priors=(function()\n" + read("encounter-priors.lua") + "\nend)()\nlocal commands=(function()\n" + read("encounter-commands.lua") + `
end)()
return {on_event=commands.on_event,export_state=commands.export_state,import_state=commands.import_state,decide=function(frame)
 local owner="ranged:maraudon_sludge_backline"
 local obs={sessionId="fixture",selfGuid="4",mapId=349,visitId="1",navigation={movementOwner={active=false}},players={['4']={guid="4",present=true,aliveKnown=true,alive=true,mapKnown=true,mapId=349,positionKnown=true,x=0,y=0,z=0,movementKnown=true,moving=false}},targets={['9']={guid="9",entry=12222,present=true,aliveKnown=true,alive=true,mapKnown=true,mapId=349,positionKnown=true,x=22,y=0,z=0}}}
 local seq=0
 local function step(now,select)
  seq=seq+1;commands.on_event({kind="encounter_observation",sequence=tostring(seq),value=obs})
  local f={session_id=obs.sessionId,input={observationSequence=tostring(seq)},pending={},clock={monotonicMs=now,unixMs=now}}
  local row,status=commands.ranged_stance(obs,f);local out={}
  commands.ranged_filter(obs,f,tostring(seq),out,row,status,nil)
  commands.ranged_finish(select and out.action or nil)
  return out,status,row
 end
 step(0,true)
 commands.on_event({receipt={action_id="1",outcome="unknown",payload={capability="movement_owner",mode="acquire",owner=owner}}})
 local held,status=step(100,true)
 local horizon=commands.export_state().data.rangedHold.untilUnixMs
 local eligible=step(600001,false)
 local unselected=commands.export_state().data.rangedHold.untilUnixMs
 local selected=step(600002,true)
 local freshHorizon=commands.export_state().data.rangedHold.untilUnixMs
 commands.on_event({receipt={action_id="2",outcome="unknown",payload={capability="movement_owner",mode="acquire",owner=owner}}})
 local later,laterStatus=step(600003,true)
 return {held=held.action,phase=status.phase,horizon=horizon,eligible=eligible.action,unselected=unselected,
 selected=selected.action,freshHorizon=freshHorizon,later=later.action,laterPhase=laterStatus.phase}
end}`
	host, e := behavior.NewSession(context.Background(), "standing-unknown", source)
	if e != nil {
		t.Fatal(e)
	}
	defer host.Close()
	d, e := host.Decide(context.Background(), map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	if d.Output["held"] != nil || d.Output["phase"] != "movement_hold_unknown" || d.Output["horizon"] != float64(600000) || d.Output["unselected"] != float64(600000) || d.Output["freshHorizon"] != float64(1200002) || d.Output["later"] != nil || d.Output["laterPhase"] != "movement_hold_unknown" {
		t.Fatal(d.Output)
	}
	for _, key := range []string{"eligible", "selected"} {
		a, _ := d.Output[key].(map[string]any)
		if a["mode"] != "acquire" || a["leaseMs"] != float64(600000) {
			t.Fatal(key, d.Output)
		}
	}
}

func TestStandingPriorAcceptedTuningReloadAndLegacyFiniteMigration(t *testing.T) {
	read := func(name string) string {
		b, e := os.ReadFile(filepath.Join("..", "..", "behaviors", name))
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	source := "local encounter_priors=(function()\n" + read("encounter-priors.lua") + "\nend)()\nlocal commands=(function()\n" + read("encounter-commands.lua") + `
end)()
return {on_event=commands.on_event,export_state=commands.export_state,import_state=commands.import_state,decide=function(frame)
 local obs={sessionId=frame.session_id,selfGuid="4",mapId=349,visitId="1",targets={a={guid="9",entry=12222,present=true,aliveKnown=true,alive=true}}}
 if frame.input.legacy then
  assert(commands.import_state({version=1,data={requests={},rangedPrior={id="maraudon_sludge_backline",sessionId=obs.sessionId,selfGuid="4",mapId=349,visitId="1"},rangedOnly={id="maraudon_sludge_backline",sessionId=obs.sessionId,selfGuid="4",mapId=349,visitId="1",creatureEntry=12222,minRangeYards=9,maxRangeYards=36,clearanceYards=2,durationMs=1000,expiresAt=1000,expiresUnixMs=1000}}}))
 end
 commands.on_event({kind="encounter_observation",sequence=frame.input.sequence,value=obs})
 local row,status=commands.ranged_stance(obs,{session_id=frame.session_id,input={observationSequence=frame.input.sequence},clock={monotonicMs=frame.input.now,unixMs=frame.input.now}})
 return {row=row,status=status}
end}`
	host, e := behavior.NewSession(context.Background(), "tuning-reload", source)
	if e != nil {
		t.Fatal(e)
	}
	defer host.Close()
	d, e := host.Decide(context.Background(), map[string]any{"now": float64(0), "sequence": "1"})
	if e != nil {
		t.Fatal(e)
	}
	row, _ := d.Output["row"].(map[string]any)
	if row["minRangeYards"] != float64(14) {
		t.Fatal(d.Output)
	}
	changed := strings.Replace(source, `tankGuid="3", minRangeYards=14`, `tankGuid="3", minRangeYards=20`, 1)
	if changed == source {
		t.Fatal("editable prior tuning not found")
	}
	if _, e = host.Reload(context.Background(), changed, host.Status().Revision); e != nil {
		t.Fatal(e)
	}
	d, e = host.Decide(context.Background(), map[string]any{"now": float64(1), "sequence": "2"})
	if e != nil {
		t.Fatal(e)
	}
	row, _ = d.Output["row"].(map[string]any)
	if row["minRangeYards"] != float64(20) || row["origin"] != "learned_prior" {
		t.Fatal("accepted source tuning did not affect active default", d.Output)
	}
	d, e = host.Decide(context.Background(), map[string]any{"now": float64(500), "sequence": "3", "legacy": true})
	if e != nil {
		t.Fatal(e)
	}
	row, _ = d.Output["row"].(map[string]any)
	status, _ := d.Output["status"].(map[string]any)
	if row["minRangeYards"] != float64(9) || row["expiresUnixMs"] != float64(1000) || status["origin"] != "legacy_finite_overlay" {
		t.Fatal("ambiguous legacy row was retuned or extended", d.Output)
	}
	d, e = host.Decide(context.Background(), map[string]any{"now": float64(1001), "sequence": "4"})
	if e != nil {
		t.Fatal(e)
	}
	row, _ = d.Output["row"].(map[string]any)
	if row["minRangeYards"] != float64(20) || row["origin"] != "learned_prior" {
		t.Fatal("standing default did not resume after legacy overlay", d.Output)
	}
}

func TestStandingPriorRealOldNativeEntryRetreatsAfterTenMinuteBoundary(t *testing.T) {
	source := strings.Replace(readEncounterBundle(t), "local function decide(frame)", "local function decide(frame)\nframe.clock={monotonicMs=frame.input.fixtureClock or 0,unixMs=frame.input.fixtureClock or 0}", 1)
	host, e := behavior.NewSession(context.Background(), fallbackSession, source)
	if e != nil {
		t.Fatal(e)
	}
	defer host.Close()
	p := &fallbackPolicyTest{t: t, host: host}
	o := sludgePriorObservation()
	o.Navigation.BodyMovementSupported = true
	for i := uint64(1); i < 5; i++ {
		n := proto.Clone(o.Targets[fallbackGUID(fallbackNPC)]).(*actionsv1.EncounterCreature)
		n.Guid = fallbackNPC + i
		n.X = 22 + float32(i)
		o.Targets[fallbackGUID(n.Guid)] = n
	}
	settlePriorLease(t, p, o, false)
	for _, n := range o.Targets {
		n.X = 3 + float32(n.Guid-fallbackNPC)*0.25
		n.Y = float32(n.Guid-fallbackNPC) * 0.1
	}
	o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: false}
	seq := observeRanged(t, p, o, false)
	d, e := p.host.Decide(context.Background(), map[string]any{"kind": "encounter", "observationSequence": seq, "fixtureClock": float64(600001)})
	if e != nil {
		t.Fatal(e)
	}
	a, _ := d.Output["action"].(map[string]any)
	if a["capability"] != "movement_owner" || a["mode"] != "acquire" || a["leaseMs"] != float64(600000) {
		t.Fatal("fresh old-host default did not acquire a bounded new lease", d.Output)
	}
	id, e := p.host.ReserveAction(d)
	if e != nil {
		t.Fatal(e)
	}
	owner := "ranged:maraudon_sludge_backline"
	o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: owner}
	_, e = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": owner, "result": map[string]any{"token": "renewed-test-token", "state": map[string]any{"sessionId": fallbackSession, "selfGuid": "4", "movementOwner": map[string]any{"active": true, "owner": owner}}}}}})
	if e != nil {
		t.Fatal(e)
	}
	seq = observeRanged(t, p, o, false)
	d, e = p.host.Decide(context.Background(), map[string]any{"kind": "encounter", "observationSequence": seq, "fixtureClock": float64(600002)})
	if e != nil {
		t.Fatal(e)
	}
	out := d.Output
	a, _ = out["action"].(map[string]any)
	status, _ := out["rangedStance"].(map[string]any)
	if a["capability"] != "move_body" || status["phase"] != "withdrawing_ground_input" || status["standing"] != true || status["matchedTargets"] != float64(5) {
		t.Fatal("old-host backline lost standing withdrawal at the pull boundary", out)
	}
	// A real source reload can retune the automatic rule, but cannot release
	// an actual Session UNKNOWN action even after an old lease horizon passes.
	id, e = p.host.ReserveAction(d)
	if e != nil {
		t.Fatal(e)
	}
	_, e = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionUnknown,
		Payload: map[string]any{"capability": "move_body", "self_guid": "4", "visit_id": "1", "sequence_id": a["sequenceId"]}}})
	if e != nil {
		t.Fatal(e)
	}
	changed := strings.Replace(source, `tankGuid="3", minRangeYards=14`, `tankGuid="3", minRangeYards=20`, 1)
	if changed == source {
		t.Fatal("prior source tuning not found")
	}
	if _, e = p.host.Reload(context.Background(), changed, p.host.Status().Revision); e != nil {
		t.Fatal(e)
	}
	seq = observeRanged(t, p, o, false)
	d, e = p.host.Decide(context.Background(), map[string]any{"kind": "encounter", "observationSequence": seq, "fixtureClock": float64(1200003)})
	if e != nil {
		t.Fatal(e)
	}
	status, _ = d.Output["rangedStance"].(map[string]any)
	pending := p.host.Outstanding()
	if d.Output["action"] != nil || status["minRangeYards"] != float64(20) || len(pending) != 1 || pending[0].ActionID != id || pending[0].Outcome != behavior.ActionUnknown {
		t.Fatal("source tuning or standing refresh changed retained UNKNOWN custody", d.Output, pending)
	}
}

func TestStandingPriorYieldsToDistantRallyAndPausesForNearbyHazard(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed_resume", true: "unknown_hold"}[unknown], func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			o := sludgePriorObservation()
			o.Navigation.BodyMovementSupported = true
			self := o.Players["4"]
			self.CombatKnown = true
			self.X, self.Y, self.Z = 0, 0, 0
			o.Players["3"].X = 100
			o.Players["87"] = fallbackPlayer(87)
			guide := o.Players["87"]
			guide.MapId = proto.Uint32(349)
			guide.PositionKnown = true
			guide.MovementKnown = true
			guide.X, guide.Y, guide.Z = 54, 0, 0
			npc := o.Targets[fallbackGUID(fallbackNPC)]
			npc.X, npc.Y, npc.Z = 80, 0, 0
			// A previously confirmed default lease must be released, never discarded.
			settlePriorLease(t, p, o, false)
			waitTaskCommand(t, p, map[string]any{"task": "rally_to", "id": "travel", "guideGuid": "87", "deadlineMonotonicMs": 999999999999})
			d := p.decide(observeRanged(t, p, o, false))
			a, _ := d.Output["action"].(map[string]any)
			if a["mode"] != "release" || a["owner"] != "ranged:maraudon_sludge_backline" {
				t.Fatal("remote harmless pack starved explicit rally", d.Output)
			}
			releaseID, err := p.host.ReserveAction(d)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: releaseID, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "movement_owner", "mode": "release", "owner": "ranged:maraudon_sludge_backline", "result": map[string]any{"sessionId": fallbackSession, "selfGuid": "4"}}}})
			if err != nil {
				t.Fatal(err)
			}
			o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
			d = p.decide(observeRanged(t, p, o, false))
			a, _ = d.Output["action"].(map[string]any)
			if a["mode"] != "acquire" || a["owner"] != "task:travel" {
				t.Fatal("rally did not acquire after exact release", d.Output)
			}
			settleWaitOwnership(t, p, d, behavior.ActionConfirmed, "acquire", "task:travel", "rally-token")
			o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "task:travel", RemainingMs: 3000}
			d = p.decide(observeRanged(t, p, o, false))
			a, _ = d.Output["action"].(map[string]any)
			if a["capability"] != "approach_target" || a["targetGuid"] != "87" {
				t.Fatal("rally failed at observed 54-yard guide gap", d.Output)
			}
			// Still OOC, but the hazard is now genuinely close. Preserve the rally order
			// and settle its exact token before any default withdrawal can take custody.
			npc.X = 3
			self.Moving = true
			o.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: 87, VisitId: 1}
			d = p.decide(observeRanged(t, p, o, false))
			a, _ = d.Output["action"].(map[string]any)
			if a["capability"] != "stop_movement" || a["targetGuid"] != "87" {
				t.Fatal("close hazard did not stop own entered rally motion", d.Output)
			}
			stopID, stopErr := p.host.ReserveAction(d)
			if stopErr != nil {
				t.Fatal(stopErr)
			}
			_, stopErr = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: stopID, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "stop_movement"}}})
			if stopErr != nil {
				t.Fatal(stopErr)
			}
			self.Moving = false
			d = p.decide(observeRanged(t, p, o, false))
			a, _ = d.Output["action"].(map[string]any)
			if a["mode"] != "release" || a["owner"] != "task:travel" {
				t.Fatal("near hazard did not pause and release stopped rally", d.Output)
			}
			settleWaitOwnership(t, p, d, behavior.ActionConfirmed, "release", "task:travel", "")
			o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
			d = p.decide(observeRanged(t, p, o, false))
			a, _ = d.Output["action"].(map[string]any)
			if a["mode"] != "acquire" || a["owner"] != "ranged:maraudon_sludge_backline" {
				t.Fatal("close danger lost withdrawal custody", d.Output)
			}
			if !unknown {
				id, err := p.host.ReserveAction(d)
				if err != nil {
					t.Fatal(err)
				}
				owner := "ranged:maraudon_sludge_backline"
				_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "movement_owner", "mode": "acquire", "owner": owner, "result": map[string]any{"token": "new-prior", "state": map[string]any{"sessionId": fallbackSession, "selfGuid": "4", "movementOwner": map[string]any{"active": true, "owner": owner}}}}}})
				if err != nil {
					t.Fatal(err)
				}
				o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: owner}
				d = p.decide(observeRanged(t, p, o, false))
				a, _ = d.Output["action"].(map[string]any)
				if a["capability"] != "move_body" {
					t.Fatal("paused rally blocked proven close-hazard withdrawal", d.Output)
				}
				moveID, moveErr := p.host.ReserveAction(d)
				if moveErr != nil {
					t.Fatal(moveErr)
				}
				_, moveErr = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: moveID, Outcome: behavior.ActionNotDispatched, Payload: map[string]any{"capability": "move_body"}}})
				if moveErr != nil {
					t.Fatal(moveErr)
				}
				npc.X = 80
				d = p.decide(observeRanged(t, p, o, false))
				a, _ = d.Output["action"].(map[string]any)
				if a["mode"] != "release" || a["owner"] != owner {
					t.Fatal("safe return failed to yield default lease", d.Output)
				}
				id, err = p.host.ReserveAction(d)
				if err != nil {
					t.Fatal(err)
				}
				_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionConfirmed, Payload: map[string]any{"capability": "movement_owner", "mode": "release", "owner": owner, "result": map[string]any{"sessionId": fallbackSession, "selfGuid": "4"}}}})
				if err != nil {
					t.Fatal(err)
				}
				o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
				d = p.decide(observeRanged(t, p, o, false))
				a, _ = d.Output["action"].(map[string]any)
				task, _ := d.Output["task"].(map[string]any)
				if a["mode"] != "acquire" || a["owner"] != "task:travel" || task["deadlineMonotonicMs"] != float64(999999999999) {
					t.Fatal("paused rally failed to resume unchanged request/deadline", d.Output)
				}
				return
			}
			// An entered unknown acquisition is never bypassed by a later safe sample.
			settleWaitOwnership(t, p, d, behavior.ActionUnknown, "acquire", "ranged:maraudon_sludge_backline", "")
			npc.X = 80
			d = p.decide(observeRanged(t, p, o, false))
			if d.Output["action"] != nil {
				t.Fatal("safe travel replayed unresolved prior acquire", d.Output)
			}
			task, _ := d.Output["task"].(map[string]any)
			if task["id"] != "travel" {
				t.Fatal("pause discarded explicit rally", d.Output)
			}
		})
	}
}

func TestStandingPriorRallyExceptionRequiresHarmlessFactsAndAutomaticOrigin(t *testing.T) {
	for _, condition := range []string{"unknown_combat", "unknown_hazard", "engaged_hazard", "explicit_finite"} {
		t.Run(condition, func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			o := sludgePriorObservation()
			self := o.Players["4"]
			self.CombatKnown = true
			self.X, self.Y, self.Z = 0, 0, 0
			o.Players["3"].X = 100
			o.Players["87"] = fallbackPlayer(87)
			guide := o.Players["87"]
			guide.MapId = proto.Uint32(349)
			guide.PositionKnown = true
			guide.MovementKnown = true
			guide.X, guide.Y, guide.Z = 54, 0, 0
			npc := o.Targets[fallbackGUID(fallbackNPC)]
			npc.X, npc.Y, npc.Z = 80, 0, 0
			settlePriorLease(t, p, o, false)
			switch condition {
			case "unknown_combat":
				self.CombatKnown = false
			case "unknown_hazard":
				npc.PositionKnown = false
			case "engaged_hazard":
				npc.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3}
			case "explicit_finite":
				waitTaskCommand(t, p, map[string]any{"task": "ranged_only", "id": "maraudon_sludge_backline", "creatureEntry": 12222, "mapId": 349, "visitId": "1", "minRangeYards": 14, "maxRangeYards": 36, "clearanceYards": 2, "durationMs": 600000})
			}
			waitTaskCommand(t, p, map[string]any{"task": "rally_to", "id": "travel", "guideGuid": "87", "deadlineMonotonicMs": 999999999999})
			out := p.decide(observeRanged(t, p, o, false)).Output
			a, _ := out["action"].(map[string]any)
			if a["mode"] == "release" && a["owner"] == "ranged:maraudon_sludge_backline" || a["owner"] == "task:travel" || a["targetGuid"] == "87" {
				t.Fatal("unproven harmlessness or explicit finite order incorrectly yielded", out)
			}
		})
	}
}
