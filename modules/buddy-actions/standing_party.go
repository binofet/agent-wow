package buddyactions

import (
	"context"
	"time"
)

type standingPartyRole struct {
	assigned, healer, withOwner bool
	checkPartyAfter             time.Time
	resumeFollowAfter           time.Time
}

// UseStandingPartyRole is set by the authenticated companion's launch profile.
func (s *Service) UseStandingPartyRole(healer bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.independent {
		s.standingParty = standingPartyRole{assigned: true, healer: healer}
	}
}

func (s *Service) standingPartyTickLocked(ctx context.Context, now time.Time) {
	role := &s.standingParty
	if !role.assigned || s.state.InteractionOwner != "" || s.state.Practicing || s.state.Traveling {
		return
	}
	if !now.Before(role.checkPartyAfter) {
		role.checkPartyAfter = now.Add(2 * time.Second)
		party := s.healingPartyLocked(ctx)
		role.withOwner = party[ownerGUID]
	}
	if s.state.MotionUnconfirmed {
		if world, err := s.snapshot(ctx); err == nil {
			s.refreshPositionLocked(world)
		}
		if s.state.MotionUnconfirmed {
			return
		}
	}

	if role.healer {
		s.state.Healing, s.healingUntil = true, now.Add(lease)
	}
	if role.withOwner {
		s.state.Assisting, s.assistUntil = true, now.Add(lease)
		if !now.Before(role.resumeFollowAfter) {
			if !s.state.Following {
				s.lastStep = now
			}
			s.state.Following, s.followUntil = true, now.Add(lease)
		}
	} else {
		s.state.Assisting = false
		if s.state.Following {
			s.state.Following = false
			s.stopLocked(ctx)
		}
	}
}
