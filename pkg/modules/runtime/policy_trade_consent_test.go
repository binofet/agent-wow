package modrt

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

const tradeConsentFixtureHooks = `on_event=function(event)return true end,
decide=function(frame)return {}end,
export_state=function()return {version=1,data={}}end,
import_state=function(checkpoint)return true end,`

func tradeConsentHostInput(sid, self string) map[string]any {
	return map[string]any{"sessionId": sid, "selfGuid": self, "trade": map[string]any{
		"sessionId": sid, "revision": "18446744073709551615", "phase": "OPEN", "partnerGuid": "3",
		"partnerAccepted": true, "acceptTransportWritten": false,
		"ownRequestedItems": []any{}, "ownRequestedCopper": float64(0),
		"partnerItems": []any{}, "partnerCopper": float64(0),
	}}
}

func tradeServicePreparationHostInput(sid, self string) map[string]any {
	input := tradeConsentHostInput(sid, self)
	input["kind"] = "prepare_trade_service"
	trade := input["trade"].(map[string]any)
	trade["partnerAccepted"] = false
	trade["partnerItems"] = []any{map[string]any{"slot": float64(6), "entry": float64(4632), "count": float64(1),
		"itemGuid": "0", "name": "", "metadataKnown": false}}
	input["service"] = map[string]any{"slot": float64(6), "entry": float64(4632), "count": float64(1), "wrapped": false, "lockId": float64(5), "partnerSpell": float64(0)}
	input["attempt"] = map[string]any{
		"item":      map[string]any{"entry": float64(0), "count": float64(0), "wrapped": false, "lockId": float64(0)},
		"castCount": float64(0), "queued": false, "accepted": false, "castGo": false, "retry": false,
		"deadlineKnown": false, "remainingMs": float64(0),
	}
	input["world"] = map[string]any{
		"visitId": "7", "mapKnown": true, "mapId": float64(0),
		"self": map[string]any{"guid": self, "present": true, "aliveKnown": true, "alive": true,
			"mapKnown": true, "mapId": float64(0), "positionKnown": true, "positionRevision": "11",
			"classKnown": true, "classId": float64(4), "unitFlagsKnown": true, "unitFlags": float64(0),
			"movementFlagsKnown": true, "movementFlags": float64(0)},
		"owner": map[string]any{"guid": "3", "present": true, "aliveKnown": true, "alive": true,
			"mapKnown": true, "mapId": float64(0), "positionKnown": true, "positionRevision": "13",
			"unitFlagsKnown": true, "unitFlags": float64(0), "x": float64(1), "y": float64(2), "z": float64(3)},
		"skills": map[string]any{"known": true, "complete": true, "revision": "17", "rows": []any{
			map[string]any{"slot": float64(5), "skillId": float64(633), "value": float64(75), "maxValue": float64(75),
				"temporaryBonus": float64(0), "permanentBonus": float64(0)},
		}},
	}
	input["spell"] = map[string]any{"sessionId": sid, "selfGuid": self, "spellbookKnown": true,
		"spellbookRevision": "19", "present": true, "spellId": float64(1804), "metadataKnown": true, "castTimeMs": float64(5000)}
	input["reference"] = map[string]any{"catalog": "client_build_12340_lockpicking", "lockId": float64(5), "known": true, "requiredSkillRank": float64(1)}
	input["window"] = map[string]any{"deadlineKnown": true, "remainingMs": float64(30000)}
	return input
}

func tradeConsentHostRequest(t *testing.T, input map[string]any) *modv1.InvokeModuleRequest {
	t.Helper()
	value, err := structpb.NewStruct(map[string]any{"sessionId": input["sessionId"], "selfGuid": input["selfGuid"], "input": input})
	if err != nil {
		t.Fatal(err)
	}
	packed, err := anypb.New(value)
	if err != nil {
		t.Fatal(err)
	}
	return &modv1.InvokeModuleRequest{Module: modcb.TradeConsentPolicyModule, Method: modcb.TradeConsentPolicyMethod, Request: packed}
}

func tradeConsentHostRead(t *testing.T, m *Manager, input map[string]any) map[string]any {
	t.Helper()
	result, err := m.invokeModule(context.Background(), "buddy-trade", tradeConsentHostRequest(t, input))
	value := &structpb.Struct{}
	if err != nil || result.GetResult() == nil || result.Result.UnmarshalTo(value) != nil {
		t.Fatal("current-body Trade callback failed", result, err)
	}
	return value.AsMap()
}

func TestPolicyTradeConsentActualBundleEditsPreserveUncertainActions(t *testing.T) {
	ctx := context.Background()
	const sid = "trade-consent-bundle"
	m := catalogManager(t, 4, sid)
	t.Cleanup(m.cancel)
	// Keep the actual composed export and state hooks. Only the persistent
	// fixture decision emits a reservable action; the pure export never does.
	source := "local actual=(function()\n" + followFallbackBundle(t) + "\nend)()\n" +
		`actual.decide=function(frame)return {action={kind="trade_consent_pending_fixture"}}end
return actual`
	host, err := m.StartPolicy(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	dispatches := 0
	_, receipt, err := host.Step(ctx, nil, func(map[string]any) (PolicyDispatch, error) {
		return PolicyDispatch{Module: "fixture", Send: func(context.Context) (behavior.ActionOutcome, map[string]any) {
			dispatches++
			return behavior.ActionUnknown, map[string]any{"requestId": "retained-trade-fixture"}
		}}, nil
	})
	if err != nil || receipt == nil || receipt.Outcome != behavior.ActionUnknown || dispatches != 1 {
		t.Fatal("fixture did not retain uncertain action", receipt, dispatches, err)
	}
	if err := host.vm.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Enqueue(behavior.Event{Kind: "command", Value: map[string]any{"note": "buffered-trade-fixture"}}); err != nil {
		t.Fatal(err)
	}
	input := tradeConsentHostInput(sid, "4")
	checkInput := func(currentInput map[string]any, revision, kind string) {
		t.Helper()
		before, err := host.vm.State(ctx)
		if err != nil || len(before.Pending) != 1 {
			t.Fatal("pure read lost pending fixture", before, err)
		}
		out := tradeConsentHostRead(t, m, currentInput)
		decision := out["decision"].(map[string]any)
		if len(out) != 3 || out["available"] != true || out["revision"] != revision || len(decision) != 2 || decision["kind"] != kind {
			t.Fatal("actual current shared export was not used", out)
		}
		assertFollowFallbackState(t, host, before)
	}
	check := func(revision, kind string) { checkInput(input, revision, kind) }
	initial := host.vm.Status().Revision
	check(initial, "accept")
	preparation := tradeServicePreparationHostInput(sid, "4")
	checkInput(preparation, initial, "prepare")
	const preparationTuning = "local prepare_owner_trade_services = true"
	servicesDisabled := strings.Replace(source, preparationTuning, "local prepare_owner_trade_services = false", 1)
	if servicesDisabled == source {
		t.Fatal("actual shared preparation tuning was not edited")
	}
	servicesRevision, err := host.Reload(ctx, servicesDisabled, initial)
	if err != nil || servicesRevision == initial {
		t.Fatal("same-body service edit failed", servicesRevision, err)
	}
	checkInput(preparation, servicesRevision, "wait")
	check(servicesRevision, "accept") // Preparation opt-out does not withdraw ordinary consent.
	restored, err := host.Reload(ctx, source, servicesRevision)
	if err != nil || restored != initial {
		t.Fatal("restoring current source lost its original revision", restored, err)
	}
	checkInput(preparation, restored, "prepare")
	check(restored, "accept")
	// The native caller can be inside a dispatch holding advance. This read
	// must not recurse into that persistent VM or request any module facts.
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for _, currentInput := range []map[string]any{input, preparation} {
		request := tradeConsentHostRequest(t, currentInput)
		done := make(chan error, 1)
		host.advance.Lock()
		go func() { _, err := m.invokeModule(bounded, "buddy-trade", request); done <- err }()
		select {
		case err := <-done:
			host.advance.Unlock()
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			host.advance.Unlock()
			t.Fatal("pure consent/preparation read waited for persistent dispatch")
		}
	}
	changed := strings.Replace(source, "local accept_owner_trades = true", "local accept_owner_trades = false", 1)
	if changed == source {
		t.Fatal("actual shared consent tuning was not edited")
	}
	revision, err := host.Reload(ctx, changed, initial)
	if err != nil || revision == initial || host.vm.Status().SessionID != sid {
		t.Fatal("same-body accepted edit failed", revision, err)
	}
	check(revision, "wait")
	checkInput(preparation, revision, "prepare") // Ordinary opt-out does not disable service preparation.
	if _, err := host.Reload(ctx, "return {", revision); err == nil {
		t.Fatal("invalid candidate replaced current consent policy")
	}
	if _, err := host.Reload(ctx, source, initial); !errors.Is(err, behavior.ErrRevisionConflict) {
		t.Fatal("stale edit replaced current consent policy", err)
	}
	check(revision, "wait")
	raw, err := m.InvokeJSON(ctx, "session.policy_source", json.RawMessage(`{"sessionId":"trade-consent-bundle","characterGuid":"4"}`))
	var view map[string]any
	if err != nil || json.Unmarshal(raw, &view) != nil {
		t.Fatal("active export metadata unavailable", err)
	}
	metadata, ok := view["tradeConsent"].(map[string]any)
	if !ok || metadata["entryPoint"] != policyTradeConsentEntryPoint || metadata["available"] != true || metadata["revision"] != revision {
		t.Fatal("source inspection lost consent publication", view)
	}
	broken := "local actual=(function()\n" + changed + "\nend)()\n" +
		`actual.trade_consent=function(input)error("current_trade_consent_fault")end
return actual`
	if _, err = host.Reload(ctx, broken, revision); err != nil {
		t.Fatal(err)
	}
	before, err := host.vm.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, currentInput := range []map[string]any{input, preparation} {
		if _, err := m.invokeModule(ctx, "buddy-trade", tradeConsentHostRequest(t, currentInput)); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "current_trade_consent_fault") {
			t.Fatal("faulted current source fell back to old consent/preparation", err)
		}
		assertFollowFallbackState(t, host, before)
	}
	pending := host.vm.Outstanding()
	if dispatches != 1 || len(pending) != 1 || pending[0].ActionID != receipt.ActionID || pending[0].Outcome != behavior.ActionUnknown {
		t.Fatal("consent reads or source edits replayed/settled uncertainty", dispatches, pending)
	}
}

func TestPolicyTradeConsentCallbackRequiresCurrentPrivateBody(t *testing.T) {
	ctx := context.Background()
	const sid = "trade-consent-body"
	m := catalogManager(t, 4, sid)
	t.Cleanup(m.cancel)
	source := `return {` + tradeConsentFixtureHooks + `trade_consent=function(input)return {kind="accept",reason="fixture"}end}`
	host, err := m.StartPolicy(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	input := tradeConsentHostInput(sid, "4")
	request := tradeConsentHostRequest(t, input)
	if _, err := m.invokeModule(ctx, "buddy-inventory", request); status.Code(err) != codes.PermissionDenied {
		t.Fatal("another module read private Trade consent", err)
	}
	for _, binding := range [][2]string{{"foreign-session", "4"}, {sid, "5"}, {sid, "04"}} {
		if _, err := m.invokeModule(ctx, "buddy-trade", tradeConsentHostRequest(t, tradeConsentHostInput(binding[0], binding[1]))); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("foreign/current-body mismatch was admitted", binding, err)
		}
	}
	for _, request := range []*modv1.InvokeModuleRequest{
		{Module: modcb.TradeConsentPolicyModule, Method: modcb.TradeConsentPolicyMethod},
		tradeConsentHostRequest(t, map[string]any{"sessionId": sid, "selfGuid": "4", "extra": strings.Repeat("x", modcb.TradeConsentPolicyMaxBytes)}),
	} {
		if _, err := m.invokeModule(ctx, "buddy-trade", request); status.Code(err) != codes.InvalidArgument {
			t.Fatal("malformed/unbounded typed input was admitted", err)
		}
	}
	for _, closed := range []*bool{&m.closing, &m.preparing, &m.prepared} {
		m.mu.Lock()
		*closed = true
		m.mu.Unlock()
		_, err := m.invokeModule(ctx, "buddy-trade", request)
		m.mu.Lock()
		*closed = false
		m.mu.Unlock()
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatal("closing/prepared body retained consent", err)
		}
	}
}

func TestPolicyTradeConsentAbsenceAndPublicationGapNeverUseOldSource(t *testing.T) {
	ctx := context.Background()
	const sid = "trade-consent-publication"
	m := catalogManager(t, 4, sid)
	t.Cleanup(m.cancel)
	input := tradeConsentHostInput(sid, "4")
	if out := tradeConsentHostRead(t, m, input); out["available"] != false || out["revision"] != "" || len(out["decision"].(map[string]any)) != 0 {
		t.Fatal("unattached policy invented consent", out)
	}
	host, err := m.StartPolicy(ctx, `return {`+tradeConsentFixtureHooks+`}`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.close)
	if out := tradeConsentHostRead(t, m, input); out["available"] != false || out["revision"] != host.vm.Status().Revision || len(out["decision"].(map[string]any)) != 0 {
		t.Fatal("absent optional export invented consent", out)
	}
	changed := `return {` + tradeConsentFixtureHooks + `trade_consent=function(input)return {kind="wait",reason="new_source"}end}`
	revision, err := host.vm.Reload(ctx, changed, host.vm.Status().Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.invokeModule(ctx, "buddy-trade", tradeConsentHostRequest(t, input)); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("committed but unpublished source restored old availability", err)
	}
	host.publishTradeConsent(changed, revision)
	if out := tradeConsentHostRead(t, m, input); out["available"] != true || out["revision"] != revision || out["decision"].(map[string]any)["kind"] != "wait" {
		t.Fatal("new publication was not used", out)
	}
}
