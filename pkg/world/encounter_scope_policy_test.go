package world

import (
	"context"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	"google.golang.org/protobuf/proto"
)

func TestCombatQueueCurrentEncounterOutranksUntouchedMarkedCandidates(t *testing.T) {
	host := urgencyModule(t, "encounter-tactics.lua", "return module.decide(frame.input)")
	npc := func(engaged bool, hp float64) map[string]any {
		return map[string]any{"present": true, "aliveKnown": true, "alive": true, "attackable": true, "engaged": engaged, "health": hp, "maxHealth": float64(100)}
	}
	targets := map[string]any{"1": npc(true, 80), "2": npc(false, 1), "3": npc(false, 10)}
	out, err := host.Decide(context.Background(), map[string]any{"kind": "combat_queue", "targets": targets, "explicitOrder": []any{"2"}, "ownerFocus": "1"})
	if err != nil {
		t.Fatal(err)
	}
	queue := out.Output["queue"].([]any)
	if out.Output["focusGuid"] != "1" || len(queue) != 3 || queue[0].(map[string]any)["targetGuid"] != "1" || queue[0].(map[string]any)["encounterScope"] != "active_encounter" || queue[1].(map[string]any)["targetGuid"] != "2" || queue[1].(map[string]any)["encounterScope"] != "future_pull" {
		t.Fatal("future pull displaced current fight", out.Output)
	}
	if out.Output["burstIntent"] != nil {
		t.Fatal("untouched low health became an emergency", out.Output)
	}
}

func TestCombatQueueKeepsEmergencyAndControlInsideCurrentEncounter(t *testing.T) {
	host := urgencyModule(t, "encounter-tactics.lua", "return module.decide(frame.input)")
	npc := func(engaged bool, hp float64) map[string]any {
		return map[string]any{"present": true, "aliveKnown": true, "alive": true, "attackable": true, "engaged": engaged, "health": hp, "maxHealth": float64(100)}
	}
	active, finisher, controlled, future := npc(true, 90), npc(true, 10), npc(true, 5), npc(false, 1)
	controlled["protected"] = true
	out, err := host.Decide(context.Background(), map[string]any{"kind": "combat_queue", "targets": map[string]any{"1": active, "2": finisher, "3": controlled, "4": future}, "explicitOrder": []any{"4", "1"}, "ownerFocus": "1"})
	if err != nil {
		t.Fatal(err)
	}
	queue := out.Output["queue"].([]any)
	if out.Output["focusGuid"] != "2" || out.Output["burstIntent"] == nil || queue[len(queue)-1].(map[string]any)["targetGuid"] != "4" {
		t.Fatal("encounter scope lost urgent finish or future target", out.Output)
	}
	for _, raw := range queue {
		row := raw.(map[string]any)
		if row["targetGuid"] == "3" && (row["protected"] != true || row["engaged"] != true) {
			t.Fatal("CC reservation was retired", row)
		}
	}
}

func TestEncounterSuppressesFuturePullUntilCurrentEncounterIncludingControlEnds(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	o := fallbackObservation()
	o.Party = &actionsv1.EncounterParty{Group: fallbackRoster(true), RaidTargets: &partyv1.RaidTargetState{Complete: true, GroupGuid: fallbackGroup, Marks: []*partyv1.RaidTarget{{Icon: 8, TargetGuid: fallbackNPC + 1}}}}
	o.Players[fallbackGUID(fallbackLeader)] = fallbackPlayer(fallbackLeader)
	addFallbackTarget(o, fallbackNPC, fallbackLeader, 500)
	addFallbackTarget(o, fallbackNPC+1, 0, 1)
	o.ObservedOwnerFocus = fallbackNPC
	out := p.expectOffense(p.observe(o), "engaged_defense", fallbackNPC, 133).Output
	if out["nextPullGuid"] != nil || out["queue"].([]any)[0].(map[string]any)["targetGuid"] != fallbackGUID(fallbackNPC) {
		t.Fatal("future pack leaked into active pull recommendation", out)
	}
	// A controlled survivor is still part of this encounter; proximity/marks do
	// not permit moving on to the untouched future pack.
	o.Targets[fallbackGUID(fallbackNPC)].Protected = true
	out = p.expectNoAction(p.observe(o))
	if out["nextPullGuid"] != nil || len(out["queue"].([]any)) != 2 {
		t.Fatal("controlled survivor caused next-pack pull", out)
	}
	o.Targets[fallbackGUID(fallbackNPC)].Alive = proto.Bool(false)
	out = p.expectNoAction(p.observe(o))
	if out["nextPullGuid"] != fallbackGUID(fallbackNPC+1) || out["focusGuid"] != nil {
		t.Fatal("finished encounter lost future planning or automatically pulled it", out)
	}
}

func TestEncounterNewPatrolActuallyAttackingPartyJoinsCurrentQueue(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	o := fallbackObservation()
	o.Party = &actionsv1.EncounterParty{Group: fallbackRoster(true)}
	o.Players[fallbackGUID(fallbackLeader)] = fallbackPlayer(fallbackLeader)
	addFallbackTarget(o, fallbackNPC, fallbackLeader, 500)
	addFallbackTarget(o, fallbackNPC+1, 0, 100)
	o.ObservedOwnerFocus = fallbackNPC
	p.expectOffense(p.observe(o), "engaged_defense", fallbackNPC, 133)
	// The same NPC becomes an active add only when an actual party attack is
	// observed. Low health can then legitimately override ordinary guidance.
	addFallbackTarget(o, fallbackNPC+1, fallbackSelf, 100)
	out := p.expectOffense(p.observe(o), "engaged_defense", fallbackNPC+1, 133).Output
	if out["burstIntent"] == nil || out["nextPullGuid"] != nil {
		t.Fatal("actual add did not share current combat policy", out)
	}
}
