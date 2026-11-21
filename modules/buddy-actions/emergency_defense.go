package buddyactions

import (
	"context"
	"time"

	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
)

// An expired cast remains unconfirmed. Pause fresh spell selection without
// silently changing unrelated enabled follow, healing or assistance intent.
func (s *Service) expirePendingCastLocked(ctx context.Context) {
	if !s.state.CastPending || s.now().Before(s.castUntil) {
		return
	}
	s.state.CastPending = false
	if s.spellKindLocked(s.pendingSpell) == spellcatalog.Interrupt {
		s.pendingSpell, s.pendingTarget = 0, 0
		s.state.Reason = "interrupt_result_unconfirmed"
		return
	}
	s.stopLocked(ctx)
	s.stopAutoAttackLocked(ctx)
	if s.fargodeep.active {
		s.stopFargodeepLocked(ctx, "cast_result_timeout_no_retry")
	}
	s.pendingSpell, s.pendingTarget = 0, 0
	until := s.now().Add(5 * time.Second)
	if s.nextHeal.Before(until) {
		s.nextHeal = until
	}
	if s.nextAssist.Before(until) {
		s.nextAssist = until
	}
	s.state.Reason = "cast_result_timeout_backoff"
}
