package modrt

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/agent-wow/agent-wow/pkg/behavior"
)

func TestPolicyFargodeepReadinessRealBundleUsesCurrentAcceptedObjectives(t *testing.T) {
	ctx := context.Background()
	const sid = "fargodeep-readiness-bundle"
	const selection = "fargodeep_pull_readiness"
	bundle, err := exec.Command("python3", "-B", filepath.Join("..", "..", "..", "dev", "encounter-bundle")).Output()
	if err != nil {
		t.Fatal(err)
	}
	source := string(bundle)
	manager := catalogManager(t, 4, sid)
	t.Cleanup(manager.cancel)
	// Use the actual persistent host defaults. The separate normal CLI proof
	// covers the standalone five-millisecond evaluator, without overriding it.
	host, err := manager.StartPolicy(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	session := host.vm
	player := func(guid string, class, health, powerType, power, maximum, base uint32, baseKnown bool) map[string]any {
		return map[string]any{"guid": guid, "classKnown": true, "classId": class,
			"healthKnown": true, "health": health, "maxHealth": uint32(100),
			"powerKnown": true, "powerType": powerType, "power": power, "maxPower": maximum,
			"baseManaKnown": baseKnown, "baseMana": base}
	}
	input := map[string]any{"sessionId": sid, "selfGuid": "4", "selection": selection, "autoAttackKnown": false,
		"observation": map[string]any{"sessionId": sid, "selfGuid": "4", "visitId": "19", "worldObservationRevision": "1",
			"players": map[string]any{
				"1": player("1", 5, 94, 0, 128, 128, 100, true),
				"4": player("4", 8, 100, 0, 128, 128, 100, true),
				"5": player("5", 4, 100, 3, 100, 100, 0, false)}},
		"mission": map[string]any{"partySessionId": "observed-fargodeep-party", "engagerGuid": "4",
			"targetGuid": strconv.FormatUint(0xf130000000000000|40<<24|111, 10)}}
	observed, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enqueue(behavior.Event{Kind: "fixture_readiness_pending", Value: map[string]any{"note": "buffered"}}); err != nil {
		t.Fatal(err)
	}
	initialState, err := host.vm.State(ctx)
	if err != nil || initialState.Status.Buffered != 1 || initialState.Status.Cursor != 0 || len(initialState.Pending) != 0 {
		t.Fatal("fixture did not retain buffered ingress before the pure export", initialState, err)
	}
	check := func(revision, kind string) {
		t.Helper()
		before, err := host.vm.State(ctx)
		if err != nil {
			t.Fatal(err)
		}
		available, actual, decision, err := host.starterSpellDecision(ctx, input)
		if err != nil || !available || actual != revision || len(decision) != 3 ||
			decision["kind"] != kind || decision["spellId"] != float64(0) || decision["selection"] != selection {
			t.Fatal("real bundled export lost the current shared readiness decision", available, actual, decision, err)
		}
		assertFollowFallbackState(t, host, before)
		current, err := json.Marshal(input)
		if err != nil || string(current) != string(observed) || host.vm != session || host.vm.Status().SessionID != sid {
			t.Fatal("readiness read changed its input, host or body", err)
		}
	}
	metadata := func(revision string, available bool) {
		t.Helper()
		raw, err := manager.InvokeJSON(ctx, "session.policy_source", json.RawMessage(`{"sessionId":"`+sid+`","characterGuid":"4"}`))
		var view policySourceResponse
		if err != nil || json.Unmarshal(raw, &view) != nil || view.Revision != revision ||
			view.StarterSpell.EntryPoint != policyStarterSpellEntryPoint || view.StarterSpell.Revision != revision ||
			view.StarterSpell.Available != available {
			t.Fatal("typed source metadata lost current readiness export identity", string(raw), err)
		}
	}
	initial := host.vm.Status().Revision
	check(initial, "wait")
	assertFollowFallbackState(t, host, initialState)
	metadata(initial, true)
	if strings.Count(source, "M.fargodeep_pull={minimum_health=95") != 1 {
		t.Fatal("actual bundled objective tuning is not uniquely editable")
	}
	edited := strings.Replace(source, "M.fargodeep_pull={minimum_health=95", "M.fargodeep_pull={minimum_health=94", 1)
	revision, err := host.Reload(ctx, edited, initial)
	if err != nil || revision == initial || host.vm != session || host.vm.Status().SessionID != sid {
		t.Fatal("same-host objective tuning CAS failed", revision, err)
	}
	check(revision, "ready") // All observations, including the ally's94 health, are unchanged.
	metadata(revision, true)
	accepted := host.starterSpell.Load()
	beforeStale, err := host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Reload(ctx, source, initial); !errors.Is(err, behavior.ErrRevisionConflict) || host.starterSpell.Load() != accepted {
		t.Fatal("stale CAS replaced the accepted readiness source", err)
	}
	assertFollowFallbackState(t, host, beforeStale)
	check(revision, "ready")
	const export = "starter_spell=starter_spells and function(input)"
	if strings.Count(edited, export) != 1 {
		t.Fatal("actual bundled starter export is not uniquely removable")
	}
	absent := strings.Replace(edited, export, "starter_spell_absent=starter_spells and function(input)", 1)
	absentRevision, err := host.Reload(ctx, absent, revision)
	if err != nil || absentRevision == revision {
		t.Fatal("accepted missing-export edit failed", absentRevision, err)
	}
	for range 2 {
		before, err := host.vm.State(ctx)
		if err != nil {
			t.Fatal(err)
		}
		available, actual, decision, err := host.starterSpellDecision(ctx, input)
		if err != nil || available || actual != absentRevision || decision != nil {
			t.Fatal("missing current export restored an older ready decision", available, actual, decision, err)
		}
		assertFollowFallbackState(t, host, before)
	}
	metadata(absentRevision, false)
}
