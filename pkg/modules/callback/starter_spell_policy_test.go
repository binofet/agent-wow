package modcb

import (
	"context"
	"errors"
	"strings"
	"testing"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

type starterSpellTestClient func(context.Context, *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error)

func (f starterSpellTestClient) InvokeModule(ctx context.Context, request *modv1.InvokeModuleRequest, _ ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	return f(ctx, request)
}

func starterSpellTestResponse(t *testing.T, fields map[string]any) *modv1.InvokeModuleResponse {
	t.Helper()
	value, err := structpb.NewStruct(fields)
	if err != nil {
		t.Fatal(err)
	}
	packed, err := anypb.New(value)
	if err != nil {
		t.Fatal(err)
	}
	return &modv1.InvokeModuleResponse{Result: packed}
}

func TestStarterSpellDecisionUsesPrivateTypedBodyBoundCallback(t *testing.T) {
	revision := strings.Repeat("ab", 32)
	input := map[string]any{"sessionId": "body-session", "selfGuid": "4", "observation": map[string]any{"visitId": "19"}}
	client := starterSpellTestClient(func(ctx context.Context, request *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
		if request.Module != StarterSpellPolicyModule || request.Method != StarterSpellPolicyMethod {
			t.Fatal("wrong private capability", request)
		}
		value := &structpb.Struct{}
		if request.GetRequest().UnmarshalTo(value) != nil {
			t.Fatal("callback lost its typed request")
		}
		fields := value.AsMap()
		gotInput, ok := fields["input"].(map[string]any)
		if len(fields) != 3 || fields["sessionId"] != "body-session" || fields["selfGuid"] != "4" || !ok || gotInput["selfGuid"] != "4" {
			t.Fatal("callback lost exact current body", fields)
		}
		return starterSpellTestResponse(t, map[string]any{"available": true, "revision": revision,
			"decision": map[string]any{"kind": "cast", "spellId": float64(133)}}), nil
	})
	available, gotRevision, decision, err := StarterSpellDecision(context.Background(), client, "body-session", 4, input)
	if err != nil || !available || gotRevision != revision || decision["spellId"] != float64(133) {
		t.Fatal("current typed callback result lost", available, gotRevision, decision, err)
	}
}

func TestStarterSpellDecisionRejectsUnboundedOrAmbiguousResponses(t *testing.T) {
	revision := strings.Repeat("ab", 32)
	for name, fields := range map[string]map[string]any{
		"missing_decision":   {"available": false, "revision": ""},
		"missing_revision":   {"available": false, "decision": map[string]any{}},
		"unbound_available":  {"available": true, "revision": "", "decision": map[string]any{}},
		"malformed_revision": {"available": true, "revision": "old", "decision": map[string]any{}},
		"uppercase_revision": {"available": true, "revision": strings.ToUpper(revision), "decision": map[string]any{}},
		"extra_field":        {"available": true, "revision": revision, "decision": map[string]any{}, "other": true},
		"nonobject_decision": {"available": true, "revision": revision, "decision": true},
		"large_decision":     {"available": true, "revision": revision, "decision": map[string]any{"payload": strings.Repeat("x", StarterSpellPolicyMaxBytes)}},
	} {
		t.Run(name, func(t *testing.T) {
			client := starterSpellTestClient(func(context.Context, *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
				return starterSpellTestResponse(t, fields), nil
			})
			available, gotRevision, decision, err := StarterSpellDecision(context.Background(), client, "s", 4, map[string]any{})
			if err == nil || available || gotRevision != "" || decision != nil {
				t.Fatal("malformed response became an actionable current choice", available, gotRevision, decision, err)
			}
		})
	}
	called := false
	client := starterSpellTestClient(func(context.Context, *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
		called = true
		return nil, nil
	})
	if _, _, _, err := StarterSpellDecision(context.Background(), client, "s", 4,
		map[string]any{"payload": strings.Repeat("x", StarterSpellPolicyMaxBytes)}); err == nil || called {
		t.Fatal("oversized pure input reached transport", called, err)
	}
	transport := errors.New("disconnected body")
	client = func(context.Context, *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
		return nil, transport
	}
	if _, _, _, err := StarterSpellDecision(context.Background(), client, "s", 4, map[string]any{}); !errors.Is(err, transport) {
		t.Fatal("transport failure was converted to unavailable source", err)
	}
}

func TestStarterSpellDecisionDistinguishesNoSourceFromMissingCurrentExport(t *testing.T) {
	for _, revision := range []string{"", strings.Repeat("cd", 32)} {
		client := starterSpellTestClient(func(context.Context, *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
			return starterSpellTestResponse(t, map[string]any{"available": false, "revision": revision, "decision": map[string]any{}}), nil
		})
		available, gotRevision, decision, err := StarterSpellDecision(context.Background(), client, "s", 4, map[string]any{})
		if err != nil || available || gotRevision != revision || decision == nil || len(decision) != 0 {
			t.Fatal("source availability distinction lost", available, gotRevision, decision, err)
		}
	}
}
