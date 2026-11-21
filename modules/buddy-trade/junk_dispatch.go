package buddytrade

import (
	"context"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const junkWriteTimeout = 3 * time.Second

func (s *Service) junkWorldSafe(w *worldv1.SnapshotResponse) error {
	if w == nil || w.Self == nil || w.Self.Guid != s.selfGUID || !w.Self.Present || !w.Self.AliveKnown || !w.Self.Alive || !w.Self.UnitFlagsKnown || w.Self.UnitFlags&0x80000 != 0 {
		return status.Error(codes.FailedPrecondition, "junk disposal requires observed alive noncombat character")
	}
	return nil
}

func junkActionsSafe(a *actionsv1.State) bool {
	return a != nil && !a.Moving && !a.MotionUnconfirmed && !a.CastPending && !a.AutoAttacking && !a.AutoAttackRequested && !a.AutoAttackUnconfirmed && !a.Traveling && (a.InteractionOwner == "" || a.InteractionOwner == "trade")
}

type junkDispatchGuard struct {
	token string
	until time.Time
}

func (s *Service) junkDispatchReady(ctx context.Context, guard junkDispatchGuard) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	if guard.token == "" || s.token != guard.token || !s.now().Before(guard.until) {
		return status.Error(codes.FailedPrecondition, "junk_disposal_lease_expired_or_changed_before_write")
	}
	return nil
}

// opMu keeps the retained token and outgoing inventory operation unchanged.
// Complete metadata and other peer reads before entering this final sequence.
func (s *Service) junkFinalWorld(ctx context.Context, request *tradev1.JunkRequest, before *worldv1.SnapshotResponse) (*worldv1.SnapshotResponse, *worldv1.BackpackSlot, junkDispatchGuard, error) {
	guard := junkDispatchGuard{token: s.token}
	if guard.token == "" {
		return nil, nil, guard, status.Error(codes.FailedPrecondition, "exact_trade_lease_required")
	}
	preRenew := s.now()
	lease := &actionsv1.InteractionLease{}
	err := s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "trade", Token: guard.token, LeaseMs: 30000}, lease)
	if err != nil || lease.Token != guard.token || !junkActionsSafe(lease.State) || lease.State.InteractionOwner != "trade" || lease.State.InteractionLeaseRemainingMs < uint64(junkWriteTimeout/time.Millisecond) || lease.State.InteractionLeaseRemainingMs > 30000 {
		return nil, nil, guard, status.Error(codes.FailedPrecondition, "exact_trade_lease_renewal_unconfirmed")
	}
	// An earlier anchor conservatively includes the renewal RPC's elapsed time.
	// The whole write context is at most three seconds; the lease covers it.
	guard.until = preRenew.Add(time.Duration(lease.State.InteractionLeaseRemainingMs) * time.Millisecond)
	if err := s.junkDispatchReady(ctx, guard); err != nil {
		return nil, nil, guard, err
	}
	// world() has no metadata sends. No dependency RPC follows this read.
	w, err := s.world(ctx)
	if err != nil {
		return nil, nil, guard, err
	}
	if err := s.junkWorldSafe(w); err != nil {
		return nil, nil, guard, err
	}
	if before.GetSelf() == nil || w.Self.PositionRevision != before.Self.PositionRevision || w.Self.MapKnown != before.Self.MapKnown || w.Self.MapId != before.Self.MapId {
		return nil, nil, guard, status.Error(codes.FailedPrecondition, "authoritative_pose_changed_during_lease_renewal")
	}
	if w.Self.MovementFlagsKnown && w.Self.MovementFlags&0xff != 0 {
		return nil, nil, guard, status.Error(codes.FailedPrecondition, "observed_movement_blocks_junk_disposal")
	}
	prior, selected := junkSource(before, request), junkSource(w, request)
	if prior == nil || selected == nil || selected.Entry != prior.Entry || selected.StackCount != prior.StackCount {
		return nil, nil, guard, status.Error(codes.FailedPrecondition, "inventory_source_changed_before_disposal")
	}
	if err := s.junkDispatchReady(ctx, guard); err != nil {
		return nil, nil, guard, err
	}
	return w, selected, guard, nil
}
