package buddyactions

import (
	"context"
	"encoding/binary"
	"errors"
	"math"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// This separate monotonic slot retains one exact activation attempt. Expiry,
// policy edits and visit changes never clear an uncertain or accepted flight.
type flightpathActivationTracker struct {
	naturalMenuToken   string
	revision           uint64
	request            *actionsv1.FlightpathActivationRequest
	operation          *actionsv1.FlightpathActivationOperation
	menu               *actionsv1.FlightpathOperation
	settlementRequest  *actionsv1.SettleFlightpathRequest
	settlementResult   *actionsv1.FlightpathObservationResponse
	transferRequest    *actionsv1.AdoptFlightpathTransferRequest
	transferInvoked    bool
	transferResult     *actionsv1.FlightpathObservationResponse
	transferAdoption   *worldv1.SplineTransferAdoption
	transferActivation *actionsv1.FlightpathActivationOperation
	transferAttempt    *actionsv1.FlightpathTransferAttempt
	transferBooking    uint64
	transferHistory    []flightpathTransferResult
}

func (s *Service) flightpathActivationObservationLocked(out *actionsv1.FlightpathObservationResponse) {
	out.ActivationRevision = s.flightpathActivation.revision
	if op := s.flightpathActivation.operation; op != nil {
		out.Activation = proto.Clone(op).(*actionsv1.FlightpathActivationOperation)
	}
	if menu := s.flightpathActivation.menu; menu != nil {
		out.ActivationMenu = proto.Clone(menu).(*actionsv1.FlightpathOperation)
	}
	if attempt := s.flightpathActivation.transferAttempt; attempt != nil {
		out.TransferAttempt = proto.Clone(attempt).(*actionsv1.FlightpathTransferAttempt)
	}
}

func (s *Service) flightpathActivationMenuMatchesLocked(r *actionsv1.FlightpathActivationRequest, menu *actionsv1.FlightpathOperation) bool {
	if s.flightpath.revision != r.MenuRevision || s.flightpath.operation != menu ||
		!s.flightpathOperationCurrentLocked(menu) || menu.Revision != r.MenuRevision || menu.NpcGuid != r.NpcGuid ||
		menu.Pending || (menu.Mode != "discover" && (menu.Mode != "arrival" || menu.Revision != s.flightpath.certifiedArrivalMenuRevision)) ||
		menu.Phase != "menu_observed" || !menu.MenuKnown ||
		(!menu.TransportWritten && !menu.TransportUnknown) || menu.SourceNode != r.SourceNode || len(menu.KnownNodes) > 448 {
		return false
	}
	source, destination := false, false
	var previous uint32
	for _, node := range menu.KnownNodes {
		if node == 0 || node > 448 || node <= previous {
			return false
		}
		previous = node
		source = source || node == r.SourceNode
		destination = destination || node == r.DestinationNode
	}
	return source && destination
}

// ActivateFlightpath sends one caller-selected normal direct-hop activation.
// A menu mask is not a route offer or fare, so missing quote acceptance is an
// explicit request field. Every physical path, fare and mount check stays with
// the server; this method never reports flight start, arrival or a live price.
func (s *Service) ActivateFlightpath(ctx context.Context, request *actionsv1.FlightpathActivationRequest) (*actionsv1.FlightpathObservationResponse, error) {
	if request == nil || request.SessionId == "" || len(request.SessionId) > 128 || request.SelfGuid == 0 || request.VisitId == 0 ||
		request.Token == "" || len(request.Token) > 128 || request.NpcGuid>>48 != 0xf130 || request.NpcGuid&0xffffff == 0 ||
		request.SourceNode == 0 || request.SourceNode > 448 || request.DestinationNode == 0 || request.DestinationNode > 448 ||
		request.SourceNode == request.DestinationNode || request.MenuRevision == 0 || request.ExpectedCopperRevision == 0 || !request.AcceptUnknownFare {
		return nil, status.Error(codes.InvalidArgument, "exact body visit token flightmaster distinct nodes menu and wallet revisions with explicit unknown-fare acceptance required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	r := proto.Clone(request).(*actionsv1.FlightpathActivationRequest)
	s.mu.Lock()
	defer s.mu.Unlock()
	f := &s.flightpathActivation
	if r.ExpectedRevision != f.revision {
		if f.request != nil && proto.Equal(r, f.request) {
			return s.flightpathObservationLocked(), nil
		}
		return nil, status.Error(codes.Aborted, "flightpath activation revision changed")
	}
	if f.operation.GetPending() {
		return nil, status.Error(codes.FailedPrecondition, "previous flightpath activation remains unresolved")
	}
	if f.operation.GetCurrentSegment() != nil && !f.operation.GetArrivalKnown() {
		return nil, status.Error(codes.FailedPrecondition, "transferred booking requires observed arrival before replacement")
	}
	if f.revision == math.MaxUint64 {
		return nil, status.Error(codes.ResourceExhausted, "flightpath activation revision exhausted")
	}
	if r.SessionId != s.sessionID || r.SelfGuid != s.selfGUID || !s.encounterVisit.bound ||
		r.VisitId != s.encounterVisit.revision || !s.ownsExploration(r.Token) {
		return nil, status.Error(codes.FailedPrecondition, "current body visit and owned exploration token required")
	}
	menu := s.flightpath.operation
	if !s.flightpathActivationMenuMatchesLocked(r, menu) {
		return nil, status.Error(codes.FailedPrecondition, "exact current completed flightmaster menu with both nodes known required")
	}
	baselineMenu := proto.Clone(menu).(*actionsv1.FlightpathOperation)
	// Reservation precedes callbacks and the game write. A lost unary reply to
	// a late refusal therefore still has one exact, retained zero-send receipt.
	f.revision++
	f.request = r
	f.menu = proto.Clone(baselineMenu).(*actionsv1.FlightpathOperation)
	f.settlementRequest, f.settlementResult = nil, nil
	f.archiveTransferResult()
	f.transferRequest, f.transferInvoked, f.transferResult = nil, false, nil
	f.transferAdoption = nil
	f.transferActivation = nil
	f.transferAttempt = nil
	f.transferBooking = 0
	f.operation = &actionsv1.FlightpathActivationOperation{Revision: f.revision, SessionId: r.SessionId,
		SelfGuid: r.SelfGuid, VisitId: r.VisitId, NpcGuid: r.NpcGuid, MenuRevision: r.MenuRevision,
		SourceNode: r.SourceNode, DestinationNode: r.DestinationNode, Pending: true, Phase: "reserved"}
	op := f.operation
	refuse := func(reason string) (*actionsv1.FlightpathObservationResponse, error) {
		op.Pending, op.Phase, op.Error = false, "not_dispatched", boundedPolicyDiagnostic(reason, 240)
		return s.flightpathObservationLocked(), nil
	}
	w, err := s.snapshot(ctx, r.NpcGuid)
	if err != nil {
		return refuse("flightpath activation world observations unavailable: " + err.Error())
	}
	query := &actionsv1.FlightpathQueryRequest{SessionId: r.SessionId, SelfGuid: r.SelfGuid, VisitId: r.VisitId,
		Token: r.Token, NpcGuid: r.NpcGuid, Mode: "activate"}
	ctx, w, stop, reason := s.flightpathGroundObservationLocked(ctx, query, w)
	if reason != "" {
		return refuse(reason)
	}
	if reason := safeQuestCharacter(w, r.SelfGuid); reason != "" {
		return refuse("flightpath_activation_" + reason)
	}
	if !s.refreshPositionLocked(w) {
		return refuse("flightpath_activation_client_position_changed")
	}
	// World, position conversion and the clock all precede this pure final
	// admission. No dependency lookup or clock callback follows it.
	now := s.now()
	if w.SelfCopperKnown {
		op.MoneyBeforeKnown, op.MoneyBeforeCopper, op.MoneyBeforeRevision = true, w.SelfCopper, w.SelfCopperRevision
	}
	if reason := s.flightpathQueryGuardLocked(ctx, query, w, now, stop); reason != "" {
		return refuse(reason)
	}
	if w.Self.PositionInferred {
		return refuse("flightpath_activation_actual_received_position_required")
	}
	if !w.SelfCopperKnown || w.SelfCopperRevision == 0 || w.SelfCopperRevision != r.ExpectedCopperRevision || w.SelfCopper != r.ExpectedCopper {
		return refuse("flightpath_activation_exact_known_wallet_required")
	}
	if !s.flightpathActivationMenuMatchesLocked(r, menu) || !proto.Equal(menu, baselineMenu) {
		return refuse("flightpath_activation_menu_changed")
	}
	if f.revision != op.Revision || f.operation != op || f.request != r || !op.Pending || op.Phase != "reserved" {
		return nil, status.Error(codes.Aborted, "flightpath activation changed before transport")
	}
	op.Phase = "requested"
	payload := binary.LittleEndian.AppendUint64(make([]byte, 0, 16), r.NpcGuid)
	payload = binary.LittleEndian.AppendUint32(payload, r.SourceNode)
	payload = binary.LittleEndian.AppendUint32(payload, r.DestinationNode)
	if err := s.sendPacketLocked(ctx, opcode.CMSGActivatetaxi, payload, stop.GetTransportScope()); err != nil {
		op.Error = boundedPolicyDiagnostic(err.Error(), 240)
		if modcb.PacketWasNotDispatched(err) {
			op.Pending, op.Phase = false, "not_dispatched"
		} else {
			op.TransportUnknown, op.Phase = true, "transport_unknown"
		}
	} else {
		op.TransportWritten = true
	}
	return s.flightpathObservationLocked(), nil
}

// Activation uses the ordinary single-leg CMSG_ACTIVATETAXI, not the express
// multi-node packet. The reply has only a uint32 result, with no NPC, route,
// request ID or fare. Its zero result is acceptance, not start or arrival.
func decodeFlightpathActivationReply(payload []byte) (uint32, error) {
	if len(payload) != 4 {
		return 0, errors.New("taxi activation reply must contain exactly one result")
	}
	return binary.LittleEndian.Uint32(payload), nil
}

// This reply has no identity; only the first reply after this slot's own send
// entry can affect it. An accepted flight deliberately remains pending until a
// future, separately qualified completion mechanic can provide actual proof.
func (s *Service) flightpathActivationPacketLocked(opcodeID uint32, payload []byte, scopes ...*modv1.TransportScope) (bool, error) {
	if opcodeID != opcode.SMSGActivatetaxireply {
		return false, nil
	}
	code, err := decodeFlightpathActivationReply(payload)
	if err != nil {
		return true, err
	}
	op := s.flightpathActivation.operation
	if op == nil || !op.Pending || op.ReplyKnown || s.closed || op.SessionId != s.sessionID || op.SelfGuid != s.selfGUID ||
		!s.encounterVisit.bound || op.VisitId == 0 || op.VisitId != s.encounterVisit.revision ||
		(op.Phase != "requested" && op.Phase != "transport_unknown") || (!op.TransportWritten && !op.TransportUnknown) {
		return true, nil
	}
	op.ReplyKnown, op.ReplyCode = true, code
	switch {
	case code == 0:
		op.ActivationAccepted, op.Phase = true, "accepted_unsettled"
		if len(scopes) != 0 {
			op.AcceptanceScope = cloneFlightpathScope(scopes[0])
		}
	case code <= 12:
		op.Pending, op.Phase = false, "server_rejected"
	default:
		op.Phase, op.Error = "reply_unknown", "flightpath_activation_reply_unrecognized"
	}
	return true, nil
}
