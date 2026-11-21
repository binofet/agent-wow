package modrt

import (
	"context"
	"path/filepath"
	"strconv"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

// readDisposalDecision is a read-only classification callback, not action
// admission. In particular, an eligible result grants no packet authority.
func (m *Manager) readDisposalDecision(ctx context.Context, caller string, request *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
	if caller != "buddy-trade" {
		return nil, status.Error(codes.PermissionDenied, "disposal policy callback belongs to Trade")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	value := &structpb.Struct{}
	if request.GetRequest() == nil || request.Request.UnmarshalTo(value) != nil || proto.Size(value) > modcb.DisposalPolicyMaxBytes {
		return nil, status.Error(codes.InvalidArgument, "bounded typed disposal policy input required")
	}
	fields := value.AsMap()
	sid, a := fields["sessionId"].(string)
	self, b := fields["selfGuid"].(string)
	input, c := fields["input"].(map[string]any)
	if !a || !b || !c || len(fields) != 3 || input["selfGuid"] != self || input["sessionId"] != sid {
		return nil, status.Error(codes.InvalidArgument, "exact disposal body and pure input required")
	}
	m.mu.Lock()
	valid := m.ctx.Err() == nil && !m.closing && !m.preparing && !m.prepared && m.err == nil &&
		sid != "" && sid == filepath.Base(m.dir) && self == strconv.FormatUint(m.session.Identity.CharacterGUID, 10)
	host := m.policy
	if host != nil && host.body != self {
		valid = false
	}
	m.mu.Unlock()
	if !valid {
		return nil, status.Error(codes.FailedPrecondition, "current connected disposal body required")
	}
	available, revision, decision := false, "", map[string]any{}
	if host != nil {
		var err error
		available, revision, decision, err = host.disposalDecision(ctx, input)
		if err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "active disposal policy: %v", err)
		}
		if decision == nil {
			decision = map[string]any{}
		}
	}
	m.mu.Lock()
	current := m.ctx.Err() == nil && !m.closing && !m.preparing && !m.prepared && m.err == nil && m.policy == host &&
		sid == filepath.Base(m.dir) && self == strconv.FormatUint(m.session.Identity.CharacterGUID, 10)
	m.mu.Unlock()
	if !current {
		return nil, status.Error(codes.FailedPrecondition, "disposal session changed during evaluation")
	}
	result, err := structpb.NewStruct(map[string]any{"available": available, "revision": revision, "decision": decision})
	if err != nil || proto.Size(result) > modcb.DisposalPolicyMaxBytes {
		return nil, status.Error(codes.Internal, "bounded disposal policy result required")
	}
	packed, err := anypb.New(result)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &modv1.InvokeModuleResponse{Result: packed}, nil
}
