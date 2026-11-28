package behavior

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func followGoalSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "behaviors", "encounter-movement.lua"))
	if err != nil {
		t.Fatal(err)
	}
	return "local movement=(function()\n" + string(raw) + "\nend)()\n" +
		`return {decide=function(frame)
    assert(frame.session_id==frame.input.observation.sessionId)
    if frame.input.restore then assert(movement.import_state(frame.input.restore)) end
    return {goal=movement.follow_goal(frame.input.observation)}
end,on_event=movement.on_event,
export_state=function() return {version=1,data=movement.export_state()} end,
import_state=function(saved) return movement.import_state(saved.data) end}`
}

func followGoalObservation() map[string]any {
	return map[string]any{"sessionId": "follow-body-a", "selfGuid": "4", "visitId": "1", "mapId": 0,
		"navigation": map[string]any{"following": false, "followRevision": "0", "ownerGuid": "3"},
		"party":      map[string]any{"group": map[string]any{"known": true, "members": []any{map[string]any{"guid": "3", "online": true}}}}}
}

func decideFollowGoal(t *testing.T, s *Session, obs map[string]any, active bool, phase string) map[string]any {
	t.Helper()
	result, err := s.Decide(context.Background(), map[string]any{"observation": obs})
	if err != nil {
		t.Fatal(err)
	}
	g, ok := result.Output["goal"].(map[string]any)
	if !ok || g["targetGuid"] != "3" || g["selfGuid"] != obs["selfGuid"] || g["active"] != active || g["phase"] != phase {
		t.Fatalf("follow goal: active=%v phase=%s output=%v", active, phase, result.Output)
	}
	return g
}

func TestFollowGoalKeepsOnlineOwnerAcrossInvisibleMapsAndLeaseExpiry(t *testing.T) {
	s, err := NewSession(context.Background(), "follow-body-a", followGoalSource(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	obs := followGoalObservation()
	nav := obs["navigation"].(map[string]any)
	// No native Follow command is required, and absence is not an offline fact.
	decideFollowGoal(t, s, obs, true, "reacquiring_owner")
	nav["following"], nav["followRevision"] = true, "1"
	nav["followOwner"] = map[string]any{"guid": "3", "present": true, "mapKnown": true, "mapId": 0,
		"positionKnown": true, "positionRevision": "9", "x": 10, "y": 20, "z": 3}
	decideFollowGoal(t, s, obs, true, "owner_visible")
	// The native lease expired without any new command. The parent goal remains.
	nav["following"] = false
	nav["followOwner"] = nil
	obs["visitId"], obs["mapId"] = "2", 1
	g := decideFollowGoal(t, s, obs, true, "reacquiring_owner")
	if g["lastSeen"].(map[string]any)["positionRevision"] != "9" {
		t.Fatal("map or expired lease discarded the intent/history", g)
	}
	// A partial party observation cannot mean that a still-online owner logged off.
	delete(obs, "party")
	decideFollowGoal(t, s, obs, true, "reacquiring_owner")
}

func TestFollowGoalOnlineDefaultIsIndependentOfNativeStopAndResume(t *testing.T) {
	s, err := NewSession(context.Background(), "follow-body-a", followGoalSource(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	obs := followGoalObservation()
	nav := obs["navigation"].(map[string]any)
	decideFollowGoal(t, s, obs, true, "reacquiring_owner")
	nav["followRevision"] = "1" // Fences the earlier native action, not the Lua parent goal.
	decideFollowGoal(t, s, obs, true, "reacquiring_owner")
	obs["visitId"], obs["mapId"] = "2", 1
	decideFollowGoal(t, s, obs, true, "reacquiring_owner")
	nav["followRevision"], nav["following"] = "2", true
	decideFollowGoal(t, s, obs, true, "reacquiring_owner")
	member := obs["party"].(map[string]any)["group"].(map[string]any)["members"].([]any)[0].(map[string]any)
	member["online"] = false
	decideFollowGoal(t, s, obs, false, "owner_offline")
	savedParty := obs["party"]
	delete(obs, "party")
	decideFollowGoal(t, s, obs, false, "owner_offline")
	obs["party"] = savedParty
	member["online"] = true
	decideFollowGoal(t, s, obs, true, "reacquiring_owner")
}

func TestFollowGoalPersistsOnlineHistoryAndSightingThroughReloadAndReconnect(t *testing.T) {
	ctx := context.Background()
	source := followGoalSource(t)
	s, err := NewSession(ctx, "follow-body-a", source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	obs := followGoalObservation()
	nav := obs["navigation"].(map[string]any)
	nav["followOwner"] = map[string]any{"guid": "3", "present": true, "mapKnown": true, "mapId": 0,
		"positionKnown": true, "positionRevision": "12", "x": 10, "y": 20, "z": 3}
	decideFollowGoal(t, s, obs, true, "owner_visible")
	member := obs["party"].(map[string]any)["group"].(map[string]any)["members"].([]any)[0].(map[string]any)
	member["online"] = false
	decideFollowGoal(t, s, obs, false, "owner_offline")
	revision := s.Status().Revision
	if _, err = s.Reload(ctx, source+"\n-- new authored revision\n", revision); err != nil {
		t.Fatal(err)
	}
	state, err := s.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reconnected, err := NewSession(ctx, "follow-body-b", source)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close()
	obs["sessionId"], obs["visitId"], obs["mapId"] = "follow-body-b", "1", 1
	nav["followRevision"] = "0"
	nav["followOwner"] = nil
	imported, err := reconnected.Decide(ctx, map[string]any{"observation": obs, "restore": state.Checkpoint["data"]})
	if err != nil {
		t.Fatal(err)
	}
	g := imported.Output["goal"].(map[string]any)
	if g["active"] != false || g["phase"] != "owner_offline" {
		t.Fatal("reconnect lost online history", g)
	}
	if g["lastSeen"].(map[string]any)["positionRevision"] != "12" {
		t.Fatal("lost historical sighting", g)
	}
	member["online"] = true
	decideFollowGoal(t, reconnected, obs, true, "reacquiring_owner")
}

func TestFollowGoalDoesNotLatchLegacyLeaseOrCounter(t *testing.T) {
	for _, rev := range []string{"0", "1", "17", "18446744073709551614"} {
		t.Run(rev, func(t *testing.T) {
			s, err := NewSession(context.Background(), "follow-body-a", followGoalSource(t))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			obs := followGoalObservation()
			nav := obs["navigation"].(map[string]any)
			nav["followRevision"] = rev
			result, err := s.Decide(context.Background(), map[string]any{"observation": obs,
				"restore": map[string]any{"identity": "follow-body-a:4:8:" + rev}})
			if err != nil {
				t.Fatal(err)
			}
			g := result.Output["goal"].(map[string]any)
			if g["active"] != true {
				t.Fatal("legacy counter suppressed the online default", g)
			}
		})
	}
}

func TestFollowGoalVisibleOwnerOutsidePartyAndIndependentMode(t *testing.T) {
	s, err := NewSession(context.Background(), "follow-body-a", followGoalSource(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	obs := followGoalObservation()
	delete(obs, "party")
	nav := obs["navigation"].(map[string]any)
	nav["followOwner"] = map[string]any{"guid": "3", "present": true, "mapKnown": true, "mapId": 0,
		"positionKnown": true, "positionRevision": "9", "x": 10, "y": 20, "z": 3}
	g := decideFollowGoal(t, s, obs, true, "owner_visible")
	if g["ownerOnline"] != true || g["onlineKnown"] != true {
		t.Fatal("lost positive non-party owner fact", g)
	}
	nav["independent"] = true
	decideFollowGoal(t, s, obs, false, "independent")
}

func TestFollowGoalCapturesObservationsWithoutAnIdleDecision(t *testing.T) {
	s, err := NewSession(context.Background(), "follow-body-a", followGoalSource(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	obs := followGoalObservation()
	member := obs["party"].(map[string]any)["group"].(map[string]any)["members"].([]any)[0].(map[string]any)
	member["online"] = false
	if _, err = s.Enqueue(Event{Kind: "encounter_observation", Value: obs}); err != nil {
		t.Fatal(err)
	}
	if err = s.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	delete(obs, "party")
	decideFollowGoal(t, s, obs, false, "owner_offline")
}

func TestFollowGoalKeepsRealSightingsAndRejectsAnotherSelf(t *testing.T) {
	ctx := context.Background()
	source := followGoalSource(t)
	s, err := NewSession(ctx, "follow-body-a", source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	obs := followGoalObservation()
	nav := obs["navigation"].(map[string]any)
	owner := map[string]any{"guid": "3", "present": true, "mapKnown": true, "mapId": 0,
		"positionKnown": true, "positionRevision": "12", "x": 3, "y": 4, "z": 5}
	nav["followOwner"] = owner
	decideFollowGoal(t, s, obs, true, "owner_visible")
	owner["positionRevision"], owner["positionInferred"], owner["x"] = "13", true, 90
	g := decideFollowGoal(t, s, obs, true, "owner_visible")
	if g["lastSeen"].(map[string]any)["positionRevision"] != "12" {
		t.Fatal("inferred pose replaced real sighting", g)
	}
	member := obs["party"].(map[string]any)["group"].(map[string]any)["members"].([]any)[0].(map[string]any)
	member["online"] = false
	decideFollowGoal(t, s, obs, false, "owner_offline")
	party := obs["party"]
	delete(obs, "party")
	owner["positionInferred"] = false
	owner["positionRevision"], owner["x"] = "12", 3
	decideFollowGoal(t, s, obs, false, "owner_offline")
	owner["positionRevision"] = "14"
	decideFollowGoal(t, s, obs, true, "owner_visible")
	obs["party"] = party
	decideFollowGoal(t, s, obs, false, "owner_offline")
	state, err := s.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewSession(ctx, "other-self", source)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	obs["sessionId"], obs["selfGuid"] = "other-self", "15"
	member["online"] = true
	nav["followOwner"] = nil
	result, err := other.Decide(ctx, map[string]any{"observation": obs, "restore": state.Checkpoint["data"]})
	if err != nil {
		t.Fatal(err)
	}
	g = result.Output["goal"].(map[string]any)
	if g["active"] != true || g["selfGuid"] != "15" || g["lastSeen"] != nil {
		t.Fatal("another character inherited the follow history", g)
	}
}
