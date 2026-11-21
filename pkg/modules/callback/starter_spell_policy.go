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
	StarterSpellPolicyCallbackEnv = "AGENT_WOW_STARTER_SPELL_POLICY_CALLBACK"
	StarterSpellPolicyModule      = "session"
	StarterSpellPolicyMethod      = "starter_spell_decision"
	StarterSpellPolicyMaxBytes    = 32 * 1024
)

// StarterSpellDecision reads the pure optional export of this body's current
// accepted source. It cannot advance the persistent policy or dispatch actions.
func StarterSpellDecision(ctx context.Context, client FollowPolicyClient, sid string, self uint64, input map[string]any) (bool, string, map[string]any, error) {
	value, err := structpb.NewStruct(map[string]any{"sessionId": sid, "selfGuid": strconv.FormatUint(self, 10), "input": input})
	if err != nil || proto.Size(value) > StarterSpellPolicyMaxBytes {
		return false, "", nil, errors.New("invalid starter spell policy input")
	}
	packed, err := anypb.New(value)
	if err != nil {
		return false, "", nil, err
	}
	response, err := client.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: StarterSpellPolicyModule, Method: StarterSpellPolicyMethod, Request: packed})
	if err != nil {
		return false, "", nil, err
	}
	result := &structpb.Struct{}
	if response.GetResult() == nil || response.Result.UnmarshalTo(result) != nil || proto.Size(result) > StarterSpellPolicyMaxBytes {
		return false, "", nil, errors.New("invalid starter spell policy response")
	}
	fields := result.AsMap()
	available, known := fields["available"].(bool)
	revision, revisionKnown := fields["revision"].(string)
	decision, decisionKnown := fields["decision"].(map[string]any)
	if !known || !revisionKnown || len(fields) != 3 || !decisionKnown {
		return false, "", nil, errors.New("incomplete starter spell policy response")
	}
	if revision != "" {
		decoded, err := hex.DecodeString(revision)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != revision {
			return false, "", nil, errors.New("invalid starter spell policy source revision")
		}
	}
	if available && revision == "" {
		return false, "", nil, errors.New("starter spell policy source revision required")
	}
	return available, revision, decision, nil
}
