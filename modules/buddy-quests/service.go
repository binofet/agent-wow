package buddyquests

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

const operationTimeout = 4 * time.Second
const responseTimeout = 1500 * time.Millisecond

type sessionAPI interface {
	SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error)
	InvokeModule(context.Context, *modv1.InvokeModuleRequest, ...grpc.CallOption) (*modv1.InvokeModuleResponse, error)
}
type Service struct {
	questsv1.UnimplementedBuddyQuestsServer
	session                             sessionAPI
	selfGUID                            uint64
	mu                                  sync.Mutex
	now                                 func() time.Time
	state                               *questsv1.StateResponse
	offerUntil, acceptUntil, queryUntil time.Time
	queryNPC                            uint64
	queryQuest                          uint32
	wait                                chan struct{}
	responseReceived                    bool
	turnIn                              turnInRuntime
	journal                             journalRuntime
	shares                              sharedQuestRuntime
	explorationRequest                  *questsv1.ExploreRequest
}

func NewService(session sessionAPI, sessionID string, selfGUID uint64) *Service {
	return &Service{session: session, selfGUID: selfGUID, now: time.Now, state: &questsv1.StateResponse{SessionId: sessionID, SharedQuestsEnabled: sessionID != "" && selfGUID != 0 && selfGUID != 3 && selfGUID>>48 == 0}}
}

func NewIndependentService(session sessionAPI, sessionID string, selfGUID uint64) *Service {
	s := NewService(session, sessionID, selfGUID)
	s.state.SharedQuestsEnabled = false
	return s
}

func (s *Service) invoke(ctx context.Context, module, method string, input, output proto.Message) error {
	if s.session == nil {
		return status.Error(codes.FailedPrecondition, "session callback unavailable")
	}
	request, err := anypb.New(input)
	if err != nil {
		return err
	}
	result, err := s.session.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: module, Method: method, Request: request})
	if err != nil {
		return err
	}
	if result == nil || result.Result == nil {
		return status.Error(codes.Unavailable, "module returned no result")
	}
	return result.Result.UnmarshalTo(output)
}
func (s *Service) world(ctx context.Context) (*worldv1.SnapshotResponse, error) {
	result := &worldv1.SnapshotResponse{}
	if err := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, result); err != nil {
		return nil, err
	}
	if result.Self == nil || result.Self.Guid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "world observation belongs to a different character")
	}
	s.mu.Lock()
	s.applyLogLocked(result)
	s.applySharedLogLocked()
	s.applyRewardWorldLocked(result)
	s.applyExplorationLocked()
	s.mu.Unlock()
	return result, nil
}
func (s *Service) actions(ctx context.Context) (*actionsv1.State, error) {
	result := &actionsv1.State{}
	err := s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, result)
	return result, err
}
func (s *Service) applyLogLocked(world *worldv1.SnapshotResponse) {
	// Concurrent snapshot callbacks can complete out of order. An older known
	// log must never lower the baseline used to prove a later acceptance.
	stale := world.SelfQuestLogKnown && world.SelfQuestLogRevision < s.state.QuestLogRevision
	if !stale {
		s.state.QuestLogKnown = false
	}
	if !stale && world.SelfQuestLogKnown && len(world.SelfQuestLog) == 25 {
		seen := [25]bool{}
		valid := true
		for _, entry := range world.SelfQuestLog {
			if entry == nil || entry.Slot >= 25 || seen[entry.Slot] {
				valid = false
				break
			}
			seen[entry.Slot] = true
		}
		if valid {
			s.state.QuestLogKnown = true
			s.state.QuestLogRevision = world.SelfQuestLogRevision
			s.state.QuestLog = nil
			for _, entry := range world.SelfQuestLog {
				s.state.QuestLog = append(s.state.QuestLog, proto.Clone(entry).(*worldv1.QuestLogEntry))
			}
		}
	}
	if a := s.state.LastAcceptance; a != nil && a.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG && a.Status != questsv1.AcceptanceStatus_SERVER_REJECTED {
		if s.state.QuestLogKnown && s.state.QuestLogRevision > a.BaselineLogRevision && s.inLogLocked(a.QuestId) {
			a.Status = questsv1.AcceptanceStatus_CONFIRMED_IN_LOG
			a.ConfirmedLogRevision = s.state.QuestLogRevision
			a.Failure = ""
			s.state.Revision++
		} else if a.Status == questsv1.AcceptanceStatus_REQUESTED && !s.now().Before(s.acceptUntil) {
			a.Status = questsv1.AcceptanceStatus_UNCONFIRMED_TIMEOUT
			a.Failure = "no_server_quest_log_confirmation"
			s.state.Revision++
		}
	}
}
func (s *Service) inLogLocked(id uint32) bool {
	for _, entry := range s.state.QuestLog {
		if entry.QuestId == id {
			return true
		}
	}
	return false
}
func (s *Service) snapshotLocked() *questsv1.StateResponse {
	result := proto.Clone(s.state).(*questsv1.StateResponse)
	if result.Offer != nil {
		result.Offer.ExpiresInMs = 0
		if s.now().Before(s.offerUntil) {
			result.Offer.ExpiresInMs = uint64(s.offerUntil.Sub(s.now()).Milliseconds())
		}
	}
	if result.TurnInOffer != nil {
		result.TurnInOffer.ExpiresInMs = 0
		if s.now().Before(s.turnIn.offerUntil) {
			result.TurnInOffer.ExpiresInMs = uint64(s.turnIn.offerUntil.Sub(s.now()).Milliseconds())
		}
	}
	if result.PendingShare != nil {
		result.PendingShare.ExpiresInMs = 0
		if s.now().Before(s.shares.until) {
			result.PendingShare.ExpiresInMs = uint64(s.shares.until.Sub(s.now()).Milliseconds())
		}
	}
	return result
}
func (s *Service) State(ctx context.Context, _ *emptypb.Empty) (*questsv1.StateResponse, error) {
	read, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if _, err := s.world(read); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked(), nil
}
func (s *Service) Discover(ctx context.Context, _ *emptypb.Empty) (*questsv1.StateResponse, error) {
	read, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	w, err := s.world(read)
	if err != nil {
		return nil, err
	}
	a, err := s.actions(read)
	if err != nil {
		return nil, err
	}
	nearby := make([]*questsv1.NearbyGiver, 0, min(64, len(w.QuestGivers)))
	for _, npc := range w.QuestGivers[:min(64, len(w.QuestGivers))] {
		if npc == nil {
			continue
		}
		reason, d, known := interaction(w, a, npc, s.selfGUID)
		nearby = append(nearby, &questsv1.NearbyGiver{Creature: proto.Clone(npc).(*worldv1.CreatureState), DistanceKnown: known, Distance: d, CanInteract: reason == "", BlockedReason: reason})
	}
	sort.SliceStable(nearby, func(i, j int) bool {
		if nearby[i].DistanceKnown != nearby[j].DistanceKnown {
			return nearby[i].DistanceKnown
		}
		return nearby[i].Distance < nearby[j].Distance
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Nearby = nearby
	return s.snapshotLocked(), nil
}

func finite(v float32) bool {
	return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) && math.Abs(float64(v)) < 40000
}
func interaction(w *worldv1.SnapshotResponse, a *actionsv1.State, npc *worldv1.CreatureState, selfGUID uint64) (string, float64, bool) {
	if w == nil || w.Self == nil || w.Self.Guid != selfGUID || !w.Self.Present || !w.Self.MapKnown || !w.Self.AliveKnown || !w.Self.Alive {
		return "character_not_observed_alive", 0, false
	}
	if a == nil {
		return "character_motion_or_cast_unconfirmed", 0, false
	}
	p := a.Position
	if p == nil || !p.Known || !p.MapKnown || p.MapId != w.Self.MapId || !finite(p.X) || !finite(p.Y) || !finite(p.Z) {
		return "current_character_position_unknown", 0, false
	}
	if npc == nil || !creatureGUID(npc.Guid) || !npc.Present || npc.NpcFlags&2 == 0 || !npc.AliveKnown || !npc.Alive || !npc.MapKnown || npc.MapId != p.MapId || !npc.PositionKnown || !finite(npc.X) || !finite(npc.Y) || !finite(npc.Z) {
		return "questgiver_not_observed_alive_in_this_map", 0, false
	}
	if npc.MovementFlags&0x08000000 != 0 {
		return "questgiver_spline_position_not_current", 0, false
	}
	d := math.Sqrt(math.Pow(float64(npc.X-p.X), 2) + math.Pow(float64(npc.Y-p.Y), 2) + math.Pow(float64(npc.Z-p.Z), 2))
	if a.Moving || a.MotionUnconfirmed || a.CastPending {
		return "character_motion_or_cast_unconfirmed", d, true
	}
	if npc.MovementFlags&0xff != 0 && npc.PositionAgeMs > 1500 {
		return "moving_questgiver_position_stale", d, true
	}
	if d > 4 {
		return "questgiver_outside_conservative_range", d, true
	}
	return "", d, true
}
func (s *Service) gate(ctx context.Context, npcGUID uint64) (*worldv1.SnapshotResponse, error) {
	w, err := s.world(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.actions(ctx)
	if err != nil {
		return nil, err
	}
	for _, npc := range w.QuestGivers {
		if npc != nil && npc.Guid == npcGUID {
			reason, _, _ := interaction(w, a, npc, s.selfGUID)
			if reason != "" {
				return nil, status.Error(codes.FailedPrecondition, reason)
			}
			return w, nil
		}
	}
	return nil, status.Error(codes.FailedPrecondition, "questgiver is absent from bounded server observations")
}
func (s *Service) reserve(ctx context.Context, sessionID string, shared ...bool) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionID == "" || sessionID != s.state.SessionId {
		return status.Error(codes.FailedPrecondition, "quest session changed; discover again")
	}
	if s.state.ActionInFlight {
		return status.Error(codes.FailedPrecondition, "another quest operation is in flight")
	}
	if (len(shared) == 0 || !shared[0]) && s.state.SharedQuestsEnabled && s.state.PendingShare != nil && !s.state.PendingShare.AcceptRequested && s.now().Before(s.shares.until) {
		return status.Error(codes.FailedPrecondition, "Joel's shared quest requires a decision before NPC interaction")
	}
	if a := s.state.LastAcceptance; a != nil && a.Status == questsv1.AcceptanceStatus_REQUESTED && s.now().Before(s.acceptUntil) {
		return status.Error(codes.FailedPrecondition, "quest acceptance awaits server confirmation")
	}
	if a := s.state.LastSharedAcceptance; a != nil && a.Status == questsv1.AcceptanceStatus_REQUESTED && s.now().Before(s.shares.acceptUntil) {
		return status.Error(codes.FailedPrecondition, "shared quest acceptance awaits server confirmation")
	}
	s.state.ActionInFlight = true
	s.state.Revision++
	return nil
}
func (s *Service) releaseBusy() { s.mu.Lock(); s.state.ActionInFlight = false; s.mu.Unlock() }
func (s *Service) send(ctx context.Context, op uint32, payload []byte) error {
	if s.session == nil {
		return status.Error(codes.FailedPrecondition, "session callback unavailable")
	}
	_, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: op, Payload: payload})
	return err
}
func (s *Service) validateQuestLocked(r *questsv1.QuestRequest) (*questsv1.Quest, error) {
	o := s.state.Offer
	if r == nil || r.QuestId == 0 || o == nil || r.SessionId != s.state.SessionId || r.OfferRevision != o.Revision || r.NpcGuid != o.NpcGuid || !s.now().Before(s.offerUntil) {
		return nil, status.Error(codes.FailedPrecondition, "quest offer no longer matches this session, revision, NPC and quest")
	}
	for _, q := range o.Quests {
		if q.QuestId == r.QuestId {
			if !q.Available || q.AcceptRequested {
				return nil, status.Error(codes.FailedPrecondition, "quest is not an actionable unconsumed ordinary offer")
			}
			return q, nil
		}
	}
	return nil, status.Error(codes.FailedPrecondition, "quest was not offered by this NPC")
}

func (s *Service) Offer(ctx context.Context, r *questsv1.OfferRequest) (*questsv1.OperationResult, error) {
	if r == nil || !creatureGUID(r.NpcGuid) {
		return nil, status.Error(codes.InvalidArgument, "observed creature npc_guid is required")
	}
	return s.query(ctx, r.SessionId, r.NpcGuid, nil)
}
func (s *Service) Details(ctx context.Context, r *questsv1.QuestRequest) (*questsv1.OperationResult, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "quest request required")
	}
	return s.query(ctx, r.SessionId, r.NpcGuid, r)
}
func (s *Service) query(ctx context.Context, sessionID string, npcGUID uint64, detail *questsv1.QuestRequest) (*questsv1.OperationResult, error) {
	call, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := s.reserve(call, sessionID); err != nil {
		return nil, err
	}
	defer s.releaseBusy()
	// Reject stale selections before acquiring a lease can stop movement.
	if detail != nil {
		s.mu.Lock()
		_, err := s.validateQuestLocked(detail)
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	token, err := s.acquire(call)
	if err != nil {
		return nil, err
	}
	defer s.release(token)
	guard := func() error {
		if detail == nil {
			return nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		_, err := s.validateQuestLocked(detail)
		return err
	}
	if _, err := s.prepareInteraction(call, token, npcGUID, guard); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if detail != nil {
		if _, err := s.validateQuestLocked(detail); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	s.queryNPC, s.queryQuest = npcGUID, 0
	if detail != nil {
		s.queryQuest = detail.QuestId
	}
	s.queryUntil = s.now().Add(responseTimeout)
	s.wait = make(chan struct{})
	wait := s.wait
	s.responseReceived = false
	defer func() { s.mu.Lock(); s.queryUntil = time.Time{}; s.mu.Unlock() }()
	s.state.Offer = nil
	s.state.TurnInOffer = nil
	s.state.GiverStatus = nil
	s.state.Revision++
	s.mu.Unlock()
	if detail == nil {
		if err := s.send(call, opcode.CMSGQuestgiverStatusQuery, npcPacket(npcGUID)); err != nil {
			return nil, err
		}
		err = s.send(call, opcode.CMSGQuestgiverHello, npcPacket(npcGUID))
	} else {
		err = s.send(call, opcode.CMSGQuestgiverQueryQuest, questPacket(npcGUID, detail.QuestId, false))
	}
	if err != nil {
		s.mu.Lock()
		s.queryUntil = time.Time{}
		s.mu.Unlock()
		return nil, err
	}
	timer := time.NewTimer(responseTimeout)
	defer timer.Stop()
	select {
	case <-wait:
	case <-timer.C:
	case <-call.Done():
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queryUntil = time.Time{}
	result := s.snapshotLocked()
	result.ActionInFlight = false
	return &questsv1.OperationResult{TransportWritten: true, ServerResponseReceived: s.responseReceived, State: result}, nil
}

func (s *Service) Accept(ctx context.Context, r *questsv1.QuestRequest) (*questsv1.OperationResult, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "quest request required")
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
	guard := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, err := s.validateQuestLocked(r)
		if err == nil && (!s.state.QuestLogKnown || s.inLogLocked(r.QuestId)) {
			err = status.Error(codes.FailedPrecondition, "quest log is unknown or already contains this quest")
		}
		return err
	}
	if err := guard(); err != nil {
		return nil, err
	}
	token, err := s.acquire(call)
	if err != nil {
		return nil, err
	}
	defer s.release(token)
	if _, err := s.prepareInteraction(call, token, r.NpcGuid, guard); err != nil {
		return nil, err
	}
	s.mu.Lock()
	q, err := s.validateQuestLocked(r)
	if err == nil && (!s.state.QuestLogKnown || s.inLogLocked(r.QuestId)) {
		err = status.Error(codes.FailedPrecondition, "quest log is unknown or already contains this quest")
	}
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	q.AcceptRequested = true
	s.acceptUntil = s.now().Add(responseTimeout)
	s.state.LastAcceptance = &questsv1.Acceptance{NpcGuid: r.NpcGuid, QuestId: r.QuestId, OfferRevision: r.OfferRevision, Status: questsv1.AcceptanceStatus_REQUESTED, BaselineLogRevision: s.state.QuestLogRevision}
	s.state.Revision++
	s.mu.Unlock()
	err = s.send(call, opcode.CMSGQuestgiverAcceptQuest, questPacket(r.NpcGuid, r.QuestId, true))
	s.mu.Lock()
	if err != nil {
		if s.state.LastAcceptance.Status == questsv1.AcceptanceStatus_REQUESTED {
			s.state.LastAcceptance.Status = questsv1.AcceptanceStatus_WRITE_UNCONFIRMED
			s.state.LastAcceptance.Failure = "accept_write_outcome_unconfirmed"
		}
	} else {
		s.state.LastAcceptance.TransportWritten = true
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
		finished := s.state.LastAcceptance.Status != questsv1.AcceptanceStatus_REQUESTED
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
	if a := s.state.LastAcceptance; a.Status == questsv1.AcceptanceStatus_REQUESTED {
		a.Status = questsv1.AcceptanceStatus_UNCONFIRMED_TIMEOUT
		a.Failure = "no_server_quest_log_confirmation"
	}
	result := s.snapshotLocked()
	result.ActionInFlight = false
	return &questsv1.OperationResult{TransportWritten: true, ServerResponseReceived: s.state.LastAcceptance.Status == questsv1.AcceptanceStatus_CONFIRMED_IN_LOG || s.state.LastAcceptance.Status == questsv1.AcceptanceStatus_SERVER_REJECTED, State: result}, nil
}

func (s *Service) OnPacket(_ context.Context, packet *modv1.WorldPacket) (*emptypb.Empty, error) {
	if packet != nil && s.handleSharedPacket(packet) {
		return &emptypb.Empty{}, nil
	}
	if packet != nil && (s.handleJournalPacket(packet) || s.handleTurnInPacket(packet)) {
		return &emptypb.Empty{}, nil
	}
	var offer *questsv1.Offer
	var giver *questsv1.GiverStatus
	var err error
	var failure string
	var reason, failedQuest uint32
	if packet == nil {
		s.mu.Lock()
		s.state.IgnoredPackets++
		s.mu.Unlock()
		return &emptypb.Empty{}, nil
	}
	switch packet.Opcode {
	case opcode.SMSGQuestgiverQuestList:
		offer, err = decodeList(packet.Payload, false)
	case opcode.SMSGGossipMessage:
		offer, err = decodeList(packet.Payload, true)
	case opcode.SMSGQuestgiverQuestDetails:
		offer, err = decodeDetails(packet.Payload)
	case opcode.SMSGQuestgiverStatus:
		r := reader{data: packet.Payload}
		giver = &questsv1.GiverStatus{NpcGuid: r.u64(), Status: r.u8()}
		err = r.done()
		if !creatureGUID(giver.NpcGuid) || giver.Status > 10 {
			err = malformed
		}
	case opcode.SMSGQuestgiverQuestInvalid:
		r := reader{data: packet.Payload}
		reason = r.u32()
		err = r.done()
		failure = "server_quest_invalid"
	case opcode.SMSGQuestlogFull:
		if len(packet.Payload) != 0 {
			err = malformed
		}
		failure = "server_quest_log_full"
	case opcode.SMSGQuestgiverQuestFailed:
		r := reader{data: packet.Payload}
		failedQuest, reason = r.u32(), r.u32()
		err = r.done()
		failure = "server_quest_failed"
	case opcode.SMSGGossipComplete:
		if len(packet.Payload) != 0 {
			err = malformed
		}
	default:
		s.mu.Lock()
		s.state.IgnoredPackets++
		s.mu.Unlock()
		return &emptypb.Empty{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.state.MalformedPackets++
		return &emptypb.Empty{}, nil
	}
	if offer != nil {
		followup := s.followupLocked(offer)
		if !followup && (offer.NpcGuid != s.queryNPC || !s.now().Before(s.queryUntil) || (s.queryQuest != 0 && (offer.Source != "quest_details" || len(offer.Quests) != 1 || offer.Quests[0].QuestId != s.queryQuest))) {
			s.state.IgnoredPackets++
			return &emptypb.Empty{}, nil
		}
		s.state.Revision++
		offer.Revision = s.state.Revision
		s.offerUntil = s.now().Add(30 * time.Second)
		s.state.Offer = offer
		s.responseReceived = true
		if s.wait != nil {
			close(s.wait)
			s.wait = nil
		}
	} else if giver != nil {
		if giver.NpcGuid == s.queryNPC && s.now().Before(s.queryUntil) {
			s.state.GiverStatus = giver
		} else {
			s.state.IgnoredPackets++
		}
	} else if failure != "" {
		s.rejectSharedLocked(failure, reason, failedQuest)
		s.rejectRewardLocked(failure, reason, failedQuest)
		if a := s.state.LastAcceptance; a != nil && a.Status == questsv1.AcceptanceStatus_REQUESTED && s.now().Before(s.acceptUntil) && (failedQuest == 0 || failedQuest == a.QuestId) {
			a.Status = questsv1.AcceptanceStatus_SERVER_REJECTED
			a.Failure = failure
			a.ServerReason = reason
			s.state.Revision++
		}
	}
	return &emptypb.Empty{}, nil
}
