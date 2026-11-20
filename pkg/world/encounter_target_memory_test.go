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
