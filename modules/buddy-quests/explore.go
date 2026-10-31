package buddyquests

import (
	"context"
	"encoding/binary"
	"math"
	"regexp"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var explorationID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Installed build12340 AreaTrigger.dbc:88, map0, spherical radius10. The
// conservative9yd gate does not depend on combat-target or NPC visibility.
const scoutX, scoutY, scoutZ = -9843.5400390625, 127.5250015258789, 5.369800090789795

func (s *Service) scoutCompleteLocked() bool {
	if !s.state.QuestLogKnown {
		return false
	}
	for _, q := range s.state.QuestLog {
		if q.QuestId == 62 {
			return q.Complete && !q.Failed
		}
	}
	return false
}

func (s *Service) applyExplorationLocked() {
	r := s.state.LastExploration
	if r != nil && r.Status != questsv1.ExplorationStatus_EXPLORATION_ALREADY_COMPLETE &&
		s.state.QuestLogRevision > r.BaselineLogRevision && s.scoutCompleteLocked() {
		r.Status = questsv1.ExplorationStatus_EXPLORATION_CONFIRMED
		r.ConfirmedLogRevision = s.state.QuestLogRevision
		r.Failure = ""
	}
}

func scoutScene(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64) error {
	if w == nil || w.Self == nil || w.Self.Guid != self || self == 0 {
		return status.Error(codes.FailedPrecondition, "actual exploration character identity unavailable")
	}
	p := w.Self
	const unsafe = uint32(0x1 | 0x4 | 0x40000 | 0x80000 | 0x100000 | 0x400000 | 0x800000 | 0x1000000 | 0x2000000 | 0x8000000)
	if !p.Present || !p.MapKnown || p.MapId != 0 || !p.PositionKnown || !p.AliveKnown || !p.Alive ||
		!p.MovementFlagsKnown || p.MovementFlags & ^uint32(0x1ff) != 0 || p.ExtraMovementFlags != 0 ||
		!p.UnitFlagsKnown || p.UnitFlags&unsafe != 0 {
		return status.Error(codes.FailedPrecondition, "exploration requires a safely observed alive ground character on map0")
	}
	if a == nil || a.Moving || a.MotionUnconfirmed || a.CastPending || a.AutoAttacking || a.AutoAttackUnconfirmed ||
		a.Traveling || a.Practicing || a.Position == nil || !a.Position.Known || !a.Position.MapKnown ||
		a.Position.MapId != p.MapId || !finite(a.Position.X) || !finite(a.Position.Y) || !finite(a.Position.Z) {
		return status.Error(codes.FailedPrecondition, "exploration motion, cast or current position unconfirmed")
	}
	pos := a.Position
	if math.Sqrt(math.Pow(float64(pos.X)-scoutX, 2)+math.Pow(float64(pos.Y)-scoutY, 2)+math.Pow(float64(pos.Z)-scoutZ, 2)) > 9 {
		return status.Error(codes.FailedPrecondition, "character is outside actual Fargodeep trigger88 conservative radius")
	}
	return nil
}

// Explore sends only the normal client trigger88 after the character has
// physically entered its client-data sphere. A write does not prove credit.
// One reserved request per body is retained, even when its write is ambiguous.
func (s *Service) Explore(ctx context.Context, input *questsv1.ExploreRequest) (out *questsv1.OperationResult, err error) {
	if input == nil || input.QuestId != 62 || !explorationID.MatchString(input.RequestId) {
		return nil, status.Error(codes.InvalidArgument, "only quest62 with a bounded request_id is supported")
	}
	r := proto.Clone(input).(*questsv1.ExploreRequest)
	s.mu.Lock()
	if r.SessionId == "" || r.SessionId != s.state.SessionId {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "exploration body session changed")
	}
	if old := s.explorationRequest; old != nil {
		if !proto.Equal(old, r) {
			s.mu.Unlock()
			return nil, status.Error(codes.AlreadyExists, "an exploration request is already reserved for this body; inspect actual state")
		}
		out = &questsv1.OperationResult{State: s.snapshotLocked(), TransportWritten: s.state.LastExploration.TransportWritten}
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()
	call, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := s.reserve(call, r.SessionId); err != nil {
		return nil, err
	}
	defer s.releaseBusy()
	// A previous caller may have finished between the early idempotency read
	// and reservation. Never consume another request after that race.
	s.mu.Lock()
	if s.explorationRequest != nil {
		match := proto.Equal(s.explorationRequest, r)
		out = &questsv1.OperationResult{State: s.snapshotLocked(), TransportWritten: s.state.LastExploration.TransportWritten}
		s.mu.Unlock()
		if !match {
			return nil, status.Error(codes.AlreadyExists, "an exploration request is already reserved for this body")
		}
		return out, nil
	}
	s.mu.Unlock()
	if _, err := s.world(call); err != nil {
		return nil, err
	}
	guard := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.state.QuestLogKnown || s.state.QuestLogRevision != r.QuestLogRevision || !s.inLogLocked(62) {
			return status.Error(codes.FailedPrecondition, "actual own quest62 log or revision changed")
		}
		for _, q := range s.state.QuestLog {
			if q.QuestId == 62 && q.Failed {
				return status.Error(codes.FailedPrecondition, "actual quest62 failed")
			}
		}
		return nil
	}
	if err := guard(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	already := s.scoutCompleteLocked()
	if already {
		s.explorationRequest = r
		s.state.LastExploration = &questsv1.ExplorationReceipt{RequestId: r.RequestId, QuestId: 62, TriggerId: 88,
			Status: questsv1.ExplorationStatus_EXPLORATION_ALREADY_COMPLETE, BaselineLogRevision: r.QuestLogRevision}
		out = &questsv1.OperationResult{State: s.snapshotLocked()}
		out.State.ActionInFlight = false
	}
	s.mu.Unlock()
	if already {
		return out, nil
	}
	token, err := s.acquire(call)
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		defer cancel()
		a := &actionsv1.State{}
		released := s.invoke(cleanup, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "quests", Token: token}, a) == nil &&
			a.InteractionOwner != "quests" && !a.Moving && !a.MotionUnconfirmed
		s.mu.Lock()
		defer s.mu.Unlock()
		if receipt := s.state.LastExploration; receipt != nil && receipt.RequestId == r.RequestId {
			receipt.LeaseReleased = released
		}
		if out != nil {
			out.State = s.snapshotLocked()
			out.State.ActionInFlight = false
		}
	}()
	w, err := s.world(call)
	if err != nil {
		return nil, err
	}
	a, err := s.actions(call)
	if err != nil {
		return nil, err
	}
	if err := scoutScene(w, a, s.selfGUID); err != nil {
		return nil, err
	}
	if a.InteractionOwner != "quests" || a.InteractionLeaseRemainingMs < 1000 {
		return nil, status.Error(codes.FailedPrecondition, "exploration owned lease is no longer fresh")
	}
	if err := guard(); err != nil {
		return nil, err
	}
	if err := call.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	s.explorationRequest = r
	s.state.LastExploration = &questsv1.ExplorationReceipt{RequestId: r.RequestId, QuestId: 62, TriggerId: 88,
		Status: questsv1.ExplorationStatus_EXPLORATION_REQUESTED, BaselineLogRevision: r.QuestLogRevision}
	s.state.Revision++
	s.mu.Unlock()
	writeErr := s.send(call, opcode.CMSGAreatrigger, binary.LittleEndian.AppendUint32(nil, 88))
	s.mu.Lock()
	receipt := s.state.LastExploration
	if writeErr == nil {
		receipt.TransportWritten = true
	} else if receipt.Status == questsv1.ExplorationStatus_EXPLORATION_REQUESTED {
		receipt.Status, receipt.Failure = questsv1.ExplorationStatus_EXPLORATION_WRITE_UNCONFIRMED, "trigger_write_outcome_unconfirmed"
	}
	s.mu.Unlock()
	deadline := time.NewTimer(responseTimeout)
	defer deadline.Stop()
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
wait:
	for {
		_, _ = s.world(call)
		s.mu.Lock()
		confirmed := s.state.LastExploration.Status == questsv1.ExplorationStatus_EXPLORATION_CONFIRMED
		s.mu.Unlock()
		if confirmed || writeErr != nil {
			break
		}
		select {
		case <-call.Done():
			break wait
		case <-deadline.C:
			break wait
		case <-poll.C:
		}
	}
	s.mu.Lock()
	if receipt.Status == questsv1.ExplorationStatus_EXPLORATION_REQUESTED {
		receipt.Status, receipt.Failure = questsv1.ExplorationStatus_EXPLORATION_UNCONFIRMED, "no_newer_actual_quest62_completion"
	}
	out = &questsv1.OperationResult{TransportWritten: receipt.TransportWritten,
		ServerResponseReceived: receipt.Status == questsv1.ExplorationStatus_EXPLORATION_CONFIRMED, State: s.snapshotLocked()}
	s.mu.Unlock()
	return out, nil
}
