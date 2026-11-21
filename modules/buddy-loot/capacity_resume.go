package buddyloot

import (
	"context"
	"strconv"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func (s *Service) capacityPauseToken() string {
	if s.state.LastGather == nil {
		return ""
	}
	return s.capacityEpoch + ":" + strconv.FormatUint(s.state.LastGather.Id, 10)
}

// ResumeSource changes only a source-bound local refusal record. Lua chooses
// the inventory conditions and timing; the retained opener independently checks
// its current source/quest/position before any later game write.
func (s *Service) ResumeSource(ctx context.Context, r *lootv1.ResumeSourceRequest) (*lootv1.ResumeSourceReceipt, error) {
	if r.GetSessionId() == "" || r.GetSelfGuid() == 0 || r.GetSourceGuid() == 0 || r.GetGatherId() == 0 || r.GetExpectedInventoryRevision() == 0 || r.GetPauseToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "exact session body source gather and inventory revision required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || r.SessionId != s.state.SessionId || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current owned session required")
	}
	if s.capacityResumeRequest != nil && proto.Equal(r, s.capacityResumeRequest) {
		return proto.Clone(s.capacityResumeReceipt).(*lootv1.ResumeSourceReceipt), nil
	}
	gather := s.state.LastGather
	if !s.state.InventoryPaused || !s.pausedGather || s.pausedSource != r.SourceGuid || gather == nil || gather.Id != r.GatherId || gather.ObjectGuid != r.SourceGuid || r.PauseToken != s.capacityPauseToken() ||
		s.token != "" || s.state.PendingCorpseGuid != 0 || s.state.PendingGameObjectGuid != 0 || s.cleanup.source != 0 || s.hasUnsettledLootMutation() || s.retired[r.SourceGuid] ||
		gather.OpeningTransportWritten && !gather.ReleaseObserved {
		return nil, status.Error(codes.FailedPrecondition, "matching settled capacity pause without unresolved source custody required")
	}
	w, a := &worldv1.SnapshotResponse{}, &actionsv1.State{}
	if err := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, w); err != nil {
		return nil, err
	}
	if err := s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, a); err != nil {
		return nil, err
	}
	if w.GetSelf().GetGuid() != s.selfGUID || a.SessionId != r.SessionId || !w.InventoryKnown || w.InventoryRevision != r.ExpectedInventoryRevision {
		return nil, status.Error(codes.FailedPrecondition, "current body and exact observed inventory revision required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	// No elapsed time, bag-space threshold, item preference or retry decision is
	// hidden here. This cannot acknowledge a pickup, release or quest effect.
	delete(s.gatherSeen, r.SourceGuid)
	s.state.InventoryPaused = false
	s.pausedSource, s.pausedGather, s.rejectedInventoryRevision = 0, false, 0
	s.state.Reason = "caller_resumed_exact_container_capacity_pause"
	s.capacityResumeRequest = proto.Clone(r).(*lootv1.ResumeSourceRequest)
	s.capacityResumeReceipt = &lootv1.ResumeSourceReceipt{SessionId: r.SessionId, SelfGuid: r.SelfGuid, SourceGuid: r.SourceGuid,
		GatherId: r.GatherId, InventoryRevision: r.ExpectedInventoryRevision, Cleared: true, PauseToken: r.PauseToken}
	return proto.Clone(s.capacityResumeReceipt).(*lootv1.ResumeSourceReceipt), nil
}
