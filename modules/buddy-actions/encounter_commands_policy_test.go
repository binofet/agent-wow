package buddyactions

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func commandPolicySource(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile("../../behaviors/encounter-commands.lua")
	if err != nil {
		t.Fatal(err)
	}
	tactics, err := os.ReadFile("../../behaviors/encounter-tactics.lua")
	if err != nil {
		t.Fatal(err)
	}
	return "local encounter_tactics=(function()\n" + string(tactics) + "\nend)()\nlocal commands=(function()\n" + string(source) + "\nend)()\n" + `
local observation
return {
  on_event=function(event)
    if event.kind=="encounter_observation" then observation=event.value end
    return commands.on_event(event)
  end,
  decide=function(frame)
    local action, control=commands.control_action(observation,frame,frame.input.focusGuid,frame.input.plannedControl)
    return {action=action,control=control,intent=commands.queue_intent(observation)}
  end,
  export_state=function() return {version=1,data={observation=observation,commands=commands.export_state()}} end,
  import_state=function(saved)
    observation=saved.data.observation
    return commands.import_state(saved.data.commands)
  end,
}`
}

func commandObservation() *actionsv1.EncounterObservationResponse {
	return &actionsv1.EncounterObservationResponse{SessionId: "commands-body", SelfGuid: 1, VisitId: 1, MapId: proto.Uint32(0),
		HostOwned: proto.Bool(true), SpellbookKnown: true,
		Players: map[string]*actionsv1.EncounterPlayer{"1": {Guid: 1, Present: true, AliveKnown: true, Alive: proto.Bool(true),
			MapKnown: true, MapId: proto.Uint32(0), PositionKnown: true, MovementKnown: true, CombatKnown: true}},
		Targets: map[string]*actionsv1.EncounterCreature{encounterGUID(sheepTarget): {Guid: sheepTarget, Present: true,
			AliveKnown: true, Alive: proto.Bool(true), MapKnown: true, MapId: proto.Uint32(0), PositionKnown: true,
			Attackable: true, X: 40, UnitFlagsKnown: true}},
		Party: &actionsv1.EncounterParty{Group: &partyv1.Group{Known: true, InGroup: true, GroupGuid: 7, LeaderGuid: 3},
			RaidTargets: &partyv1.RaidTargetState{Complete: true, GroupGuid: 7, Marks: []*partyv1.RaidTarget{{Icon: 5, TargetGuid: sheepTarget}}}},
		Spells: map[string]*actionsv1.EncounterSpell{"118": {Known: true, Ready: true, Available: true,
			Capability: "polymorph", MetadataKnown: true, FamilyId: 118, Rank: 1, MaxRange: 30}},
	}
}

func observeCommandPolicy(t *testing.T, host *behavior.Session, observation *actionsv1.EncounterObservationResponse) string {
	t.Helper()
	raw, err := protojson.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	seq, err := host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: value})
	if err != nil {
		t.Fatal(err)
	}
	return encounterGUID(seq)
}

func sendPolicyCommand(t *testing.T, host *behavior.Session, value map[string]any) {
	t.Helper()
	if _, err := host.Enqueue(behavior.Event{Kind: "command", Value: value}); err != nil {
		t.Fatal(err)
	}
}

func commandDecision(t *testing.T, host *behavior.Session, sequence, focus string) map[string]any {
	t.Helper()
	out, err := host.Decide(context.Background(), map[string]any{"observationSequence": sequence, "focusGuid": focus})
	if err != nil {
		t.Fatal(err)
	}
	return out.Output
}

func expectControlAction(t *testing.T, out map[string]any, capability, target string) {
	t.Helper()
	action, _ := out["action"].(map[string]any)
	if action["capability"] != capability || action["targetGuid"] != target || action["selfGuid"] != "1" || action["visitId"] != "1" {
		t.Fatalf("expected exact %s for %s, got %+v", capability, target, out)
	}
}

func TestAutomaticLuaPolymorphMaintainsHealthyAddWithoutACommandOrHealthPressure(t *testing.T) {
	host, err := behavior.NewSession(context.Background(), "commands-body", commandPolicySource(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	obs := commandObservation()
	obs.Party.Group.Members = []*partyv1.Member{{Guid: 1, Online: true}, {Guid: 3, Online: true}}
	obs.Engagement = map[string]*actionsv1.EncounterEngagement{}
	focus, add, other := sheepTarget, sheepTarget+1, sheepTarget+2
	for _, id := range []uint64{focus, add, other} {
		npc := proto.Clone(obs.Targets[encounterGUID(sheepTarget)]).(*actionsv1.EncounterCreature)
		npc.Guid, npc.X, npc.HealthKnown, npc.Health, npc.MaxHealth, npc.Engaged = id, 10, true, 100, 100, true
		obs.Targets[encounterGUID(id)] = npc
		obs.Engagement[encounterGUID(id)] = &actionsv1.EncounterEngagement{VictimGuid: 1, AgeMs: 100}
	}
	obs.Targets[encounterGUID(other)].Health = 60
	zero := proto.Clone(obs.Targets[encounterGUID(focus)]).(*actionsv1.EncounterCreature)
	zero.Guid, zero.Health = other+1, 0
	obs.Targets[encounterGUID(zero.Guid)] = zero
	obs.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: add, VisitId: 1, RangeYards: 30, RequireLos: true, Ready: true}
	decide := func(kill uint64) map[string]any {
		return commandDecision(t, host, observeCommandPolicy(t, host, obs), encounterGUID(kill))
	}
	seq := observeCommandPolicy(t, host, obs)
	before, err := host.Decide(context.Background(), map[string]any{"observationSequence": seq, "focusGuid": encounterGUID(focus), "plannedControl": true})
	if err != nil || before.Output["action"] != nil {
		t.Fatal("automatic default displaced an explicitly authored control plan", before.Output, err)
	}
	expectControlAction(t, decide(focus), "polymorph", encounterGUID(add))
	obs.ProtectedAuras = []*actionsv1.ProtectedTarget{{TargetGuid: add, CasterGuid: 1, Capability: "polymorph", SpellId: 118}}
	if out := decide(focus); out["action"] != nil {
		t.Fatal("cast again while the actual sheep aura remained active", out)
	}
	obs.ProtectedAuras = nil
	expectControlAction(t, decide(focus), "polymorph", encounterGUID(add))
	// Observe that retry's aura before it breaks onto the current kill focus.
	obs.ProtectedAuras = []*actionsv1.ProtectedTarget{{TargetGuid: add, CasterGuid: 1, Capability: "polymorph", SpellId: 118}}
	decide(focus)
	obs.ProtectedAuras = nil
	obs.Targets[encounterGUID(focus)].Alive, obs.Targets[encounterGUID(other)].Alive = proto.Bool(false), proto.Bool(false)
	if out := decide(add); out["action"] != nil {
		t.Fatal("resheeped the final enemy after it became kill focus", out)
	}
	// The standing policy survives a new pull instead of consuming one command.
	obs.VisitId, obs.TargetMovement.VisitId = 2, 2
	obs.Targets[encounterGUID(focus)].Alive, obs.Targets[encounterGUID(other)].Alive = proto.Bool(true), proto.Bool(true)
	if out := decide(focus); out["action"].(map[string]any)["targetGuid"] != encounterGUID(add) ||
		out["action"].(map[string]any)["visitId"] != "2" {
		t.Fatal("standing control did not rebind for the next pull", out)
	}
}

func TestAutomaticPolymorphRespondsToPartyPressure(t *testing.T) {
	for _, tc := range []struct {
		name            string
		health, class   uint32
		explicit, sheep bool
	}{
		{"comfortable tank", 100, 1, false, false},
		{"tank above threshold", 76, 1, false, false},
		{"tank at threshold", 75, 1, false, true},
		{"two mobs on a priest", 100, 5, false, true},
		{"explicit moon on easy pull", 100, 1, true, true},
		{"dead victim", 0, 5, false, false},
		{"victim left map", 50, 5, false, false},
		{"victim absent", 50, 5, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, err := behavior.NewSession(context.Background(), "commands-body", commandPolicySource(t))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(host.Close)
			obs := commandObservation()
			obs.Party.Group.Members = []*partyv1.Member{{Guid: 1, Online: true}, {Guid: 3, Online: true}}
			victim := proto.Clone(obs.Players["1"]).(*actionsv1.EncounterPlayer)
			victim.Guid, victim.HealthKnown, victim.Health, victim.MaxHealth = 3, true, tc.health, 100
			victim.ClassKnown, victim.ClassId = true, tc.class
			switch tc.name {
			case "dead victim":
				victim.Alive = proto.Bool(false)
			case "victim left map":
				victim.MapId = proto.Uint32(1)
			case "victim absent":
				victim.Present = false
			}
			obs.Players["3"] = victim
			target := encounterGUID(sheepTarget)
			add := obs.Targets[target]
			add.X, add.Engaged, add.HealthKnown, add.Health, add.MaxHealth = 10, true, true, 100, 100
			focus := proto.Clone(add).(*actionsv1.EncounterCreature)
			focus.Guid = sheepTarget + 1
			kill := encounterGUID(focus.Guid)
			obs.Targets[kill] = focus
			obs.Engagement = map[string]*actionsv1.EncounterEngagement{target: {VictimGuid: 3, AgeMs: 100}, kill: {VictimGuid: 3, AgeMs: 100}}
			obs.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: sheepTarget, VisitId: 1, RangeYards: 30, RequireLos: true, Ready: true}
			if tc.explicit {
				sendPolicyCommand(t, host, map[string]any{"task": "control", "technique": "polymorph", "mark": "moon"})
			}
			out := commandDecision(t, host, observeCommandPolicy(t, host, obs), kill)
			if tc.sheep {
				expectControlAction(t, out, "polymorph", target)
				return
			}
			intent := out["intent"].(map[string]any)
			if out["action"] != nil || len(intent["controlTargets"].(map[string]any)) != 0 {
				t.Fatal("easy pull reserved or controlled an enemy", out)
			}
		})
	}
}

func TestGenericLuaControlAndExplicitMarkOverrideDefaultSelection(t *testing.T) {
	host, err := behavior.NewSession(context.Background(), "commands-body", commandPolicySource(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	obs := commandObservation()
	sendPolicyCommand(t, host, map[string]any{"task": "control", "technique": "polymorph", "auto": true})
	out := commandDecision(t, host, observeCommandPolicy(t, host, obs), "2")
	if out["control"] == nil || out["action"] != nil {
		t.Fatal("generic request was silently dropped instead of awaiting multiple attackers", out)
	}
	sendPolicyCommand(t, host, map[string]any{"task": "cancel_control"})
	sendPolicyCommand(t, host, map[string]any{"task": "control", "technique": "polymorph", "mark": "moon"})
	expectControlAction(t, commandDecision(t, host, observeCommandPolicy(t, host, obs), "2"), "approach_target", encounterGUID(sheepTarget))
}

func TestLuaControlCommandPersistsThroughApproachAndReloadUntilActualAura(t *testing.T) {
	host, err := behavior.NewSession(context.Background(), "commands-body", commandPolicySource(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	obs, target := commandObservation(), encounterGUID(sheepTarget)
	sendPolicyCommand(t, host, map[string]any{"task": "control", "technique": "polymorph", "mark": "moon", "maintain": false})
	seq := observeCommandPolicy(t, host, obs)
	expectControlAction(t, commandDecision(t, host, seq, "2"), "approach_target", target)
	// The binding and outstanding request survive a source revision while moving.
	if _, err := host.Reload(context.Background(), commandPolicySource(t)+"\n-- revision two", host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	obs.Players["1"].Moving = true
	expectControlAction(t, commandDecision(t, host, observeCommandPolicy(t, host, obs), "2"), "approach_target", target)
	obs.Players["1"].Moving = false
	obs.Targets[target].X = 20
	obs.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: sheepTarget, VisitId: 1, RangeYards: 30, RequireLos: true, Ready: true}
	expectControlAction(t, commandDecision(t, host, observeCommandPolicy(t, host, obs), "2"), "polymorph", target)
	// A fresh sample without an aura cannot turn an uncertain cast into a retry.
	out := commandDecision(t, host, observeCommandPolicy(t, host, obs), "2")
	if out["action"] != nil || out["control"].(map[string]any)["phase"] != "waiting_for_control_aura" {
		t.Fatal("request was forgotten or uncertain cast replayed", out)
	}
	obs.ProtectedAuras = []*actionsv1.ProtectedTarget{{TargetGuid: sheepTarget, SpellId: 118, Capability: "polymorph", DurationKnown: true, RemainingMs: 20000}}
	out = commandDecision(t, host, observeCommandPolicy(t, host, obs), "2")
	if out["action"] != nil || out["control"] != nil {
		t.Fatal("actual aura did not complete the request", out)
	}
}

func TestLuaControlCommandNeverSheepsKillFocusAndKeepsExactMarkBinding(t *testing.T) {
	host, err := behavior.NewSession(context.Background(), "commands-body", commandPolicySource(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	obs, target := commandObservation(), encounterGUID(sheepTarget)
	sendPolicyCommand(t, host, map[string]any{"task": "control", "technique": "polymorph", "mark": "moon"})
	out := commandDecision(t, host, observeCommandPolicy(t, host, obs), target)
	if out["action"] != nil || out["control"].(map[string]any)["phase"] != "waiting_for_focus_change" {
		t.Fatal("current kill focus was crowd controlled", out)
	}
	obs.Party.RaidTargets.Marks[0].TargetGuid++
	expectControlAction(t, commandDecision(t, host, observeCommandPolicy(t, host, obs), "2"), "approach_target", target)
	sendPolicyCommand(t, host, map[string]any{"task": "focus", "mark": "moon"})
	sendPolicyCommand(t, host, map[string]any{"task": "kill_order", "order": []any{"skull", target, "moon"}})
	out = commandDecision(t, host, observeCommandPolicy(t, host, obs), target)
	intent := out["intent"].(map[string]any)
	if intent["focusGuid"] != encounterGUID(sheepTarget+1) || len(intent["killOrder"].([]any)) != 2 {
		t.Fatal("authored focus and kill order were not exposed to the shared queue", out)
	}
	sendPolicyCommand(t, host, map[string]any{"task": "control", "mark": "moon", "cancel": true})
	out = commandDecision(t, host, observeCommandPolicy(t, host, obs), "2")
	if out["action"] != nil || out["control"] != nil {
		t.Fatal("explicit cancellation did not clear the request", out)
	}
}

func TestLuaSapCommandPreparesStealthAndWaitsForObservedAura(t *testing.T) {
	host, err := behavior.NewSession(context.Background(), "commands-body", commandPolicySource(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	obs, target := commandObservation(), encounterGUID(sheepTarget)
	obs.Spells = map[string]*actionsv1.EncounterSpell{
		"6770": {Known: true, Capability: "sap", Rank: 1, MetadataKnown: true, MaxRange: 5},
		"1784": {Known: true, Capability: "stealth", Ready: true, Available: true, SelfCastSupported: true, Rank: 1},
	}
	sendPolicyCommand(t, host, map[string]any{"task": "control", "technique": "sap", "targetGuid": target})
	obs.Spells["1784"].Ready = false
	obs.Players["1"].Moving = true
	expectControlAction(t, commandDecision(t, host, observeCommandPolicy(t, host, obs), "2"), "stop_movement", target)
	obs.Spells["1784"].Ready = true
	obs.Players["1"].Moving = false
	expectControlAction(t, commandDecision(t, host, observeCommandPolicy(t, host, obs), "2"), "cast_self", "1")
	out := commandDecision(t, host, observeCommandPolicy(t, host, obs), "2")
	if out["action"] != nil || out["control"].(map[string]any)["phase"] != "waiting_for_stealth_aura" {
		t.Fatal("Stealth was replayed or assumed from transport", out)
	}
	obs.Spells["1784"].SelfAuraActive = true
	obs.Spells["6770"].Ready, obs.Spells["6770"].Available = true, true
	expectControlAction(t, commandDecision(t, host, observeCommandPolicy(t, host, obs), "2"), "approach_target", target)
	obs.Targets[target].X = 4
	obs.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: sheepTarget, VisitId: 1, RangeYards: 5, RequireLos: true, Behind: true, Ready: true}
	expectControlAction(t, commandDecision(t, host, observeCommandPolicy(t, host, obs), "2"), "sap", target)
	obs.Targets[target].Alive = proto.Bool(false)
	out = commandDecision(t, host, observeCommandPolicy(t, host, obs), "2")
	if out["control"] != nil {
		t.Fatal("witnessed target death did not complete the request", out)
	}
}

func TestLuaControlCommandKeepsRequestAfterReadOnlyPreparation(t *testing.T) {
	host, err := behavior.NewSession(context.Background(), "commands-body", commandPolicySource(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	obs, target := commandObservation(), encounterGUID(sheepTarget)
	obs.Targets[target].X = 20
	obs.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: sheepTarget, VisitId: 1, RangeYards: 30, RequireLos: true, Ready: true}
	sendPolicyCommand(t, host, map[string]any{"task": "control", "technique": "polymorph", "targetGuid": target})
	seq := observeCommandPolicy(t, host, obs)
	decision, err := host.Decide(context.Background(), map[string]any{"observationSequence": seq, "focusGuid": "2"})
	if err != nil {
		t.Fatal(err)
	}
	expectControlAction(t, decision.Output, "polymorph", target)
	actionID, err := host.ReserveAction(decision)
	if err != nil {
		t.Fatal(err)
	}
	// An exact request can acquire only missing mechanical observations. Its
	// not-dispatched receipt must preserve the command and require a fresh read.
	_, err = host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{
		SessionID: "commands-body", ActionID: actionID, Outcome: behavior.ActionNotDispatched,
		Payload: map[string]any{"capability": "polymorph", "target_guid": target, "spell_id": 118},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if out := commandDecision(t, host, seq, "2"); out["action"] != nil || out["control"] == nil {
		t.Fatal("settlement reused stale evidence or forgot the request", out)
	}
	expectControlAction(t, commandDecision(t, host, observeCommandPolicy(t, host, obs), "2"), "polymorph", target)
}

func TestLuaMaintainedControlReappliesAfterBreakUntilKillFocusOrDeath(t *testing.T) {
	host, err := behavior.NewSession(context.Background(), "commands-body", commandPolicySource(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	obs, target := commandObservation(), encounterGUID(sheepTarget)
	obs.Targets[target].X = 20
	obs.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: sheepTarget, VisitId: 1, RangeYards: 30, RequireLos: true, Ready: true}
	sendPolicyCommand(t, host, map[string]any{"task": "control", "technique": "polymorph", "mark": "moon"})
	expectControlAction(t, commandDecision(t, host, observeCommandPolicy(t, host, obs), "2"), "polymorph", target)
	for i := 0; i < 2; i++ {
		obs.ProtectedAuras = []*actionsv1.ProtectedTarget{{TargetGuid: sheepTarget, SpellId: 118, Capability: "polymorph", DurationKnown: true, RemainingMs: 20000}}
		out := commandDecision(t, host, observeCommandPolicy(t, host, obs), "2")
		if out["action"] != nil || out["control"].(map[string]any)["phase"] != "control_observed" {
			t.Fatal("active aura did not hold the maintained request without casting", out)
		}
		obs.ProtectedAuras = nil
		if i == 0 {
			expectControlAction(t, commandDecision(t, host, observeCommandPolicy(t, host, obs), "2"), "polymorph", target)
		} else {
			out = commandDecision(t, host, observeCommandPolicy(t, host, obs), target)
			if out["action"] != nil || out["control"].(map[string]any)["phase"] != "waiting_for_focus_change" {
				t.Fatal("maintained request resheeped the current kill focus", out)
			}
		}
	}
	obs.Targets[target].Alive = proto.Bool(false)
	if out := commandDecision(t, host, observeCommandPolicy(t, host, obs), target); out["control"] != nil {
		t.Fatal("maintained request survived the witnessed death", out)
	}
}

func TestLuaControlCommandStopsOwnSwingBeforeReservingCast(t *testing.T) {
	host, err := behavior.NewSession(context.Background(), "commands-body", commandPolicySource(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	obs, target := commandObservation(), encounterGUID(sheepTarget)
	obs.ActiveMeleeTargetGuid = 2
	obs.Targets[target].X = 20
	obs.TargetMovement = &actionsv1.TargetMovementState{TargetGuid: sheepTarget, VisitId: 1, RangeYards: 30, RequireLos: true, Ready: true}
	sendPolicyCommand(t, host, map[string]any{"task": "control", "technique": "polymorph", "mark": "moon"})
	expectControlAction(t, commandDecision(t, host, observeCommandPolicy(t, host, obs), "2"), "melee_stop", "2")
	obs.ActiveMeleeTargetGuid = 0
	expectControlAction(t, commandDecision(t, host, observeCommandPolicy(t, host, obs), "2"), "polymorph", target)
}
