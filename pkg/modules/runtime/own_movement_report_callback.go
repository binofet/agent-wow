package modrt

import (
	"context"
	"path/filepath"
	"strconv"

	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

// Only the normal Actions executor may read its own transport's successful
// movement report. This callback adds no packet-writing or generic RPC route.
func (m *Manager) readOwnMovementReport(ctx context.Context, caller string, request *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
	if caller != actionsModule {
		return nil, status.Error(codes.PermissionDenied, "own movement report belongs to Actions")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	input := &structpb.Struct{}
	if request.GetRequest() == nil || request.Request.UnmarshalTo(input) != nil || proto.Size(input) > 512 {
		return nil, status.Error(codes.InvalidArgument, "bounded typed movement-report identity required")
	}
	fields := input.AsMap()
	sid, a := fields["sessionId"].(string)
	self, b := fields["selfGuid"].(string)
	guid, err := strconv.ParseUint(self, 10, 64)
	if !a || !b || len(fields) != 2 || sid == "" || len(sid) > 128 || err != nil || guid == 0 || strconv.FormatUint(guid, 10) != self {
		return nil, status.Error(codes.InvalidArgument, "exact canonical movement-report body required")
	}
	m.mu.Lock()
	valid := m.ctx.Err() == nil && !m.closing && !m.preparing && !m.prepared && m.err == nil &&
		sid == filepath.Base(m.dir) && guid == m.session.Identity.CharacterGUID
	read, scope := m.session.OwnMovementReport, m.session.TransportScope
	m.mu.Unlock()
	if !valid || read == nil || scope == nil {
		return nil, status.Error(codes.FailedPrecondition, "current own movement-report transport unavailable")
	}
	packet := read()
	if packet == nil || packet.TransportScope == nil || packet.TransportScope.SelfGuid != guid ||
		packet.TransportScope.ConnectionId == "" || !packet.TransportScope.VisitBound || packet.TransportScope.VisitRevision == 0 ||
		len(packet.Payload) == 0 || len(packet.Payload) > 256 || !proto.Equal(packet.TransportScope, scope()) {
		return nil, status.Error(codes.FailedPrecondition, "successful own movement report is no longer current")
	}
	m.mu.Lock()
	current := m.ctx.Err() == nil && !m.closing && !m.preparing && !m.prepared && m.err == nil &&
		sid == filepath.Base(m.dir) && guid == m.session.Identity.CharacterGUID
	m.mu.Unlock()
	if !current {
		return nil, status.Error(codes.FailedPrecondition, "movement-report session changed during read")
	}
	packed, err := anypb.New(packet)
	if err != nil {
		return nil, status.Error(codes.Internal, "movement-report encoding failed")
	}
	return &modv1.InvokeModuleResponse{Result: packed}, nil
}
