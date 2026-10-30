package buddyquests

import (
	"context"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) acquire(ctx context.Context) (string, error) {
	result := &actionsv1.InteractionLease{}
	if err := s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "quests", LeaseMs: 15000}, result); err != nil {
		return "", err
	}
	if result.Token == "" || result.State == nil || result.State.Moving || result.State.MotionUnconfirmed || result.State.InteractionOwner != "quests" {
		if result.Token != "" {
			s.release(result.Token)
		}
		return "", status.Error(codes.FailedPrecondition, "quest interaction lease did not confirm stopped ownership")
	}
	return result.Token, nil
}

func (s *Service) release(token string) {
	// Release remains bounded even after the requesting RPC was canceled. A
	// failed release expires naturally; it never initiates another game action.
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	_ = s.invoke(ctx, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "quests", Token: token}, &actionsv1.State{})
}
