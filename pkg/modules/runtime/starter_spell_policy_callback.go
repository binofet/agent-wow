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

func (m *Manager) readStarterSpellDecision(ctx context.Context, caller string, request *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
	if caller != actionsModule {
		return nil, status.Error(codes.PermissionDenied, "starter spell policy callback belongs to Actions")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	value := &structpb.Struct{}
	if request.GetRequest() == nil || request.Request.UnmarshalTo(value) != nil || proto.Size(value) > modcb.StarterSpellPolicyMaxBytes {
		return nil, status.Error(codes.InvalidArgument, "bounded typed starter spell policy input required")
	}
	fields := value.AsMap()
	sid, a := fields["sessionId"].(string)
	self, b := fields["selfGuid"].(string)
	input, c := fields["input"].(map[string]any)
	if !a || !b || !c || len(fields) != 3 || input["selfGuid"] != self || input["sessionId"] != sid {
		return nil, status.Error(codes.InvalidArgument, "exact starter spell body and pure input required")
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
		return nil, status.Error(codes.FailedPrecondition, "current connected starter spell body required")
	}
	available, revision, decision := false, "", map[string]any{}
	if host != nil {
		var err error
		available, revision, decision, err = host.starterSpellDecision(ctx, input)
		if err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "active starter spell policy: %v", err)
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
		return nil, status.Error(codes.FailedPrecondition, "starter spell session changed during evaluation")
	}
	result, err := structpb.NewStruct(map[string]any{"available": available, "revision": revision, "decision": decision})
	if err != nil || proto.Size(result) > modcb.StarterSpellPolicyMaxBytes {
		return nil, status.Error(codes.Internal, "bounded starter spell policy result required")
	}
	packed, err := anypb.New(result)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &modv1.InvokeModuleResponse{Result: packed}, nil
}
