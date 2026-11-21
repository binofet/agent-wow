package world

import (
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/proto"
)

func TestEncounterExplicitPullUsesTheSameQueueWithoutInventingEngagement(t *testing.T) {
	p, obs := newFallbackPolicyTest(t, ""), fallbackObservation()
	addFallbackTarget(obs, fallbackNPC, 0, 100)
	p.expectNoAction(p.observe(obs))
	command := func(enabled bool) {
		t.Helper()
		if _, err := p.host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"task": "engage", "enabled": enabled, "targetGuid": fallbackGUID(fallbackNPC)}}); err != nil {
			t.Fatal(err)
		}
	}
	command(true)
	decision := p.decide(p.observe(obs))
	action, _ := decision.Output["action"].(map[string]any)
	if action["capability"] != "offense" || action["targetGuid"] != fallbackGUID(fallbackNPC) {
		t.Fatal("explicit Lua intent did not drive execution", decision.Output)
	}
	rows := decision.Output["queue"].([]any)
	row := rows[0].(map[string]any)
	if row["engaged"] != false || row["pullRequested"] != true || len(rows) != 1 {
		t.Fatal("explicit intent invented attack evidence or a second queue", rows)
	}
	obs.Targets[fallbackGUID(fallbackNPC)].Protected = true
	p.expectNoAction(p.observe(obs))
	obs.Targets[fallbackGUID(fallbackNPC)].Protected = false
	command(false)
	p.expectNoAction(p.observe(obs))
	// Canceling a voluntary pull cannot abandon an enemy actually attacking a
	// teammate, including when the owner has died.
	obs.Party = &actionsv1.EncounterParty{Group: fallbackRoster(true)}
	obs.Players[fallbackGUID(fallbackLeader)] = fallbackPlayer(fallbackLeader)
	obs.Players[fallbackGUID(fallbackLeader)].Alive = proto.Bool(false)
	addFallbackTarget(obs, fallbackNPC, fallbackLeader, 20)
	action, _ = p.decide(p.observe(obs)).Output["action"].(map[string]any)
	if action["capability"] != "offense" {
		t.Fatal("cancel request disabled actual defense", action)
	}
}

func TestEncounterRequestedFuturePullWaitsForCurrentFightAndExpiresOnVisitChange(t *testing.T) {
	p, obs := newFallbackPolicyTest(t, ""), fallbackObservation()
	const future = fallbackNPC + 1
	addFallbackTarget(obs, fallbackNPC, fallbackSelf, 100)
	addFallbackTarget(obs, future, 0, 100)
	if _, err := p.host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"task": "engage", "enabled": true, "targetGuid": fallbackGUID(future)}}); err != nil {
		t.Fatal(err)
	}
	action, _ := p.decide(p.observe(obs)).Output["action"].(map[string]any)
	if action["targetGuid"] != fallbackGUID(fallbackNPC) {
		t.Fatal("new pull displaced current fight", action)
	}
	obs.Targets[fallbackGUID(fallbackNPC)].Alive = proto.Bool(false)
	obs.Targets[fallbackGUID(fallbackNPC)].Health = 0
	action, _ = p.decide(p.observe(obs)).Output["action"].(map[string]any)
	if action["targetGuid"] != fallbackGUID(future) {
		t.Fatal("same queue lost explicit future intent", action)
	}
	obs.VisitId++
	p.expectNoAction(p.observe(obs))
}

func TestEncounterVoluntaryPullRetiresWhenItsOwnBodyDies(t *testing.T) {
	p, obs := newFallbackPolicyTest(t, ""), fallbackObservation()
	addFallbackTarget(obs, fallbackNPC, 0, 100)
	if _, err := p.host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"task": "engage", "enabled": true, "targetGuid": fallbackGUID(fallbackNPC)}}); err != nil {
		t.Fatal(err)
	}
	p.decide(p.observe(obs))
	obs.Players[fallbackGUID(fallbackSelf)].Alive = proto.Bool(false)
	p.expectNoAction(p.observe(obs))
	obs.Players[fallbackGUID(fallbackSelf)].Alive = proto.Bool(true)
	p.expectNoAction(p.observe(obs))
}
