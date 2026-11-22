package modcb

import (
	"context"
	"errors"
	"strings"
	"testing"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestTradeConsentDecisionUsesExactTypedPrivateBodyEnvelope(t *testing.T) {
	revision := strings.Repeat("ab", 32)
	input := map[string]any{"sessionId": "trade-body", "selfGuid": "4", "trade": map[string]any{"phase": "OPEN", "revision": "7"}}
	client := starterSpellTestClient(func(_ context.Context, request *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
		if request.Module != TradeConsentPolicyModule || request.Method != TradeConsentPolicyMethod {
			t.Fatal("wrong private trade consent route", request)
		}
		value := &structpb.Struct{}
		if request.GetRequest().UnmarshalTo(value) != nil {
			t.Fatal("request lost its typed object")
		}
		fields := value.AsMap()
		gotInput, ok := fields["input"].(map[string]any)
		if len(fields) != 3 || fields["sessionId"] != "trade-body" || fields["selfGuid"] != "4" || !ok || gotInput["sessionId"] != "trade-body" || gotInput["selfGuid"] != "4" || gotInput["trade"].(map[string]any)["revision"] != "7" {
			t.Fatal("callback lost the exact body or factual input", fields)
		}
		return starterSpellTestResponse(t, map[string]any{"available": true, "revision": revision,
			"decision": map[string]any{"kind": "accept", "reason": "current_policy"}}), nil
	})
	available, gotRevision, decision, err := TradeConsentDecision(context.Background(), client, "trade-body", 4, input)
	if err != nil || !available || gotRevision != revision || decision["kind"] != "accept" {
		t.Fatal("current trade consent result lost", available, gotRevision, decision, err)
	}
}

func TestTradeConsentDecisionRejectsMalformedAndUnboundedResults(t *testing.T) {
	revision := strings.Repeat("ab", 32)
	for name, fields := range map[string]map[string]any{
		"missing_available":  {"revision": revision, "decision": map[string]any{}},
		"nonbool_available":  {"available": "true", "revision": revision, "decision": map[string]any{}},
		"missing_revision":   {"available": false, "decision": map[string]any{}},
		"unbound_available":  {"available": true, "revision": "", "decision": map[string]any{}},
		"malformed_revision": {"available": true, "revision": "old", "decision": map[string]any{}},
		"uppercase_revision": {"available": true, "revision": strings.ToUpper(revision), "decision": map[string]any{}},
		"missing_decision":   {"available": false, "revision": ""},
		"nonobject_decision": {"available": true, "revision": revision, "decision": []any{}},
		"caller_grant_field": {"available": true, "revision": revision, "decision": map[string]any{}, "eligible": true},
		"large_decision":     {"available": true, "revision": revision, "decision": map[string]any{"reason": strings.Repeat("x", TradeConsentPolicyMaxBytes)}},
	} {
		t.Run(name, func(t *testing.T) {
			client := starterSpellTestClient(func(context.Context, *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
				return starterSpellTestResponse(t, fields), nil
			})
			available, gotRevision, decision, err := TradeConsentDecision(context.Background(), client, "s", 4, map[string]any{})
			if err == nil || available || gotRevision != "" || decision != nil {
				t.Fatal("malformed result became an accepted decision", available, gotRevision, decision, err)
			}
		})
	}
	wrongType, err := anypb.New(&emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	for name, response := range map[string]*modv1.InvokeModuleResponse{
		"nil_response": nil, "missing_result": {}, "wrong_result_type": {Result: wrongType},
	} {
		t.Run(name, func(t *testing.T) {
			client := starterSpellTestClient(func(context.Context, *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
				return response, nil
			})
			if available, revision, decision, err := TradeConsentDecision(context.Background(), client, "s", 4, map[string]any{}); err == nil || available || revision != "" || decision != nil {
				t.Fatal("untyped response accepted", available, revision, decision, err)
			}
		})
	}
}

func TestTradeConsentDecisionInputBoundsTransportAndAvailability(t *testing.T) {
	calls := 0
	client := starterSpellTestClient(func(context.Context, *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
		calls++
		return nil, nil
	})
	for _, input := range []map[string]any{
		{"payload": strings.Repeat("x", TradeConsentPolicyMaxBytes)},
		{"invalid": make(chan int)},
	} {
		if _, _, _, err := TradeConsentDecision(context.Background(), client, "s", 4, input); err == nil || calls != 0 {
			t.Fatal("invalid input reached transport", calls, err)
		}
	}
	transport := errors.New("trade consent callback disconnected")
	client = func(context.Context, *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
		return nil, transport
	}
	if _, _, _, err := TradeConsentDecision(context.Background(), client, "s", 4, map[string]any{}); !errors.Is(err, transport) {
		t.Fatal("transport failure became an absent source", err)
	}
	for _, revision := range []string{"", strings.Repeat("cd", 32)} {
		client = func(context.Context, *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
			return starterSpellTestResponse(t, map[string]any{"available": false, "revision": revision, "decision": map[string]any{}}), nil
		}
		available, gotRevision, decision, err := TradeConsentDecision(context.Background(), client, "s", 4, map[string]any{})
		if err != nil || available || gotRevision != revision || decision == nil || len(decision) != 0 {
			t.Fatal("no source and missing current export were conflated", available, gotRevision, decision, err)
		}
	}
}
