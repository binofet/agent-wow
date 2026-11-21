package buddyactions

import (
	"context"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReleaseSpirit executes one ordinary release for the positively observed death.
// It never chooses whether/when to release or turns on the legacy corpse runner.
func (s *Service) ReleaseSpirit(ctx context.Context, request *actionsv1.DeathActionRequest) (*actionsv1.State, error) {
	return s.deathAction(ctx, request, false)
}

// QueryCorpse issues the ordinary empty corpse query once for this death. The
// server reply, not a successful write, supplies corpse presence and location.
func (s *Service) QueryCorpse(ctx context.Context, request *actionsv1.DeathActionRequest) (*actionsv1.State, error) {
	return s.deathAction(ctx, request, true)
}

func (s *Service) deathAction(ctx context.Context, request *actionsv1.DeathActionRequest, query bool) (*actionsv1.State, error) {
	if request == nil || request.SessionId == "" || request.SelfGuid == 0 || request.DeathEpoch == 0 {
		return nil, status.Error(codes.InvalidArgument, "current session, body and observed death epoch required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || request.SessionId != s.sessionID || request.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "death action body/session changed")
	}
	if s.state.InteractionOwner != "" && !s.recoveryOwnsLocked() {
		return nil, status.Error(codes.FailedPrecondition, "another physical interaction owns this body")
	}
	w, err := s.snapshot(ctx)
	if err != nil || !recoverySelf(w, s.selfGUID) || w.Self.Alive || request.DeathEpoch != s.recovery.state.DeathEpoch {
		return nil, status.Error(codes.FailedPrecondition, "current observed death required")
	}
	r, p := &s.recovery, w.Self
	if query {
		if p.PlayerFlags&ghostFlag == 0 {
			return nil, status.Error(codes.FailedPrecondition, "actual released ghost required")
		}
		if r.state.CorpseQueryKnown || r.queryPending {
			return s.stateLocked(), nil
		}
		// Reserve before the callback. Unknown transport remains reserved until an
		// actual response or a positively observed new life/death, including reload.
		r.queryPending = true
		r.state.Phase = "querying_actual_corpse"
		if err := s.sendPacketLocked(modcb.ForwardPolicySpell(ctx), opcode.MSGCorpseQuery, nil); err != nil {
			if modcb.PacketWasNotDispatched(err) {
				r.queryPending = false
				r.state.Phase = "corpse_query_not_dispatched"
				return s.stateLocked(), err
			}
			r.state.Reason = "corpse_query_transport_unconfirmed_no_retry"
		}
	} else {
		if r.state.ReleaseAttempted {
			return s.stateLocked(), nil
		}
		if p.PlayerFlags&ghostFlag != 0 || p.Health != 0 {
			return nil, status.Error(codes.FailedPrecondition, "actual unreleased zero-health body required")
		}
		r.releasePositionRevision, r.releaseMap = p.PositionRevision, p.MapId
		r.state.ReleaseAttempted = true
		r.state.Phase = "release_requested"
		err := s.sendPacketLocked(modcb.ForwardPolicySpell(ctx), opcode.CMSGRepopRequest, []byte{0})
		r.state.ReleaseWritten = err == nil
		if modcb.PacketWasNotDispatched(err) {
			r.state.ReleaseAttempted = false
			r.state.Phase = "release_not_dispatched"
			return s.stateLocked(), err
		}
		if !r.state.ReleaseWritten {
			r.state.Reason = "release_transport_unconfirmed_no_retry"
		}
	}
	return s.stateLocked(), nil
}
