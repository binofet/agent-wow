package modrt

import (
	"context"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

func (m *Manager) readKnockback(ctx context.Context, caller string, req *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
	world := caller == "buddy-world"
	if !world && caller != actionsModule {
		return nil, status.Error(codes.PermissionDenied, "native knockback report is private to World and Actions")
	}
	p := &modv1.WorldPacket{}
	if req.GetRequest() == nil || len(req.Request.Value) > 512 || req.Request.UnmarshalTo(p) != nil || p.Opcode != opcode.SMSGMoveKnockBack {
		return nil, status.Error(codes.InvalidArgument, "actual scoped knockback order required")
	}
	k, err := movementinfo.DecodeKnockback(p.Payload)
	m.mu.Lock()
	valid := err == nil && ctx.Err() == nil && m.ctx.Err() == nil && !m.closing && !m.preparing && !m.prepared && m.err == nil && k.Mover == m.session.Identity.CharacterGUID && routeScopeBound(p.TransportScope) && p.TransportScope.SelfGuid == k.Mover
	read := m.session.KnockbackReport
	m.mu.Unlock()
	if !valid || read == nil {
		return nil, status.Error(codes.FailedPrecondition, "current knockback source unavailable")
	}
	out := read(p, world)
	if out == nil {
		return nil, status.Error(codes.FailedPrecondition, "knockback report unavailable")
	}
	value, err := anypb.New(out)
	return &modv1.InvokeModuleResponse{Result: value}, err
}
