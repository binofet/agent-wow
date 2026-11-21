package modrt

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

const starterSpellHostSource = `local s={events=0}
return {starter_spell=function(input)return {kind="cast",spellId=133}end,
on_event=function(e)s.events=s.events+1;return true end,
decide=function(f)return {action={chosen=true}}end,
export_state=function()return {version=1,data=s}end,
import_state=function(c)s=c.data;return true end}`

func starterSpellHostInput(sid, self string) map[string]any {
	return map[string]any{"sessionId": sid, "selfGuid": self, "observation": map[string]any{"visitId": "19"}}
}

func TestPolicyStarterSpellRealBundleUsesSharedSelectorAfterAcceptedEdit(t *testing.T) {
	ctx := context.Background()
	const sid = "starter-bundle"
	bundle, err := exec.Command("python3", filepath.Join("..", "..", "..", "dev", "encounter-bundle")).Output()
	if err != nil {
		t.Fatal(err)
	}
	source := string(bundle)
	manager := catalogManager(t, 4, sid)
	t.Cleanup(manager.cancel)
	host, err := manager.StartPolicy(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	observation := &actionsv1.EncounterObservationResponse{SessionId: sid, SelfGuid: 4, VisitId: 19,
		WorldObservationRevision: 1, SpellbookRevision: 1, SpellbookKnown: true,
		Players: map[string]*actionsv1.EncounterPlayer{"4": {Guid: 4, ClassKnown: true, ClassId: 8,
			PowerKnown: true, PowerType: 0, Power: 500, MaxPower: 500, PositionKnown: true}},
		Spells: map[string]*actionsv1.EncounterSpell{
			"100": {Known: true, MetadataKnown: true, Ready: true, Available: true, Capability: "offense",
				FamilyId: 100, Rank: 1, Level: 1, Cost: 10, DirectDamage: 60, CastTimeMs: 1500,
				GlobalCooldownMs: 1500, MaxRange: 30},
			"200": {Known: true, MetadataKnown: true, Ready: true, Available: true, Capability: "offense",
				FamilyId: 100, Rank: 2, Level: 2, Cost: 10, DirectDamage: 30, CastTimeMs: 1500,
				GlobalCooldownMs: 1500, MaxRange: 30}}}
	target := &actionsv1.EncounterCreature{Guid: 17379390974694817793, PositionKnown: true, X: 5,
		HealthKnown: true, Health: 100, MaxHealth: 100}
	typedInput := func(value proto.Message) map[string]any {
		t.Helper()
		raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	input := map[string]any{"sessionId": sid, "selfGuid": "4", "autoAttackKnown": false,
		"observation": typedInput(observation), "target": typedInput(target)}
	check := func(revision string, want float64) {
		t.Helper()
		before, err := host.vm.State(ctx)
		if err != nil {
			t.Fatal(err)
		}
		available, actual, decision, err := host.starterSpellDecision(ctx, input)
		if err != nil || !available || actual != revision || len(decision) != 2 ||
			decision["kind"] != "cast" || decision["spellId"] != want {
			t.Fatal("full bundled export did not use the shared rank profile", available, actual, decision, err)
		}
		assertFollowFallbackState(t, host, before)
	}
	initial := host.vm.Status().Revision
	check(initial, 100) // Starter profile compares every learned rank's actual damage.
	changed := strings.Replace(source, "all_ranks=true", "all_ranks=false", 1)
	if changed == source {
		t.Fatal("test did not edit the actual shared starter profile in the bundle")
	}
	revision, err := host.Reload(ctx, changed, initial)
	if err != nil || revision == initial || host.vm.Status().SessionID != sid {
		t.Fatal("same-session bundled source edit failed", revision, err)
	}
	check(revision, 200) // Family pruning now retains the higher learned rank.
	if metadata := host.starterSpellMetadata(); metadata.Revision != revision || !metadata.Available {
		t.Fatal("bundled export read retained its launch revision", metadata)
	}
}

func TestPolicyStarterSpellUsesCurrentSourceWithoutAdvancingOrSettlingActions(t *testing.T) {
	ctx := context.Background()
	manager := catalogManager(t, 4, "starter-read")
	t.Cleanup(manager.cancel)
	host, err := manager.StartPolicy(ctx, starterSpellHostSource)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	initial := host.vm.Status().Revision
	_, receipt, err := host.Step(ctx, nil, func(map[string]any) (PolicyDispatch, error) {
		return PolicyDispatch{Module: "fixture", Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
			return behavior.ActionUnknown, map[string]any{"written": false}
		}}, nil
	})
	if err != nil || receipt == nil || receipt.Outcome != behavior.ActionUnknown {
		t.Fatal("fixture did not retain uncertain action", receipt, err)
	}
	if err = host.vm.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"note": "buffered"}}); err != nil {
		t.Fatal(err)
	}
	before, err := host.vm.State(ctx)
	if err != nil || before.Status.Buffered != 1 || len(before.Pending) != 1 || before.LatestDecision == nil {
		t.Fatal("fixture lost buffered input, pending action or decision", before, err)
	}
	input := starterSpellHostInput("starter-read", "4")
	evaluate := func(revision string, spell float64) {
		t.Helper()
		available, actual, decision, err := host.starterSpellDecision(ctx, input)
		if err != nil || !available || actual != revision || decision["spellId"] != spell {
			t.Fatal("pure read lost the current exact choice", available, actual, decision, err)
		}
	}
	evaluate(initial, 133)
	assertFollowFallbackState(t, host, before)

	// A synchronous Actions caller can hold advance while asking for this
	// export; only the accepted-source cache participates in the read.
	finished := make(chan error, 1)
	host.advance.Lock()
	go func() {
		available, revision, decision, err := host.starterSpellDecision(ctx, input)
		if err == nil && (!available || revision != initial || decision["spellId"] != float64(133)) {
			err = errors.New("wrong source under writer custody")
		}
		finished <- err
	}()
	select {
	case err := <-finished:
		host.advance.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		host.advance.Unlock()
		t.Fatal("pure starter read deadlocked behind the persistent writer")
	}
	assertFollowFallbackState(t, host, before)

	oldCache := host.starterSpell.Load()
	if _, err = host.Reload(ctx, "return {", initial); err == nil || host.starterSpell.Load() != oldCache {
		t.Fatal("rejected source replaced the pure cache", err)
	}
	changed := strings.Replace(starterSpellHostSource, "spellId=133", "spellId=116", 1)
	revision, err := host.Reload(ctx, changed, initial)
	if err != nil || revision == initial || host.vm.Status().SessionID != "starter-read" {
		t.Fatal("same-session source replacement failed", revision, err)
	}
	evaluate(revision, 116)
	pending := host.vm.Outstanding()
	if len(pending) != 1 || pending[0].ActionID != receipt.ActionID || pending[0].Outcome != behavior.ActionUnknown {
		t.Fatal("pure source replacement settled or replayed uncertain action", pending)
	}
	current := host.starterSpell.Load()
	if _, err = host.Reload(ctx, starterSpellHostSource, initial); !errors.Is(err, behavior.ErrRevisionConflict) || host.starterSpell.Load() != current {
		t.Fatal("stale CAS changed starter source", err)
	}
	raw, err := manager.InvokeJSON(ctx, "session.policy_source", json.RawMessage(`{"sessionId":"starter-read","characterGuid":"4"}`))
	var view policySourceResponse
	if err != nil || json.Unmarshal(raw, &view) != nil || view.StarterSpell.EntryPoint != policyStarterSpellEntryPoint ||
		!view.StarterSpell.Available || view.StarterSpell.Revision != revision || view.Revision != revision {
		t.Fatal("source inspection lost current starter identity", string(raw), err)
	}
	reflected := false
	for _, method := range hostMethods() {
		if method.Name != "session.policy_source" {
			continue
		}
		properties := method.Output["properties"].(map[string]any)
		starter, ok := properties["starterSpell"].(map[string]any)
		if !ok {
			t.Fatal("current starter source metadata is not discoverable", method.Output)
		}
		fields := starter["properties"].(map[string]any)
		reflected = fields["entryPoint"] != nil && fields["available"] != nil && fields["revision"] != nil && fields["lastError"] != nil
	}
	if !reflected {
		t.Fatal("source metadata lost the current starter export contract")
	}
	failing := strings.Replace(changed, `return {kind="cast",spellId=116}`, `error("active starter failure")`, 1)
	failedRevision, err := host.Reload(ctx, failing, revision)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		available, gotRevision, out, err := host.starterSpellDecision(ctx, input)
		if !available || gotRevision != failedRevision || out != nil || err == nil || !strings.Contains(err.Error(), "active starter failure") {
			t.Fatal("failed current source revived an older choice", available, gotRevision, out, err)
		}
	}
	if view := host.starterSpellMetadata(); view.Revision != failedRevision || !strings.Contains(view.LastError, "active starter failure") {
		t.Fatal("active starter failure lost its source identity", view)
	}
}

func TestPolicyStarterSpellFencesPublicationAndAbsentExport(t *testing.T) {
	ctx := context.Background()
	manager := catalogManager(t, 4, "starter-publication")
	t.Cleanup(manager.cancel)
	host, err := manager.StartPolicy(ctx, starterSpellHostSource)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	input := starterSpellHostInput("starter-publication", "4")
	initial := host.vm.Status().Revision
	changed := strings.Replace(starterSpellHostSource, "spellId=133", "spellId=116", 1)
	revision, err := host.vm.Reload(ctx, changed, initial)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, out, err := host.starterSpellDecision(ctx, input); err == nil || out != nil {
		t.Fatal("stale export escaped between VM commit and cache publication", out, err)
	}
	host.source = changed
	host.publishStarterSpell(changed, revision)
	if available, actual, out, err := host.starterSpellDecision(ctx, input); err != nil || !available || actual != revision || out["spellId"] != float64(116) {
		t.Fatal("current export did not resume after publication", available, actual, out, err)
	}
	absent := strings.Replace(changed, `starter_spell=function(input)return {kind="cast",spellId=116}end`, `starter_spell="not callable"`, 1)
	revision, err = host.Reload(ctx, absent, revision)
	if err != nil {
		t.Fatal(err)
	}
	if available, actual, out, err := host.starterSpellDecision(ctx, input); err != nil || available || actual != revision || out != nil {
		t.Fatal("current missing export restored a previous choice", available, actual, out, err)
	}
	cache := host.starterSpell.Load()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.attempted || cache.runtime != nil {
		t.Fatal("nonfunction optional export created a pure runtime")
	}
}

func starterSpellHostRequest(t *testing.T, fields map[string]any) *modv1.InvokeModuleRequest {
	t.Helper()
	value, err := structpb.NewStruct(fields)
	if err != nil {
		t.Fatal(err)
	}
	packed, err := anypb.New(value)
	if err != nil {
		t.Fatal(err)
	}
	return &modv1.InvokeModuleRequest{Module: modcb.StarterSpellPolicyModule, Method: modcb.StarterSpellPolicyMethod, Request: packed}
}

func TestPolicyStarterSpellPrivateCallbackFencesBodyInputAndLifecycle(t *testing.T) {
	ctx := context.Background()
	manager := catalogManager(t, 4, "starter-callback")
	t.Cleanup(manager.cancel)
	request := func(sid, self string, input map[string]any) *modv1.InvokeModuleRequest {
		return starterSpellHostRequest(t, map[string]any{"sessionId": sid, "selfGuid": self, "input": input})
	}
	input := starterSpellHostInput("starter-callback", "4")
	valid := request("starter-callback", "4", input)
	check := func(available bool, revision string) {
		t.Helper()
		response, err := manager.invokeModule(ctx, actionsModule, valid)
		if err != nil {
			t.Fatal(err)
		}
		value := &structpb.Struct{}
		if response.GetResult().UnmarshalTo(value) != nil {
			t.Fatal("private response lost typed object", response)
		}
		fields := value.AsMap()
		if len(fields) != 3 || fields["available"] != available || fields["revision"] != revision {
			t.Fatal("private response lost availability or source identity", fields)
		}
	}
	check(false, "") // No attached source is distinct from a missing export.
	host, err := manager.StartPolicy(ctx, starterSpellHostSource)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	check(true, host.vm.Status().Revision)
	if _, err = manager.invokeModule(ctx, "fixture", valid); status.Code(err) != codes.PermissionDenied {
		t.Fatal("foreign module read private selector", err)
	}
	for name, test := range map[string]struct {
		request *modv1.InvokeModuleRequest
		code    codes.Code
	}{
		"stale_session":         {request("old-session", "4", starterSpellHostInput("old-session", "4")), codes.FailedPrecondition},
		"foreign_body":          {request("starter-callback", "5", starterSpellHostInput("starter-callback", "5")), codes.FailedPrecondition},
		"input_body":            {request("starter-callback", "4", starterSpellHostInput("starter-callback", "5")), codes.InvalidArgument},
		"input_session":         {request("starter-callback", "4", starterSpellHostInput("old-session", "4")), codes.InvalidArgument},
		"missing_input_session": {request("starter-callback", "4", map[string]any{"selfGuid": "4"}), codes.InvalidArgument},
		"oversized_input":       {request("starter-callback", "4", map[string]any{"sessionId": "starter-callback", "selfGuid": "4", "payload": strings.Repeat("x", modcb.StarterSpellPolicyMaxBytes)}), codes.InvalidArgument},
		"extra_outer":           {starterSpellHostRequest(t, map[string]any{"sessionId": "starter-callback", "selfGuid": "4", "input": input, "extra": true}), codes.InvalidArgument},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := manager.invokeModule(ctx, actionsModule, test.request); status.Code(err) != test.code {
				t.Fatal("private callback admitted invalid body/input", test.code, err)
			}
		})
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = manager.invokeModule(canceled, actionsModule, valid); status.Code(err) != codes.Canceled {
		t.Fatal("canceled read executed", err)
	}
	for _, stage := range []string{"closing", "preparing", "prepared"} {
		manager.closing, manager.preparing, manager.prepared = stage == "closing", stage == "preparing", stage == "prepared"
		if _, err = manager.invokeModule(ctx, actionsModule, valid); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("read escaped leaving-world lifecycle", stage, err)
		}
	}
	manager.closing, manager.preparing, manager.prepared = false, false, false
	manager.cancel()
	if _, err = manager.invokeModule(ctx, actionsModule, valid); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("read escaped canceled manager", err)
	}
}
