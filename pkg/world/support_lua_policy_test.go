package world

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
)

func newSupportPolicy(t *testing.T, decision string) *fallbackPolicyTest {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "support.lua"))
	if err != nil {
		t.Fatal(err)
	}
	source := "local support=(function()\n" + string(script) + "\nend)()\n" + `
local observation
return {
  on_event=function(event) if event.kind=="encounter_observation" then observation=event.value end return true end,
  decide=function(frame) if #frame.pending~=0 or not observation then return {} end
    return {action=support.` + decision + `(observation)} end,
  export_state=function() return {version=1,data={}} end,
  import_state=function(state) return state.version==1 end,
}`
	host, err := behavior.NewSession(context.Background(), fallbackSession, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Close)
	return &fallbackPolicyTest{t: t, host: host}
}

func supportResObservation() *actionsv1.EncounterObservationResponse {
	obs := fallbackObservation()
	obs.Party = &actionsv1.EncounterParty{Group: fallbackRoster(true)}
	dead := fallbackPlayer(fallbackLeader)
	dead.Alive = proto.Bool(false)
	obs.Players[fallbackGUID(fallbackLeader)] = dead
	obs.Support = &actionsv1.EncounterSupport{Resurrections: []*actionsv1.ResurrectionOption{{PatientGuid: fallbackLeader, CastTargetGuid: fallbackLeader, SpellId: 2006, Ready: true, CanApproach: true, RangeYards: 30}}}
	return obs
}

func TestLuaSupportResurrectsDeadLeaderAndRejectsOfflineOrRevivedPatient(t *testing.T) {
	policy := newSupportPolicy(t, "resurrect")
	obs := supportResObservation()
	result := policy.decide(policy.observe(obs))
	action, _ := result.Output["action"].(map[string]any)
	if action["capability"] != "resurrect" || action["targetGuid"] != fallbackGUID(fallbackLeader) || action["spellId"] != float64(2006) {
		t.Fatal(result.Output)
	}
	obs.Party.Group = fallbackRoster(false)
	policy.expectNoAction(policy.observe(obs))
	obs.Party.Group = fallbackRoster(true)
	obs.Players[fallbackGUID(fallbackLeader)].Alive = proto.Bool(true)
	policy.expectNoAction(policy.observe(obs))
}

func TestLuaSupportApproachesAndUsesTheOwnedCorpse(t *testing.T) {
	policy := newSupportPolicy(t, "resurrect")
	obs := supportResObservation()
	const corpse = uint64(0xf101000000000035)
	option := obs.Support.Resurrections[0]
	option.Ready, option.CastTargetGuid = false, corpse
	result := policy.decide(policy.observe(obs))
	action, _ := result.Output["action"].(map[string]any)
	if action["capability"] != "approach_target" || action["targetGuid"] != fallbackGUID(corpse) || action["rangeYards"] != float64(30) {
		t.Fatal(result.Output)
	}
	option.Ready = true
	result = policy.decide(policy.observe(obs))
	action, _ = result.Output["action"].(map[string]any)
	if action["capability"] != "resurrect" || action["targetGuid"] != fallbackGUID(fallbackLeader) || action["castTargetGuid"] != fallbackGUID(corpse) {
		t.Fatal(result.Output)
	}
}

func TestLuaSupportInterruptsHealingBeforeDamageWithoutLivingOwner(t *testing.T) {
	policy := newSupportPolicy(t, "interrupt")
	obs := supportResObservation()
	addFallbackTarget(obs, fallbackNPC, fallbackLeader, 100)
	addFallbackTarget(obs, fallbackNPC+1, fallbackLeader, 100)
	obs.Support.Interrupts = []*actionsv1.InterruptOption{
		{TargetGuid: fallbackNPC, SpellId: 1766, CastRevision: 7, RemainingMs: 300, Ready: true, CanApproach: true, RangeYards: 5},
		{TargetGuid: fallbackNPC + 1, SpellId: 1766, CastRevision: 8, RemainingMs: 1500, Healing: true, Ready: true, CanApproach: true, RangeYards: 5},
	}
	result := policy.decide(policy.observe(obs))
	action, _ := result.Output["action"].(map[string]any)
	if action["capability"] != "interrupt" || action["targetGuid"] != fallbackGUID(fallbackNPC+1) || action["castRevision"] != "8" {
		t.Fatal(result.Output)
	}
	obs.Targets[fallbackGUID(fallbackNPC+1)].Protected = true
	result = policy.decide(policy.observe(obs))
	action, _ = result.Output["action"].(map[string]any)
	if action["targetGuid"] != fallbackGUID(fallbackNPC) {
		t.Fatal("broke protected CC", result.Output)
	}
}

func TestLuaSupportResumesFromEmptyBusyObservationWithoutModeToggle(t *testing.T) {
	policy := newSupportPolicy(t, "interrupt")
	obs := supportResObservation()
	addFallbackTarget(obs, fallbackNPC, fallbackLeader, 100)
	policy.expectNoAction(policy.observe(obs)) // Fear/channel currently blocks mechanics.
	obs.Support.Interrupts = []*actionsv1.InterruptOption{{TargetGuid: fallbackNPC, SpellId: 1766, CastRevision: 9, RemainingMs: 1500, Healing: true, Ready: true, CanApproach: true}}
	result := policy.decide(policy.observe(obs))
	action, _ := result.Output["action"].(map[string]any)
	if action["capability"] != "interrupt" || action["castRevision"] != "9" {
		t.Fatal(result.Output)
	}
	obs.Players[fallbackGUID(fallbackSelf)].Alive = proto.Bool(false)
	policy.expectNoAction(policy.observe(obs))
}
