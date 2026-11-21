package buddyloot

import (
	"context"
	"sort"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type corpseRequest struct {
	request *lootv1.OpenCorpseRequest
	receipt *lootv1.CorpseOperation
}

// ObserveCorpses is a factual read. In particular it does not select, mark
// visited, take body custody, move toward, or open any observed corpse.
func (s *Service) ObserveCorpses(ctx context.Context, _ *emptypb.Empty) (*lootv1.CorpseObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, status.Error(codes.FailedPrecondition, "session closing")
	}
	w, a := &worldv1.SnapshotResponse{}, &actionsv1.State{}
	if err := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, w); err != nil {
		return nil, err
	}
	if err := s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, a); err != nil {
		return nil, err
	}
	if w.GetSelf().GetGuid() != s.selfGUID || a.SessionId != s.state.SessionId {
		return nil, status.Error(codes.FailedPrecondition, "current body observations required")
	}
	out := &lootv1.CorpseObservation{SessionId: s.state.SessionId, SelfGuid: s.selfGUID, Self: w.Self, Actions: a,
		Corpses: w.LootableCorpses, NearbyCreatures: w.NearbyCreatures, InventoryKnown: w.InventoryKnown,
		InventoryRevision: w.InventoryRevision, GeneralFreeSlots: uint32(ownKnownFreeSlots(w, s.selfGUID)), State: s.snapshotLocked()}
	if s.corpseRequest != nil {
		out.Operation = s.corpseReceiptLocked()
	}
	for guid := range s.retired {
		out.RetiredSources = append(out.RetiredSources, guid)
	}
	sort.Slice(out.RetiredSources, func(i, j int) bool { return out.RetiredSources[i] < out.RetiredSources[j] })
	return out, nil
}

func exactCorpseCurrent(r *lootv1.OpenCorpseRequest, w *worldv1.SnapshotResponse, a *actionsv1.State) bool {
	if r == nil || w.GetSelf().GetGuid() != r.SelfGuid || a.GetSessionId() != r.SessionId || a.Position == nil ||
		a.Position.Revision != r.ExpectedPositionRevision || w.InventoryRevision != r.ExpectedInventoryRevision || a.MotionUnconfirmed {
		return false
	}
	var found *worldv1.CreatureState
	for _, c := range w.LootableCorpses {
		if c != nil && c.Guid == r.CorpseGuid {
			if found != nil {
				return false
			}
			found = c
		}
	}
	return found != nil && found.PositionRevision == r.ExpectedCorpseRevision && eligible(w.Self, a.Position, found)
}

func (s *Service) corpseReceiptLocked() *lootv1.CorpseOperation {
	if s.corpseRequest == nil {
		return nil
	}
	out := proto.Clone(s.corpseRequest.receipt).(*lootv1.CorpseOperation)
	if !out.Terminal {
		out.Phase, out.Reason = s.state.Phase, s.state.Reason
	}
	return out
}

func (s *Service) finishCorpseReceipt(phase string, terminal bool) {
	if s.corpseRequest == nil {
		return
	}
	r := s.corpseRequest.receipt
	r.Phase, r.Reason, r.Terminal = phase, s.state.Reason, terminal
	if r.WindowObserved && r.ReleaseObserved && !s.hasUnsettledLootMutation() {
		r.Phase = "window_and_release_observed"
	}
}

// OpenCorpse admits only the exact caller-selected source. The operation
// retains normal loot-window/item/money settlement and never chooses a route.
func (s *Service) OpenCorpse(ctx context.Context, r *lootv1.OpenCorpseRequest) (*lootv1.CorpseOperation, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetCorpseGuid() == 0 || r.GetRequestId() == "" ||
		r.GetExpectedPositionRevision() == 0 || r.GetExpectedCorpseRevision() == 0 || r.GetExpectedInventoryRevision() == 0 {
		return nil, status.Error(codes.InvalidArgument, "exact session, body, corpse, source revisions and request ID required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || r.SessionId != s.state.SessionId || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current owned session required")
	}
	if prior := s.corpseRequests[r.RequestId]; prior != nil {
		if !proto.Equal(prior.request, r) {
			return nil, status.Error(codes.FailedPrecondition, "request ID already binds different corpse facts")
		}
		if prior == s.corpseRequest {
			return s.corpseReceiptLocked(), nil
		}
		return proto.Clone(prior.receipt).(*lootv1.CorpseOperation), nil
	}
	if s.token != "" || s.cleanup.source != 0 || s.retired[r.CorpseGuid] {
		return nil, status.Error(codes.FailedPrecondition, "source remains uncertain or another loot operation remains active")
	}
	w, a := &worldv1.SnapshotResponse{}, &actionsv1.State{}
	if err := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{CreatureGuid: r.CorpseGuid}, w); err != nil {
		return nil, err
	}
	if err := s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, a); err != nil {
		return nil, err
	}
	if !exactCorpseCurrent(r, w, a) || !ownKnownFreeSlot(w, s.selfGUID) || a.Moving || a.CastPending || a.AutoAttacking || a.AutoAttackRequested || a.AutoAttackUnconfirmed || a.InteractionOwner != "" {
		return nil, status.Error(codes.FailedPrecondition, "current exact in-range corpse, owned position, capacity and unoccupied body required")
	}
	lease := &actionsv1.InteractionLease{}
	if err := s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "loot", LeaseMs: 15000}, lease); err != nil {
		return nil, err
	}
	if lease.Token == "" || !stoppedLootLease(lease.State) {
		return nil, status.Error(codes.FailedPrecondition, "exact stopped loot custody required")
	}
	s.token, s.exactCorpse = lease.Token, true
	s.corpseRequest = &corpseRequest{request: proto.Clone(r).(*lootv1.OpenCorpseRequest), receipt: &lootv1.CorpseOperation{
		SessionId: r.SessionId, SelfGuid: r.SelfGuid, CorpseGuid: r.CorpseGuid, RequestId: r.RequestId, Phase: "reserved"}}
	if s.corpseRequests == nil {
		s.corpseRequests = map[string]*corpseRequest{}
	}
	s.corpseRequests[r.RequestId] = s.corpseRequest
	s.state.PendingCorpseGuid = r.CorpseGuid
	s.transactionUntil = s.now().Add(12 * time.Second)
	s.moneySent, s.moneyCleared, s.itemRequested = false, false, false
	s.openLocked(ctx)
	if s.corpseRequest.receipt.OpeningTransportAttempted {
		s.seen[r.CorpseGuid] = true
		if !s.corpseRequest.receipt.OpeningTransportWritten {
			if s.retired == nil {
				s.retired = map[uint64]bool{}
			}
			s.retired[r.CorpseGuid] = true
		}
	}
	return s.corpseReceiptLocked(), nil
}

// CloseCorpse yields physical custody immediately after reserving the normal
// release. Its source-bound receipt may remain unknown; no mutation is replayed.
func (s *Service) CloseCorpse(ctx context.Context, r *lootv1.CloseCorpseRequest) (*lootv1.CorpseOperation, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetCorpseGuid() == 0 {
		return nil, status.Error(codes.InvalidArgument, "exact session, body and active corpse required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || r.SessionId != s.state.SessionId || r.SelfGuid != s.selfGUID || s.corpseRequest == nil || r.CorpseGuid != s.corpseRequest.request.CorpseGuid {
		return nil, status.Error(codes.FailedPrecondition, "current exact corpse operation required")
	}
	if s.corpseRequest.receipt.Terminal || !s.exactCorpse || s.token == "" || s.cleanup.source != 0 {
		return s.corpseReceiptLocked(), nil
	}
	if s.state.Phase != "waiting_release" && s.state.Phase != "release_observed" {
		s.state.Reason = "caller_released_corpse_operation"
		s.beginRelease(ctx)
	}
	if s.token != "" {
		// Keep the private UI token for matching the outstanding release ACK,
		// but it must no longer occupy the shared movement/cast writer.
		_ = s.invoke(ctx, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "loot", Token: s.token}, &actionsv1.State{})
	}
	return s.corpseReceiptLocked(), nil
}
