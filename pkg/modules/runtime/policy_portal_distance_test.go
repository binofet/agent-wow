package modrt

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPolicyPortalDistanceReloadUpdatesCurrentSourceChoices(t *testing.T) {
	const sid = "portal-distance-policy"
	const defaultDistance = "local portal_approach_yards = 45"
	ctx := context.Background()
	source := followFallbackBundle(t)
	if !strings.Contains(source, defaultDistance) {
		t.Fatal("complete bundle did not contain the editable portal approach distance")
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
		raw, err := manager.InvokeJSON(ctx, "session.policy_source", json.RawMessage(`{"sessionId":"portal-distance-policy","characterGuid":"4"}`))
		if err != nil {
			t.Fatal(err)
		}
		var view policySourceResponse
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		if view.Source != host.source || view.Revision != revision || view.FollowFallback.Revision != revision ||
			view.FollowFallback.EntryPoint != "follow_formation" || !view.FollowFallback.Available {
			t.Fatal("source metadata lost the current portal distance revision", view.FollowFallback)
		}
	}
	inputAt := func(mode string, x, z float64) map[string]any {
		input := followFallbackTestInput(mode)
		owner := input["owner"].(map[string]any)
		owner["x"], owner["y"], owner["z"] = x, float64(0), z
		return input
	}
	evaluate := func(input map[string]any) map[string]any {
		t.Helper()
		available, gotRevision, out, err := host.followDecision(ctx, input)
		if err != nil || !available || gotRevision != revision {
			t.Fatal("current portal distance source was unavailable", available, gotRevision, out, err)
		}
		return out
	}
	sighting := func(x, z float64, catchingUp bool) {
		t.Helper()
		out := evaluate(inputAt("portal_sighting", x, z))
		if out["catchingUp"] != catchingUp {
			t.Fatalf("wrong 3D sighting classification at (%v,0,%v): %+v", x, z, out)
		}
	}
	direct := func(x, z float64, catchingUp, eligible bool) {
		t.Helper()
		input := inputAt("portal_follow", x, z)
		input["portal"] = map[string]any{
			"sightingAgeKnown": true, "sightingAgeMs": float64(1000),
			"disappearanceAgeKnown": true, "disappearanceAgeMs": float64(1000),
			"catchingUp": catchingUp, "ownerAbsent": true, "following": true,
			"sourceVisitId": "7", "currentVisitId": "7", "followRevision": "3", "currentFollowRevision": "3",
		}
		out := evaluate(input)
		if out["eligible"] != eligible || out["ready"] != eligible {
			t.Fatalf("wrong current 3D direct approach at (%v,0,%v), catchup=%t: %+v", x, z, catchingUp, out)
		}
	}
	candidates := func(x, z float64, count int) {
		t.Helper()
		out := evaluate(inputAt("portal_catch_up", x, z))
		rows, _ := out["candidates"].([]any)
		if len(rows) != count || rows[0].(map[string]any)["x"] != x || rows[0].(map[string]any)["z"] != z {
			t.Fatalf("wrong portal candidate expansion at (%v,0,%v): %+v", x, z, out)
		}
		if count == 7 {
			first := rows[1].(map[string]any)
			last := rows[6].(map[string]any)
			if first["x"] != float64(8) || first["y"] != float64(-6) || first["z"] != float64(3) ||
				last["x"] != float64(16) || last["y"] != float64(6) || last["z"] != float64(3) {
				t.Fatal("distance tuning changed the existing ordered bend geometry", out)
			}
		}
	}
	before, err := host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	readMetadata()
	sighting(45, 3, false)
	sighting(45.001, 3, true)
	sighting(30, 43, true) // Horizontal distance 30, three-dimensional distance 50.
	direct(45, 3, false, true)
	direct(45.001, 3, false, false)
	direct(30, 43, false, false)
	direct(100, 3, true, true)
	candidates(50, 3, 7)
	candidates(30, 43, 1) // Candidate expansion retains its horizontal geometry.
	assertFollowFallbackState(t, host, before)

	reloadDistance := func(literal string) {
		t.Helper()
		changed := strings.Replace(source, defaultDistance, literal, 1)
		revision, err = host.Reload(ctx, changed, revision)
		if err != nil || host.vm.Status().SessionID != sid {
			t.Fatal("portal distance reload changed the connected policy session", revision, err)
		}
		readMetadata()
	}
	reloadDistance("local portal_approach_yards = 90")
	atNinety, err := host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sighting(50, 3, false)
	sighting(30, 43, false)
	sighting(90, 3, false)
	sighting(90.001, 3, true)
	direct(50, 3, false, true)
	direct(30, 43, false, true)
	direct(90.001, 3, false, false)
	candidates(50, 3, 1)
	assertFollowFallbackState(t, host, atNinety)

	reloadDistance("local portal_approach_yards = 20")
	atTwenty, err := host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sighting(30, 3, true)
	direct(30, 3, false, false)
	candidates(30, 3, 7)
	// An already captured catch-up mode remains the observed mode while its
	// owner is absent. The new near-distance cap applies to direct approaches.
	direct(100, 3, true, true)
	assertFollowFallbackState(t, host, atTwenty)
	if atTwenty.Status.Cursor != 0 || atTwenty.Status.Buffered != 0 || atTwenty.Status.Pending != 0 ||
		atTwenty.LatestDecision != nil {
		t.Fatal("pure portal distance reads ran persistent hooks", atTwenty)
	}
}
