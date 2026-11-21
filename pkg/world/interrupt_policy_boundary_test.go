package world

import (
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
)

func TestSharedLuaInterruptApproachesThenCastsExactHealRevisionWithoutLivingOwner(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	obs := supportResObservation()
	addFallbackTarget(obs, fallbackNPC, fallbackLeader, 100)
	addFallbackTarget(obs, fallbackNPC+1, fallbackSelf, 100)
	obs.Support.CasterInCombat = true
	obs.Support.Interrupts = []*actionsv1.InterruptOption{
		{TargetGuid: fallbackNPC, SpellId: 1766, CastRevision: 7, RemainingMs: 200, CanApproach: true, RangeYards: 5},
		{TargetGuid: fallbackNPC + 1, SpellId: 1766, CastRevision: 8, RemainingMs: 1500, Healing: true, CanApproach: true, RangeYards: 5},
	}
	out := p.decide(observeSparseEncounter(t, p, obs)).Output
	a, _ := out["action"].(map[string]any)
	if a["capability"] != "approach_target" || a["targetGuid"] != fallbackGUID(fallbackNPC+1) || a["requireLos"] != true || a["rangeYards"] != float64(5) {
		t.Fatal("Lua did not position for the healing enemy cast", out)
	}
	obs.Support.Interrupts[1].Ready = true
	out = p.decide(observeSparseEncounter(t, p, obs)).Output
	a, _ = out["action"].(map[string]any)
	if a["capability"] != "interrupt" || a["targetGuid"] != fallbackGUID(fallbackNPC+1) || a["spellId"] != float64(1766) || a["castRevision"] != "8" {
		t.Fatal("Lua did not preserve exact learned spell and enemy cast revision", out)
	}
	obs.Support.Interrupts[1].CastRevision = 9
	out = p.decide(observeSparseEncounter(t, p, obs)).Output
	if a, _ = out["action"].(map[string]any); a["castRevision"] != "9" {
		t.Fatal("new enemy heal did not become the current exact choice", out)
	}
	obs.Targets[fallbackGUID(fallbackNPC+1)].Protected = true
	obs.Support.Interrupts[0].Ready = true
	out = p.decide(observeSparseEncounter(t, p, obs)).Output
	if a, _ = out["action"].(map[string]any); a["targetGuid"] == fallbackGUID(fallbackNPC+1) {
		t.Fatal("Lua interrupted a protected crowd control target", out)
	}
}
