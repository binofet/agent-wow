package buddyquests

import (
	"context"
	"encoding/binary"
	"fmt"
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

// Installed build12340 AreaTrigger.dbc, spherical triggers on map0.
const scoutX, scoutY, scoutZ = -9843.5400390625, 127.5250015258789, 5.369800090789795

type explorationSpec struct {
	trigger         uint32
	x, y, z, radius float64
}
type explorationRecord struct {
	request *questsv1.ExploreRequest
	receipt *questsv1.ExplorationReceipt
}

func fixedExploration(id uint32) (explorationSpec, bool) {
	switch id {
	case 62:
		return explorationSpec{88, scoutX, scoutY, scoutZ, 9}, true
	case 76:
		return explorationSpec{87, -9077.33984375, -552.9249877929688, 60.347599029541016, 29}, true
	}
	return explorationSpec{}, false
}
func (s *Service) explorationCompleteLocked(id uint32) bool {
	if !s.state.QuestLogKnown {
		return false
	}
	for _, q := range s.state.QuestLog {
		if q.QuestId == id {
			return q.Complete && !q.Failed
		}
	}
	return false
}
func (s *Service) scoutCompleteLocked() bool { return s.explorationCompleteLocked(62) }
func (s *Service) applyExplorationLocked() {
	for _, record := range s.explorations {
		r := record.receipt
		if r.Status != questsv1.ExplorationStatus_EXPLORATION_ALREADY_COMPLETE && s.state.QuestLogRevision > r.BaselineLogRevision && s.explorationCompleteLocked(r.QuestId) {
			r.Status = questsv1.ExplorationStatus_EXPLORATION_CONFIRMED
			r.ConfirmedLogRevision = s.state.QuestLogRevision
			r.Failure = ""
		}
	}
}
func (s *Service) explorationReplayLocked(r *questsv1.ExploreRequest) (*questsv1.OperationResult, error) {
	old := s.explorations[r.QuestId]
	if old == nil {
		return nil, nil
	}
	if !proto.Equal(old.request, r) {
		return nil, status.Error(codes.AlreadyExists, "an exploration request is already reserved for this body and quest; inspect actual state")
	}
	state := s.snapshotLocked()
	state.LastExploration = proto.Clone(old.receipt).(*questsv1.ExplorationReceipt)
	return &questsv1.OperationResult{State: state, TransportWritten: old.receipt.TransportWritten, ServerResponseReceived: old.receipt.Status == questsv1.ExplorationStatus_EXPLORATION_CONFIRMED}, nil
}
func (s *Service) reserveExplorationLocked(r *questsv1.ExploreRequest, receipt *questsv1.ExplorationReceipt) {
	if s.explorations == nil {
		s.explorations = map[uint32]*explorationRecord{}
	}
	s.explorations[r.QuestId] = &explorationRecord{r, receipt}
	s.explorationRequest = r
	s.state.LastExploration = receipt
}

func scoutScene(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64) error {
	return explorationScene(w, a, self, 62)
}
func explorationScene(w *worldv1.SnapshotResponse, a *actionsv1.State, self uint64, id uint32) error {
	spec, ok := fixedExploration(id)
	if !ok {
		return status.Error(codes.InvalidArgument, "unsupported exploration quest")
	}
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
	if !questPositionBound(w, a, self) {
		return status.Error(codes.FailedPrecondition, "exploration character pose binding changed")
	}
	pos := a.Position
	if math.Sqrt(math.Pow(float64(pos.X)-spec.x, 2)+math.Pow(float64(pos.Y)-spec.y, 2)+math.Pow(float64(pos.Z)-spec.z, 2)) > spec.radius {
		return status.Error(codes.FailedPrecondition, fmt.Sprintf("character is outside fixed quest%d trigger%d conservative radius", id, spec.trigger))
	}
	return nil
}

// Explore sends only fixed normal client triggers after the character has
// physically entered its client-data sphere. A write does not prove credit.
// One reserved request per body and quest is retained, even for ambiguous writes.
func (s *Service) Explore(ctx context.Context, input *questsv1.ExploreRequest) (out *questsv1.OperationResult, err error) {
	if input == nil || !explorationID.MatchString(input.RequestId) {
		return nil, status.Error(codes.InvalidArgument, "a fixed exploration quest and bounded request_id are required")
	}
	r := proto.Clone(input).(*questsv1.ExploreRequest)
	spec, supported := fixedExploration(r.QuestId)
	if !supported {
		return nil, status.Error(codes.InvalidArgument, "only exploration quests62 and76 are supported")
	}
	s.mu.Lock()
	if r.SessionId == "" || r.SessionId != s.state.SessionId {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "exploration body session changed")
	}
	if out, replayErr := s.explorationReplayLocked(r); out != nil || replayErr != nil {
		s.mu.Unlock()
		return out, replayErr
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
	if out, replayErr := s.explorationReplayLocked(r); out != nil || replayErr != nil {
		s.mu.Unlock()
		return out, replayErr
	}
	s.mu.Unlock()
	if _, err := s.world(call); err != nil {
		return nil, err
	}
	guard := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.state.QuestLogKnown || s.state.QuestLogRevision != r.QuestLogRevision || !s.inLogLocked(r.QuestId) {
			return status.Error(codes.FailedPrecondition, "actual own exploration quest log or revision changed")
		}
		for _, q := range s.state.QuestLog {
			if q.QuestId == r.QuestId && q.Failed {
				return status.Error(codes.FailedPrecondition, "actual exploration quest failed")
			}
		}
		return nil
	}
	if err := guard(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	already := s.explorationCompleteLocked(r.QuestId)
	if already {
		s.reserveExplorationLocked(r, &questsv1.ExplorationReceipt{RequestId: r.RequestId, QuestId: r.QuestId, TriggerId: spec.trigger,
			Status: questsv1.ExplorationStatus_EXPLORATION_ALREADY_COMPLETE, BaselineLogRevision: r.QuestLogRevision})
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
	a, err := s.actions(call)
	if err != nil {
		return nil, err
	}
	w, err := s.world(call)
	if err != nil {
		return nil, err
	}
	if err := explorationScene(w, a, s.selfGUID, r.QuestId); err != nil {
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
	s.reserveExplorationLocked(r, &questsv1.ExplorationReceipt{RequestId: r.RequestId, QuestId: r.QuestId, TriggerId: spec.trigger,
		Status: questsv1.ExplorationStatus_EXPLORATION_REQUESTED, BaselineLogRevision: r.QuestLogRevision})
	s.state.Revision++
	s.mu.Unlock()
	writeErr := s.send(call, opcode.CMSGAreatrigger, binary.LittleEndian.AppendUint32(nil, spec.trigger))
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
		receipt.Status, receipt.Failure = questsv1.ExplorationStatus_EXPLORATION_UNCONFIRMED, "no_newer_actual_exploration_completion"
	}
	out = &questsv1.OperationResult{TransportWritten: receipt.TransportWritten,
		ServerResponseReceived: receipt.Status == questsv1.ExplorationStatus_EXPLORATION_CONFIRMED, State: s.snapshotLocked()}
	s.mu.Unlock()
	return out, nil
}
