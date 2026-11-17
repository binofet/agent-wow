package buddyactions

import (
	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UseHostEncounterWriter hands autonomous offense to the attached Lua session
// host. Call it before starting the service or exposing its RPCs.
func (s *Service) UseHostEncounterWriter() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hostEncounterOwned = true
}

func (s *Service) inactiveNativeEncounterLocked(enable bool) (*actionsv1.State, error) {
	if enable {
		return nil, status.Error(codes.FailedPrecondition, "Lua session host owns autonomous offense")
	}
	// There is no native intent to stop. An exact host action can be in flight.
	return s.stateLocked(), nil
}
