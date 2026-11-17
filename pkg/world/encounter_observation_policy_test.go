package world

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Exercise the reflected protobuf JSON, including decimal uint64 values and
// optional false fields, through the actual persistent encounter Lua VM.
func TestReflectedEncounterObservationPolicy(t *testing.T) {
	const (
		session        = "reflected-encounter-test"
		skull   uint64 = 0xf130002c37000001
		moon           = skull + 1
		next           = skull + 2
		nextCC         = skull + 3
	)
	key := func(guid uint64) string { return strconv.FormatUint(guid, 10) }
	source := `
local encounter_intent = {
    plan={id="marked-pack", groupGuid="7", leaderGuid="3", killOrder={"skull", "moon"},
          controls={{mark="moon", technique="polymorph", casterGuid="4", spellId=118}}}
}
` + readEncounterBundle(t)
	ctx := context.Background()
	host, err := behavior.NewSession(ctx, session, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	revision := host.Status().Revision

	player := func(guid uint64) *actionsv1.EncounterPlayer {
		return &actionsv1.EncounterPlayer{Guid: guid, Present: true, MapKnown: true, MapId: proto.Uint32(389),
			AliveKnown: true, Alive: proto.Bool(true), HealthKnown: true, Health: 100, MaxHealth: 100}
	}
	creature := func(guid uint64) *actionsv1.EncounterCreature {
		return &actionsv1.EncounterCreature{Guid: guid, Present: true, MapKnown: true, MapId: proto.Uint32(389),
			AliveKnown: true, Alive: proto.Bool(true), HealthKnown: true, Health: 100, MaxHealth: 100,
			Attackable: true, PositionKnown: true}
	}
	obs := &actionsv1.EncounterObservationResponse{SessionId: session, SelfGuid: 4, VisitId: 9007199254740993,
		MapId: proto.Uint32(389), HostOwned: proto.Bool(false), WorldObservationRevision: 9007199254740995,
		Party: &actionsv1.EncounterParty{
			Group: &partyv1.Group{Known: true, InGroup: true, GroupGuid: 7, LeaderGuid: 3,
				Members: []*partyv1.Member{{Guid: 3, Online: true}, {Guid: 4, Online: true, Self: true}}},
			RaidTargets: &partyv1.RaidTargetState{Complete: true, GroupGuid: 7, Revision: 10,
				Marks: []*partyv1.RaidTarget{{Icon: 8, TargetGuid: skull}, {Icon: 5, TargetGuid: moon}}},
		},
		Players:             map[string]*actionsv1.EncounterPlayer{"3": player(3), "4": player(4)},
		Targets:             map[string]*actionsv1.EncounterCreature{key(skull): creature(skull), key(moon): creature(moon)},
		Spells:              map[string]*actionsv1.EncounterSpell{"118": {Known: true, Ready: true, Capability: "polymorph"}},
		ControlCapabilities: []*actionsv1.EncounterControlCapability{{Guid: 4, Technique: "polymorph", Ready: true}},
	}
	decide := func(sequence string) behavior.Decision {
		t.Helper()
		result, err := host.Decide(ctx, map[string]any{"observationSequence": sequence})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	observe := func() string {
		t.Helper()
		raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(obs)
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
		return key(seq)
	}
	expect := func(result behavior.Decision, phase string, focus any) {
		t.Helper()
		if got := result.Output; got["phase"] != phase || got["focusGuid"] != focus || got["action"] != nil {
			t.Fatalf("want phase=%s focus=%v and no action, got %+v", phase, focus, got)
		}
	}
	expectQueue := func(result behavior.Decision, front, controlled uint64) {
		t.Helper()
		rows, _ := result.Output["queue"].([]any)
		if len(rows) != 2 || rows[0].(map[string]any)["targetGuid"] != key(front) ||
			rows[1].(map[string]any)["targetGuid"] != key(controlled) || rows[1].(map[string]any)["protected"] != true {
			t.Fatalf("wrong observed queue or protected target: %+v", result.Output)
		}
	}

	pre := decide(observe())
	expect(pre, "apply_control", nil)
	expectQueue(pre, skull, moon)

	obs.Targets[key(skull)].Engaged = true
	obs.Engagement = map[string]*actionsv1.EncounterEngagement{key(skull): {VictimGuid: 3}}
	obs.Targets[key(moon)].Protected = true
	foreign := &actionsv1.ProtectedTarget{TargetGuid: moon, CasterGuid: 3, SpellId: 118, Revision: 6,
		Slot: 1, DurationKnown: true, RemainingMs: 20000, Capability: "polymorph"}
	wrongSpell := &actionsv1.ProtectedTarget{TargetGuid: moon, CasterGuid: 4, SpellId: 6770, Revision: 7,
		Slot: 2, DurationKnown: true, RemainingMs: 20000, Capability: "sap"}
	assigned := &actionsv1.ProtectedTarget{TargetGuid: moon, CasterGuid: 4, SpellId: 118, Revision: 8,
		Slot: 3, DurationKnown: true, RemainingMs: 20000, Capability: "polymorph"}
	obs.ProtectedAuras = []*actionsv1.ProtectedTarget{foreign, wrongSpell}
	expect(decide(observe()), "apply_control", nil)
	// The matching aura is deliberately neither the first nor the last row
	// for this GUID. Collapsing to just one aura must not unlock/hold by order.
	obs.ProtectedAuras = []*actionsv1.ProtectedTarget{foreign, assigned, wrongSpell}
	engaged := decide(observe())
	expect(engaged, "engaged_focus", key(skull))
	expectQueue(engaged, skull, moon)

	// An owned hypothetical feed uses the same ABI; production still emits
	// hostOwned=false. Only the exact fresh nested observation can heal.
	obs.Players["3"].Health = 20
	obs.Spells["2050"] = &actionsv1.EncounterSpell{Known: true, Ready: true, Capability: "heal"}
	obs.Healing = &actionsv1.HealingOptionsResponse{SessionId: session, HostOwned: true,
		Caster:   &actionsv1.HealingCaster{Guid: 4, Ready: true, Mana: proto.Uint32(30)},
		Patients: []*actionsv1.HealingPatient{{Guid: 3, Health: 20, MaxHealth: 100, Distance: proto.Float64(5)}},
		Spells: []*actionsv1.HealingRank{{SpellId: 2050, Rank: proto.Uint32(1), Cost: proto.Uint32(30),
			Amount: 45, CastMs: proto.Uint32(1500), Level: proto.Uint32(1),
			MinRange: proto.Float64(0), MaxRange: proto.Float64(40)}},
	}
	// The nested standing-heal lane can independently be owned without
	// handing the encounter's action authority to Lua.
	expect(decide(observe()), "engaged_focus", key(skull))
	obs.HostOwned = proto.Bool(true)
	seq := observe()
	expect(decide("1"), "engaged_focus", key(skull))
	proposal := decide(seq)
	action, _ := proposal.Output["action"].(map[string]any)
	if action["capability"] != "heal" || action["sessionId"] != session || action["selfGuid"] != "4" ||
		action["targetGuid"] != "3" || action["spellId"] != float64(2050) || action["observationSequence"] != seq {
		t.Fatalf("unexpected heal proposal: %+v", proposal.Output)
	}
	loaded, err := host.Reload(ctx, source+"\n-- replacement\n", revision)
	if err != nil || loaded == revision {
		t.Fatalf("reload did not install a new policy revision: %s %v", loaded, err)
	}
	if _, err := host.ReserveAction(proposal); !errors.Is(err, behavior.ErrFenced) {
		t.Fatalf("old proposal remained admissible after reload: %v", err)
	}
	expect(decide(seq), "waiting_for_encounter_observation", nil)

	obs.HostOwned = proto.Bool(false)
	obs.Healing = nil
	obs.ProtectedAuras = nil
	obs.Targets = map[string]*actionsv1.EncounterCreature{key(next): creature(next), key(nextCC): creature(nextCC)}
	obs.Engagement = nil
	obs.Party.RaidTargets.Marks = []*partyv1.RaidTarget{{Icon: 8, TargetGuid: next}, {Icon: 5, TargetGuid: nextCC}}
	obs.Party.RaidTargets.Revision = 11
	expect(decide(observe()), "raid_mark_changed_replan", nil)
	obs.VisitId++
	obs.Party.RaidTargets.Revision = 1
	fresh := decide(observe())
	expect(fresh, "apply_control", nil)
	expectQueue(fresh, next, nextCC)
}
