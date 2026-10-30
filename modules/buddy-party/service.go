package buddyparty

import (
	"context"
	"errors"
	"strings"
	"sync"

	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type packetSender interface {
	SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error)
}

// Service never accepts invitations automatically. Membership comes exclusively
// from validated server packets, not successful client transport writes.
type Service struct {
	partyv1.UnimplementedBuddyPartyServer
	session    packetSender
	selfGUID   uint64
	selfName   string
	mu         sync.Mutex
	state      *partyv1.StateResponse
	nextInvite uint64
}

func NewService(session packetSender, sessionID string, selfGUID uint64, selfName string) *Service {
	return &Service{session: session, selfGUID: selfGUID, selfName: selfName,
		state: &partyv1.StateResponse{SessionId: sessionID, Group: &partyv1.Group{}}}
}

func (s *Service) State(ctx context.Context, _ *emptypb.Empty) (*partyv1.StateResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.Clone(s.state).(*partyv1.StateResponse), nil
}

func (s *Service) OnPacket(_ context.Context, packet *modv1.WorldPacket) (*emptypb.Empty, error) {
	var group *partyv1.Group
	var result *partyv1.CommandResult
	var name string
	var active bool
	var err error
	if packet == nil {
		err = errUnsupported
	} else {
		switch packet.Opcode {
		case uint32(opcode.SMSGGroupInvite):
			name, active, err = decodeInvite(packet.Payload)
			if err == nil && !active {
				err = errUnsupported
			}
		case uint32(opcode.SMSGGroupList):
			group, err = decodeGroup(packet.Payload, s.selfGUID, s.selfName)
		case uint32(opcode.SMSGPartyCommandResult):
			result, err = decodeCommandResult(packet.Payload)
		case uint32(opcode.SMSGGroupDecline):
			name, err = decodeDecline(packet.Payload)
		case uint32(opcode.SMSGGroupDestroyed), uint32(opcode.SMSGGroupUninvite):
			if len(packet.Payload) != 0 {
				err = errMalformed
			} else {
				group = &partyv1.Group{Known: true}
			}
		default:
			err = errUnsupported
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Revision++
	if err != nil {
		if errors.Is(err, errUnsupported) {
			s.state.IgnoredPackets++
		} else {
			s.state.MalformedPackets++
		}
		// Returning a handler error would terminate the entire gameplay session.
		return &emptypb.Empty{}, nil
	}
	switch {
	case group != nil:
		s.state.Group, s.state.PendingInvite = group, nil
	case result != nil:
		s.state.LastCommandResult = result
	case active:
		s.nextInvite++
		s.state.PendingInvite = &partyv1.PendingInvite{Sequence: s.nextInvite, InviterName: name}
	default:
		s.state.LastDeclinedName = name
	}
	return &emptypb.Empty{}, nil
}

func (s *Service) Accept(ctx context.Context, request *partyv1.InviteDecisionRequest) (*partyv1.ActionResponse, error) {
	return s.decide(ctx, request, partyv1.InvitationDecision_ACCEPT_REQUESTED, uint32(opcode.CMSGGroupAccept), make([]byte, 4))
}
func (s *Service) Decline(ctx context.Context, request *partyv1.InviteDecisionRequest) (*partyv1.ActionResponse, error) {
	return s.decide(ctx, request, partyv1.InvitationDecision_DECLINE_REQUESTED, uint32(opcode.CMSGGroupDecline), nil)
}

func (s *Service) decide(ctx context.Context, request *partyv1.InviteDecisionRequest, decision partyv1.InvitationDecision, operation uint32, payload []byte) (*partyv1.ActionResponse, error) {
	if request == nil || request.SessionId == "" || request.InviteSequence == 0 || request.InviterName == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id, invite_sequence, and inviter_name are required")
	}
	if err := s.begin(ctx, func() error {
		invite := s.state.PendingInvite
		if request.SessionId != s.state.SessionId || invite == nil || request.InviteSequence != invite.Sequence || request.InviterName != invite.InviterName {
			return status.Error(codes.FailedPrecondition, "invitation no longer matches this session, sequence, and inviter name; read state again")
		}
		if invite.Decision != partyv1.InvitationDecision_PENDING {
			return status.Error(codes.FailedPrecondition, "a decision was already requested for this invitation; do not retry")
		}
		invite.Decision = decision // Reserve before releasing mu; network calls never hold it.
		return nil
	}); err != nil {
		return nil, err
	}
	return s.write(ctx, operation, payload, request.InviteSequence)
}

func (s *Service) Invite(ctx context.Context, request *partyv1.InviteRequest) (*partyv1.ActionResponse, error) {
	if request == nil {
		return nil, status.Error(codes.InvalidArgument, "invite request is required")
	}
	payload, err := encodeInvite(request.Name)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if strings.EqualFold(request.Name, s.selfName) {
		return nil, status.Error(codes.InvalidArgument, "cannot invite the current character")
	}
	if err := s.begin(ctx, nil); err != nil {
		return nil, err
	}
	return s.write(ctx, uint32(opcode.CMSGGroupInvite), payload, 0)
}

func (s *Service) Leave(ctx context.Context, request *partyv1.LeaveRequest) (*partyv1.ActionResponse, error) {
	if request == nil || request.SessionId == "" || request.ExpectedGroupGuid == 0 {
		return nil, status.Error(codes.InvalidArgument, "session_id and expected_group_guid are required")
	}
	if err := s.begin(ctx, func() error {
		group := s.state.Group
		if request.SessionId != s.state.SessionId || !group.Known || !group.InGroup || request.ExpectedGroupGuid != group.GroupGuid {
			return status.Error(codes.FailedPrecondition, "observed group no longer matches; read state again")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return s.write(ctx, uint32(opcode.CMSGGroupDisband), nil, 0)
}

// begin serializes actions without blocking packet handling while a callback is
// outstanding. No operation is retried after an ambiguous callback failure.
func (s *Service) begin(ctx context.Context, validate func() error) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	if s.session == nil {
		return status.Error(codes.FailedPrecondition, "session callback is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.ActionInFlight {
		return status.Error(codes.FailedPrecondition, "another party action is in flight")
	}
	if validate != nil {
		if err := validate(); err != nil {
			return err
		}
	}
	s.state.ActionInFlight = true
	s.state.Revision++
	return nil
}

func (s *Service) write(ctx context.Context, operation uint32, payload []byte, inviteSequence uint64) (*partyv1.ActionResponse, error) {
	_, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: operation, Payload: payload})
	s.mu.Lock()
	s.state.ActionInFlight = false
	if invite := s.state.PendingInvite; err == nil && inviteSequence != 0 && invite != nil && invite.Sequence == inviteSequence {
		invite.TransportWritten = true
	}
	s.state.Revision++
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &partyv1.ActionResponse{TransportWritten: true}, nil
}
