package world

import (
	"context"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func TestOrdinaryEncounterQuestContainerDetourHandsBackExistingFormation(t *testing.T) {
	p, obs, source := flightpointEncounter(t)
	obs.Targets = nil
	obs.Flightpath = nil
	obs.Navigation.FollowFormationSupported = true
	obs.Navigation.FollowOwner = obs.Players["3"]
	guid := fallbackGUID(uint64(0xf110)<<48 | uint64(124389)<<24 | 17)
	self := map[string]any{"guid": "4", "present": true, "aliveKnown": true, "alive": true, "healthKnown": true, "health": 100, "unitFlagsKnown": true, "mapKnown": true, "movementFlagsKnown": true, "positionKnown": true, "positionRevision": "4", "speedsKnown": true, "runSpeed": 7}
	owner := map[string]any{"guid": "3", "present": true, "aliveKnown": true, "alive": true, "unitFlagsKnown": true, "mapKnown": true, "positionKnown": true, "movementFlagsKnown": true, "x": 13, "y": 10, "z": 10}
	position := map[string]any{"known": true, "mapKnown": true, "x": 10, "y": 10, "z": 10, "revision": "9"}
	actions := map[string]any{"position": position}
	rows := make([]any, 25)
	for i := range rows {
		rows[i] = map[string]any{"slot": i}
	}
	rows[0].(map[string]any)["questId"] = 1360
	loot := map[string]any{"sessionId": fallbackSession, "selfGuid": "4", "visitId": "1", "self": self, "owner": owner, "actions": actions,
		"state": map[string]any{"enabled": true}, "inventoryKnown": true, "generalFreeSlots": 3, "inventoryRevision": "10", "itemCountsKnown": true,
		"questLogKnown": true, "questLogRevision": "5", "questLog": rows, "gatherCandidatesKnown": true, "gatherRecipes": []any{map[string]any{"objectEntry": 124389, "questId": 1360, "itemId": 8027, "count": 1, "openSpell": 6478}},
		"questObjects": []any{map[string]any{"guid": guid, "entry": 124389, "present": true, "typeKnown": true, "gameObjectType": 3, "mapKnown": true, "positionKnown": true, "positionRevision": "2", "flagsKnown": true, "x": 58, "y": 10, "z": 15}}}
	decide := func() behavior.Decision {
		t.Helper()
		seq, err := p.host.Enqueue(behavior.Event{Kind: "corpse_loot_observation", Value: loot})
		if err != nil {
			t.Fatal(err)
		}
		d, err := p.host.Decide(context.Background(), map[string]any{"kind": "encounter", "observationSequence": observeSparseEncounter(t, p, obs), "observationSequences": map[string]any{"corpse_loot": fallbackGUID(seq)}})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	first := decide()
	a, _ := first.Output["action"].(map[string]any)
	if a["capability"] != "approach_target" || a["targetGuid"] != guid || a["questItemId"] != float64(8027) || a["questOwnerGuid"] != "3" || a["questFollowRevision"] != "1" || first.Output["followFormation"] != nil {
		t.Fatal(first.Output)
	}
	id, err := p.host.ReserveAction(first)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.host.Enqueue(behavior.Event{Kind: "action_result", Receipt: &behavior.ActionReceipt{SessionID: fallbackSession, ActionID: id, Outcome: behavior.ActionSettledUnverified, Payload: map[string]any{"capability": "approach_target", "target_guid": guid, "visit_id": "1", "reconciliationRequired": true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.host.Reload(context.Background(), source, p.host.Status().Revision); err != nil {
		t.Fatal(err)
	}
	later := decide()
	result, _ := later.Output["corpseLoot"].(map[string]any)
	if later.Output["action"] != nil || later.Output["followFormation"] != nil || result["phase"] != "awaiting_confirmed_quest_movement" {
		t.Fatal("old unresolved trip replayed", later.Output)
	}
	self["positionRevision"] = "5"
	loot["itemCounts"] = []any{map[string]any{"itemId": 8027, "count": "1"}}
	last := decide()
	formation, _ := last.Output["followFormation"].(map[string]any)
	if last.Output["action"] != nil || formation["ownerGuid"] != "3" || formation["followRevision"] != "1" {
		t.Fatal("normal role did not resume unchanged Follow", last.Output)
	}
}
