package buddyactions

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

func spellPolicyObservation(t *testing.T) (*Service, *fakeSession, *actionsv1.EncounterObservationResponse) {
	t.Helper()
	s, f, _ := rig(t)
	f.world.Self.ClassId, f.world.Self.LevelKnown, f.world.Self.Level = 8, true, 30
	f.world.Self.Power, f.world.Self.MaxPower = 2000, 2000
	f.world.Self.BaseManaKnown, f.world.Self.BaseMana = true, 1000
	f.world.Self.ChannelSpellKnown, f.world.Self.ChannelObjectKnown = true, true
	learnSpells(s, 133, 8401)
	obs := &actionsv1.EncounterObservationResponse{SelfGuid: s.selfGUID,
		Players: map[string]*actionsv1.EncounterPlayer{}, Spells: map[string]*actionsv1.EncounterSpell{}}
	s.encounterPlayers(f.world, nil, obs)
	s.encounterSpells(f.world, nil, obs)
	return s, f, obs
}

func TestEncounterSpellReflectsExistingRankMechanicsWithoutChoosing(t *testing.T) {
	s, f, obs := spellPolicyObservation(t)
	for _, id := range []uint32{133, 8401} {
		key := encounterGUID(uint64(id))
		spell, _ := spellcatalog.Default().Lookup(id)
		row := obs.Spells[key]
		if row == nil || !row.Known || !row.MetadataKnown || !row.Available || !row.Ready ||
			row.FamilyId != 133 || row.Rank != spell.Rank || row.Name != spell.Name || row.Cost != spellCost(spell, f.world.Self) ||
			row.CastTimeMs != spell.CastMs || row.DirectDamage != directDamage(spell) || row.MinRange != spell.MinHostile || row.MaxRange != spell.MaxHostile {
			t.Fatalf("rank %d mechanics missing or changed: %v", id, row)
		}
	}
	if obs.Players["1"].ClassId != 8 || obs.Players["1"].Level != 30 || !obs.Players["1"].PositionKnown || obs.Players["1"].Power != 2000 {
		t.Fatal("Lua cannot observe the caster profile and geometry", obs.Players)
	}
	s.spellReady[8401] = s.now().Add(time.Minute)
	s.encounterSpells(f.world, nil, obs)
	if obs.Spells["8401"].Available || obs.Spells["8401"].Ready || !obs.Spells["133"].Ready {
		t.Fatal("exact rank cooldown was not reflected independently", obs.Spells)
	}
	delete(s.spellReady, 8401)
	f.world.Self.MovementFlags = 1
	s.encounterSpells(f.world, nil, obs)
	if obs.Spells["8401"].Ready || !obs.Spells["8401"].Available {
		t.Fatal("movement erased available rank mechanics", obs.Spells["8401"])
	}
	if len(f.packets) != 0 || len(f.movementObservations) != 0 {
		t.Fatal("spell observation performed gameplay actions", f.packets)
	}
}

func spellSelectorSource(t *testing.T, tuning string) string {
	t.Helper()
	source, err := os.ReadFile("../../behaviors/encounter-spells.lua")
	if err != nil {
		t.Fatal(err)
	}
	return "local spells=(function()\n" + string(source) + "\nend)()\n" + tuning + `
local observed
return {
  on_event=function(event) observed=event.value; return true end,
  decide=function(frame)
    return {spellId=spells.damage_spell(observed, frame.input.target, frame.input.override, frame.input.options)}
  end,
  export_state=function() return {version=1,data={observed=observed}} end,
  import_state=function(saved) observed=saved.data.observed; return true end,
}`
}

func enqueueSpellObservation(t *testing.T, host *behavior.Session, obs *actionsv1.EncounterObservationResponse) {
	t.Helper()
	raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enqueue(behavior.Event{Kind: "spell_observation", Value: value}); err != nil {
		t.Fatal(err)
	}
}

func TestEncounterLuaSelectsHighestEligibleRankAndReloadsInSameSession(t *testing.T) {
	s, f, obs := spellPolicyObservation(t)
	ctx := context.Background()
	host, err := behavior.NewSession(ctx, "rank-policy", spellSelectorSource(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	decide := func(expected float64, frame map[string]any) {
		t.Helper()
		out, err := host.Decide(ctx, frame)
		if err != nil || out.Output["spellId"] != expected {
			t.Fatalf("want exact learned rank %v, got %+v (%v)", expected, out.Output, err)
		}
	}
	enqueueSpellObservation(t, host, obs)
	decide(8401, nil) // Mira knows rank6 as well as rank1.
	s.spellReady[8401] = s.now().Add(time.Minute)
	s.encounterSpells(f.world, nil, obs)
	enqueueSpellObservation(t, host, obs)
	decide(133, nil)
	delete(s.spellReady, 8401)
	s.encounterSpells(f.world, nil, obs)
	obs.Players["1"].Power = 100
	enqueueSpellObservation(t, host, obs)
	// A policy revision changes conservation in this same character session.
	before := host.Status().Revision
	if after, err := host.Reload(ctx, spellSelectorSource(t, "spells.tuning.mana_reserve_fraction=0.99\n"), before); err != nil || after == before {
		t.Fatal("rank policy failed to reload", after, err)
	}
	out, err := host.Decide(ctx, nil)
	if err != nil || out.Output["spellId"] != nil {
		t.Fatal("hot reload did not change spell conservation", out.Output, err)
	}
}

func TestEncounterLuaRotationUsesAuraCooldownAndAuthoredProfiles(t *testing.T) {
	ctx := context.Background()
	host, err := behavior.NewSession(ctx, "rotation-policy", spellSelectorSource(t, `
spells.class_profiles[8]={family_weights={[100]=0,[200]=0,[300]=1}}
spells.race_profiles[7]={family_weights={[100]=10,[200]=0,[300]=0}}
`))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	row := func(family uint32, damage float64, cast, cooldown uint32) *actionsv1.EncounterSpell {
		return &actionsv1.EncounterSpell{Known: true, Ready: true, Available: true, Capability: "offense",
			MetadataKnown: true, FamilyId: family, Rank: 1, Cost: 20, DirectDamage: damage,
			CastTimeMs: cast, CooldownMs: cooldown, MaxRange: 30}
	}
	obs := &actionsv1.EncounterObservationResponse{SelfGuid: 1, Players: map[string]*actionsv1.EncounterPlayer{"1": {}},
		Spells: map[string]*actionsv1.EncounterSpell{"100": row(100, 90, 3000, 0), "200": row(200, 10, 0, 0), "300": row(300, 300, 0, 60000)}}
	obs.Spells["200"].PeriodicDamage = []*actionsv1.EncounterPeriodicDamage{{DamagePerTick: 60, PeriodMs: 2000}}
	target := map[string]any{"healthKnown": true, "health": 1000, "maxHealth": 1000}
	decide := func(expected float64, options map[string]any) {
		t.Helper()
		enqueueSpellObservation(t, host, obs)
		out, err := host.Decide(ctx, map[string]any{"target": target, "options": options})
		if err != nil || out.Output["spellId"] != expected {
			t.Fatalf("want spell %v, got %+v (%v)", expected, out.Output, err)
		}
	}
	decide(300, nil)
	obs.Spells["300"].Ready, obs.Spells["300"].Available = false, false
	decide(200, nil) // Damage over time is useful on a durable enemy.
	target["ownDamageFamilies"] = []any{200}
	decide(100, nil) // The observed own aura prevents reapplication.
	delete(target, "ownDamageFamilies")
	obs.Spells["300"].Ready, obs.Spells["300"].Available = true, true
	target["health"] = 40
	decide(100, nil) // Save the long cooldown when a regular cast can finish.
	target["health"] = 1000
	obs.Players["1"].ClassKnown, obs.Players["1"].ClassId = true, 8
	decide(300, nil)
	obs.Players["1"].RaceKnown, obs.Players["1"].RaceId = true, 7
	decide(100, nil) // Race-specific authored tuning composes over class tuning.
}

func TestEncounterPublishedDescriptorMatchesAllGeneratedContracts(t *testing.T) {
	body, err := os.ReadFile("module.pb")
	if err != nil {
		t.Fatal(err)
	}
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(body, set); err != nil {
		t.Fatal(err)
	}
	want := protodesc.ToFileDescriptorProto(actionsv1.File_modules_buddy_actions_actions_proto)
	for _, file := range set.File {
		if file.GetName() == want.GetName() {
			if !proto.Equal(file, want) {
				t.Fatal("published actions descriptor differs from generated binary API")
			}
			return
		}
	}
	t.Fatal("published module descriptor omitted actions contracts")
}
