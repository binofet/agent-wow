package buddyactions

import (
	"context"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func (s *Service) stopAutoAttackLocked(ctx context.Context) bool {
	if s.encounterSwing.target != 0 || s.autoStopping || s.state.AutoAttackUnconfirmed {
		return false
	}
	if s.autoTarget == 0 && !s.state.AutoAttacking {
		return true
	}
	// An independently authorized legacy stop supersedes any typed operation.
	s.endMeleeOperationLocked()
	s.autoStopping = true
	s.state.AutoAttackRequested = false
	s.state.AutoAttackUnconfirmed = true
	if !s.sendLocked(ctx, opcode.CMSGAttackstop, nil) {
		s.state.Reason = "auto_attack_stop_unconfirmed"
	}
	return false // Only a matching server ATTACKSTOP confirms the stop.
}
