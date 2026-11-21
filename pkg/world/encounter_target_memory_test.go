package world

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestEncounterKeepsPlanningQueueWithoutPullingIdleTargets(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	o := fallbackObservation()
	addFallbackTarget(o, fallbackNPC, 0, 500)
	addFallbackTarget(o, fallbackNPC+1, 0, 50)
	out := p.expectNoAction(p.observe(o))
	q := out["queue"].([]any)
	if len(q) != 2 || out["nextPullGuid"] != fallbackGUID(fallbackNPC+1) {
		t.Fatalf("missing ranked pre-pull queue: %+v", out)
	}
	addFallbackTarget(o, fallbackNPC, fallbackSelf, 500)
	p.expectOffense(p.observe(o), "engaged_defense", fallbackNPC, 133)
}

func TestEncounterKeepsFightingAfterOriginalVictimDiesAndReloads(t *testing.T) {
	p := newFallbackPolicyTest(t, "")
	o := fallbackObservation()
	o.Party = &actionsv1.EncounterParty{Group: fallbackRoster(true)}
	o.Players[fallbackGUID(fallbackLeader)] = fallbackPlayer(fallbackLeader)
	addFallbackTarget(o, fallbackNPC, fallbackLeader, 80)
	p.expectOffense(p.observe(o), "engaged_defense", fallbackNPC, 133)
	o.Players[fallbackGUID(fallbackLeader)].Alive = proto.Bool(false)
	o.Targets[fallbackGUID(fallbackNPC)].Engaged = false
	delete(o.Engagement, fallbackGUID(fallbackNPC))
	p.expectOffense(p.observe(o), "engaged_defense", fallbackNPC, 133)
	revision := p.host.Status().Revision
	if _, err := p.host.Reload(context.Background(), readEncounterBundle(t)+"\n-- memory survives policy edits\n", revision); err != nil {
		t.Fatal(err)
	}
	p.expectOffense(p.observe(o), "engaged_defense", fallbackNPC, 133)
	o.Targets[fallbackGUID(fallbackNPC)].Alive = proto.Bool(false)
	p.expectNoAction(p.observe(o))
	o.VisitId++
	o.Targets[fallbackGUID(fallbackNPC)].Alive = proto.Bool(true)
	p.expectNoAction(p.observe(o))
}

func TestEncounterResetLifecycleRetainsQueueAndDoesNotChaseAfterWipe(t *testing.T) {
	for _, mode := range []string{"full_reset", "runner", "unknown_flags", "injured", "changed_victim", "protected_flag", "protected_aura"} {
		t.Run(mode, func(t *testing.T) {
			p := newFallbackPolicyTest(t, "")
			o := fallbackObservation()
			o.Party = &actionsv1.EncounterParty{Group: fallbackRoster(true)}
			o.Players[fallbackGUID(fallbackLeader)] = fallbackPlayer(fallbackLeader)
			addFallbackTarget(o, fallbackNPC, fallbackLeader, 80)
			npc := o.Targets[fallbackGUID(fallbackNPC)]
			npc.UnitFlagsKnown, npc.UnitFlags = true, 0x80000
			p.expectOffense(p.observe(o), "engaged_defense", fallbackNPC, 133)
			// A victim death is not a reset, including when the victim field disappears.
			o.Players[fallbackGUID(fallbackLeader)].Alive = proto.Bool(false)
			npc.Engaged = false
			delete(o.Engagement, fallbackGUID(fallbackNPC))
			p.expectOffense(p.observe(o), "engaged_defense", fallbackNPC, 133)
			npc.Health, npc.UnitFlags = npc.MaxHealth, 0
			protected := false
			switch mode {
			case "runner":
				npc.UnitFlags = 0x80000 | 0x800000
			case "unknown_flags":
				npc.UnitFlagsKnown = false
			case "injured":
				npc.Health--
			case "changed_victim":
				npc.Engaged = true
				o.Engagement[fallbackGUID(fallbackNPC)] = &actionsv1.EncounterEngagement{VictimGuid: 17}
			case "protected_flag":
				npc.Protected, protected = true, true
			case "protected_aura":
				protected = true
				o.ProtectedAuras = []*actionsv1.ProtectedTarget{{TargetGuid: fallbackNPC, SpellId: 118, CasterGuid: fallbackSelf, Revision: 7}}
			}
			if mode != "full_reset" && !protected {
				p.expectOffense(p.observe(o), "engaged_defense", fallbackNPC, 133)
				return
			}
			out := p.expectNoAction(p.observe(o))
			queue := out["queue"].([]any)
			if len(queue) != 1 || queue[0].(map[string]any)["targetGuid"] != fallbackGUID(fallbackNPC) ||
				queue[0].(map[string]any)["engaged"] != protected || queue[0].(map[string]any)["protected"] != protected {
				t.Fatal("reset lost the planning target or erased CC engagement", out)
			}
			if protected {
				// Prove history survived CC: after CC ends, an injured target is still ours.
				npc.Protected, o.ProtectedAuras, npc.Health = false, nil, npc.MaxHealth-1
				p.expectOffense(p.observe(o), "engaged_defense", fallbackNPC, 133)
			} else {
				if out["nextPullGuid"] != fallbackGUID(fallbackNPC) || out["focusGuid"] != nil {
					t.Fatal("reset NPC was automatically repulled", out)
				}
				revision := p.host.Status().Revision
				if _, err := p.host.Reload(context.Background(), readEncounterBundle(t)+"\n-- reset lifecycle survives reload\n", revision); err != nil {
					t.Fatal(err)
				}
				p.expectNoAction(p.observe(o))
			}
		})
	}
}
