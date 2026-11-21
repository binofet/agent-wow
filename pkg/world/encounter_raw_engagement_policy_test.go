package world

import (
	"context"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/proto"
)

func rawEngagementObservation(victim uint64, age uint64) *actionsv1.EncounterObservationResponse {
	o := fallbackObservation()
	o.Party = &actionsv1.EncounterParty{Group: fallbackRoster(true)}
	o.Players[fallbackGUID(fallbackLeader)] = fallbackPlayer(fallbackLeader)
	addFallbackTarget(o, fallbackNPC, 0, 500)
	o.Targets[fallbackGUID(fallbackNPC)].AttackObservation = &actionsv1.EncounterAttackObservation{
		VictimKnown: victim != 0, VictimGuid: victim, AgeMs: age,
	}
	return o
}

func TestRawEncounterEngagementIsChosenByLuaRatherThanLegacyGoClassification(t *testing.T) {
	for _, mode := range []string{"recent party attack", "recent attack on dead teammate", "sustained party combat", "self defense without owner", "selection alone", "contradicted attack", "foreign victim", "stale out of combat"} {
		t.Run(mode, func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			o := rawEngagementObservation(fallbackLeader, 900)
			npc := o.Targets[fallbackGUID(fallbackNPC)]
			fight := true
			switch mode {
			case "recent attack on dead teammate":
				o.Players[fallbackGUID(fallbackLeader)].Alive = proto.Bool(false)
			case "sustained party combat":
				npc.AttackObservation.AgeMs = 12000
				o.Players[fallbackGUID(fallbackLeader)].CombatKnown = true
				o.Players[fallbackGUID(fallbackLeader)].InCombat = true
			case "self defense without owner":
				npc.AttackObservation.VictimGuid = fallbackSelf
				delete(o.Players, fallbackGUID(fallbackLeader))
			case "selection alone":
				npc.AttackObservation = &actionsv1.EncounterAttackObservation{TargetKnown: true, TargetGuid: fallbackLeader}
				fight = false
			case "contradicted attack":
				npc.AttackObservation.TargetKnown, npc.AttackObservation.TargetGuid = true, 99
				fight = false
			case "foreign victim":
				npc.AttackObservation.VictimGuid = 99
				o.Players["99"] = fallbackPlayer(99)
				fight = false
			case "stale out of combat":
				npc.AttackObservation.AgeMs = 6000
				fight = false
			}
			// Even deliberately contradictory legacy hints cannot override a
			// current raw observation's meaning in the one Lua policy.
			npc.Engaged = !fight
			o.Engagement[fallbackGUID(fallbackNPC)] = &actionsv1.EncounterEngagement{VictimGuid: fallbackLeader}
			if fight {
				p.expectOffense(p.observe(o), "engaged_defense", fallbackNPC, 133)
			} else {
				out := p.expectNoAction(p.observe(o))
				queue := out["queue"].([]any)
				if len(queue) != 1 || queue[0].(map[string]any)["engaged"] != false {
					t.Fatal(out)
				}
			}
		})
	}
}

func TestRawEncounterRetainsRunnerAfterVictimDeathAndRetiresOnlyObservedReset(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	o := rawEngagementObservation(fallbackLeader, 1)
	p.expectOffense(p.observe(o), "engaged_defense", fallbackNPC, 133)
	npc := o.Targets[fallbackGUID(fallbackNPC)]
	o.Players[fallbackGUID(fallbackLeader)].Alive = proto.Bool(false)
	npc.AttackObservation = &actionsv1.EncounterAttackObservation{}
	npc.Health = 100
	p.expectOffense(p.observe(o), "engaged_defense", fallbackNPC, 133)
	// Full health under CC is not an evade or permission to move to a new pack.
	npc.UnitFlagsKnown, npc.UnitFlags, npc.Health, npc.Protected = true, 0, npc.MaxHealth, true
	out := p.expectNoAction(p.observe(o))
	row := out["queue"].([]any)[0].(map[string]any)
	if row["engaged"] != true || row["protected"] != true {
		t.Fatal(out)
	}
	npc.Protected = false
	out = p.expectNoAction(p.observe(o))
	row = out["queue"].([]any)[0].(map[string]any)
	if row["engaged"] != false || row["encounterScope"] != "future_pull" || out["focusGuid"] != nil {
		t.Fatal(out)
	}
}

func TestRawEncounterFreshnessTuningReloadsInSameSession(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	o := rawEngagementObservation(fallbackLeader, 7000)
	p.expectNoAction(p.observe(o))
	before := p.host.Status()
	source := strings.Replace(readEncounterBundle(t), "recent_attack_ms=5000", "recent_attack_ms=10000", 1)
	if source == readEncounterBundle(t) {
		t.Fatal("test did not change the Lua policy tuning")
	}
	revision, err := p.host.Reload(context.Background(), source, before.Revision)
	if err != nil {
		t.Fatal(err)
	}
	p.expectOffense(p.observe(o), "engaged_defense", fallbackNPC, 133)
	after := p.host.Status()
	if after.SessionID != before.SessionID || after.Revision != revision || after.Revision == before.Revision {
		t.Fatal("engagement tuning required replacing the character session", before, after)
	}
}
