package buddyloot

import (
	"context"
	"sort"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

const maxRetiredLootSources = 128

type ordinaryLootCleanup struct {
	source           uint64
	timedOutPhase    string
	safeToContinue   bool
	releaseRequested bool
	releaseObserved  bool
	releaseRejected  bool
}

func (s *Service) hasUnsettledLootMutation() bool {
	return s.itemRequested && !(s.removed && s.pushed) || s.moneySent && !s.moneyCleared
}

// A timeout retires this attempt, never the user's opt-in intent. Only a
// positive source-bound cleanup and no unsettled GUID-less item/money request
// can authorize trying another source. An uncertain mutation is never retried.
func (s *Service) beginOrdinaryTimeout(ctx context.Context, reason string) {
	phase := s.state.Phase
	s.cleanup = ordinaryLootCleanup{source: s.state.PendingCorpseGuid, timedOutPhase: phase, safeToContinue: !s.hasUnsettledLootMutation()}
	if s.retired == nil {
		s.retired = make(map[uint64]bool)
	}
	if !s.retired[s.cleanup.source] && len(s.retired) >= maxRetiredLootSources {
		s.cleanup.safeToContinue = false
	} else {
		s.retired[s.cleanup.source] = true
	}
	s.state.Reason = reason + "_source_cleanup_pending"
	if !s.opened {
		// No OPEN or mutation was issued; no server loot window needs closing.
		s.state.Reason = "loot_unopened_timeout_source_skipped"
		s.finish(ctx)
		s.cleanup = ordinaryLootCleanup{}
		return
	}
	if phase == "waiting_release" {
		// The existing release has already consumed its single write allowance.
		s.cleanup.releaseRequested = true
		s.pauseOrdinaryCleanup(ctx)
		return
	}
	// Renew the exact opaque token before touching this character's loot UI.
	// A same-owner replacement or expired token never permits cleanup writes.
	lease := &actionsv1.InteractionLease{}
	if ctx.Err() != nil || s.token == "" || s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "loot", Token: s.token, LeaseMs: 15000}, lease) != nil || lease.Token != s.token || lease.State == nil || lease.State.InteractionOwner != "loot" || lease.State.InteractionLeaseRemainingMs < 1000 {
		s.state.Reason = "loot_timeout_cleanup_lease_unconfirmed"
		s.pauseOrdinaryCleanup(ctx)
		return
	}
	s.cleanup.releaseRequested = true // reserve before an ambiguous transport write
	s.state.Phase = "waiting_release"
	s.deadline = s.now().Add(2 * time.Second)
	if !s.write(ctx, opcode.CMSGLootRelease, guidPacket(s.cleanup.source)) {
		s.state.Reason = "loot_timeout_cleanup_transport_unconfirmed"
		s.pauseOrdinaryCleanup(ctx)
	}
}

func (s *Service) pauseOrdinaryCleanup(ctx context.Context) {
	s.releaseLease(ctx)
	s.state.Phase = "loot_cleanup_unconfirmed"
	if !s.cleanup.safeToContinue {
		s.state.Phase = "loot_mutation_unconfirmed"
	}
}

func (s *Service) tickOrdinaryCleanup(ctx context.Context) {
	if s.state.Enabled && !s.now().Before(s.until) {
		s.state.Enabled = false
		s.state.Reason = "automatic_loot_enable_expired"
	}
	if s.cleanup.releaseObserved && s.cleanup.safeToContinue {
		s.state.Reason = "loot_timeout_source_released_and_skipped"
		s.finish(ctx)
		s.cleanup = ordinaryLootCleanup{}
		return
	}
	if s.cleanup.releaseObserved || s.cleanup.releaseRejected || s.state.Phase != "waiting_release" || !s.now().Before(s.deadline) {
		s.pauseOrdinaryCleanup(ctx)
		if s.cleanup.releaseRejected {
			s.state.Reason = "loot_timeout_cleanup_server_unconfirmed"
		} else if !s.cleanup.safeToContinue {
			s.state.Reason = "loot_timeout_" + s.cleanup.timedOutPhase + "_mutation_unconfirmed_no_retry"
		} else if s.state.Reason != "disabled_by_request" {
			s.state.Reason = "loot_timeout_cleanup_unconfirmed_no_retry"
		}
	}
}

// Retirement belongs to this body. Projection churn and Enable(true) cannot
// replay a failed corpse. A real later living creature with that GUID is a new
// life; absence alone, an unknown health baseline or a different GUID is not.
// Probe at most one exact cached creature per idle tick, without game writes.
func (s *Service) refreshRetiredSource(ctx context.Context) bool {
	if len(s.retired) == 0 {
		return false
	}
	keys := make([]uint64, 0, len(s.retired))
	for guid := range s.retired {
		keys = append(keys, guid)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	selected := keys[0]
	for _, guid := range keys {
		if guid > s.retiredCursor {
			selected = guid
			break
		}
	}
	s.retiredCursor = selected
	w := &worldv1.SnapshotResponse{}
	if s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{CreatureGuid: selected}, w) != nil || w.Self == nil || w.Self.Guid != s.selfGUID || !w.Self.Present || !w.Self.MapKnown {
		return false
	}
	c := w.RequestedCreature
	if c == nil || c.Guid != selected || !c.Present || !c.MapKnown || c.MapId != w.Self.MapId || !c.AliveKnown || !c.Alive || !c.HealthKnown || c.Health == 0 || c.MaxHealth < c.Health {
		return false
	}
	delete(s.retired, selected)
	delete(s.seen, selected)
	return true
}
