package buddyquests

import (
	"context"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
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

// Only an explicitly requested interaction may close a short range gap. The
// actuator owns navigation bounds and movement cleanup; this module still
// requires a fresh strict interaction gate before any quest packet is sent.
func (s *Service) prepareInteraction(ctx context.Context, token string, npcGUID uint64, guard func() error) (*worldv1.SnapshotResponse, error) {
	w, err := s.gate(ctx, npcGUID)
	if err != nil && (status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "questgiver_outside_conservative_range") {
		return nil, err
	}
	if guard != nil {
		if check := guard(); check != nil {
			return nil, check
		}
	}
	if err == nil {
		return w, nil
	}
	result := &actionsv1.State{}
	if err := s.invoke(ctx, "buddy-actions", "approach_quest", &actionsv1.ApproachQuestRequest{Token: token, NpcGuid: npcGUID}, result); err != nil {
		return nil, err
	}
	if result.Reason != "quest_approach_within_distance" || result.Moving || result.MotionUnconfirmed || result.InteractionOwner != "quests" {
		return nil, status.Error(codes.FailedPrecondition, "quest approach did not confirm a stopped owned interaction")
	}
	w, err = s.gate(ctx, npcGUID)
	if err != nil {
		return nil, err
	}
	if guard != nil {
		if err := guard(); err != nil {
			return nil, err
		}
	}
	return w, nil
}

func (s *Service) release(token string) {
	// Release remains bounded even after the requesting RPC was canceled. A
	// failed release expires naturally; it never initiates another game action.
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	_ = s.invoke(ctx, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "quests", Token: token}, &actionsv1.State{})
}
