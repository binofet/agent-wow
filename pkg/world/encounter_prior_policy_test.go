package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
	"os"
	"path/filepath"
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

func TestEncounterPriorFiniteActivationAndCancellationScope(t *testing.T) {
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
