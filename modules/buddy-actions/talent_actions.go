package buddyactions

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"io"
	"math"
	"strconv"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type talentAttempt struct {
	request *actionsv1.LearnTalentRequest
	view    *actionsv1.TalentOperation
}

type talentObservation struct {
	snapshot      *playerTalentSnapshot
	revision      string
	epoch         [16]byte
	sequence      uint64
	entropy       io.Reader
	error         string
	attempts      map[string]*talentAttempt
	last          *talentAttempt
	spentRevision string
}

func (s *Service) nextTalentRevisionLocked() (string, error) {
	t := &s.talents
	if t.epoch == [16]byte{} {
		reader := t.entropy
		if reader == nil {
			reader = rand.Reader
		}
		var epoch [16]byte
		if _, err := io.ReadFull(reader, epoch[:]); err != nil || epoch == [16]byte{} {
			return "", status.Error(codes.Unavailable, "talent observation identity unavailable")
		}
		t.epoch = epoch
	}
	if t.sequence == math.MaxUint64 {
		return "", status.Error(codes.ResourceExhausted, "talent observation revision exhausted")
	}
	t.sequence++
	return hex.EncodeToString(t.epoch[:]) + ":" + strconv.FormatUint(t.sequence, 10), nil
}

func (s *Service) observePlayerTalentsLocked(payload []byte) error {
	observed, err := decodePlayerTalentsInfo(payload)
	if err != nil {
		s.talents.error = err.Error()
		return err
	}
	if observed == nil { // Pet state cannot replace the player's state.
		return nil
	}
	t := &s.talents
	t.snapshot, t.error = observed, ""
	t.revision, err = s.nextTalentRevisionLocked()
	if err != nil {
		t.error = err.Error() // Preserve raw facts, but refuse an unbound spend.
		return nil
	}
	for _, attempt := range t.attempts {
		if attempt.view.Phase == "not_dispatched" {
			continue
		}
		attempt.view.ObservedRevision = t.revision
		if int(attempt.request.SpecIndex) < len(observed.specs) {
			for _, rank := range observed.specs[attempt.request.SpecIndex].ranks {
				if rank.id == attempt.request.TalentId && uint32(rank.rank) >= attempt.request.RankIndex {
					attempt.view.RequestedRankObserved = true
					attempt.view.Phase = "requested_rank_observed"
				}
			}
		}
		if !attempt.view.RequestedRankObserved {
			// The packet has no client request ID. An unchanged snapshot is not
			// a correlated rejection or permission to replay an unknown write.
			attempt.view.Phase = "talent_state_updated_effect_unverified"
		}
	}
	return nil
}

func (s *Service) talentStateLocked(attempt *talentAttempt) *actionsv1.TalentStateResponse {
	t := &s.talents
	out := &actionsv1.TalentStateResponse{SessionId: s.sessionID, SelfGuid: s.selfGUID,
		Known: t.snapshot != nil, Revision: t.revision, ObservationError: t.error}
	if observed := t.snapshot; observed != nil {
		out.FreePoints, out.ActiveSpecIndex = observed.free, uint32(observed.active)
		for i, spec := range observed.specs {
			view := &actionsv1.TalentSpec{Index: uint32(i)}
			for _, rank := range spec.ranks {
				view.Talents = append(view.Talents, &actionsv1.TalentRank{TalentId: rank.id, RankIndex: uint32(rank.rank)})
			}
			for _, glyph := range spec.glyphs {
				view.Glyphs = append(view.Glyphs, uint32(glyph))
			}
			out.Specs = append(out.Specs, view)
		}
	}
	if attempt == nil {
		attempt = t.last
	}
	if attempt != nil {
		out.Operation = proto.Clone(attempt.view).(*actionsv1.TalentOperation)
	}
	return out
}

func (s *Service) TalentState(_ context.Context, request *actionsv1.TalentStateRequest) (*actionsv1.TalentStateResponse, error) {
	if request.GetSessionId() == "" || request.GetSelfGuid() == 0 {
		return nil, status.Error(codes.InvalidArgument, "current talent body required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || request.SessionId != s.sessionID || request.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current talent body required")
	}
	return s.talentStateLocked(nil), nil
}

// LearnTalent sends one exact normal talent/rank pair. This contains no build,
// class/tree ranking, retry timer, respec, glyph or specialization-switch policy.
func (s *Service) LearnTalent(ctx context.Context, request *actionsv1.LearnTalentRequest) (*actionsv1.TalentStateResponse, error) {
	if request.GetSessionId() == "" || request.GetSelfGuid() == 0 || request.GetExpectedRevision() == "" ||
		len(request.GetExpectedRevision()) > 128 ||
		request.GetTalentId() == 0 || request.GetRankIndex() >= 5 || request.GetSpecIndex() >= 2 ||
		request.GetRequestId() == "" || len(request.GetRequestId()) > 128 {
		return nil, status.Error(codes.InvalidArgument, "current body, talent revision, spec, exact talent/rank and request ID required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	request = proto.Clone(request).(*actionsv1.LearnTalentRequest)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.session == nil || request.SessionId != s.sessionID || request.SelfGuid != s.selfGUID {
		return nil, status.Error(codes.FailedPrecondition, "current connected talent body required")
	}
	t := &s.talents
	if prior := t.attempts[request.RequestId]; prior != nil {
		if !proto.Equal(prior.request, request) {
			return nil, status.Error(codes.FailedPrecondition, "talent request ID already belongs to another exact request")
		}
		return s.talentStateLocked(prior), nil
	}
	observed := t.snapshot
	if observed == nil || t.error != "" || request.ExpectedRevision != t.revision ||
		request.SpecIndex != uint32(observed.active) || int(request.SpecIndex) >= len(observed.specs) {
		return nil, status.Error(codes.FailedPrecondition, "complete current talent revision and active spec required")
	}
	if t.spentRevision == t.revision {
		return nil, status.Error(codes.FailedPrecondition, "one talent request already reserved this observed revision")
	}
	for _, attempt := range t.attempts {
		if attempt.view.TransportOutcomeUnknown && !attempt.view.RequestedRankObserved {
			return nil, status.Error(codes.FailedPrecondition, "unknown talent write must reconcile before another spend")
		}
	}
	current := uint32(0)
	for _, rank := range observed.specs[request.SpecIndex].ranks {
		if rank.id == request.TalentId {
			current = uint32(rank.rank) + 1
		}
	}
	if request.RankIndex+1 <= current || request.RankIndex+1-current > observed.free {
		return nil, status.Error(codes.FailedPrecondition, "new requested rank and sufficient observed free points required")
	}
	if len(t.attempts) >= 4096 {
		return nil, status.Error(codes.ResourceExhausted, "talent request ledger full; no prior attempts were evicted")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if t.attempts == nil {
		t.attempts = map[string]*talentAttempt{}
	}
	attempt := &talentAttempt{request: request, view: &actionsv1.TalentOperation{RequestId: request.RequestId,
		Phase: "reserved", ExpectedRevision: request.ExpectedRevision, TalentId: request.TalentId,
		RankIndex: request.RankIndex, SpecIndex: request.SpecIndex}}
	t.attempts[request.RequestId], t.last, t.spentRevision = attempt, attempt, t.revision
	payload := binary.LittleEndian.AppendUint32(nil, request.TalentId)
	payload = binary.LittleEndian.AppendUint32(payload, request.RankIndex)
	if err := s.sendPacketLocked(ctx, opcode.CMSGLearnTalent, payload); err != nil {
		attempt.view.Error = err.Error()
		if modcb.PacketWasNotDispatched(err) {
			attempt.view.Phase, t.spentRevision = "not_dispatched", ""
		} else {
			attempt.view.Phase, attempt.view.TransportOutcomeUnknown = "transport_unconfirmed_no_retry", true
		}
		return s.talentStateLocked(attempt), err
	}
	attempt.view.TransportWritten, attempt.view.Phase = true, "waiting_for_talent_state"
	return s.talentStateLocked(attempt), nil
}
