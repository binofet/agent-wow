package buddyquests

import (
	"context"
	"time"

	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type turnInRuntime struct {
	queryNPC, followupNPC                               uint64
	queryQuest                                          uint32
	queryUntil, offerUntil, receiptUntil, followupUntil time.Time
	wait                                                chan struct{}
	received                                            bool
	baselineItems, expectedItems                        map[uint32]uint64
	baselineItemsKnown                                  bool
}

func (s *Service) beginTurnInQueryLocked(npc uint64, id uint32) chan struct{} {
	s.turnIn.queryNPC, s.turnIn.queryQuest = npc, id
	s.turnIn.queryUntil = s.now().Add(responseTimeout)
	s.turnIn.wait = make(chan struct{})
	s.turnIn.received = false
	return s.turnIn.wait
}
func waitTurnIn(ctx context.Context, ch chan struct{}) {
	timer := time.NewTimer(responseTimeout)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
	case <-ctx.Done():
	}
}

func (s *Service) TurnIn(ctx context.Context, r *questsv1.TurnInRequest) (*questsv1.OperationResult, error) {
	if r == nil || r.QuestId == 0 || !creatureGUID(r.NpcGuid) {
		return nil, status.Error(codes.InvalidArgument, "observed NPC and active quest are required")
	}
	call, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := s.reserve(call, r.SessionId); err != nil {
		return nil, err
	}
	defer s.releaseBusy()
	if _, err := s.world(call); err != nil {
		return nil, err
	}
	s.mu.Lock()
	valid := s.state.QuestLogKnown && s.state.QuestLogRevision == r.QuestLogRevision && s.inLogLocked(r.QuestId)
	s.mu.Unlock()
	if !valid {
		return nil, status.Error(codes.FailedPrecondition, "active own quest log or revision changed")
	}
	token, err := s.acquire(call)
	if err != nil {
		return nil, err
	}
	defer s.release(token)
	if _, err := s.gate(call, r.NpcGuid); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if !s.state.QuestLogKnown || s.state.QuestLogRevision != r.QuestLogRevision || !s.inLogLocked(r.QuestId) {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "active own quest changed before turn-in")
	}
	s.state.TurnInOffer = nil
	s.state.Offer = nil
	wait := s.beginTurnInQueryLocked(r.NpcGuid, r.QuestId)
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.turnIn.queryUntil = time.Time{}; s.mu.Unlock() }()
	if err := s.send(call, opcode.CMSGQuestgiverCompleteQuest, turnInPacket(r.NpcGuid, r.QuestId)); err != nil {
		return nil, err
	}
	waitTurnIn(call, wait)
	s.mu.Lock()
	received := s.turnIn.received
	o := s.state.TurnInOffer
	advance := received && o != nil && o.Source == "requirements" && o.CanComplete
	if advance {
		wait = s.beginTurnInQueryLocked(r.NpcGuid, r.QuestId)
	}
	s.mu.Unlock()
	if advance {
		if err := s.send(call, opcode.CMSGQuestgiverRequestReward, turnInPacket(r.NpcGuid, r.QuestId)); err != nil {
			return nil, err
		}
		waitTurnIn(call, wait)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := s.snapshotLocked()
	result.ActionInFlight = false
	return &questsv1.OperationResult{TransportWritten: true, ServerResponseReceived: received || s.turnIn.received, State: result}, nil
}

func (s *Service) validateRewardLocked(r *questsv1.RewardRequest) (*questsv1.TurnInOffer, uint32, error) {
	o := s.state.TurnInOffer
	if r == nil || r.SessionId != s.state.SessionId || o == nil || r.QuestId == 0 || o.QuestId != r.QuestId || o.NpcGuid != r.NpcGuid || o.Revision != r.OfferRevision || !s.now().Before(s.turnIn.offerUntil) || o.Source != "reward_offer" || !o.CanComplete || o.RewardRequested {
		return nil, 0, status.Error(codes.FailedPrecondition, "reward offer changed, expired or was consumed")
	}
	if len(o.Choices) == 0 {
		if !r.NoChoice || r.ExpectedItemId != 0 || r.ChoiceIndex != 0 {
			return nil, 0, status.Error(codes.InvalidArgument, "no-choice reward requires explicit no_choice and zero item/index")
		}
		return o, 0, nil
	}
	if !r.NoChoice {
		for _, item := range o.Choices {
			if item.Index == r.ChoiceIndex && item.ItemId == r.ExpectedItemId {
				return o, item.Index, nil
			}
		}
	}
	return nil, 0, status.Error(codes.InvalidArgument, "reward choice must match an actual server-offered index and item")
}

func (s *Service) ClaimReward(ctx context.Context, r *questsv1.RewardRequest) (*questsv1.OperationResult, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "reward request required")
	}
	call, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := s.reserve(call, r.SessionId); err != nil {
		return nil, err
	}
	defer s.releaseBusy()
	s.mu.Lock()
	_, _, err := s.validateRewardLocked(r)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	token, err := s.acquire(call)
	if err != nil {
		return nil, err
	}
	defer s.release(token)
	w, err := s.gate(call, r.NpcGuid)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	o, choice, err := s.validateRewardLocked(r)
	if err == nil && (!s.state.QuestLogKnown || !s.inLogLocked(r.QuestId)) {
		err = status.Error(codes.FailedPrecondition, "quest is absent from the active own log")
	}
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	o.RewardRequested = true // Consume before the potentially ambiguous write.
	s.turnIn.baselineItems, s.turnIn.baselineItemsKnown = backpackCounts(w)
	s.turnIn.expectedItems = map[uint32]uint64{}
	for _, item := range o.Rewards {
		s.turnIn.expectedItems[item.ItemId] += uint64(item.Count)
	}
	for _, item := range o.Choices {
		if item.Index == choice && !r.NoChoice {
			s.turnIn.expectedItems[item.ItemId] += uint64(item.Count)
		}
	}
	s.turnIn.receiptUntil = s.now().Add(30 * time.Second)
	s.state.LastReward = &questsv1.RewardReceipt{NpcGuid: r.NpcGuid, QuestId: r.QuestId, OfferRevision: r.OfferRevision, Status: questsv1.RewardStatus_REWARD_REQUESTED, BaselineLogRevision: s.state.QuestLogRevision, BaselineBackpackRevision: w.BackpackRevision, InventoryLimitation: "Item evidence covers observed backpack slots only; server reward receipt and own-log removal confirm turn-in."}
	s.state.Offer = nil
	s.state.Revision++
	s.mu.Unlock()
	err = s.send(call, opcode.CMSGQuestgiverChooseReward, rewardPacket(r.NpcGuid, r.QuestId, choice))
	s.mu.Lock()
	if err != nil {
		if s.state.LastReward.Status == questsv1.RewardStatus_REWARD_REQUESTED {
			s.state.LastReward.Status = questsv1.RewardStatus_REWARD_WRITE_UNCONFIRMED
			s.state.LastReward.Failure = "reward_write_outcome_unconfirmed"
		}
	} else {
		s.state.LastReward.TransportWritten = true
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	timer := time.NewTimer(responseTimeout)
	defer timer.Stop()
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
wait:
	for {
		_, _ = s.world(call)
		s.mu.Lock()
		finished := s.state.LastReward.Status == questsv1.RewardStatus_REWARD_CONFIRMED || s.state.LastReward.Status == questsv1.RewardStatus_REWARD_REJECTED
		s.mu.Unlock()
		if finished {
			break
		}
		select {
		case <-poll.C:
		case <-timer.C:
			break wait
		case <-call.Done():
			break wait
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.state.LastReward; a.Status == questsv1.RewardStatus_REWARD_REQUESTED {
		a.Status = questsv1.RewardStatus_REWARD_UNCONFIRMED
		a.Failure = "no_matched_reward_receipt_and_new_log_removal"
	}
	result := s.snapshotLocked()
	result.ActionInFlight = false
	return &questsv1.OperationResult{TransportWritten: true, ServerResponseReceived: s.state.LastReward.ServerRewardObserved || s.state.LastReward.Status == questsv1.RewardStatus_REWARD_REJECTED, State: result}, nil
}

func backpackCounts(w *worldv1.SnapshotResponse) (map[uint32]uint64, bool) {
	counts := map[uint32]uint64{}
	if !w.BackpackKnown || len(w.Backpack) != 16 {
		return counts, false
	}
	seen := [16]bool{}
	for _, item := range w.Backpack {
		if item == nil || item.Slot >= 16 || item.Bag != 0 || seen[item.Slot] {
			return counts, false
		}
		seen[item.Slot] = true
		if item.ItemGuid == 0 {
			continue
		}
		if !item.MetadataKnown || item.Entry == 0 || item.StackCount == 0 {
			return counts, false
		}
		counts[item.Entry] += uint64(item.StackCount)
	}
	return counts, true
}

func (s *Service) applyRewardWorldLocked(w *worldv1.SnapshotResponse) {
	a := s.state.LastReward
	if a == nil {
		return
	}
	if s.state.QuestLogKnown && s.state.QuestLogRevision > a.BaselineLogRevision && !s.inLogLocked(a.QuestId) {
		a.LogRemovalConfirmed = true
		a.ConfirmedLogRevision = s.state.QuestLogRevision
	}
	if current, known := backpackCounts(w); known && s.turnIn.baselineItemsKnown && w.BackpackRevision >= a.BaselineBackpackRevision {
		a.BackpackEvidenceKnown = true
		a.ConfirmedBackpackRevision = max(a.ConfirmedBackpackRevision, w.BackpackRevision)
		observed := len(s.turnIn.expectedItems) > 0
		for id, count := range s.turnIn.expectedItems {
			observed = observed && current[id] >= s.turnIn.baselineItems[id]+count
		}
		a.RewardItemsObservedInBackpack = a.RewardItemsObservedInBackpack || observed
	}
	s.confirmRewardLocked()
}
func (s *Service) confirmRewardLocked() {
	if a := s.state.LastReward; a != nil && a.ServerRewardObserved && a.LogRemovalConfirmed && a.Status != questsv1.RewardStatus_REWARD_CONFIRMED {
		a.Status = questsv1.RewardStatus_REWARD_CONFIRMED
		a.Failure = ""
		s.state.Revision++
	}
}
func (s *Service) rejectRewardLocked(reason string, code, quest uint32) {
	if a := s.state.LastReward; a != nil && a.Status == questsv1.RewardStatus_REWARD_REQUESTED && s.now().Before(s.turnIn.receiptUntil) && (quest == 0 || quest == a.QuestId) {
		a.Status = questsv1.RewardStatus_REWARD_REJECTED
		a.Failure, a.ServerReason = reason, code
		s.state.Revision++
	}
}
func (s *Service) followupLocked(o *questsv1.Offer) bool {
	a := s.state.LastReward
	return !s.now().Before(s.queryUntil) && a != nil && a.ServerRewardObserved && o.NpcGuid == s.turnIn.followupNPC && s.now().Before(s.turnIn.followupUntil) && o.Source == "quest_details" && len(o.Quests) == 1 && o.Quests[0].QuestId != a.QuestId
}

func (s *Service) handleTurnInPacket(p *modv1.WorldPacket) bool {
	var offer *questsv1.TurnInOffer
	var receipt *questsv1.RewardReceipt
	var err error
	switch p.Opcode {
	case opcode.SMSGQuestgiverRequestItems, opcode.SMSGQuestgiverOfferReward:
		offer, err = decodeTurnIn(p.Payload, p.Opcode == opcode.SMSGQuestgiverOfferReward)
	case opcode.SMSGQuestgiverQuestComplete:
		receipt, err = decodeRewardReceipt(p.Payload)
	default:
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.state.MalformedPackets++
		return true
	}
	if offer != nil {
		matches := offer.NpcGuid == s.turnIn.queryNPC && offer.QuestId == s.turnIn.queryQuest && s.now().Before(s.turnIn.queryUntil)
		if a := s.state.LastReward; a != nil && a.Status == questsv1.RewardStatus_REWARD_REQUESTED && offer.NpcGuid == a.NpcGuid && offer.QuestId == a.QuestId && s.now().Before(s.turnIn.receiptUntil) {
			matches = true
			s.rejectRewardLocked("server_offered_reward_again", 0, offer.QuestId)
		}
		// Hello may directly open the reward panel for a single active quest.
		matches = matches || offer.NpcGuid == s.queryNPC && s.now().Before(s.queryUntil) && s.queryQuest == 0
		if !matches || !s.state.QuestLogKnown || !s.inLogLocked(offer.QuestId) {
			s.state.IgnoredPackets++
			return true
		}
		s.state.Revision++
		offer.Revision = s.state.Revision
		s.turnIn.offerUntil = s.now().Add(30 * time.Second)
		s.state.TurnInOffer = offer
		s.turnIn.received = true
		if s.turnIn.wait != nil {
			close(s.turnIn.wait)
			s.turnIn.wait = nil
		}
		if s.wait != nil {
			s.responseReceived = true
			close(s.wait)
			s.wait = nil
		}
	} else {
		a := s.state.LastReward
		if a == nil || receipt.QuestId != a.QuestId || !s.now().Before(s.turnIn.receiptUntil) {
			s.state.IgnoredPackets++
			return true
		}
		a.ServerRewardObserved = true
		a.RewardXp, a.RewardMoney, a.RewardHonor, a.RewardTalents, a.RewardArena = receipt.RewardXp, receipt.RewardMoney, receipt.RewardHonor, receipt.RewardTalents, receipt.RewardArena
		s.turnIn.followupNPC, s.turnIn.followupUntil = a.NpcGuid, s.now().Add(5*time.Second)
		s.state.Revision++
		s.confirmRewardLocked()
	}
	return true
}
