package buddychat

import (
	"context"
	"errors"
	"sync"

	chatv1 "github.com/agent-wow/agent-wow/modules/buddy-chat/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

const messageCapacity = 256

type packetSender interface {
	SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error)
}

// Service has a session-local, memory-only ring. It never automatically sends a
// message, retries a write, resolves player names, or reads authentication data.
type Service struct {
	chatv1.UnimplementedBuddyChatServer
	session   packetSender
	selfGUID  uint64
	mu        sync.Mutex
	messages  [messageCapacity]*chatv1.Message
	latest    uint64
	ignored   uint64
	malformed uint64
}

func NewService(session packetSender, selfGUID uint64) *Service {
	return &Service{session: session, selfGUID: selfGUID}
}

func (s *Service) Send(ctx context.Context, request *chatv1.SendRequest) (*chatv1.SendResponse, error) {
	payload, err := encodeChat(request)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if s.session == nil {
		return nil, status.Error(codes.FailedPrecondition, "session callback is unavailable")
	}
	if _, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: uint32(opcode.CMSGMessagechat), Payload: payload}); err != nil {
		return nil, err
	}
	return &chatv1.SendResponse{TransportWritten: true}, nil
}

func (s *Service) Emote(ctx context.Context, request *chatv1.EmoteRequest) (*chatv1.SendResponse, error) {
	payload, err := encodeEmote(request)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if s.session == nil {
		return nil, status.Error(codes.FailedPrecondition, "session callback is unavailable")
	}
	if _, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: uint32(opcode.CMSGTextEmote), Payload: payload}); err != nil {
		return nil, err
	}
	return &chatv1.SendResponse{TransportWritten: true}, nil
}

func (s *Service) OnPacket(_ context.Context, packet *modv1.WorldPacket) (*emptypb.Empty, error) {
	// A handler error is fatal to the core session. Unknown and malformed chat
	// must therefore be accounted for locally and acknowledged successfully.
	var message *chatv1.Message
	err := errUnsupportedChat
	if packet != nil && packet.Opcode == uint32(opcode.SMSGMessagechat) {
		message, err = decodeChat(packet.Payload)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		if errors.Is(err, errUnsupportedChat) {
			s.ignored++
		} else {
			s.malformed++
		}
		return &emptypb.Empty{}, nil
	}
	s.latest++
	message.Sequence = s.latest
	message.Self = message.SenderGuid == s.selfGUID
	s.messages[(s.latest-1)%messageCapacity] = message
	return &emptypb.Empty{}, nil
}

func (s *Service) Poll(ctx context.Context, request *chatv1.PollRequest) (*chatv1.PollResponse, error) {
	if request == nil {
		return nil, status.Error(codes.InvalidArgument, "poll request is required")
	}
	if request.Limit > messageCapacity {
		return nil, status.Error(codes.InvalidArgument, "limit must be at most 256")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	limit := request.Limit
	if limit == 0 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if request.AfterSequence > s.latest {
		return nil, status.Error(codes.InvalidArgument, "cursor is newer than this module session; restart polling with after_sequence 0")
	}
	result := &chatv1.PollResponse{
		Messages: []*chatv1.Message{}, NextSequence: request.AfterSequence,
		LatestSequence: s.latest, IgnoredPackets: s.ignored, MalformedPackets: s.malformed,
	}
	if s.latest == 0 {
		return result, nil
	}
	oldest := uint64(1)
	if s.latest > messageCapacity {
		oldest = s.latest - messageCapacity + 1
	}
	result.OldestSequence = oldest
	start := request.AfterSequence + 1
	if start < oldest {
		result.CursorLost = true
		start = oldest
	}
	for sequence := start; sequence <= s.latest && len(result.Messages) < int(limit); sequence++ {
		result.Messages = append(result.Messages, proto.Clone(s.messages[(sequence-1)%messageCapacity]).(*chatv1.Message))
		result.NextSequence = sequence
	}
	return result, nil
}
