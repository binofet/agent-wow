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

func TestDisposalDecisionUsesExactTypedPrivateBodyEnvelope(t *testing.T) {
	revision := strings.Repeat("ab", 32)
	input := map[string]any{"sessionId": "disposal-body", "selfGuid": "4", "mode": "misc_destroy", "item": map[string]any{"itemId": float64(99000)}}
	client := starterSpellTestClient(func(_ context.Context, request *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
		if request.Module != DisposalPolicyModule || request.Method != DisposalPolicyMethod {
			t.Fatal("wrong private disposal route", request)
		}
		value := &structpb.Struct{}
		if request.GetRequest().UnmarshalTo(value) != nil {
			t.Fatal("request lost its typed object")
		}
		fields := value.AsMap()
		gotInput, ok := fields["input"].(map[string]any)
		if len(fields) != 3 || fields["sessionId"] != "disposal-body" || fields["selfGuid"] != "4" || !ok || gotInput["sessionId"] != "disposal-body" || gotInput["selfGuid"] != "4" || gotInput["mode"] != "misc_destroy" {
			t.Fatal("callback lost the exact body or factual input", fields)
		}
		return starterSpellTestResponse(t, map[string]any{"available": true, "revision": revision,
			"decision": map[string]any{"status": "keep", "reason": "current_policy"}}), nil
	})
	available, gotRevision, decision, err := DisposalDecision(context.Background(), client, "disposal-body", 4, input)
	if err != nil || !available || gotRevision != revision || decision["status"] != "keep" {
		t.Fatal("current disposal result lost", available, gotRevision, decision, err)
	}
}

func TestDisposalDecisionRejectsMalformedAndUnboundedResults(t *testing.T) {
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
		"large_decision":     {"available": true, "revision": revision, "decision": map[string]any{"reason": strings.Repeat("x", DisposalPolicyMaxBytes)}},
	} {
		t.Run(name, func(t *testing.T) {
			client := starterSpellTestClient(func(context.Context, *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
				return starterSpellTestResponse(t, fields), nil
			})
			available, gotRevision, decision, err := DisposalDecision(context.Background(), client, "s", 4, map[string]any{})
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
			if available, revision, decision, err := DisposalDecision(context.Background(), client, "s", 4, map[string]any{}); err == nil || available || revision != "" || decision != nil {
				t.Fatal("untyped response accepted", available, revision, decision, err)
			}
		})
	}
}

func TestDisposalDecisionInputBoundsTransportAndAvailability(t *testing.T) {
	calls := 0
	client := starterSpellTestClient(func(context.Context, *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
		calls++
		return nil, nil
	})
	for _, input := range []map[string]any{
		{"payload": strings.Repeat("x", DisposalPolicyMaxBytes)},
		{"invalid": make(chan int)},
	} {
		if _, _, _, err := DisposalDecision(context.Background(), client, "s", 4, input); err == nil || calls != 0 {
			t.Fatal("invalid input reached transport", calls, err)
		}
	}
	transport := errors.New("disposal callback disconnected")
	client = func(context.Context, *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
		return nil, transport
	}
	if _, _, _, err := DisposalDecision(context.Background(), client, "s", 4, map[string]any{}); !errors.Is(err, transport) {
		t.Fatal("transport failure became an absent source", err)
	}
	for _, revision := range []string{"", strings.Repeat("cd", 32)} {
		client = func(context.Context, *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
			return starterSpellTestResponse(t, map[string]any{"available": false, "revision": revision, "decision": map[string]any{}}), nil
		}
		available, gotRevision, decision, err := DisposalDecision(context.Background(), client, "s", 4, map[string]any{})
		if err != nil || available || gotRevision != revision || decision == nil || len(decision) != 0 {
			t.Fatal("no source and missing current export were conflated", available, gotRevision, decision, err)
		}
	}
}
