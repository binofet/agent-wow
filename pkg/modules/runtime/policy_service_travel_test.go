package modrt

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPolicyServiceTravelReloadUpdatesBothArrivalKinds(t *testing.T) {
	const sid = "service-travel-policy"
	const defaultArrival = "local service_arrival_yards = 3"
	ctx := context.Background()
	source := followFallbackBundle(t)
	if !strings.Contains(source, defaultArrival) {
		t.Fatal("complete bundle did not contain the editable service arrival distance")
	}
	manager := catalogManager(t, 4, sid)
	t.Cleanup(manager.cancel)
	host, err := manager.StartPolicy(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	revision := host.vm.Status().Revision
	readMetadata := func() {
		t.Helper()
		raw, err := manager.InvokeJSON(ctx, "session.policy_source", json.RawMessage(`{"sessionId":"service-travel-policy","characterGuid":"4"}`))
		if err != nil {
			t.Fatal(err)
		}
		var view policySourceResponse
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		if view.Source != host.source || view.Revision != revision || view.FollowFallback.Revision != revision ||
			view.FollowFallback.EntryPoint != "follow_formation" || !view.FollowFallback.Available {
			t.Fatal("source metadata lost the current service arrival revision", view.FollowFallback)
		}
	}
	inputAt := func(x, height float64, travel map[string]any) map[string]any {
		input := followFallbackTestInput("service_travel")
		owner := input["owner"].(map[string]any)
		owner["x"], owner["y"], owner["z"] = x, float64(0), float64(3)+height
		input["travel"] = travel
		input["memory"] = map[string]any{"marker": "existing-formation", "values": []any{float64(7), "preserved"}}
		return input
	}
	evaluate := func(input map[string]any) map[string]any {
		t.Helper()
		available, gotRevision, out, err := host.followDecision(ctx, input)
		if err != nil || !available || gotRevision != revision {
			t.Fatal("current service arrival source was unavailable", available, gotRevision, out, err)
		}
		wantMemory := map[string]any{"marker": "existing-formation", "values": []any{float64(7), "preserved"}}
		if !reflect.DeepEqual(input["memory"], wantMemory) {
			t.Fatal("service arrival changed caller-owned formation memory", input["memory"])
		}
		return out
	}
	check := func(kind string, x, height float64, routeKnown bool, remainingRoute float64, want bool) {
		t.Helper()
		out := evaluate(inputAt(x, height, map[string]any{
			"kind": kind, "routeKnown": routeKnown, "remainingRoute": remainingRoute,
		}))
		arrived, known := out["arrived"].(bool)
		if !known || arrived != want {
			t.Fatalf("wrong strict arrival for %s at distance (%v,0,%v), routeKnown=%t remaining=%v: %+v",
				kind, x, height, routeKnown, remainingRoute, out)
		}
	}
	before, err := host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	readMetadata()
	for _, kind := range []string{"vendor", "trainer"} {
		check(kind, 3, 0, false, 0, true)
		check(kind, 3.001, 0, false, 0, false)
		check(kind, 0, 3, false, 0, true)
		check(kind, 0, 3.001, false, 0, false)
		check(kind, 1, 0, true, 10, false) // A close endpoint can still have a long route around a bend.
		check(kind, 3, 0, true, 3, true)
		check(kind, 1, 0, true, 3.001, false)
		check(kind, 1, 0, false, 999, true)
		if out := evaluate(inputAt(1, 0, map[string]any{"kind": kind, "routeKnown": false})); out["arrived"] != true {
			t.Fatal("an initial direct-distance check required an unavailable route length", kind, out)
		}
	}
	for _, travel := range []map[string]any{
		nil,
		{"kind": "quest", "routeKnown": false},
		{"kind": "vendor"},
		{"kind": "vendor", "routeKnown": "false"},
		{"kind": "trainer", "routeKnown": true},
		{"kind": "vendor", "routeKnown": true, "remainingRoute": float64(-1)},
		{"kind": "trainer", "routeKnown": true, "remainingRoute": "1"},
		{"kind": "vendor", "routeKnown": false, "remainingRoute": float64(-1)},
		{"kind": "trainer", "routeKnown": false, "remainingRoute": "1"},
	} {
		if out := evaluate(inputAt(0, 0, travel)); len(out) != 0 {
			t.Fatal("malformed service travel facts invented an arrival decision", travel, out)
		}
	}
	assertFollowFallbackState(t, host, before)

	reloadArrival := func(literal string) {
		t.Helper()
		changed := strings.Replace(source, defaultArrival, literal, 1)
		revision, err = host.Reload(ctx, changed, revision)
		if err != nil || host.vm.Status().SessionID != sid {
			t.Fatal("service arrival reload changed the connected policy session", revision, err)
		}
		readMetadata()
	}
	reloadArrival("local service_arrival_yards = 6")
	atSix, err := host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"vendor", "trainer"} {
		check(kind, 5, 0, true, 5, true)
		check(kind, 5, 0, false, 999, true)
		check(kind, 1, 0, true, 6, true)
		check(kind, 1, 0, true, 6.001, false)
		check(kind, 0, 6, false, 0, true)
		check(kind, 0, 6.001, false, 0, false)
	}
	assertFollowFallbackState(t, host, atSix)

	reloadArrival("local service_arrival_yards = 2")
	atTwo, err := host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"vendor", "trainer"} {
		check(kind, 2, 0, true, 2, true)
		check(kind, 2.001, 0, true, 2, false)
		check(kind, 1, 0, true, 2.001, false)
		check(kind, 2, 0, false, 999, true)
		check(kind, 3, 0, false, 0, false)
	}
	assertFollowFallbackState(t, host, atTwo)
	if atTwo.Status.Cursor != 0 || atTwo.Status.Buffered != 0 || atTwo.Status.Pending != 0 ||
		atTwo.LatestDecision != nil {
		t.Fatal("pure service arrival reads ran persistent hooks", atTwo)
	}
}
