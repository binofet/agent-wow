package buddyactions

import (
	"context"
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"time"
)

type engageIntent struct {
	phase string
}

func (e *engageIntent) snapshot(time.Time) *actionsv1.EngageNPCState {
	return &actionsv1.EngageNPCState{Phase: e.phase}
}
func (s *Service) EngageNPC(ctx context.Context, r *actionsv1.EngageNPCRequest) (*actionsv1.State, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "engage request required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, status.Error(codes.FailedPrecondition, "session is closing")
	}
	if r.Enabled {
		return nil, status.Error(codes.FailedPrecondition, "EngageNPC requires the current attached Lua policy input route")
	}
	s.engage.phase = "legacy_intent_retired_lua_defense_unchanged"
	return s.stateLocked(), nil
}
