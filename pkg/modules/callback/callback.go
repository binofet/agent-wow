// Package modcb adapts session callbacks to each module's gRPC endpoint.
package modcb

import (
	"context"

	"github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Handlers supplies concurrent session callbacks. Implementations enforce packet
// policy, routing validation, deadlines, and lifecycle rules.
type Handlers struct {
	SendPacket       func(context.Context, uint32, []byte) error
	SendScopedPacket func(context.Context, uint32, []byte, *modv1.TransportScope) error
	Clock            func() uint32
	InvokeModule     func(context.Context, string, *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error)
}

// Server implements modv1.SessionServer for one module's callback endpoint.
type Server struct {
	modv1.UnimplementedSessionServer
	lifetime context.Context
	caller   string
	handlers Handlers
}

// New binds the caller and session lifetime to handlers for registration with modv1.RegisterSessionServer.
func New(lifetime context.Context, caller string, handlers Handlers) *Server {
	return &Server{lifetime: lifetime, caller: caller, handlers: handlers}
}

func (s *Server) SendPacket(ctx context.Context, req *modv1.SendPacketRequest) (*emptypb.Empty, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if s.lifetime.Err() != nil {
		return nil, status.Error(codes.FailedPrecondition, "session is closed")
	}
	if scope := req.GetTransportScope(); scope != nil {
		if s.handlers.SendScopedPacket == nil {
			return nil, PacketNotDispatchedError("transport scope is unsupported by this host")
		}
		if err := s.handlers.SendScopedPacket(ctx, req.GetOpcode(), append([]byte(nil), req.GetPayload()...), proto.Clone(scope).(*modv1.TransportScope)); err != nil {
			return nil, err
		}
		return &emptypb.Empty{}, nil
	}
	if err := s.handlers.SendPacket(ctx, req.GetOpcode(), req.GetPayload()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) GetClock(ctx context.Context, _ *emptypb.Empty) (*modv1.ClockResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if s.lifetime.Err() != nil {
		return nil, status.Error(codes.FailedPrecondition, "session is closed")
	}
	return &modv1.ClockResponse{ClientTimeMs: s.handlers.Clock()}, nil
}

func (s *Server) InvokeModule(ctx context.Context, req *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
	return s.handlers.InvokeModule(ctx, s.caller, req)
}
