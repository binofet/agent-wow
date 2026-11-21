package buddyactions

import (
	"context"
	"time"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
)

// UseHostFollowPolicy enables the read-only callback advertised by a matching
// core. No policy or no exported follow function retains legacy defaults.
func (s *Service) UseHostFollowPolicy() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.formation.remote = func(input map[string]any) (bool, string, map[string]any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		return modcb.FollowDecision(ctx, s.session, s.sessionID, s.selfGUID, input)
	}
}
