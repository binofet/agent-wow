package buddyparty

import (
	"context"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type packetRecorder struct {
	mu      sync.Mutex
	packets []*modv1.SendPacketRequest
	write   func(context.Context) error
}

func (r *packetRecorder) SendPacket(ctx context.Context, request *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	r.mu.Lock()
	r.packets = append(r.packets, proto.Clone(request).(*modv1.SendPacketRequest))
	r.mu.Unlock()
	if r.write != nil {
		if err := r.write(ctx); err != nil {
			return nil, err
		}
	}
	return &emptypb.Empty{}, nil
}
func (r *packetRecorder) snapshot() []*modv1.SendPacketRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*modv1.SendPacketRequest(nil), r.packets...)
}

func state(t *testing.T, s *Service) *partyv1.StateResponse {
	t.Helper()
	result, err := s.State(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func deliver(t *testing.T, s *Service, operation uint32, payload []byte) {
	t.Helper()
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: uint32(operation), Payload: payload}); err != nil {
		t.Fatal("packet error would terminate gameplay session", err)
	}
}
func decision(snapshot *partyv1.StateResponse) *partyv1.InviteDecisionRequest {
	return &partyv1.InviteDecisionRequest{SessionId: snapshot.SessionId, InviteSequence: snapshot.PendingInvite.Sequence, InviterName: snapshot.PendingInvite.InviterName}
}

func TestServiceInvitationBindingAndNoAutomaticAction(t *testing.T) {
	recorder := &packetRecorder{}
	s := NewService(recorder, "body-one", 2, "Arlen")
	if initial := state(t, s); initial.Group.Known || initial.PendingInvite != nil {
		t.Fatal("absence of server state was treated as known solo", initial)
	}
	deliver(t, s, opcode.SMSGGroupInvite, unhex(t, inviteHex))
	first := state(t, s)
	if first.PendingInvite.InviterName != "Joel" || first.PendingInvite.Sequence != 1 || len(recorder.snapshot()) != 0 {
		t.Fatal("invitation was lost or automatically acted on", first)
	}
	for _, request := range []*partyv1.InviteDecisionRequest{
		nil, {}, {SessionId: "body-one", InviteSequence: 1},
		{SessionId: "old-body", InviteSequence: 1, InviterName: "Joel"},
		{SessionId: "body-one", InviteSequence: 2, InviterName: "Joel"},
		{SessionId: "body-one", InviteSequence: 1, InviterName: "joel"},
	} {
		if _, err := s.Accept(context.Background(), request); err == nil {
			t.Fatal("unbound or stale accept accepted", request)
		}
	}
	if len(recorder.snapshot()) != 0 {
		t.Fatal("invalid decision reached transport")
	}
	response, err := s.Accept(context.Background(), decision(first))
	if err != nil || !response.TransportWritten {
		t.Fatalf("bound accept failed: %v, %v", response, err)
	}
	packets := recorder.snapshot()
	if len(packets) != 1 || packets[0].Opcode != uint32(opcode.CMSGGroupAccept) || hex.EncodeToString(packets[0].Payload) != "00000000" {
		t.Fatal("accept wire mismatch", packets)
	}
	after := state(t, s)
	if after.Group.Known || after.Group.InGroup || !after.PendingInvite.TransportWritten || after.PendingInvite.Decision != partyv1.InvitationDecision_ACCEPT_REQUESTED {
		t.Fatal("transport success was confused with group formation", after)
	}
	if _, err := s.Decline(context.Background(), decision(first)); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("decision was retried", err)
	}
	deliver(t, s, opcode.SMSGGroupList, unhex(t, duoHex))
	after = state(t, s)
	if !after.Group.Known || !after.Group.InGroup || len(after.Group.Members) != 2 || after.PendingInvite != nil {
		t.Fatal("server membership update not applied", after)
	}
	// State RPC returns an isolated snapshot, including nested members.
	after.Group.Members[0].Name = "mutated"
	if state(t, s).Group.Members[0].Name != "Arlen" {
		t.Fatal("caller mutation changed server-observed state")
	}
}

func TestServiceDeclineInviteAndLeave(t *testing.T) {
	recorder := &packetRecorder{}
	s := NewService(recorder, "body-one", 2, "Arlen")
	deliver(t, s, opcode.SMSGGroupInvite, unhex(t, inviteHex))
	if response, err := s.Decline(context.Background(), decision(state(t, s))); err != nil || !response.TransportWritten {
		t.Fatal(response, err)
	}
	if _, err := s.Invite(context.Background(), &partyv1.InviteRequest{Name: "Joel"}); err != nil {
		t.Fatal(err)
	}
	for _, request := range []*partyv1.InviteRequest{nil, {Name: "Arlen"}, {Name: "arlen"}, {Name: "Joel\x00"}} {
		if _, err := s.Invite(context.Background(), request); status.Code(err) != codes.InvalidArgument {
			t.Fatal("invalid outgoing invite accepted", request, err)
		}
	}
	if _, err := s.Leave(context.Background(), &partyv1.LeaveRequest{SessionId: "body-one", ExpectedGroupGuid: groupGUID}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("unobserved group could be left", err)
	}
	deliver(t, s, opcode.SMSGGroupList, unhex(t, duoHex))
	for _, request := range []*partyv1.LeaveRequest{nil, {}, {SessionId: "old-body", ExpectedGroupGuid: groupGUID}, {SessionId: "body-one", ExpectedGroupGuid: groupGUID + 1}} {
		if _, err := s.Leave(context.Background(), request); err == nil {
			t.Fatal("invalid/stale leave accepted", request)
		}
	}
	if response, err := s.Leave(context.Background(), &partyv1.LeaveRequest{SessionId: "body-one", ExpectedGroupGuid: groupGUID}); err != nil || !response.TransportWritten {
		t.Fatal(response, err)
	}
	if !state(t, s).Group.InGroup {
		t.Fatal("transport write optimistically removed group")
	}
	packets := recorder.snapshot()
	if len(packets) != 3 || packets[0].Opcode != uint32(opcode.CMSGGroupDecline) || len(packets[0].Payload) != 0 || packets[1].Opcode != uint32(opcode.CMSGGroupInvite) || hex.EncodeToString(packets[1].Payload) != "4a6f656c0000000000" || packets[2].Opcode != uint32(opcode.CMSGGroupDisband) || len(packets[2].Payload) != 0 {
		t.Fatal("decline/invite/leave packets mismatch", packets)
	}
	deliver(t, s, opcode.SMSGGroupList, unhex(t, leftHex))
	if after := state(t, s); !after.Group.Known || after.Group.InGroup || after.Group.GroupGuid != 0 {
		t.Fatal("server leave acknowledgement not applied", after)
	}
}

func TestServiceReplacementAndRestartInvalidateInvitationToken(t *testing.T) {
	recorder := &packetRecorder{}
	s := NewService(recorder, "body-one", 2, "Arlen")
	deliver(t, s, opcode.SMSGGroupInvite, unhex(t, inviteHex))
	old := decision(state(t, s))
	stranger := append([]byte("\x01Eve\x00"), make([]byte, 9)...)
	deliver(t, s, opcode.SMSGGroupInvite, stranger)
	if _, err := s.Accept(context.Background(), old); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("old Joel token accepted a replacement inviter", err)
	}
	if snapshot := state(t, s); snapshot.PendingInvite.InviterName != "Eve" || snapshot.PendingInvite.Sequence != 2 || len(recorder.snapshot()) != 0 {
		t.Fatal("stranger invitation triggered an action or retained old identity", snapshot)
	}
	// A new runtime may reuse sequence 1, but its session token must differ.
	restarted := NewService(recorder, "body-two", 2, "Arlen")
	deliver(t, restarted, opcode.SMSGGroupInvite, unhex(t, inviteHex))
	if _, err := restarted.Accept(context.Background(), old); status.Code(err) != codes.FailedPrecondition || len(recorder.snapshot()) != 0 {
		t.Fatal("previous body's invitation token survived restart", err)
	}
}

func TestServiceMalformedAndUnsupportedPacketsRemainLocal(t *testing.T) {
	s := NewService(nil, "body-one", 2, "Arlen")
	deliver(t, s, opcode.SMSGGroupList, unhex(t, duoHex))
	for _, operation := range []uint32{opcode.SMSGGroupInvite, opcode.SMSGGroupList, opcode.SMSGGroupDecline, opcode.SMSGPartyCommandResult, opcode.SMSGGroupDestroyed, opcode.SMSGGroupUninvite} {
		deliver(t, s, operation, []byte{0})
	}
	s.OnPacket(context.Background(), nil)
	deliver(t, s, opcode.CMSGGroupInvite, nil)
	notInvite := unhex(t, inviteHex)
	notInvite[0] = 0
	deliver(t, s, opcode.SMSGGroupInvite, notInvite)
	after := state(t, s)
	if !after.Group.InGroup || after.PendingInvite != nil || after.MalformedPackets != 6 || after.IgnoredPackets != 3 {
		t.Fatal("invalid packets damaged state or counters", after)
	}
	deliver(t, s, opcode.SMSGPartyCommandResult, unhex(t, "000000004a6f656c000000000000000000"))
	deliver(t, s, opcode.SMSGGroupDecline, []byte("Joel\x00"))
	if after := state(t, s); after.LastDeclinedName != "Joel" || after.LastCommandResult.MemberName != "Joel" || !after.Group.InGroup {
		t.Fatal("command result incorrectly changed group", after)
	}
	for _, operation := range []uint32{opcode.SMSGGroupDestroyed, opcode.SMSGGroupUninvite} {
		deliver(t, s, opcode.SMSGGroupList, unhex(t, duoHex))
		deliver(t, s, operation, nil)
		if after := state(t, s); !after.Group.Known || after.Group.InGroup {
			t.Fatal("server group destruction not applied", after)
		}
	}
}

func TestServiceAmbiguousDecisionFailureIsNeverRetried(t *testing.T) {
	recorder := &packetRecorder{write: func(context.Context) error { return status.Error(codes.Unavailable, "callback closed") }}
	s := NewService(recorder, "body-one", 2, "Arlen")
	deliver(t, s, opcode.SMSGGroupInvite, unhex(t, inviteHex))
	request := decision(state(t, s))
	if response, err := s.Accept(context.Background(), request); response != nil || status.Code(err) != codes.Unavailable {
		t.Fatal("callback failure misreported", response, err)
	}
	if after := state(t, s); after.ActionInFlight || after.PendingInvite.TransportWritten || after.PendingInvite.Decision != partyv1.InvitationDecision_ACCEPT_REQUESTED || after.Group.Known {
		t.Fatal("failed callback lost its reservation or invented membership", after)
	}
	if _, err := s.Accept(context.Background(), request); status.Code(err) != codes.FailedPrecondition || len(recorder.snapshot()) != 1 {
		t.Fatal("ambiguous decision retried", err)
	}
	// A genuinely new server invitation receives a new token.
	deliver(t, s, opcode.SMSGGroupInvite, unhex(t, inviteHex))
	if after := state(t, s); after.PendingInvite.Sequence != 2 || after.PendingInvite.Decision != partyv1.InvitationDecision_PENDING {
		t.Fatal("new invitation retained old decision", after)
	}
}

func TestServiceConcurrentDecisionAndPacketCallback(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	recorder := &packetRecorder{write: func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	s := NewService(recorder, "body-one", 2, "Arlen")
	deliver(t, s, opcode.SMSGGroupInvite, unhex(t, inviteHex))
	request := decision(state(t, s))
	completed := make(chan error, 1)
	go func() { _, err := s.Accept(context.Background(), request); completed <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("callback never started")
	}
	// State and packet callbacks must remain available while SendPacket is blocked.
	updated := make(chan struct{})
	go func() {
		deliver(t, s, opcode.SMSGGroupInvite, unhex(t, inviteHex))
		state(t, s)
		close(updated)
	}()
	select {
	case <-updated:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("network callback held the state mutex")
	}
	var workers sync.WaitGroup
	var rejected atomic.Int32
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, err := s.Accept(context.Background(), request); status.Code(err) == codes.FailedPrecondition {
				rejected.Add(1)
			}
		}()
	}
	workers.Wait()
	close(release)
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	if after := state(t, s); rejected.Load() != 16 || len(recorder.snapshot()) != 1 || after.ActionInFlight || after.PendingInvite.Sequence != 2 || after.PendingInvite.TransportWritten || after.PendingInvite.Decision != partyv1.InvitationDecision_PENDING {
		t.Fatal("duplicate action or old callback modified replacement invitation", after, rejected.Load())
	}
}

func TestServiceConcurrentStateSnapshots(t *testing.T) {
	s := NewService(nil, "body-one", 2, "Arlen")
	payload := unhex(t, duoHex)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 100 {
				deliver(t, s, opcode.SMSGGroupList, payload)
				if snapshot := state(t, s); len(snapshot.Group.Members) != 2 {
					t.Error("partial group snapshot", snapshot)
				}
			}
		}()
	}
	workers.Wait()
	if snapshot := state(t, s); snapshot.Revision != 800 {
		t.Fatal("concurrent packets lost state revisions", snapshot)
	}
}

type sessionRPC struct {
	modv1.UnimplementedSessionServer
	recorder *packetRecorder
}

func (s *sessionRPC) SendPacket(ctx context.Context, request *modv1.SendPacketRequest) (*emptypb.Empty, error) {
	return s.recorder.SendPacket(ctx, request)
}

func TestServiceGeneratedGRPCAndSessionCallback(t *testing.T) {
	recorder := &packetRecorder{}
	sessionConn := connectTestGRPC(t, func(server *grpc.Server) { modv1.RegisterSessionServer(server, &sessionRPC{recorder: recorder}) })
	s := NewService(modv1.NewSessionClient(sessionConn), "body-one", 2, "Arlen")
	partyConn := connectTestGRPC(t, func(server *grpc.Server) { partyv1.RegisterBuddyPartyServer(server, s) })
	client := partyv1.NewBuddyPartyClient(partyConn)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.OnPacket(ctx, &modv1.WorldPacket{Opcode: uint32(opcode.SMSGGroupInvite), Payload: unhex(t, inviteHex)}); err != nil {
		t.Fatal(err)
	}
	before, err := client.State(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Accept(ctx, decision(before))
	if err != nil || !response.TransportWritten || len(recorder.snapshot()) != 1 {
		t.Fatal("generated gRPC contract failed", response, err)
	}
	if _, err := client.Accept(ctx, decision(before)); status.Code(err) != codes.FailedPrecondition || len(recorder.snapshot()) != 1 {
		t.Fatal("gRPC duplicated a reserved decision", err)
	}
}

func connectTestGRPC(t *testing.T, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	register(server)
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	connection, err := grpc.NewClient("passthrough:///party-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxRetryRPCBufferSize(0)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	return connection
}

func TestModuleManifestAndDescriptor(t *testing.T) {
	// Isolate discovery from other source modules being developed concurrently.
	root := t.TempDir()
	module := filepath.Join(root, "buddy-party")
	if err := os.Mkdir(module, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"module.yaml", "module.pb", "compose.yaml"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(module, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := moddisc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := registry.Lookup("buddy-party")
	if !ok {
		t.Fatal("module not discovered")
	}
	for _, name := range []string{"state", "poll", "accept", "decline", "invite", "leave"} {
		if _, ok := definition.RPC(name); !ok {
			t.Error("missing public RPC alias", name)
		}
	}
	for _, operation := range []uint32{opcode.SMSGGroupInvite, opcode.SMSGGroupList, opcode.SMSGGroupDecline, opcode.SMSGGroupDestroyed, opcode.SMSGGroupUninvite, opcode.SMSGPartyCommandResult} {
		if _, ok := definition.Packet(uint16(operation)); !ok {
			t.Error("missing public packet handler", operation)
		}
	}
}
