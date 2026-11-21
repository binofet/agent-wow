package modcb

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	FollowPolicyCallbackEnv = "AGENT_WOW_FOLLOW_POLICY_CALLBACK"
	FollowPolicyModule      = "session"
	FollowPolicyMethod      = "follow_decision"
	FollowPolicyMaxBytes    = 32 * 1024
)

type FollowPolicyClient interface {
	InvokeModule(context.Context, *modv1.InvokeModuleRequest, ...grpc.CallOption) (*modv1.InvokeModuleResponse, error)
}

// FollowDecision is a read-only callback on the module's own session socket.
// It evaluates a pure export of the attached source, never its persistent
// callbacks or action ledger. Native callers retain movement admission.
func FollowDecision(ctx context.Context, client FollowPolicyClient, sid string, self uint64, input map[string]any) (bool, string, map[string]any, error) {
	value, err := structpb.NewStruct(map[string]any{"sessionId": sid, "selfGuid": strconv.FormatUint(self, 10), "input": input})
	if err != nil || proto.Size(value) > FollowPolicyMaxBytes {
		return false, "", nil, errors.New("invalid follow policy input")
	}
	packed, err := anypb.New(value)
	if err != nil {
		return false, "", nil, err
	}
	response, err := client.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: FollowPolicyModule, Method: FollowPolicyMethod, Request: packed})
	if err != nil {
		return false, "", nil, err
	}
	result := &structpb.Struct{}
	if response.GetResult() == nil || response.Result.UnmarshalTo(result) != nil || proto.Size(result) > FollowPolicyMaxBytes {
		return false, "", nil, errors.New("invalid follow policy response")
	}
	fields := result.AsMap()
	available, known := fields["available"].(bool)
	revision, revisionKnown := fields["revision"].(string)
	decision, decisionKnown := fields["decision"].(map[string]any)
	if !known || !revisionKnown || len(fields) != 3 || !decisionKnown {
		return false, "", nil, errors.New("incomplete follow policy response")
	}
	if revision != "" {
		decoded, err := hex.DecodeString(revision)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != revision {
			return false, "", nil, errors.New("invalid follow policy source revision")
		}
	}
	if available && revision == "" {
		return false, "", nil, errors.New("follow policy source revision required")
	}
	return available, revision, decision, nil
}
