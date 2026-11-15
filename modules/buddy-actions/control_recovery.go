package buddyactions

import (
	"context"

	"github.com/agent-wow/agent-wow/pkg/movementflags"
)

const (
	unitFlagsForcedMovement = 0x00400000 | 0x00800000 // confused, fleeing
	spellFailedConfused     = 26
	spellFailedFleeing      = 34
)

// The server takes our movement away during fear and restores it when the
// effect ends. Drop the old local route without sending a conflicting STOP.
func (s *Service) clientControlPacketLocked(payload []byte) bool {
	guid, n, err := readGUID(payload)
	if err != nil || len(payload) != n+1 || payload[n] > 1 {
		return false
	}
	if guid != s.selfGUID {
		return true
	}
	if payload[n] == 0 {
		s.clientControlLost, s.clientControlRestoring = true, false
		s.state.Moving = false
		s.followNav.Reset()
		s.meleeNav.Reset()
	} else if s.clientControlLost {
		s.clientControlLost, s.clientControlRestoring = false, true
	}
	return true
}

func (s *Service) controlRecoveryLocked(ctx context.Context) bool {
	if s.clientControlLost {
		s.state.Reason = "movement_control_with_server"
		return true
	}
	if !s.clientControlRestoring {
		return false
	}
	w, err := s.snapshot(ctx)
	self := w.GetSelf()
	if err != nil || self.GetGuid() != s.selfGUID || !self.GetPresent() || !self.GetMapKnown() ||
		!self.GetPositionKnown() || !finite(playerPosition(self)) || !self.GetUnitFlagsKnown() ||
		self.GetUnitFlags()&unitFlagsForcedMovement != 0 || !self.GetMovementFlagsKnown() ||
		!movementflags.GroundCompatible(self.GetMovementFlags(), self.GetExtraMovementFlags()) ||
		self.GetMovementFlags()&0xff != 0 {
		s.state.Reason = "waiting_for_position_after_control_loss"
		return true
	}
	s.clientControlRestoring = false
	s.posKnown = false
	s.refreshPositionLocked(w)
	s.state.MotionUnconfirmed = false
	return false
}
