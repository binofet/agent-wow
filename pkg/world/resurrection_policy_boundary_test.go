package world

import (
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/proto"
)

func TestSharedLuaSessionApproachesAndResurrectsObservedNonpartyAlly(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	obs := supportResObservation()
	const ally = uint64(7)
	const corpse = uint64(0xf101000000000037)
	dead := fallbackPlayer(ally)
	dead.Alive = proto.Bool(false)
	dead.Health = 0
	obs.Players[fallbackGUID(ally)] = dead
	obs.Spells["2006"] = &actionsv1.EncounterSpell{Known: true, Capability: "resurrect", Rank: 1}
	obs.Support.Resurrections = []*actionsv1.ResurrectionOption{{PatientGuid: ally, CastTargetGuid: corpse, SpellId: 2006, CanApproach: true, RangeYards: 30}}
	out := p.decide(observeSparseEncounter(t, p, obs)).Output
	action, _ := out["action"].(map[string]any)
	if action["capability"] != "approach_target" || action["targetGuid"] != fallbackGUID(corpse) || action["requireLos"] != true || action["rangeYards"] != float64(30) {
		t.Fatal("shared session did not position for the actual allied corpse", out)
	}
	obs.Support.Resurrections[0].Ready = true
	out = p.decide(observeSparseEncounter(t, p, obs)).Output
	action, _ = out["action"].(map[string]any)
	if action["capability"] != "resurrect" || action["targetGuid"] != fallbackGUID(ally) || action["castTargetGuid"] != fallbackGUID(corpse) || action["spellId"] != float64(2006) {
		t.Fatal("shared session did not choose the exact learned resurrection", out)
	}
	dead.Alive = proto.Bool(true)
	dead.Health = 100
	out = p.decide(observeSparseEncounter(t, p, obs)).Output
	if action, _ := out["action"].(map[string]any); action["capability"] == "resurrect" {
		t.Fatal("already revived ally was selected again", out)
	}
}
