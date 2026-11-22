package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// One monotonic CAS slot avoids an unbounded request-ID history. An exact
// retry reads its reserved operation; a stale revision never writes again.
// Policy reload, lease expiry and elapsed time do not erase uncertain sends.
type flightpathTracker struct {
	revision                     uint64
	request                      *actionsv1.FlightpathQueryRequest
	operation                    *actionsv1.FlightpathOperation
	expectLearning               bool
	arrivalReplyScope            *modv1.TransportScope
	certifiedArrivalMenuRevision uint64
}

func (s *Service) flightpathObservationLocked() *actionsv1.FlightpathObservationResponse {
	out := &actionsv1.FlightpathObservationResponse{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: s.encounterVisit.revision,
		Revision: s.flightpath.revision, VisitBound: !s.closed && s.encounterVisit.bound,
	}
	if s.flightpath.operation != nil {
		out.Operation = proto.Clone(s.flightpath.operation).(*actionsv1.FlightpathOperation)
	}
	s.flightpathActivationObservationLocked(out)
	return out
}

// This read neither queries the realm nor expires an unresolved operation.
func (s *Service) FlightpathObservation(ctx context.Context, _ *emptypb.Empty) (*actionsv1.FlightpathObservationResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flightpathObservationLocked(), nil
}

func (s *Service) flightpathOperationCurrentLocked(op *actionsv1.FlightpathOperation) bool {
	return op != nil && !s.closed && op.SessionId == s.sessionID && op.SelfGuid == s.selfGUID &&
		s.encounterVisit.bound && op.VisitId != 0 && op.VisitId == s.encounterVisit.revision
}

func (s *Service) flightpathQueryGuardLocked(ctx context.Context, r *actionsv1.FlightpathQueryRequest, w *worldv1.SnapshotResponse, now time.Time) string {
	if ctx.Err() != nil {
		return "flightpath_query_canceled"
	}
	if s.sessionID != r.SessionId || s.selfGUID != r.SelfGuid || !s.matchesCurrentEncounterVisitLocked(w) || w.VisitRevision != r.VisitId {
		return "flightpath_body_or_visit_changed"
	}
	if !s.ownsExploration(r.Token) || s.interactionUntil.Sub(now) < time.Second {
		return "flightpath_exploration_lease_changed_or_expiring"
	}
	if s.state.Moving || s.bodyMoving || s.state.MotionUnconfirmed || s.clientControlLost || s.clientControlRestoring ||
		s.state.CastPending || s.state.AutoAttackRequested || s.state.AutoAttacking || s.state.AutoAttackUnconfirmed ||
		s.autoTarget != 0 || s.autoStopping || s.encounterSwing.target != 0 || s.state.Traveling || s.questApproaching ||
		s.state.Practicing || s.fargodeep.active || s.recovery.state.Active {
		return "flightpath_other_body_action_pending"
	}
	if reason := safeQuestCharacter(w, s.selfGUID); reason != "" {
		return "flightpath_" + reason
	}
	self := w.Self
	if self.PositionAgeMs > 2000 || self.MovementFlags&0xff != 0 || !self.ChannelSpellKnown ||
		!self.ChannelObjectKnown || self.ChannelSpellId != 0 || self.ChannelObjectGuid != 0 {
		return "flightpath_stationary_current_unchanneled_body_required"
	}
	if !s.posKnown || !finite(s.pos) || s.posMap != self.MapId || s.positionRevision != self.PositionRevision {
		return "flightpath_client_position_changed"
	}
	if len(w.NearbyCreatures) > 32 || len(w.QuestGivers) > 64 || len(w.LootableCorpses) > 64 {
		return "flightpath_world_observation_bounds"
	}
	entry := uint32(r.NpcGuid >> 24 & 0xffffff)
	var seen *worldv1.CreatureState
	for _, group := range [][]*worldv1.CreatureState{{w.RequestedCreature}, w.NearbyCreatures, w.QuestGivers, w.LootableCorpses, {w.OwnerTarget}} {
		for _, npc := range group {
			if npc == nil || npc.Guid != r.NpcGuid {
				continue
			}
			p := position{npc.X, npc.Y, npc.Z, npc.Orientation}
			if npc.Entry != entry || !npc.Present || !npc.MapKnown || npc.MapId != self.MapId || !npc.PositionKnown ||
				npc.PositionAgeMs > 2000 || npc.ObservationAgeMs > 2000 || npc.PositionRevision == 0 || !finite(p) ||
				!npc.AliveKnown || !npc.Alive || !npc.HealthKnown || npc.Health == 0 || !npc.UnitFlagsKnown ||
				npc.NpcFlags&0x2000 == 0 || npc.UnitFlags&0x80000 != 0 || npc.MovementFlags&0x080000ff != 0 ||
				npc.Trajectory != nil && npc.Trajectory.Known && (!npc.Trajectory.Stopped || npc.Trajectory.TransportRelative) {
				return "flightpath_actual_stationary_flightmaster_required"
			}
			if !(distance(s.pos, p) <= 5 && math.Abs(float64(s.pos.z-p.z)) <= 2.5) {
				return "flightpath_flightmaster_out_of_range"
			}
			if seen != nil && (seen.PositionRevision != npc.PositionRevision ||
				seen.X != npc.X || seen.Y != npc.Y || seen.Z != npc.Z || seen.Orientation != npc.Orientation) {
				return "flightpath_flightmaster_views_disagree"
			}
			seen = npc
		}
	}
	if seen == nil {
		return "flightpath_flightmaster_not_observed"
	}
	return ""
}

func (s *Service) QueryFlightpath(ctx context.Context, request *actionsv1.FlightpathQueryRequest) (*actionsv1.FlightpathObservationResponse, error) {
	if request == nil || request.SessionId == "" || len(request.SessionId) > 128 || request.SelfGuid == 0 || request.VisitId == 0 ||
		request.Token == "" || len(request.Token) > 128 || request.NpcGuid>>48 != 0xf130 || request.NpcGuid&0xffffff == 0 ||
		(request.Mode != "status" && request.Mode != "discover" && request.Mode != "arrival") ||
		(request.Mode == "arrival" && (request.ActivationRevision == 0 || request.CompletionRevision == 0)) ||
		(request.Mode != "arrival" && (request.ActivationRevision != 0 || request.CompletionRevision != 0)) {
		return nil, status.Error(codes.InvalidArgument, "exact session body visit exploration token flightmaster and query mode required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	r := proto.Clone(request).(*actionsv1.FlightpathQueryRequest)
	s.mu.Lock()
	defer s.mu.Unlock()
	f := &s.flightpath
	if r.ExpectedRevision != f.revision {
		if f.request != nil && proto.Equal(r, f.request) {
			return s.flightpathObservationLocked(), nil
		}
		return nil, status.Error(codes.Aborted, "flightpath query revision changed")
	}
	if r.Mode == "arrival" {
		if !s.flightpathAcceptedCurrentLocked(r.ActivationRevision) {
			return nil, status.Error(codes.FailedPrecondition, "current accepted activation and actual acceptance scope required")
		}
	} else if s.flightpathActivation.operation.GetPending() {
		return nil, status.Error(codes.FailedPrecondition, "flightpath activation remains unsettled; its menu custody is retained")
	}
	if f.operation.GetPending() {
		return nil, status.Error(codes.FailedPrecondition, "previous flightpath query remains unresolved")
	}
	if f.revision == math.MaxUint64 {
		return nil, status.Error(codes.ResourceExhausted, "flightpath revision exhausted")
	}
	if r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID || !s.encounterVisit.bound ||
		r.VisitId != s.encounterVisit.revision || !s.ownsExploration(r.Token) {
		return nil, status.Error(codes.FailedPrecondition, "current body visit and owned exploration token required")
	}
	expectLearning := false
	activation := s.flightpathActivation.operation
	var baselineActivation *actionsv1.FlightpathActivationOperation
	if r.Mode == "arrival" {
		baselineActivation = proto.Clone(activation).(*actionsv1.FlightpathActivationOperation)
	}
	if r.Mode == "discover" {
		prior := f.operation
		if !s.flightpathOperationCurrentLocked(prior) || prior.NpcGuid != r.NpcGuid ||
			(!prior.StatusKnown && !prior.Learned && !prior.MenuKnown) {
			return nil, status.Error(codes.FailedPrecondition, "current matching flightmaster status or menu baseline required")
		}
		expectLearning = prior.StatusKnown && !prior.NodeKnown
	}
	// Reserve before callbacks as well as transport. A known refusal after
	// reservation remains inspectable if the unary reply is lost; it cannot be
	// confused with an uncertain game send by the existing task-call ledger.
	f.revision++
	f.request, f.expectLearning = r, expectLearning
	f.arrivalReplyScope = nil
	f.operation = &actionsv1.FlightpathOperation{Revision: f.revision, SessionId: r.SessionId, SelfGuid: r.SelfGuid,
		VisitId: r.VisitId, NpcGuid: r.NpcGuid, Mode: r.Mode, Pending: true, Phase: "reserved"}
	if r.Mode == "arrival" {
		f.operation.ActivationRevision, f.operation.CompletionRevision = r.ActivationRevision, r.CompletionRevision
	}
	op := f.operation
	refuse := func(reason string) (*actionsv1.FlightpathObservationResponse, error) {
		op.Pending, op.Phase, op.Error = false, "not_dispatched", boundedPolicyDiagnostic(reason, 240)
		return s.flightpathObservationLocked(), nil
	}
	// Finish World, position conversion and clock callbacks before the final
	// pure admission checks. No dependency lookup follows admission.
	w, err := s.snapshot(ctx, r.NpcGuid)
	if err != nil {
		return refuse("flightpath world observations unavailable: " + err.Error())
	}
	if reason := safeQuestCharacter(w, r.SelfGuid); reason != "" {
		return refuse("flightpath_" + reason)
	}
	if !s.refreshPositionLocked(w) {
		return refuse("flightpath_client_position_changed")
	}
	now := s.now()
	if reason := s.flightpathQueryGuardLocked(ctx, r, w, now); reason != "" {
		return refuse(reason)
	}
	if r.Mode == "arrival" {
		if reason := s.flightpathRetiredWorldReasonLocked(r.ActivationRevision, r.CompletionRevision, w); reason != "" {
			return refuse(reason)
		}
		if s.flightpathActivation.operation != activation || !proto.Equal(activation, baselineActivation) {
			return refuse("flightpath_arrival_activation_changed")
		}
		op.TransportScope = cloneFlightpathScope(w.SelfMotionAuthority.TransportScope)
	}
	if f.revision != op.Revision || f.operation != op || f.request != r || !op.Pending {
		return nil, status.Error(codes.Aborted, "flightpath query changed before transport")
	}
	op.Phase = "requested"
	payload := make([]byte, 8)
	binary.LittleEndian.PutUint64(payload, r.NpcGuid)
	opcodeID := uint32(opcode.CMSGTaxinodeStatusQuery)
	if r.Mode == "discover" || r.Mode == "arrival" {
		opcodeID = opcode.CMSGTaxiqueryavailablenodes
	}
	if err := s.sendPacketLocked(ctx, opcodeID, payload); err != nil {
		op.Error = boundedPolicyDiagnostic(err.Error(), 240)
		if modcb.PacketWasNotDispatched(err) {
			op.Pending = false
			op.Phase = "not_dispatched"
		} else {
			op.TransportUnknown = true
			op.Phase = "transport_unknown"
		}
	} else {
		op.TransportWritten = true
	}
	return s.flightpathObservationLocked(), nil
}

// Only one reserved, still-current operation can consume a reply. Unsolicited,
// foreign-NPC or old-visit packets cannot authorize another request. The
// identity-free learning notice alone never produces a node ID or menu.
func (s *Service) flightpathPacketLocked(opcodeID uint32, payload []byte, scopes ...*modv1.TransportScope) (bool, error) {
	op := s.flightpath.operation
	current := s.flightpathOperationCurrentLocked(op) && op.Pending &&
		(op.Phase == "requested" || op.Phase == "transport_unknown")
	switch opcodeID {
	case opcode.SMSGTaxinodeStatus:
		npc, known, err := decodeFlightpathStatus(payload)
		if err != nil {
			return true, err
		}
		if !current || npc != op.NpcGuid {
			return true, nil
		}
		if op.Mode == "arrival" {
			return true, nil
		}
		op.StatusKnown, op.NodeKnown = true, known
		if op.Mode == "status" {
			op.Pending = false
			op.Phase = "status_observed"
		} else if s.flightpath.expectLearning && known && op.LearningNoticeObserved {
			op.Learned = true
			op.Pending = false
			op.Phase = "learned"
		}
		return true, nil
	case opcode.SMSGNewTaxiPath:
		if len(payload) != 0 {
			return true, errors.New("taxi learning notice must be empty")
		}
		if current && op.Mode == "discover" && s.flightpath.expectLearning {
			op.LearningNoticeObserved = true
			if op.StatusKnown && op.NodeKnown {
				op.Learned = true
				op.Pending = false
				op.Phase = "learned"
			}
		}
		return true, nil
	case opcode.SMSGShowtaxinodes:
		npc, source, nodes, err := decodeFlightpathMenu(payload)
		if err != nil {
			return true, err
		}
		var scope *modv1.TransportScope
		if len(scopes) != 0 {
			scope = scopes[0]
		}
		arrival := current && op.Mode == "arrival" && s.flightpathArrivalReplyScopeLocked(op, scope)
		if current && npc == op.NpcGuid && (op.Mode == "discover" || arrival) {
			op.MenuKnown, op.SourceNode, op.KnownNodes = true, source, nodes
			op.Pending = false
			op.Phase = "menu_observed"
			if arrival {
				s.flightpath.arrivalReplyScope = cloneFlightpathScope(scope)
			}
		}
		return true, nil
	}
	return false, nil
}
