package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
	"os"
	"testing"
)

func passivePriorPolicy(t *testing.T, prefix string) *fallbackPolicyTest {
	t.Helper()
	path := os.Getenv("BUDDY_TEST_ENCOUNTER_BUNDLE")
	if path == "" {
		return newFallbackPolicyTest(t, prefix)
	}
	source, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	h, e := behavior.NewSession(context.Background(), fallbackSession, string(source))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(h.Close)
	return &fallbackPolicyTest{t: t, host: h}
}

func TestPassiveDistantPriorDoesNotAcquireButNearOrAttackProtects(t *testing.T) {
	for _, kind := range []string{"distant", "estimated_walking", "near", "attack", "stale_self", "unknown_target", "explicit"} {
		t.Run(kind, func(t *testing.T) {
			p := passivePriorPolicy(t, "")
			o := sludgePriorObservation()
			o.Spells = map[string]*actionsv1.EncounterSpell{}
			self := o.Players["4"]
			self.CombatKnown = true
			self.PositionAgeKnown = true
			self.PositionAgeMs = 0
			self.X, self.Y, self.Z = 0, 0, 0
			o.Navigation.Following = true
			o.Navigation.OwnerGuid = 3
			o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
			n := o.Targets[fallbackGUID(fallbackNPC)]
			n.X, n.Y, n.Z = 170, 0, 0
			switch kind {
			case "estimated_walking":
				self.PositionEstimated = true
				self.Moving = true
			case "near":
				n.X = 15
			case "attack":
				n.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3}
			case "stale_self":
				self.PositionAgeMs = 5001
			case "unknown_target":
				n.PositionKnown = false
			case "explicit":
				waitTaskCommand(t, p, map[string]any{"task": "ranged_only", "id": "hold", "creatureEntry": 12222, "mapId": 349, "visitId": "1", "minRangeYards": 14, "maxRangeYards": 36, "clearanceYards": 2, "durationMs": 600000})
			}
			out := p.decide(observeRanged(t, p, o, false)).Output
			a, _ := out["action"].(map[string]any)
			if kind == "distant" || kind == "estimated_walking" {
				if out["action"] != nil {
					t.Fatal("passive sighting seized movement", out)
				}
			} else if a["capability"] != "movement_owner" || a["mode"] != "acquire" {
				t.Fatal("danger/unknown/explicit lost existing protection", out)
			}
		})
	}
}

func TestPassiveDistantPriorKeepsActualDirectHealWithFollow(t *testing.T) {
	p := passivePriorPolicy(t, "")
	o := positionedHealObservation()
	o.MapId = proto.Uint32(349)
	for _, v := range o.Players {
		v.MapId = proto.Uint32(349)
		v.CombatKnown = true
		v.InCombat = false
	}
	n := proto.Clone(sludgePriorObservation().Targets[fallbackGUID(fallbackNPC)]).(*actionsv1.EncounterCreature)
	o.Targets[fallbackGUID(fallbackNPC)] = n
	n.Entry = 12222
	n.MapId = proto.Uint32(349)
	n.X = o.Players["4"].X + 170
	n.Y = o.Players["4"].Y
	n.Z = o.Players["4"].Z
	n.Engaged = false
	n.AttackObservation = &actionsv1.EncounterAttackObservation{}
	o.Navigation = &actionsv1.NavigationObservation{Following: true, OwnerGuid: 3, MovementOwner: &actionsv1.MovementOwnerState{}}
	out := p.decide(observeRanged(t, p, o, false)).Output
	a, _ := out["action"].(map[string]any)
	if a["capability"] != "heal" || a["targetGuid"] != "8" {
		t.Fatal("remote visible mob starved real friendly care", out)
	}
}

func TestPassiveDistantPriorKeepsActualEquipmentProposal(t *testing.T) {
	gear, e := os.ReadFile("../../behaviors/gear.lua")
	if e != nil {
		t.Fatal(e)
	}
	auto, e := os.ReadFile("../../behaviors/auto-equipment.lua")
	if e != nil {
		t.Fatal(e)
	}
	p := passivePriorPolicy(t, "local encounter_gear=(function()\n"+string(gear)+"\nend)()\nlocal encounter_auto_equipment=(function()\n"+string(auto)+"\nend)()\n")
	o := sludgePriorObservation()
	o.Spells = map[string]*actionsv1.EncounterSpell{}
	o.Players["4"].CombatKnown = true
	o.Navigation.Following = true
	o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
	o.Targets[fallbackGUID(fallbackNPC)].X = 170
	inventory := map[string]any{"sessionId": fallbackSession, "selfGuid": "4", "revision": "1", "inventoryKnown": true, "inventoryRevision": "2", "equipmentKnown": true, "equipmentRevision": "3", "equipment": []any{map[string]any{"slot": 6, "slotKnown": true, "enhancementsKnown": true}}, "gearContext": map[string]any{"templates": map[string]any{"100": map[string]any{"itemId": 100, "available": true, "armorComparisonKnown": true, "itemClass": 4, "inventoryType": 7}}}, "items": []any{map[string]any{"metadataKnown": true, "item": map[string]any{"itemGuid": "4611686018427387905", "entry": 100, "slot": 2, "stackCount": 1}, "gearFacts": map[string]any{"enhancementsKnown": true, "destinations": []any{map[string]any{"eligible": true, "equipmentSlot": 6}}}}}}
	if _, e = p.host.Enqueue(behavior.Event{Kind: "inventory_observation", Value: inventory}); e != nil {
		t.Fatal(e)
	}
	out := p.decide(observeRanged(t, p, o, false)).Output
	a, _ := out["action"].(map[string]any)
	if a["capability"] != "equip_gear" {
		t.Fatal("passive sighting starved actual gear choice", out)
	}
}

func TestPassiveDistantPriorPreservesFormationButNotOffensiveAdmission(t *testing.T) {
	b, e := os.ReadFile("../../behaviors/encounter-priors.lua")
	if e != nil {
		t.Fatal(e)
	}
	// Exercise the actual module with exact sentinel proposals: no fake movement
	// receipt or realm effect is created by a read-only admission check.
	source := "local prior=(function()\n" + string(b) + "\nend)()\n" + `return {on_event=function()end,export_state=function()return{version=1,data={}}end,import_state=function()return true end,decide=function()
 local obs={mapId=349,selfGuid="4",players={['4']={present=true,aliveKnown=true,alive=true,combatKnown=true,mapKnown=true,mapId=349,positionKnown=true}},targets={['9']={guid="9",entry=12222,present=true,aliveKnown=true,alive=true,mapKnown=true,mapId=349,positionKnown=true,x=170,attackObservation={}}}}
 local out={followFormation={witness="original"},portalCatchup={witness="original"},action={capability="offense",targetGuid="9"}}
 prior.filter(obs,out,nil)
 local far=out.followFormation~=nil and out.portalCatchup~=nil and out.action==nil
 obs.targets['9'].x=3
 out={followFormation={witness="original"},portalCatchup={witness="original"}}
 prior.filter(obs,out,nil)
 return{far=far,near=out.followFormation==nil and out.portalCatchup==nil}
end}`
	h, e := behavior.NewSession(context.Background(), "passive-filter", source)
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	d, e := h.Decide(context.Background(), map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	if d.Output["far"] != true || d.Output["near"] != true {
		t.Fatal(d.Output)
	}
}

func TestPassivePriorEstimatedNearHazardUsesOnlyNativeRangeGuard(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "old_abi_holds", true: "guarded_band_withdraws"}[modern], func(t *testing.T) {
			p := passivePriorPolicy(t, "")
			o := sludgePriorObservation()
			o.Spells = map[string]*actionsv1.EncounterSpell{}
			self := o.Players["4"]
			self.CombatKnown = true
			self.PositionEstimated = true
			self.Moving = false
			self.X, self.Y, self.Z = 0, 0, 0
			o.Targets[fallbackGUID(fallbackNPC)].X = 3
			o.Navigation.Following = true
			o.Navigation.OwnerGuid = 3
			o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
			d := p.decide(observeRanged(t, p, o, modern))
			a, _ := d.Output["action"].(map[string]any)
			if a["mode"] != "acquire" {
				t.Fatal(d.Output)
			}
			settleFollowPrior(t, p, d, "acquire", behavior.ActionConfirmed)
			o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "ranged:maraudon_sludge_backline"}
			out := p.decide(observeRanged(t, p, o, modern)).Output
			a, _ = out["action"].(map[string]any)
			if modern {
				if a["capability"] != "approach_target" || a["minRangeYards"] != float64(16) || a["clearanceYards"] != float64(16) || a["visitId"] != "1" {
					t.Fatal("typed native guarded withdrawal suppressed", out)
				}
			} else if out["action"] != nil {
				t.Fatal("old ABI invented safe estimated withdrawal", out)
			}
		})
	}
}

func TestPassivePriorCleansHistoricalOwnLeaseBeforeWalkingOnlyWithRemoteProof(t *testing.T) {
	for _, kind := range []string{"remote", "estimated", "stale", "near", "changed_visit", "foreign", "unknown_target"} {
		t.Run(kind, func(t *testing.T) {
			p, o, _ := nativeFollowPrior(t) // Actual near acquisition, confirmed token; current pack is distant.
			o.Navigation.Following = false
			switch kind {
			case "estimated":
				o.Players["4"].PositionEstimated = true
			case "stale":
				o.Players["4"].PositionAgeKnown = true
				o.Players["4"].PositionAgeMs = 5001
			case "near":
				o.Targets[fallbackGUID(fallbackNPC)].X = 3
			case "changed_visit":
				o.VisitId = 2
			case "foreign":
				o.Navigation.MovementOwner.Owner = "other-task"
			case "unknown_target":
				o.Targets[fallbackGUID(fallbackNPC)].PositionKnown = false
			}
			out := p.decide(observeRanged(t, p, o, false)).Output
			a, _ := out["action"].(map[string]any)
			if kind == "remote" {
				if a["mode"] != "release" || a["token"] != "prior-fixture" {
					t.Fatal("historical optional lease still freezes ordinary gameplay", out)
				}
			} else if a["mode"] == "release" {
				t.Fatal("cleanup waived stale/near/scope/custody guard", out)
			}
		})
	}
}

func TestPassivePriorDoesNotBorrowOldVictimsDifferentFight(t *testing.T) {
	for _, kind := range []string{"stale", "contradicted", "victim_unknown"} {
		t.Run(kind, func(t *testing.T) {
			p := passivePriorPolicy(t, "")
			o := sludgePriorObservation()
			o.Spells = map[string]*actionsv1.EncounterSpell{}
			self := o.Players["4"]
			self.CombatKnown = true
			self.X, self.Y, self.Z = 0, 0, 0
			o.Navigation.Following = true
			o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{}
			victim := o.Players["3"]
			victim.CombatKnown = true
			victim.InCombat = true
			n := o.Targets[fallbackGUID(fallbackNPC)]
			n.X, n.Y, n.Z = 170, 0, 0
			n.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3, AgeMs: 6001}
			if kind == "contradicted" {
				n.AttackObservation.AgeMs = 0
				n.AttackObservation.TargetKnown = true
				n.AttackObservation.TargetGuid = 8
			}
			if kind == "victim_unknown" {
				n.AttackObservation.AgeMs = 0
				n.AttackObservation.VictimKnown = false
			}
			other := proto.Clone(n).(*actionsv1.EncounterCreature)
			other.Guid = fallbackNPC + 10
			other.Entry = 777
			other.X = 30
			other.AttackObservation = &actionsv1.EncounterAttackObservation{VictimKnown: true, VictimGuid: 3, TargetKnown: true, TargetGuid: 3}
			o.Targets[fallbackGUID(other.Guid)] = other
			out := p.decide(observeRanged(t, p, o, false)).Output
			a, _ := out["action"].(map[string]any)
			status, _ := out["rangedStance"].(map[string]any)
			if a["capability"] == "movement_owner" || status["phase"] != "passive_distant_prior" || status["active"] != false {
				t.Fatal("old or contradictory victim borrowed unrelated combat", out)
			}
		})
	}
}

func TestPassivePriorRetainsStationaryCareAfterCorrelatedGroundSettlement(t *testing.T) {
	p := passivePriorPolicy(t, "")
	o := positionedHealObservation()
	o.MapId = proto.Uint32(349)
	for _, v := range o.Players {
		v.MapId = proto.Uint32(349)
		v.CombatKnown = true
		v.InCombat = false
		v.HealthKnown = true
		v.Health = 100
		v.MaxHealth = 100
	}
	o.Healing.Patients[0].Health = 100
	n := proto.Clone(sludgePriorObservation().Targets[fallbackGUID(fallbackNPC)]).(*actionsv1.EncounterCreature)
	o.Targets[fallbackGUID(fallbackNPC)] = n
	self := o.Players["4"]
	n.X, n.Y, n.Z = self.X+3, self.Y, self.Z
	o.Navigation = &actionsv1.NavigationObservation{Following: false, BodyMovementSupported: true, MovementOwner: &actionsv1.MovementOwnerState{}}
	d := p.decide(observeRanged(t, p, o, false))
	a, _ := d.Output["action"].(map[string]any)
	if a["mode"] != "acquire" {
		t.Fatal("fixture did not acquire actual near-hazard lease", d.Output)
	}
	settleFollowPrior(t, p, d, "acquire", behavior.ActionConfirmed)
	o.Navigation.MovementOwner = &actionsv1.MovementOwnerState{Active: true, Owner: "ranged:maraudon_sludge_backline"}
	d = p.decide(observeRanged(t, p, o, false))
	a, _ = d.Output["action"].(map[string]any)
	if a["capability"] != "move_body" {
		t.Fatal("fixture did not enter actual ground proposal", d.Output)
	}
	id, e := p.host.ReserveAction(d)
	if e != nil {
		t.Fatal(e)
	}
	payload := map[string]any{"capability": "move_body", "self_guid": "4", "visit_id": "1", "sequence_id": a["sequenceId"], "phase": "body_input_written", "transport_written": true}
	if _, e = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionUnknown, Payload: payload}}); e != nil {
		t.Fatal(e)
	}
	n.X = self.X + 170
	o.Healing.Patients[0].Health = 20
	o.Players["8"].Health = 20
	out := p.decide(observeRanged(t, p, o, false)).Output
	if out["action"] != nil || len(p.host.Outstanding()) != 1 {
		t.Fatal("unresolved harness custody permitted another effect", out)
	}
	// A later exact correlated receipt may settle the original reservation; it
	// is not an erased ledger, reissued input, or invented server pose.
	if _, e = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionSettledUnverified, Payload: payload}}); e != nil {
		t.Fatal(e)
	}
	out = p.decide(observeRanged(t, p, o, false)).Output
	a, _ = out["action"].(map[string]any)
	if a["capability"] != "heal" || a["targetGuid"] != "8" || len(p.host.Outstanding()) != 0 {
		t.Fatal("passive default suppressed otherwise-admitted stationary care", out, p.host.Outstanding())
	}
	o.Healing.Patients[0].Health = 100
	o.Players["8"].Health = 100
	out = p.decide(observeRanged(t, p, o, false)).Output
	if out["action"] != nil {
		t.Fatal("friendly care cleared or replayed retained ground custody", out)
	}
}
