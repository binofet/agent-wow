package modcb

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	DisposalPolicyModule   = "session"
	DisposalPolicyMethod   = "disposal_decision"
	DisposalPolicyMaxBytes = 32 * 1024
)

// DisposalDecision reads a pure classification export of the current accepted
// policy through the module's own session socket. It cannot reserve an action,
// advance the persistent policy, or authorize a native packet write.
func DisposalDecision(ctx context.Context, client FollowPolicyClient, sid string, self uint64, input map[string]any) (bool, string, map[string]any, error) {
	value, err := structpb.NewStruct(map[string]any{"sessionId": sid, "selfGuid": strconv.FormatUint(self, 10), "input": input})
	if err != nil || proto.Size(value) > DisposalPolicyMaxBytes {
		return false, "", nil, errors.New("invalid disposal policy input")
	}
	packed, err := anypb.New(value)
	if err != nil {
		return false, "", nil, err
	}
	response, err := client.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: DisposalPolicyModule, Method: DisposalPolicyMethod, Request: packed})
	if err != nil {
		return false, "", nil, err
	}
	result := &structpb.Struct{}
	if response.GetResult() == nil || response.Result.UnmarshalTo(result) != nil || proto.Size(result) > DisposalPolicyMaxBytes {
		return false, "", nil, errors.New("invalid disposal policy response")
	}
	fields := result.AsMap()
	available, known := fields["available"].(bool)
	revision, revisionKnown := fields["revision"].(string)
	decision, decisionKnown := fields["decision"].(map[string]any)
	if !known || !revisionKnown || len(fields) != 3 || !decisionKnown {
		return false, "", nil, errors.New("incomplete disposal policy response")
	}
	if revision != "" {
		decoded, err := hex.DecodeString(revision)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != revision {
			return false, "", nil, errors.New("invalid disposal policy source revision")
		}
	}
	if available && revision == "" {
		return false, "", nil, errors.New("disposal policy source revision required")
	}
	return available, revision, decision, nil
}
