package buddyprofessions

import (
	"context"

	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PrepareStop serializes with Attempt's reservation. A read followed by SIGTERM
// alone cannot prevent another caller from starting a destructive item cast.
// This latch has no expiry: a late stop signal must never outlive its proof.
func (s *Service) PrepareStop(ctx context.Context, r *professionsv1.StopRequest) (*professionsv1.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r == nil || r.SessionId != s.state.SessionId || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.InvalidArgument, "exact_connected_session_and_character_required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if s.active != nil && s.active.spec.skill == disenchantSkill || s.pendingDisenchant != nil {
		return nil, status.Error(codes.FailedPrecondition, "selected_disenchant_pending_keep_connected_do_not_close_material_window")
	}
	if op := s.last; op != nil && op.spec.skill == disenchantSkill && (op.receipt.CleanupUnconfirmed || disenchantUnsettled(op) || op.receipt.Status == professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED) {
		return nil, status.Error(codes.FailedPrecondition, "selected_disenchant_outcome_or_cleanup_unconfirmed_keep_connected")
	}
	if !s.state.StopPrepared {
		s.state.StopPrepared = true
		s.state.Revision++
	}
	return s.snapshotLocked(), nil
}
