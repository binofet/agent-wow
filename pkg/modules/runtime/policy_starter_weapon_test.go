package modrt

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestPolicyStarterWeaponRealBundleUsesAcceptedSharedFamilyWeightEdit(t *testing.T) {
	ctx := context.Background()
	const sid = "starter-weapon-bundle"
	source := followFallbackBundle(t) // Execute the maintained dev/encounter-bundle composer.
	manager := catalogManager(t, 4, sid)
	t.Cleanup(manager.cancel)
	host, err := manager.StartPolicy(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	observation := &actionsv1.EncounterObservationResponse{SessionId: sid, SelfGuid: 4, VisitId: 19,
		WorldObservationRevision: 1, SpellbookRevision: 1, SpellbookKnown: true,
		Players: map[string]*actionsv1.EncounterPlayer{"4": {Guid: 4, ClassKnown: true, ClassId: 4,
			PowerKnown: true, PowerType: 3, Power: 100, MaxPower: 100, PositionKnown: true}},
		Spells: map[string]*actionsv1.EncounterSpell{
			"53": {Known: true, MetadataKnown: true, Ready: true, Available: true, Capability: "offense",
				FamilyId: 53, Rank: 1, Level: 4, Cost: 60, PowerType: 3, DirectDamage: 52.5,
				GlobalCooldownMs: 1000, MaxRange: 5, WeaponStrike: true, DamageModelKnown: true,
				WeaponDamageMultiplier: 1.5, RequiresBehind: true, RequiresFacing: true},
			"1752": {Known: true, MetadataKnown: true, Ready: true, Available: true, Capability: "offense",
				FamilyId: 1752, Rank: 1, Level: 1, Cost: 40, PowerType: 3, DirectDamage: 35,
				GlobalCooldownMs: 1000, MaxRange: 5, WeaponStrike: true, DamageModelKnown: true,
				WeaponDamageMultiplier: 1, RequiresFacing: true}}}
	// Facing +X with the caster three yards behind supplies Backstab geometry;
	// zero orientation and instant cast time are actually omitted by protojson.
	target := &actionsv1.EncounterCreature{Guid: 17379390974694817793, PositionKnown: true, X: 3,
		HealthKnown: true, Health: 1000, MaxHealth: 1000}
	typedInput := func(value proto.Message) map[string]any {
		t.Helper()
		raw, err := protojson.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	input := map[string]any{"selection": "weapon_strike", "sessionId": sid, "selfGuid": "4", "autoAttackKnown": false,
		"observation": typedInput(observation), "target": typedInput(target)}
	rows := input["observation"].(map[string]any)["spells"].(map[string]any)
	if _, present := rows["53"].(map[string]any)["castTimeMs"]; present || rows["1752"].(map[string]any)["cost"] != "40" {
		t.Fatal("fixture masked sparse instant mechanics or lost the adjusted energy cost", rows)
	}
	check := func(revision string, want float64) {
		t.Helper()
		before, err := host.vm.State(ctx)
		if err != nil {
			t.Fatal(err)
		}
		available, actual, decision, err := host.starterSpellDecision(ctx, input)
		if err != nil || !available || actual != revision || len(decision) != 2 ||
			decision["kind"] != "cast" || decision["spellId"] != want {
			t.Fatal("weapon mode did not use the current bundled shared selector", available, actual, decision, err)
		}
		assertFollowFallbackState(t, host, before)
	}
	initial := host.vm.Status().Revision
	check(initial, 53)
	beforeEdit, err := host.vm.State(ctx)
	if err != nil || beforeEdit.Status.Cursor != 0 || beforeEdit.Status.Buffered != 0 || len(beforeEdit.Pending) != 0 || beforeEdit.LatestDecision != nil {
		t.Fatal("pure fixture unexpectedly advanced persistent gameplay", beforeEdit, err)
	}
	edited := strings.Replace(source, "finish_at_combo=5}", "finish_at_combo=5, family_weights={[53]=0}}", 1)
	if edited == source {
		t.Fatal("test did not edit the actual shared offense profile in the full bundle")
	}
	revision, err := host.Reload(ctx, edited, initial)
	if err != nil || revision == initial || host.vm.Status().SessionID != sid {
		t.Fatal("same-host weapon policy CAS failed", revision, err)
	}
	check(revision, 1752)
	afterEdit, err := host.vm.State(ctx)
	if err != nil || afterEdit.Status.Cursor != 0 || afterEdit.Status.Buffered != 0 || len(afterEdit.Pending) != 0 ||
		afterEdit.LatestDecision != nil || !reflect.DeepEqual(beforeEdit.Checkpoint, afterEdit.Checkpoint) {
		t.Fatal("pure weapon edit advanced events, changed checkpoint or dispatched gameplay", afterEdit, err)
	}
	if metadata := host.starterSpellMetadata(); !metadata.Available || metadata.Revision != revision || metadata.EntryPoint != policyStarterSpellEntryPoint {
		t.Fatal("weapon selection did not retain current accepted-source metadata", metadata)
	}
}
