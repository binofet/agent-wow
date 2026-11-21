package buddyactions

import actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"

func (s *Service) interactionObservationLocked() *actionsv1.InteractionObservation {
	return &actionsv1.InteractionObservation{Owner: s.state.InteractionOwner, Token: s.interactionToken}
}
