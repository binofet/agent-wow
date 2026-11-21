package modrt

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
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

func followFallbackBundle(t *testing.T) string {
	t.Helper()
	bundle, err := exec.Command("python3", filepath.Join("..", "..", "..", "dev", "encounter-bundle")).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(bundle)
}

func followFallbackTestInput(mode string) map[string]any {
	return map[string]any{
		"mode": mode, "nowMs": float64(1000), "selfGuid": "4", "classId": float64(8),
		"self":  map[string]any{"x": float64(0), "y": float64(0), "z": float64(3), "moving": false},
		"owner": map[string]any{"x": float64(10), "y": float64(20), "z": float64(3), "o": float64(0), "moving": false},
	}
}

func assertFollowFallbackState(t *testing.T, host *SessionPolicy, before behavior.SessionState) {
	t.Helper()
	after, err := host.vm.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The host clock continues independently of this read.
	before.Clock, after.Clock = behavior.ClockReading{}, behavior.ClockReading{}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("pure fallback changed persistent events, ledger, checkpoint or latest decision: before %+v after %+v", before, after)
	}
}

func TestPolicyFollowFallbackUsesCurrentFullBundleWithoutAdvancingSession(t *testing.T) {
	ctx := context.Background()
	manager := catalogManager(t, 4, "follow-fallback-session")
	t.Cleanup(manager.cancel)
	source := followFallbackBundle(t)
	host, err := manager.StartPolicy(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	initialRevision := host.vm.Status().Revision
	metadata := func() policyFollowFallbackMetadata {
		t.Helper()
		raw, err := manager.InvokeJSON(ctx, "session.policy_source", json.RawMessage(`{"sessionId":"follow-fallback-session","characterGuid":"4"}`))
		if err != nil {
			t.Fatal(err)
		}
		var view policySourceResponse
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		if view.Source != host.source || view.FollowFallback.EntryPoint != "follow_formation" ||
			!view.FollowFallback.Available || view.FollowFallback.Revision != view.Revision {
			t.Fatal("source inspection lost the active fallback identity", view.FollowFallback)
		}
		return view.FollowFallback
	}
	if metadata().Revision != initialRevision {
		t.Fatal("initial source was not published")
	}
	evaluate := func(input map[string]any, revision string) map[string]any {
		t.Helper()
		available, gotRevision, decision, err := host.followDecision(ctx, input)
		if err != nil || !available || gotRevision != revision || decision == nil {
			t.Fatal("current follow source unavailable", available, gotRevision, decision, err)
		}
		return decision
	}
	checkChoices := func(revision string, rearX, side float64, nearHold bool) {
		t.Helper()
		formation := evaluate(followFallbackTestInput("formation"), revision)
		position, _ := formation["position"].(map[string]any)
		if x, ok := position["x"].(float64); !ok || math.Abs(x-rearX) > .000001 {
			t.Fatal("formation did not use the current role offset", formation)
		}
		nearInput := followFallbackTestInput("near_owner")
		nearInput["owner"].(map[string]any)["x"], nearInput["owner"].(map[string]any)["y"] = float64(2.5), float64(0)
		if near := evaluate(nearInput, revision); near["hold"] != nearHold {
			t.Fatal("near-owner follow retained an older stop policy", near)
		}
		portalInput := followFallbackTestInput("portal_catch_up")
		portalInput["owner"].(map[string]any)["x"], portalInput["owner"].(map[string]any)["y"] = float64(100), float64(0)
		portal := evaluate(portalInput, revision)
		candidates, _ := portal["candidates"].([]any)
		if len(candidates) != 7 || candidates[0].(map[string]any)["x"] != float64(100) ||
			candidates[1].(map[string]any)["x"] != float64(8) || candidates[1].(map[string]any)["y"] != side ||
			candidates[2].(map[string]any)["y"] != -side {
			t.Fatal("portal fallback retained an older candidate order", portal)
		}
	}
	checkChoices(initialRevision, 5.016, -6, true)
	available, gotRevision, decision, err := host.followDecision(ctx, map[string]any{"invalid": make(chan int)})
	if !available || gotRevision != initialRevision || decision != nil || err == nil {
		t.Fatal("invalid pure input bypassed validation", available, gotRevision, decision, err)
	}
	checkChoices(initialRevision, 5.016, -6, true)

	// Create an actual uncertain heal reservation in the persistent bundle.
	self := &actionsv1.EncounterPlayer{Guid: 4, Present: true, AliveKnown: true, Alive: proto.Bool(true),
		MapKnown: true, MapId: proto.Uint32(0), PositionKnown: true, MovementKnown: true,
		HealthKnown: true, Health: 20, MaxHealth: 100}
	obs := &actionsv1.EncounterObservationResponse{SessionId: "follow-fallback-session", SelfGuid: 4,
		VisitId: 7, MapId: proto.Uint32(0), HostOwned: proto.Bool(true), WorldObservationRevision: 1, SpellbookKnown: true,
		Players: map[string]*actionsv1.EncounterPlayer{"4": self},
		Party: &actionsv1.EncounterParty{Group: &partyv1.Group{Known: true, InGroup: true, GroupGuid: 1, LeaderGuid: 3,
			Members: []*partyv1.Member{{Guid: 4, Self: true, Online: true}, {Guid: 3, Online: true}}}},
		Spells: map[string]*actionsv1.EncounterSpell{"2050": {Known: true, Ready: true, Capability: "heal"}},
		Healing: &actionsv1.HealingOptionsResponse{SessionId: "follow-fallback-session", HostOwned: true,
			Caster:   &actionsv1.HealingCaster{Guid: 4, Ready: true, Mana: proto.Uint32(30)},
			Patients: []*actionsv1.HealingPatient{{Guid: 4, Health: 20, MaxHealth: 100}},
			Spells: []*actionsv1.HealingRank{{SpellId: 2050, Rank: proto.Uint32(1), Level: proto.Uint32(1),
				Cost: proto.Uint32(30), Amount: 45, CastMs: proto.Uint32(1500), MinRange: proto.Float64(0), MaxRange: proto.Float64(40)}}}}
	raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	var observation map[string]any
	if err := json.Unmarshal(raw, &observation); err != nil {
		t.Fatal(err)
	}
	seq, err := host.Enqueue(behavior.Event{Kind: "encounter_observation", Value: observation})
	if err != nil {
		t.Fatal(err)
	}
	_, receipt, err := host.Step(ctx, map[string]any{"kind": "encounter", "observationSequence": strconv.FormatUint(seq, 10)},
		func(action map[string]any) (PolicyDispatch, error) {
			if action["capability"] != "heal" || action["targetGuid"] != "4" {
				t.Fatal("expected a real self-heal proposal", action)
			}
			return PolicyDispatch{Module: actionsModule, Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
				return behavior.ActionUnknown, map[string]any{"capability": "heal"}
			}}, nil
		})
	if err != nil || receipt == nil || receipt.Outcome != behavior.ActionUnknown {
		t.Fatal("uncertain action was not reserved", receipt, err)
	}
	if err := host.vm.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"note": "still buffered"}}); err != nil {
		t.Fatal(err)
	}
	before, err := host.vm.State(ctx)
	if err != nil || before.Status.Buffered != 1 || len(before.Pending) != 1 || before.Pending[0].Outcome != behavior.ActionUnknown ||
		before.LatestDecision == nil {
		t.Fatal("fixture lost its buffered event or uncertain action", before, err)
	}
	checkChoices(initialRevision, 5.016, -6, true)
	assertFollowFallbackState(t, host, before)

	// Follow reads must complete even while a synchronous Step owns advance.
	type answer struct {
		available bool
		revision  string
		decision  map[string]any
		err       error
	}
	finished := make(chan answer, 1)
	host.advance.Lock()
	go func() {
		available, revision, decision, err := host.followDecision(ctx, followFallbackTestInput("formation"))
		finished <- answer{available, revision, decision, err}
	}()
	select {
	case got := <-finished:
		host.advance.Unlock()
		if got.err != nil || !got.available || got.revision != initialRevision || got.decision == nil {
			t.Fatal("follow read failed under persistent writer custody", got)
		}
	case <-time.After(time.Second):
		host.advance.Unlock()
		t.Fatal("follow read deadlocked behind the persistent writer")
	}
	assertFollowFallbackState(t, host, before)

	oldCache := host.followFallback.Load()
	if _, err := host.Reload(ctx, "return {", initialRevision); err == nil || host.followFallback.Load() != oldCache {
		t.Fatal("broken Lua changed the accepted fallback source", err)
	}
	changed := strings.Replace(source, "[8]={5.1,.2}", "[8]={7.1,.2}", 1)
	changed = strings.Replace(changed, "local near_stop, near_limit = 3,30", "local near_stop, near_limit = 2,30", 1)
	changed = strings.Replace(changed, "local portal_ahead, portal_side = {8,12,16}, {-6,6}",
		"local portal_ahead, portal_side = {8,12,16}, {6,-6}", 1)
	if changed == source || strings.Contains(changed, "[8]={5.1,.2}") ||
		strings.Contains(changed, "local near_stop, near_limit = 3,30") || strings.Contains(changed, "local portal_ahead, portal_side = {8,12,16}, {-6,6}") {
		t.Fatal("test did not update all three editable follow choices")
	}
	revision, err := host.Reload(ctx, changed, initialRevision)
	if err != nil || revision == initialRevision || host.vm.Status().SessionID != "follow-fallback-session" {
		t.Fatal("current follow source did not replace in the same session", revision, err)
	}
	pending := host.vm.Outstanding()
	if len(pending) != 1 || pending[0].ActionID != receipt.ActionID || pending[0].Outcome != behavior.ActionUnknown {
		t.Fatal("reload lost or settled an uncertain receipt", pending)
	}
	checkChoices(revision, 3.016, 6, false)
	if metadata().Revision != revision {
		t.Fatal("source inspection still describes the older fallback")
	}
	currentCache := host.followFallback.Load()
	if _, err := host.Reload(ctx, source, initialRevision); !errors.Is(err, behavior.ErrRevisionConflict) ||
		host.followFallback.Load() != currentCache {
		t.Fatal("stale CAS changed the fallback cache", err)
	}
	// This optional entry remains a valid persistent export. Its failure is
	// attributable to the active revision and cannot revive the launch source.
	failing := strings.Replace(changed, "follow_formation=encounter_formation and encounter_formation.decide",
		"follow_formation=function(input) error('current follow failed') end", 1)
	if failing == changed {
		t.Fatal("missing editable follow export in the complete bundle")
	}
	failedRevision, err := host.Reload(ctx, failing, revision)
	if err != nil {
		t.Fatal(err)
	}
	failedState, err := host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		available, gotRevision, decision, err := host.followDecision(ctx, followFallbackTestInput("formation"))
		if !available || gotRevision != failedRevision || decision != nil || err == nil || !strings.Contains(err.Error(), "current follow failed") {
			t.Fatal("failing active follow source restored older behavior or lost its cause", available, gotRevision, decision, err)
		}
	}
	assertFollowFallbackState(t, host, failedState)
	if view := metadata(); view.Revision != failedRevision || !strings.Contains(view.LastError, "current follow failed") {
		t.Fatal("active follow failure was absent from source inspection", view)
	}
}

func TestPolicyFollowFallbackSkipsAnAbsentOptionalExport(t *testing.T) {
	manager := catalogManager(t, 4, "follow-unavailable")
	t.Cleanup(manager.cancel)
	// A same-named nonfunction is not an optional capability.
	const source = `return {follow_formation="unavailable",on_event=function(e)return true end,
 decide=function(f)return {} end,export_state=function()return {version=1,data={}} end,import_state=function(c)return true end}`
	host, err := manager.StartPolicy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	available, revision, decision, err := host.followDecision(context.Background(), followFallbackTestInput("formation"))
	if err != nil || available || revision != host.vm.Status().Revision || decision != nil {
		t.Fatal("generic policy advertised an optional pure function", available, revision, decision, err)
	}
	cache := host.followFallback.Load()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.attempted || cache.runtime != nil {
		t.Fatal("unavailable optional function compiled a pure runtime")
	}
}

func TestPolicyFollowFallbackFencesVMCommitBeforeCachePublication(t *testing.T) {
	ctx := context.Background()
	manager := catalogManager(t, 4, "follow-publication-gap")
	t.Cleanup(manager.cancel)
	source := followFallbackBundle(t)
	host, err := manager.StartPolicy(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	old := host.vm.Status().Revision
	changed := strings.Replace(source, "local near_stop, near_limit = 3,30", "local near_stop, near_limit = 1,30", 1)
	// Hold the exact production interval between accepted VM replacement and
	// publication of its pure-source cache, without adding a timing hook.
	revision, err := host.vm.Reload(ctx, changed, old)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, out, err := host.followDecision(ctx, followFallbackTestInput("near_owner")); err == nil || out != nil {
		t.Fatal("old pure source escaped after the persistent source committed", out, err)
	}
	host.source = changed
	host.publishFollowFallback(changed, revision)
	available, current, out, err := host.followDecision(ctx, followFallbackTestInput("near_owner"))
	if err != nil || !available || current != revision || out["stopDistance"] != float64(1) {
		t.Fatal("current pure source did not resume after publication", available, current, out, err)
	}
}

func TestPolicyFollowFallbackPrivateCallbackBindsCurrentBodyAndLifecycle(t *testing.T) {
	manager := catalogManager(t, 4, "follow-callback")
	t.Cleanup(manager.cancel)
	host, err := manager.StartPolicy(context.Background(), `return {
 follow_formation=function(input)return {selfGuid=input.selfGuid,mode=input.mode} end,
 on_event=function(e)return true end,decide=function(f)return {} end,
 export_state=function()return {version=1,data={}} end,import_state=function(c)return true end}`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	request := func(sid, self string, input map[string]any) *modv1.InvokeModuleRequest {
		t.Helper()
		value, err := structpb.NewStruct(map[string]any{"sessionId": sid, "selfGuid": self, "input": input})
		if err != nil {
			t.Fatal(err)
		}
		packed, err := anypb.New(value)
		if err != nil {
			t.Fatal(err)
		}
		return &modv1.InvokeModuleRequest{Module: modcb.FollowPolicyModule, Method: modcb.FollowPolicyMethod, Request: packed}
	}
	input := followFallbackTestInput("formation")
	valid := request("follow-callback", "4", input)
	result, err := manager.invokeModule(context.Background(), actionsModule, valid)
	if err != nil {
		t.Fatal(err)
	}
	value := &structpb.Struct{}
	if result.GetResult().UnmarshalTo(value) != nil {
		t.Fatal("private follow result lost its typed object", result)
	}
	view := value.AsMap()
	decision, _ := view["decision"].(map[string]any)
	if view["available"] != true || view["revision"] != host.vm.Status().Revision ||
		decision["selfGuid"] != "4" || decision["mode"] != "formation" {
		t.Fatal("private callback returned a foreign decision or source revision", view)
	}
	if _, err := manager.invokeModule(context.Background(), "fixture", valid); status.Code(err) != codes.PermissionDenied {
		t.Fatal("foreign module read the private follow capability", err)
	}
	foreignInput := followFallbackTestInput("formation")
	foreignInput["selfGuid"] = "5"
	for _, test := range []struct {
		request *modv1.InvokeModuleRequest
		code    codes.Code
	}{
		{request("previous-session", "4", input), codes.FailedPrecondition},
		{request("follow-callback", "5", foreignInput), codes.FailedPrecondition},
		{request("follow-callback", "5", input), codes.InvalidArgument},
	} {
		if _, err := manager.invokeModule(context.Background(), actionsModule, test.request); status.Code(err) != test.code {
			t.Fatal("private follow callback accepted a stale or inconsistent body", test.code, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.invokeModule(ctx, actionsModule, valid); status.Code(err) != codes.Canceled {
		t.Fatal("canceled follow read executed", err)
	}
	manager.closing = true
	if _, err := manager.invokeModule(context.Background(), actionsModule, valid); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("follow read escaped session shutdown", err)
	}
}
