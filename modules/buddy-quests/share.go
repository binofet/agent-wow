package buddyquests

import (
	"context"
	"encoding/binary"
	"time"

	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type sharedQuestRuntime struct {
	sequence           uint64
	until, acceptUntil time.Time
	consumed           map[uint32]bool
	expectInsertion    bool
}

// Normal manual shares are DETAILS[raw recipientGUID,raw divider/sharerGUID,
// usual full details]. PARTY_ACCEPT instead sends CONFIRM[qid,titlez,sharer].
// These identities and layouts are produced by public QuestHandler.cpp,
// GossipDef.cpp and QuestPackets.cpp; never infer sharing from PUSH_RESULT.
func decodeSharedDetails(payload []byte, self uint64) (*questsv1.SharedQuestOffer, error) {
	o, divider, err := decodeDetailsFields(payload)
	if err != nil || o.GetNpcGuid() != self || divider != 3 || len(o.GetQuests()) != 1 || !o.Quests[0].Available || o.Quests[0].Flags&8 == 0 {
		return nil, malformed
	}
	return &questsv1.SharedQuestOffer{SharerGuid: divider, Quest: o.Quests[0], Kind: questsv1.SharedQuestKind_SHARED_DETAILS}, nil
}
func decodeSharedConfirm(payload []byte) (*questsv1.SharedQuestOffer, error) {
	if len(payload) > 528 {
		return nil, malformed
	}
	r := reader{data: payload}
	q := &questsv1.Quest{QuestId: r.u32(), Title: r.text(512), Available: true}
	sharer := r.u64()
	if err := r.done(); err != nil || q.QuestId == 0 || q.Title == "" || sharer != 3 {
		return nil, malformed
	}
	return &questsv1.SharedQuestOffer{SharerGuid: sharer, Quest: q, Kind: questsv1.SharedQuestKind_PARTY_CONFIRM}, nil
}

// Manual method-zero/auto-complete shares have the sharer's GUID in the
// REQUEST_ITEMS/OFFER_REWARD frame, with no divider. Neither frame includes
// quest Method; require its actual SHARABLE flag, not an inferred method bit.
// These frames authorize acceptance only, never player-origin reward claims.
func decodeSharedAutocomplete(payload []byte, reward bool) (*questsv1.SharedQuestOffer, error) {
	o, err := decodeTurnInFields(payload, reward)
	if err != nil || o.GetNpcGuid() != 3 || o.Flags&8 == 0 {
		return nil, malformed
	}
	r := reader{data: payload[12:]}
	r.text(512)
	r.text(16384)
	if reward {
		if r.u8() != 1 { // SendQuestGiverOfferReward(..., true).
			return nil, malformed
		}
	} else {
		r.u32()
		r.u32()
		if r.u32() != 1 { // Manual sharing requests close-on-cancel=true.
			return nil, malformed
		}
	}
	q := &questsv1.Quest{QuestId: o.QuestId, Title: o.Title, Flags: o.Flags, Available: true}
	// Request/reward prose is not quest objectives or quest details.
	return &questsv1.SharedQuestOffer{SharerGuid: 3, Quest: q, Kind: questsv1.SharedQuestKind_SHARED_AUTOCOMPLETE}, nil
}

func (s *Service) handleSharedPacket(packet *modv1.WorldPacket) bool {
	var offer *questsv1.SharedQuestOffer
	var err error
	switch packet.Opcode {
	case opcode.SMSGQuestgiverQuestDetails:
		if len(packet.Payload) >= 16 && creatureGUID(binary.LittleEndian.Uint64(packet.Payload)) && binary.LittleEndian.Uint64(packet.Payload[8:]) == 0 {
			return false
		}
		offer, err = decodeSharedDetails(packet.Payload, s.selfGUID)
	case opcode.SMSGQuestConfirmAccept:
		offer, err = decodeSharedConfirm(packet.Payload)
	case opcode.SMSGQuestgiverRequestItems, opcode.SMSGQuestgiverOfferReward:
		if len(packet.Payload) < 8 || binary.LittleEndian.Uint64(packet.Payload) == 0 || binary.LittleEndian.Uint64(packet.Payload)>>48 != 0 {
			return false
		}
		offer, err = decodeSharedAutocomplete(packet.Payload, packet.Opcode == opcode.SMSGQuestgiverOfferReward)
	default:
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.state.SharedQuestsEnabled {
		s.state.IgnoredPackets++
		return true
	}
	if err != nil {
		s.state.MalformedPackets++
		return true
	}
	if offer == nil {
		s.state.IgnoredPackets++
		return true
	}
	if old := s.state.PendingShare; old != nil && old.SharerGuid == offer.SharerGuid && old.Kind == offer.Kind && old.Quest.QuestId == offer.Quest.QuestId {
		// An unanswered packet cannot refresh expiry or create another decision.
		// A new real offer may follow a definite rejection/cleanup, however.
		resolvedCleanup := false
		if a := s.state.LastSharedAcceptance; a != nil && a.ShareSequence == old.Sequence {
			resolvedCleanup = a.Status == questsv1.AcceptanceStatus_SERVER_REJECTED || (a.TransportWritten || old.Kind == questsv1.SharedQuestKind_SHARED_AUTOCOMPLETE) && (a.Status == questsv1.AcceptanceStatus_ALREADY_IN_LOG || a.Status == questsv1.AcceptanceStatus_SHARE_EXPIRED || a.Status == questsv1.AcceptanceStatus_SHARE_LOG_FULL)
		}
		if old.AcceptRequested && !resolvedCleanup || !old.AcceptRequested && s.now().Before(s.shares.until) {
			s.state.IgnoredPackets++
			return true
		}
	}
	s.shares.sequence++
	offer.Sequence = s.shares.sequence
	s.state.PendingShare = offer
	s.shares.until = s.now().Add(30 * time.Second)
	s.state.Revision++
	return true
}

func sharedPartyKnown(p *partyv1.StateResponse, session string, self uint64) bool {
	if p == nil || p.SessionId != session || p.Group == nil || !p.Group.Known || !p.Group.InGroup || len(p.Group.Members) < 2 || len(p.Group.Members) > 40 {
		return false
	}
	seen := make(map[uint64]bool, len(p.Group.Members))
	own, owner := false, false
	for _, m := range p.Group.Members {
		if m == nil || m.Guid == 0 || seen[m.Guid] || m.Self != (m.Guid == self) {
			return false
		}
		seen[m.Guid] = true
		if m.Guid == self {
			own = m.Online
		}
		if m.Guid == 3 {
			owner = m.Name == "Joel" && m.Online && !m.Self
		}
	}
	return own && owner
}
func (s *Service) sharedContext(ctx context.Context) error {
	w, err := s.world(ctx)
	if err != nil {
		return err
	}
	if !w.Self.Present || !w.Self.AliveKnown || !w.Self.Alive {
		return status.Error(codes.FailedPrecondition, "shared quest requires observed alive character")
	}
	p := &partyv1.StateResponse{}
	if err := s.invoke(ctx, "buddy-party", "state", &emptypb.Empty{}, p); err != nil {
		return err
	}
	s.mu.Lock()
	session := s.state.SessionId
	s.mu.Unlock()
	if !sharedPartyKnown(p, session, s.selfGUID) {
		return status.Error(codes.FailedPrecondition, "current same-session party with Joel is unconfirmed")
	}
	return nil
}
func (s *Service) validateSharedLocked(r *questsv1.SharedQuestRequest) (*questsv1.SharedQuestOffer, error) {
	p := s.state.PendingShare
	if !s.state.SharedQuestsEnabled || s.selfGUID == 0 || s.selfGUID == 3 || s.selfGUID>>48 != 0 || r.SessionId == "" || r.SessionId != s.state.SessionId || r.SharerGuid != 3 || p == nil || p.Quest == nil || p.Sequence != r.ShareSequence || p.SharerGuid != r.SharerGuid || p.Quest.QuestId != r.QuestId || p.AcceptRequested || p.Kind != questsv1.SharedQuestKind_SHARED_DETAILS && p.Kind != questsv1.SharedQuestKind_PARTY_CONFIRM && p.Kind != questsv1.SharedQuestKind_SHARED_AUTOCOMPLETE {
		return nil, status.Error(codes.FailedPrecondition, "shared quest session, sender or pending sequence changed")
	}
	if !s.state.QuestLogKnown || len(s.state.QuestLog) != 25 {
		return nil, status.Error(codes.FailedPrecondition, "own complete quest log is unknown")
	}
	return p, nil
}
func (s *Service) applySharedLogLocked() {
	a := s.state.LastSharedAcceptance
	if !s.shares.expectInsertion || a == nil || a.Status == questsv1.AcceptanceStatus_CONFIRMED_IN_LOG || a.Status == questsv1.AcceptanceStatus_SERVER_REJECTED || a.Status >= questsv1.AcceptanceStatus_ALREADY_IN_LOG {
		return
	}
	if s.state.QuestLogKnown && s.state.QuestLogRevision > a.BaselineLogRevision && s.inLogLocked(a.QuestId) {
		a.Status = questsv1.AcceptanceStatus_CONFIRMED_IN_LOG
		a.ConfirmedLogRevision = s.state.QuestLogRevision
		a.Failure = ""
		s.state.Revision++
	} else if a.Status == questsv1.AcceptanceStatus_REQUESTED && !s.now().Before(s.shares.acceptUntil) {
		a.Status = questsv1.AcceptanceStatus_UNCONFIRMED_TIMEOUT
		a.Failure = "shared_quest_not_confirmed_in_own_log"
		s.state.Revision++
	}
}
func (s *Service) rejectSharedLocked(failure string, reason, quest uint32) {
	if a := s.state.LastSharedAcceptance; a != nil && a.Status == questsv1.AcceptanceStatus_REQUESTED && s.now().Before(s.shares.acceptUntil) && (quest == 0 || quest == a.QuestId) {
		a.Status = questsv1.AcceptanceStatus_SERVER_REJECTED
		a.Failure = failure
		a.ServerReason = reason
		delete(s.shares.consumed, a.QuestId)
		s.state.Revision++
	}
}

func (s *Service) AcceptShared(ctx context.Context, r *questsv1.SharedQuestRequest) (*questsv1.OperationResult, error) {
	if r == nil || r.ShareSequence == 0 || r.QuestId == 0 || r.SharerGuid != 3 {
		return nil, status.Error(codes.InvalidArgument, "exact Joel share sequence and quest required")
	}
	call, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := s.reserve(call, r.SessionId, true); err != nil {
		return nil, err
	}
	defer s.releaseBusy()
	// Pure observation only: this operation never acquires a movement lease,
	// interrupts a cast, or imposes an NPC interaction-distance requirement.
	if err := s.sharedContext(call); err != nil {
		return nil, err
	}
	s.mu.Lock()
	_, err := s.validateSharedLocked(r)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := s.sharedContext(call); err != nil {
		return nil, err
	}
	if err := call.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	p, err := s.validateSharedLocked(r)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	key := r.QuestId
	statusValue := questsv1.AcceptanceStatus_REQUESTED
	message := byte(0)
	switch {
	case s.inLogLocked(r.QuestId):
		statusValue = questsv1.AcceptanceStatus_ALREADY_IN_LOG
		message = 6
	case !s.now().Before(s.shares.until):
		statusValue = questsv1.AcceptanceStatus_SHARE_EXPIRED
		message = 3
	case s.shares.consumed[key]:
		statusValue = questsv1.AcceptanceStatus_SHARE_ALREADY_REQUESTED
		message = 3
	default:
		free := false
		for _, q := range s.state.QuestLog {
			free = free || q.QuestId == 0
		}
		if !free {
			statusValue = questsv1.AcceptanceStatus_SHARE_LOG_FULL
			message = 5
		}
	}
	if message == 0 && len(s.shares.consumed) >= 128 {
		s.mu.Unlock()
		return nil, status.Error(codes.ResourceExhausted, "shared acceptance capacity reached")
	}
	p.AcceptRequested = true
	if message == 0 {
		if s.shares.consumed == nil {
			s.shares.consumed = map[uint32]bool{}
		}
		s.shares.consumed[key] = true
	}
	s.shares.expectInsertion = message == 0
	a := &questsv1.SharedQuestAcceptance{ShareSequence: p.Sequence, SharerGuid: p.SharerGuid, QuestId: r.QuestId, Kind: p.Kind, Status: statusValue, BaselineLogRevision: s.state.QuestLogRevision}
	if statusValue == questsv1.AcceptanceStatus_ALREADY_IN_LOG {
		a.ConfirmedLogRevision = s.state.QuestLogRevision
		a.Failure = "already_active_share_cleanup_requested"
	}
	if message != 0 && a.Failure == "" {
		a.Failure = "shared_offer_cleanup_requested_without_acceptance"
	}
	s.state.LastSharedAcceptance = a
	s.shares.acceptUntil = s.now().Add(responseTimeout)
	s.state.Revision++
	if message != 0 && p.Kind == questsv1.SharedQuestKind_SHARED_AUTOCOMPLETE {
		// This producer never set Divider. Retire only this local offer rather
		// than sending a divider-clearing decision for another possible share.
		a.Failure = "shared_alternate_offer_retired_without_acceptance"
		result := s.snapshotLocked()
		result.ActionInFlight = false
		s.mu.Unlock()
		return &questsv1.OperationResult{State: result}, nil
	}
	op, payload := uint32(opcode.CMSGQuestgiverAcceptQuest), questPacket(3, r.QuestId, true)
	if p.Kind == questsv1.SharedQuestKind_PARTY_CONFIRM {
		op = opcode.CMSGQuestConfirmAccept
		payload = binary.LittleEndian.AppendUint32(nil, r.QuestId)
	}
	if message != 0 {
		op = opcode.MSGQuestPushResult
		payload = append(binary.LittleEndian.AppendUint32(npcPacket(3), r.QuestId), message)
	}
	s.mu.Unlock()
	err = s.send(call, op, payload)
	s.mu.Lock()
	if err != nil {
		if a.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG && a.Status != questsv1.AcceptanceStatus_SERVER_REJECTED {
			a.Status = questsv1.AcceptanceStatus_WRITE_UNCONFIRMED
			a.Failure = "shared_quest_write_outcome_unconfirmed"
		}
	} else {
		a.TransportWritten = true
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if message == 0 {
		timer := time.NewTimer(responseTimeout)
		defer timer.Stop()
		poll := time.NewTicker(50 * time.Millisecond)
		defer poll.Stop()
	wait:
		for {
			_, _ = s.world(call)
			s.mu.Lock()
			finished := a.Status != questsv1.AcceptanceStatus_REQUESTED
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
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.Status == questsv1.AcceptanceStatus_REQUESTED {
		a.Status = questsv1.AcceptanceStatus_UNCONFIRMED_TIMEOUT
		a.Failure = "shared_quest_not_confirmed_in_own_log"
	}
	result := s.snapshotLocked()
	result.ActionInFlight = false
	return &questsv1.OperationResult{TransportWritten: true, ServerResponseReceived: a.Status == questsv1.AcceptanceStatus_CONFIRMED_IN_LOG || a.Status == questsv1.AcceptanceStatus_SERVER_REJECTED, State: result}, nil
}
