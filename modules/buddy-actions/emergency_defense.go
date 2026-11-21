package buddyactions

import (
	"context"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
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

func (s *Service) hasPartyAttackerLocked(ctx context.Context, w *worldv1.SnapshotResponse) bool {
	if s.independent || w == nil || w.Self == nil || len(w.NearbyCreatures) > 32 {
		return false
	}
	online := s.healingPartyLocked(ctx)
	for _, c := range w.NearbyCreatures {
		if partyDefenseEligible(w, c, online) {
			return true
		}
	}
	return false
}

// Facing/clock callbacks can revoke the attack witness or change resources.
// Initial selection alone never authorizes the subsequent attack or spell.
func (s *Service) emergencyDefenseLocked(ctx context.Context) bool {
	if s.closed || s.state.CastPending || s.state.MotionUnconfirmed || s.state.AutoAttackUnconfirmed {
		return false
	}
	// Existing native missions retain their bound-target/quarantine scheduler.
	// Ordinary enabled controls already reach defense below, before owner gates.
	if s.state.Practicing || s.fargodeep.active || s.state.InteractionOwner == "" &&
		(s.state.Following && s.now().Before(s.followUntil) || s.hasLearnedHealingLocked() ||
			s.state.Assisting && s.now().Before(s.assistUntil) || s.state.Buffing) {
		return false
	}
	w, err := s.snapshot(ctx)
	if err != nil || safeSelfCombat(w, s.selfGUID) != "" {
		return false
	}
	if !s.hasObservedSelfAttackerLocked(w) && !s.hasPartyAttackerLocked(ctx, w) {
		return false
	}
	if !s.refreshPositionLocked(w) {
		return true
	}
	preempted := s.state.InteractionOwner != ""
	if preempted {
		stopped := s.stopLocked(ctx)
		s.state.InteractionOwner, s.interactionToken = "", ""
		s.state.InteractionHarvest = false
		s.state.HarvestGuardGuids = nil
		s.interactionUntil = time.Time{}
		s.state.Traveling = false
		if !stopped {
			s.state.Reason = "emergency_defense_stop_unconfirmed"
			return true
		}
		// STOP may coincide with a correction or ended attack. Refresh before
		// selecting any cast; the initial witness grants no later authority.
		w, err = s.snapshot(ctx)
		if err != nil || safeSelfCombat(w, s.selfGUID) != "" || !s.refreshPositionLocked(w) {
			return true
		}
	}

	if s.hostEncounterActiveLocked() {
		return preempted
	}
	if s.hostEncounterKey != "" {
		// Configured Lua owns enemy and spell selection even while its writer
		// is briefly unavailable. Mechanical preemption remains observable.
		return preempted
	}
	return preempted
}
