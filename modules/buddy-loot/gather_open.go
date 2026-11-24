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

type gatherRequest struct {
	request *lootv1.OpenGatherRequest
	receipt *lootv1.CorpseOperation
}

func (s *Service) gatherVisitCurrent(w *worldv1.SnapshotResponse) bool {
	return s.gatherRequest == nil || w != nil && w.VisitRevision == s.gatherRequest.request.VisitId &&
		w.GetSelf().GetGuid() == s.gatherRequest.request.SelfGuid
}

// Only observations/catalog relationships are reflected here. No quest,
// distance, history or preference chooses or starts an operation during reads.
func (s *Service) observeGatherCandidates(w *worldv1.SnapshotResponse, out *lootv1.CorpseObservation) {
	out.VisitId, out.QuestLogRevision = w.VisitRevision, w.SelfQuestLogRevision
	if len(w.QuestObjects) > 32 || len(s.gatherSeen) > 128 {
		return
	}
	objects, entries := map[uint64]bool{}, map[uint32]bool{}
	for _, object := range w.QuestObjects {
		if object == nil || objects[object.Guid] || object.Guid == 0 {
			return
		}
		objects[object.Guid] = true
		out.QuestObjects = append(out.QuestObjects, proto.Clone(object).(*worldv1.GameObjectState))
		if entries[object.Entry] {
			continue
		}
		entries[object.Entry] = true
		record, ok := s.catalog.Container(object.Entry)
		if !ok || record.Type != 3 || record.Action != "open_and_loot" || !ordinaryOpeningSpell(record.OpenSpell) {
			continue
		}
		for _, relation := range record.QuestItems {
			if relation.QuestID == 0 || relation.ItemID == 0 || relation.Count == 0 || len(out.GatherRecipes) >= 128 {
				return
			}
			out.GatherRecipes = append(out.GatherRecipes, &lootv1.GatherRecipe{ObjectEntry: object.Entry,
				QuestId: relation.QuestID, ItemId: relation.ItemID, Count: relation.Count, OpenSpell: record.OpenSpell})
		}
	}
	for guid := range s.gatherSeen {
		out.GatherAttemptedSources = append(out.GatherAttemptedSources, guid)
	}
	sort.Slice(out.GatherAttemptedSources, func(i, j int) bool { return out.GatherAttemptedSources[i] < out.GatherAttemptedSources[j] })
	out.GatherCandidatesKnown = true
}

func (s *Service) gatherReceiptLocked(g *gatherRequest) *lootv1.CorpseOperation {
	if g == nil {
		return nil
	}
	if g == s.gatherRequest && s.questGather && s.state.PendingGameObjectGuid == g.request.SourceGuid {
		g.receipt.Phase, g.receipt.Reason = s.state.Phase, s.state.Reason
		if r := s.state.LastGather; r != nil && r.ObjectGuid == g.request.SourceGuid {
			g.receipt.OpeningTransportWritten = r.OpeningTransportWritten
			g.receipt.WindowObserved, g.receipt.ReleaseObserved = r.LootWindowObserved, r.ReleaseObserved
		}
	}
	return proto.Clone(g.receipt).(*lootv1.CorpseOperation)
}

func (s *Service) finishGatherRequest() {
	g := s.gatherRequest
	if g == nil || !s.questGather || s.state.PendingGameObjectGuid != g.request.SourceGuid {
		return
	}
	s.gatherReceiptLocked(g)
	// A written opening without its observed window/release remains uncertain,
	// even if scheduling is disabled/re-enabled or a new request ID appears.
	if g.receipt.OpeningTransportAttempted && (!g.receipt.WindowObserved || !g.receipt.ReleaseObserved) &&
		s.state.LastGather.GetStatus() != lootv1.GatherStatus_GATHER_SERVER_REJECTED {
		if s.retired == nil {
			s.retired = map[uint64]bool{}
		}
		s.retired[g.request.SourceGuid] = true
	}
	g.receipt.Terminal = true
	g.receipt.Phase = "closed_unverified"
	if !g.receipt.OpeningTransportAttempted {
		g.receipt.Phase = "not_dispatched"
	} else if g.receipt.WindowObserved && g.receipt.ReleaseObserved && !s.hasUnsettledLootMutation() {
		g.receipt.Phase = "window_and_release_observed"
	}
}

func (s *Service) exactGatherCurrent(r *lootv1.OpenGatherRequest, w *worldv1.SnapshotResponse, a *actionsv1.State) (*worldv1.GameObjectState, harvestRecipe) {
	recipe := harvestRecipe{r.QuestId, uint32(r.SourceGuid >> 24 & 0xffffff), r.ItemId, r.OpenSpell}
	allowed := false
	for _, known := range s.harvestRecipes(recipe.objectEntry) {
		allowed = allowed || known == recipe
	}
	if !allowed || w.GetSelf().GetGuid() != r.SelfGuid || a.GetSessionId() != r.SessionId ||
		w.VisitRevision != r.VisitId || w.SelfQuestLogRevision != r.ExpectedQuestLogRevision ||
		!w.InventoryKnown || w.InventoryRevision != r.ExpectedInventoryRevision || !w.GetSelf().GetPositionKnown() ||
		a.GetPosition().GetRevision() != r.ExpectedPositionRevision || !recipe.context(w, a, r.SelfGuid, true) ||
		len(w.QuestObjects) > 32 {
		return nil, harvestRecipe{}
	}
	var selected *worldv1.GameObjectState
	for _, object := range w.QuestObjects {
		if object != nil && object.Guid == r.SourceGuid {
			if selected != nil {
				return nil, harvestRecipe{}
			}
			selected = object
		}
	}
	rangeLimit := 4.5
	if r.Approach {
		rangeLimit = 12 // Existing finite mechanical reach; Lua may choose less.
	}
	if selected == nil || selected.PositionRevision != r.ExpectedObjectRevision || !recipe.objectWithin(a.Position, selected, rangeLimit) {
		return nil, harvestRecipe{}
	}
	return selected, recipe
}

// OpenGather never searches for a target or recipe. Repeated IDs only read the
// immutable original request/receipt; pending or unknown sources cannot replay.
func (s *Service) OpenGather(ctx context.Context, r *lootv1.OpenGatherRequest) (*lootv1.CorpseOperation, error) {
	if r.GetSessionId() == "" || len(r.GetSessionId()) > 128 || r.GetSelfGuid() == 0 || r.GetSourceGuid() == 0 ||
		r.GetQuestId() == 0 || r.GetItemId() == 0 || !ordinaryOpeningSpell(r.GetOpenSpell()) ||
		r.GetRequestId() == "" || len(r.GetRequestId()) > 128 || r.GetVisitId() == 0 || r.GetExpectedPositionRevision() == 0 ||
		r.GetExpectedObjectRevision() == 0 || r.GetExpectedInventoryRevision() == 0 || r.GetExpectedQuestLogRevision() == 0 {
		return nil, status.Error(codes.InvalidArgument, "exact body visit source recipe revisions and request ID required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || r.SessionId != s.state.SessionId || r.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current owned session required")
	}
	if prior := s.gatherRequests[r.RequestId]; prior != nil {
		if !proto.Equal(prior.request, r) {
			return nil, status.Error(codes.FailedPrecondition, "request ID already binds different gather facts")
		}
		return s.gatherReceiptLocked(prior), nil
	}
	if !s.state.Enabled || s.lootPolicyExpired(s.now()) || s.token != "" || s.cleanup.source != 0 ||
		s.gatherSeen[r.SourceGuid] || s.retired[r.SourceGuid] || len(s.gatherRequests) >= 128 || len(s.gatherSeen) >= 128 || s.state.InventoryPaused {
		return nil, status.Error(codes.FailedPrecondition, "available gather operation with no active uncertain or retired source required")
	}
	w, a := &worldv1.SnapshotResponse{}, &actionsv1.State{}
	if err := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, w); err != nil {
		return nil, err
	}
	if err := s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, a); err != nil {
		return nil, err
	}
	selected, recipe := s.exactGatherCurrent(r, w, a)
	if selected == nil || a.InteractionOwner != "" || a.AutoAttackRequested || !ownKnownFreeSlot(w, s.selfGUID) {
		return nil, status.Error(codes.FailedPrecondition, "current exact supported source recipe pose capacity and unoccupied body required")
	}
	lease := &actionsv1.InteractionLease{}
	if err := s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "loot", LeaseMs: 15000}, lease); err != nil {
		return nil, err
	}
	if lease.Token == "" || !stoppedLootLease(lease.State) {
		if lease.Token != "" && lease.State != nil && lease.State.SessionId == r.SessionId && lease.State.InteractionOwner == "loot" {
			s.token = lease.Token
			s.releaseLease(ctx)
		}
		return nil, status.Error(codes.FailedPrecondition, "exact stopped loot custody required")
	}
	s.token = lease.Token
	fresh := &worldv1.SnapshotResponse{}
	if err := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, fresh); err != nil {
		s.releaseLease(ctx)
		return nil, err
	}
	current, currentRecipe := s.exactGatherCurrent(r, fresh, lease.State)
	if current == nil || currentRecipe != recipe || !ownKnownFreeSlot(fresh, s.selfGUID) || ctx.Err() != nil {
		s.releaseLease(ctx)
		return nil, status.Error(codes.FailedPrecondition, "selected gather facts changed after stop")
	}
	id := uint64(1)
	if s.state.LastGather != nil {
		id = s.state.LastGather.Id + 1
	}
	s.questGather, s.exactCorpse = true, false
	s.gatherRequest = &gatherRequest{request: proto.Clone(r).(*lootv1.OpenGatherRequest), receipt: &lootv1.CorpseOperation{
		SessionId: r.SessionId, SelfGuid: r.SelfGuid, SourceGuid: r.SourceGuid, RequestId: r.RequestId, Phase: "reserved"}}
	if s.gatherRequests == nil {
		s.gatherRequests = map[string]*gatherRequest{}
	}
	s.gatherRequests[r.RequestId] = s.gatherRequest
	if s.gatherSeen == nil {
		s.gatherSeen = map[uint64]bool{}
	}
	s.gatherSeen[current.Guid] = true
	s.state.PendingGameObjectGuid, s.gatherObjectRevision = current.Guid, current.PositionRevision
	s.state.LastGather = &lootv1.GatherReceipt{Id: id, ObjectGuid: current.Guid, ObjectEntry: recipe.objectEntry, QuestId: recipe.questID,
		ItemId: recipe.itemID, Status: lootv1.GatherStatus_GATHER_REQUESTED, BaselineQuestLogRevision: fresh.SelfQuestLogRevision,
		ObservedQuestLogRevision: fresh.SelfQuestLogRevision, Reason: "caller_selected_opening_requested"}
	s.transactionUntil = s.now().Add(12 * time.Second)
	s.moneySent, s.moneyCleared, s.itemRequested = false, false, false
	if recipe.object(lease.State.Position, current) {
		s.openLocked(ctx)
	} else {
		s.state.Phase, s.state.Reason = "gather_approaching", "caller_selected_container_approach_pending"
	}
	return s.gatherReceiptLocked(s.gatherRequest), nil
}
