package modrt

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"google.golang.org/protobuf/types/known/structpb"
)

// Reuse the maintained full-bundle/real-Manager harness. These pure reads must
// leave the accepted Session checkpoint, queued ingress and native facts alone.
func practiceLifecycleCallback(t *testing.T, x *thermapluggPolicyFixture, input map[string]any) map[string]any {
	t.Helper()
	before, err := x.host.vm.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	original, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := starterSpellHostRequest(t, map[string]any{"sessionId": x.sid, "selfGuid": "4", "input": input})
	response, err := x.manager.invokeModule(context.Background(), actionsModule, request)
	if err != nil {
		t.Fatal(err)
	}
	assertFollowFallbackState(t, x.host, before)
	after, err := json.Marshal(input)
	if err != nil || string(original) != string(after) {
		t.Fatal("pure lifecycle mutated supplied facts", err)
	}
	value := &structpb.Struct{}
	if response.GetResult().UnmarshalTo(value) != nil {
		t.Fatal("lost typed lifecycle response", response)
	}
	fields := value.AsMap()
	if fields["available"] != true || fields["revision"] != x.host.vm.Status().Revision {
		t.Fatal("lifecycle returned another accepted source", fields)
	}
	out, ok := fields["decision"].(map[string]any)
	if !ok {
		t.Fatal("missing lifecycle decision", fields)
	}
	return out
}

func practiceLifecycleRuntimeInput(t *testing.T, sid string) map[string]any {
	t.Helper()
	self := &worldv1.PlayerState{Guid: 4, Present: true, ClassKnown: true, ClassId: 8, LevelKnown: true, Level: 1,
		MapKnown: true, PositionKnown: true, X: -8933, Y: -136, Z: 81, HealthKnown: true, Health: 94, MaxHealth: 100,
		PowerKnown: true, Power: 100, MaxPower: 100, BaseManaKnown: true, BaseMana: 100}
	target := &worldv1.CreatureState{Guid: 0xf13000012b000001, Entry: 299, Present: true, MapKnown: true, PositionKnown: true,
		X: -8928, Y: -136, Z: 81, LevelKnown: true, Level: 1, HealthKnown: true, Health: 55, MaxHealth: 55, AliveKnown: true, Alive: true}
	return map[string]any{"selection": "practice_lifecycle", "stage": "tick", "sessionId": sid, "selfGuid": "4", "visitId": "1", "nowMs": float64(1000000),
		"self": thermapluggPolicyJSON(t, self), "selected": nil, "nearby": []any{thermapluggPolicyJSON(t, target)}, "questLogKnown": false, "questLog": []any{},
		"clientPosition": map[string]any{"x": float64(-8933), "y": float64(-136), "z": float64(81)},
		"practice":       map[string]any{"enabled": true, "targetGuid": "0", "pullStarted": false, "questGoal": false, "phase": "waiting_for_safe_pull", "remainingMs": float64(600000)},
		"spells":         map[string]any{"fireball": true, "smite": false, "autoAttack": false, "sinisterStrike": false},
		"attack":         map[string]any{"active": false, "unconfirmed": false, "failedTargetGuid": "0"}}
}

func TestPolicyPracticeLifecycleFullBundleSameSessionReadinessCASAndOwnedFinish(t *testing.T) {
	x := newThermapluggPolicyFixture(t)
	input := practiceLifecycleRuntimeInput(t, x.sid)
	if out := practiceLifecycleCallback(t, x, input); len(out) != 6 || out["kind"] != "wait" || out["phase"] != "recovering_before_next_pull" {
		t.Fatal(out)
	}
	initial := x.host.vm.Status().Revision
	x.reload(t, "M.practice_pull={minimum_health=95", "M.practice_pull={minimum_health=90")
	if out := practiceLifecycleCallback(t, x, input); out["kind"] != "select" || out["targetGuid"] != input["nearby"].([]any)[0].(map[string]any)["guid"] {
		t.Fatal("accepted readiness edit was not used", out)
	}
	near := input["nearby"].([]any)[0].(map[string]any)
	far := make(map[string]any, len(near))
	for key, value := range near {
		far[key] = value
	}
	far["guid"], far["x"] = "17379390969546588162", float64(-8920)
	input["nearby"] = []any{near, far}
	x.reload(t, "lease_ms=600000,prefer_nearest=true", "lease_ms=600000,prefer_nearest=false")
	if out := practiceLifecycleCallback(t, x, input); out["targetGuid"] != far["guid"] {
		t.Fatal("accepted target-ranking edit was not used", out)
	}
	if _, err := x.host.Reload(context.Background(), x.source, initial); !errors.Is(err, behavior.ErrRevisionConflict) {
		t.Fatal("stale lifecycle CAS accepted", err)
	}
	self := input["self"].(map[string]any)
	selected := input["nearby"].([]any)[0].(map[string]any)
	p := input["practice"].(map[string]any)
	input["selected"] = selected
	p["targetGuid"], p["pullStarted"] = selected["guid"], true
	self["level"], self["unitFlags"] = float64(2), float64(0x80000)
	selected["attackVictimKnown"], selected["attackVictimGuid"], selected["attackAgeMs"] = true, "4", "30000"
	if out := practiceLifecycleCallback(t, x, input); out["kind"] != "continue" || out["finishing"] != true || out["phase"] != "finishing_current_fight_to_level2" {
		t.Fatal("lost actual owned survivor", out)
	}
	selected["attackAgeMs"] = "30001"
	if out := practiceLifecycleCallback(t, x, input); out["kind"] != "stop" || out["phase"] != "level_two_observed" {
		t.Fatal("stale attack invented owned engagement", out)
	}
}

func TestPolicyColdridgeLifecycleFullBundlePriorityAndObjectiveCAS(t *testing.T) {
	x := newThermapluggPolicyFixture(t)
	input := practiceLifecycleRuntimeInput(t, x.sid)
	input["selection"], input["stage"] = "coldridge_lifecycle", "start"
	self := input["self"].(map[string]any)
	self["classId"], self["level"] = float64(9), float64(2)
	rows := make([]any, 25)
	for i := range rows {
		rows[i] = thermapluggPolicyJSON(t, &worldv1.QuestLogEntry{Slot: uint32(i)})
	}
	rows[0] = thermapluggPolicyJSON(t, &worldv1.QuestLogEntry{QuestId: 179})
	rows[1] = thermapluggPolicyJSON(t, &worldv1.QuestLogEntry{Slot: 1, QuestId: 170, ObjectiveCounts: []uint32{0, 0, 0, 0}})
	input["questLogKnown"], input["questLog"] = true, rows
	input["goal"] = map[string]any{"questId": float64(0), "counts": []any{float64(0), float64(0)}, "neededEntries": []any{}}
	input["targetGuid"], input["pullStarted"], input["nearby"] = "0", false, []any{}
	if out := practiceLifecycleCallback(t, x, input); len(out) != 11 || out["kind"] != "start" || out["questId"] != float64(179) {
		t.Fatal(out)
	}
	x.reload(t, "priority={179,170,183,182}", "priority={170,179,183,182}")
	if out := practiceLifecycleCallback(t, x, input); out["kind"] != "start" || out["questId"] != float64(170) {
		t.Fatal("accepted quest-priority edit was not used", out)
	}
	input["stage"] = "observe"
	input["goal"] = map[string]any{"questId": float64(170), "counts": []any{float64(0), float64(0)}, "neededEntries": []any{float64(707), float64(724)}}
	rows[1].(map[string]any)["objectiveCounts"] = []any{float64(3), float64(0), float64(0), float64(0)}
	x.reload(t, "[170]={{entry=707,count=6},{entry=724,count=6}}", "[170]={{entry=707,count=3},{entry=724,count=6}}")
	out := practiceLifecycleCallback(t, x, input)
	entries, ok := out["neededEntries"].([]any)
	if !ok || len(entries) != 1 || entries[0] != float64(724) || out["active"] != true || out["needed"] != true {
		t.Fatal("objective edit failed or invented overall completion", out)
	}
	input["goal"].(map[string]any)["counts"] = []any{float64(3), float64(0)}
	rows[1].(map[string]any)["objectiveCounts"] = []any{float64(2), float64(0), float64(0), float64(0)}
	if out := practiceLifecycleCallback(t, x, input); out["active"] != false || out["needed"] != false {
		t.Fatal("regressed own counters remained authority", out)
	}
}
