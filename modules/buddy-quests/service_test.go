package buddyquests

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fakeSession struct {
	mu                 sync.Mutex
	world              *worldv1.SnapshotResponse
	actions            *actionsv1.State
	packets            []*modv1.SendPacketRequest
	acquired, released int
	lease              bool
	onSend             func(context.Context, *modv1.SendPacketRequest) error
	onAcquire          func()
	approaches         []*actionsv1.ApproachQuestRequest
	onApproach         func(context.Context, *actionsv1.ApproachQuestRequest) (*actionsv1.State, error)
}

func fixture() *fakeSession {
	w := &worldv1.SnapshotResponse{Self: &worldv1.PlayerState{Guid: 1, Present: true, MapKnown: true, AliveKnown: true, Alive: true}, SelfQuestLogKnown: true, SelfQuestLogRevision: 1,
		QuestGivers: []*worldv1.CreatureState{{Guid: npcGUID, Entry: 51, NpcFlags: 2, Present: true, MapKnown: true, PositionKnown: true, X: 1, AliveKnown: true, Alive: true}}}
	for i := range 25 {
		w.SelfQuestLog = append(w.SelfQuestLog, &worldv1.QuestLogEntry{Slot: uint32(i)})
	}
	return &fakeSession{world: w, actions: &actionsv1.State{Position: &actionsv1.ClientPosition{Known: true, MapKnown: true}}}
}
func (f *fakeSession) SendPacket(ctx context.Context, r *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.mu.Lock()
	f.packets = append(f.packets, proto.Clone(r).(*modv1.SendPacketRequest))
	fn := f.onSend
	f.mu.Unlock()
	if fn != nil {
		if err := fn(ctx, r); err != nil {
			return nil, err
		}
	}
	return &emptypb.Empty{}, nil
}
func (f *fakeSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, _ ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module == "buddy-actions" && r.Method == "approach_quest" {
		input := &actionsv1.ApproachQuestRequest{}
		if err := r.Request.UnmarshalTo(input); err != nil {
			return nil, err
		}
		f.mu.Lock()
		if !f.lease || input.Token != "fixture-token" || input.NpcGuid != npcGUID {
			f.mu.Unlock()
			return nil, status.Error(codes.FailedPrecondition, "wrong approach lease or NPC")
		}
		f.approaches = append(f.approaches, proto.Clone(input).(*actionsv1.ApproachQuestRequest))
		fn := f.onApproach
		f.mu.Unlock()
		if fn == nil {
			return nil, status.Error(codes.FailedPrecondition, "approach not configured")
		}
		result, err := fn(ctx, input)
		if err != nil {
			return nil, err
		}
		packed, err := anypb.New(result)
		return &modv1.InvokeModuleResponse{Result: packed}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var result proto.Message
	switch r.Module + "." + r.Method {
	case "buddy-world.snapshot":
		result = proto.Clone(f.world)
	case "buddy-actions.status":
		result = proto.Clone(f.actions)
	case "buddy-actions.acquire_interaction":
		input := &actionsv1.InteractionRequest{}
		if err := r.Request.UnmarshalTo(input); err != nil {
			return nil, err
		}
		if input.Owner != "quests" || input.LeaseMs != 15000 || f.lease {
			return nil, status.Error(codes.FailedPrecondition, "lease unavailable")
		}
		f.lease = true
		f.acquired++
		f.actions.InteractionOwner = "quests"
		if f.onAcquire != nil {
			f.onAcquire()
		}
		result = &actionsv1.InteractionLease{Token: "fixture-token", State: proto.Clone(f.actions).(*actionsv1.State)}
	case "buddy-actions.release_interaction":
		input := &actionsv1.InteractionRelease{}
		if err := r.Request.UnmarshalTo(input); err != nil {
			return nil, err
		}
		if input.Owner != "quests" || input.Token != "fixture-token" {
			return nil, status.Error(codes.InvalidArgument, "bad lease release")
		}
		f.lease = false
		f.released++
		f.actions.InteractionOwner = ""
		result = proto.Clone(f.actions)
	default:
		return nil, status.Error(codes.NotFound, "unknown module method")
	}
	packed, err := anypb.New(result)
	return &modv1.InvokeModuleResponse{Result: packed}, err
}
func (f *fakeSession) counters() (int, int, int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.packets), f.acquired, f.released, f.lease
}
func (f *fakeSession) confirm(id uint32) {
	f.mu.Lock()
	f.world.SelfQuestLog[0].QuestId = id
	f.world.SelfQuestLogRevision++
	f.mu.Unlock()
}
func packet(t *testing.T, s *Service, op uint32, p []byte) {
	t.Helper()
	if _, err := s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: op, Payload: p}); err != nil {
		t.Fatal("packet error would kill core", err)
	}
}
func snapshot(t *testing.T, s *Service) *questsv1.StateResponse {
	t.Helper()
	r, err := s.State(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func request(o *questsv1.Offer) *questsv1.QuestRequest {
	return &questsv1.QuestRequest{SessionId: "body-one", OfferRevision: o.Revision, NpcGuid: o.NpcGuid, QuestId: 7}
}
func offered(t *testing.T) (*Service, *fakeSession, *questsv1.QuestRequest) {
	t.Helper()
	f := fixture()
	s := NewService(f, "body-one", 1)
	f.onSend = func(_ context.Context, r *modv1.SendPacketRequest) error {
		if r.Opcode == opcode.CMSGQuestgiverHello {
			packet(t, s, opcode.SMSGQuestgiverQuestList, unhex(t, listHex))
		}
		return nil
	}
	result, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "body-one", NpcGuid: npcGUID})
	if err != nil || !result.TransportWritten || !result.ServerResponseReceived || result.State.Offer == nil {
		t.Fatal("offer failed", result, err)
	}
	return s, f, request(result.State.Offer)
}

func TestExplicitOfferAndDetailsUnderExclusiveLease(t *testing.T) {
	s, f, r := offered(t)
	if n, a, release, held := f.counters(); n != 2 || a != 1 || release != 1 || held {
		t.Fatal(n, a, release, held)
	}
	for _, p := range f.packets {
		if len(p.Payload) != 8 || binary.LittleEndian.Uint64(p.Payload) != npcGUID {
			t.Fatal("wrong NPC query wire", p)
		}
	}
	oldRevision := r.OfferRevision
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverQueryQuest {
			if len(p.Payload) != 13 {
				t.Error("details wire length")
			}
			packet(t, s, opcode.SMSGQuestgiverQuestDetails, unhex(t, detailsHex))
		}
		return nil
	}
	result, err := s.Details(context.Background(), r)
	if err != nil || !result.ServerResponseReceived || result.State.Offer.Source != "quest_details" || result.State.Offer.Revision <= oldRevision || result.State.Offer.Quests[0].Details != "Find the person." {
		t.Fatal(result, err)
	}
	if _, err := s.Accept(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("old list token accepted after details replacement", err)
	}
	if _, a, release, held := f.counters(); a != 2 || release != 2 || held {
		t.Fatal("stale token acquired a movement lease", a, release, held)
	}
}

func TestAcceptedOnlyAfterAuthoritativeQuestLogInsertion(t *testing.T) {
	s, f, r := offered(t)
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverAcceptQuest {
			if len(p.Payload) != 16 || binary.LittleEndian.Uint64(p.Payload) != npcGUID || binary.LittleEndian.Uint32(p.Payload[8:]) != 7 || binary.LittleEndian.Uint32(p.Payload[12:]) != 0 {
				t.Error("accept wire mismatch", p)
			}
			f.confirm(7)
		}
		return nil
	}
	result, err := s.Accept(context.Background(), r)
	if err != nil || !result.TransportWritten || !result.ServerResponseReceived || result.State.LastAcceptance.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG || result.State.LastAcceptance.BaselineLogRevision != 1 || result.State.LastAcceptance.ConfirmedLogRevision != 2 {
		t.Fatal(result, err)
	}
	if _, err := s.Accept(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("accept repeated", err)
	}
	if n, a, release, held := f.counters(); n != 3 || a != 2 || release != 2 || held {
		t.Fatal("decision retried or lease leaked", n, a, release, held)
	}
	result.State.QuestLog[0].QuestId = 999
	if snapshot(t, s).QuestLog[0].QuestId != 7 {
		t.Fatal("caller changed observed quest log")
	}
}

func TestFlaggedStarterOfferNeedsExplicitAcceptAndNewOwnLogProof(t *testing.T) {
	f := fixture()
	s := NewService(f, "body-one", 1)
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverHello {
			packet(t, s, opcode.SMSGGossipMessage, starterGossip(t))
		} else if p.Opcode == opcode.CMSGQuestgiverAcceptQuest {
			if len(p.Payload) != 16 || binary.LittleEndian.Uint64(p.Payload) != npcGUID || binary.LittleEndian.Uint32(p.Payload[8:]) != 783 || binary.LittleEndian.Uint32(p.Payload[12:]) != 0 {
				t.Error("wrong explicit starter acceptance", p)
			}
			f.confirm(783)
		}
		return nil
	}
	offered, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "body-one", NpcGuid: npcGUID})
	if err != nil || offered.State.Offer == nil || !offered.State.Offer.Quests[0].Available {
		t.Fatal(offered, err)
	}
	if n, _, _, _ := f.counters(); n != 2 || offered.State.LastAcceptance != nil || offered.State.QuestLog[0].QuestId != 0 {
		t.Fatal("auto-accept hint caused an unsolicited acceptance", offered)
	}
	result, err := s.Accept(context.Background(), &questsv1.QuestRequest{SessionId: "body-one", OfferRevision: offered.State.Offer.Revision, NpcGuid: npcGUID, QuestId: 783})
	if err != nil || result.State.LastAcceptance.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG || result.State.LastAcceptance.ConfirmedLogRevision <= result.State.LastAcceptance.BaselineLogRevision || result.State.QuestLog[0].QuestId != 783 {
		t.Fatal("explicit starter acceptance lacked new own-log confirmation", result, err)
	}
}

func TestStaleOrWrongSelectionCannotAcquireLeaseOrSendAccept(t *testing.T) {
	s, f, r := offered(t)
	for _, change := range []func(*questsv1.QuestRequest){
		func(r *questsv1.QuestRequest) { r.SessionId = "previous-body" }, func(r *questsv1.QuestRequest) { r.OfferRevision++ }, func(r *questsv1.QuestRequest) { r.NpcGuid++ }, func(r *questsv1.QuestRequest) { r.QuestId++ },
	} {
		bad := proto.Clone(r).(*questsv1.QuestRequest)
		change(bad)
		if _, err := s.Accept(context.Background(), bad); err == nil {
			t.Fatal("invalid selection accepted", bad)
		}
	}
	if n, a, release, held := f.counters(); n != 2 || a != 1 || release != 1 || held {
		t.Fatal("invalid selection caused side effects", n, a, release, held)
	}
}

func TestTransportAndGossipClosureAreNotQuestAcceptance(t *testing.T) {
	s, f, r := offered(t)
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverAcceptQuest {
			packet(t, s, opcode.SMSGGossipComplete, nil)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err := s.Accept(ctx, r)
	if err != nil || !result.TransportWritten || result.ServerResponseReceived || result.State.LastAcceptance.Status != questsv1.AcceptanceStatus_UNCONFIRMED_TIMEOUT {
		t.Fatal("invented acceptance", result, err)
	}
	if _, err := s.Accept(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("timeout decision retried", err)
	}
	f.confirm(7)
	if later := snapshot(t, s); later.LastAcceptance.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG {
		t.Fatal("late actual insertion not recognized", later)
	}
	if n, _, _, held := f.counters(); n != 3 || held {
		t.Fatal("timeout retried or lease leaked", n, held)
	}
}

func TestAmbiguousAcceptWriteConsumedAndNeverRetried(t *testing.T) {
	s, f, r := offered(t)
	f.onSend = func(context.Context, *modv1.SendPacketRequest) error {
		return status.Error(codes.Unavailable, "fixture write outcome unknown")
	}
	if response, err := s.Accept(context.Background(), r); response != nil || status.Code(err) != codes.Unavailable {
		t.Fatal(response, err)
	}
	if state := snapshot(t, s); state.LastAcceptance.Status != questsv1.AcceptanceStatus_WRITE_UNCONFIRMED || state.LastAcceptance.TransportWritten {
		t.Fatal("failed callback claimed success", state)
	}
	if _, err := s.Accept(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("ambiguous write retried", err)
	}
	if n, a, release, held := f.counters(); n != 3 || a != 2 || release != 2 || held {
		t.Fatal(n, a, release, held)
	}
}

func TestServerLogProofSurvivesAmbiguousWriteAcknowledgement(t *testing.T) {
	s, f, r := offered(t)
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		f.confirm(7)
		// Another polling caller observes the actual insertion while the write
		// callback is still outstanding. Its proof must not be overwritten.
		if observed := snapshot(t, s); observed.LastAcceptance.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG {
			t.Error("insertion not recognized", observed)
		}
		return status.Error(codes.Unavailable, "fixture acknowledgement lost")
	}
	if _, err := s.Accept(context.Background(), r); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	if observed := snapshot(t, s); observed.LastAcceptance.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG || observed.LastAcceptance.TransportWritten {
		t.Fatal("transport error overwrote server proof", observed)
	}
}

func TestServerFailureAndUnknownQuestLogRemainUnaccepted(t *testing.T) {
	for _, operation := range []uint32{opcode.SMSGQuestgiverQuestInvalid, opcode.SMSGQuestlogFull, opcode.SMSGQuestgiverQuestFailed} {
		t.Run(opcode.WorldName(operation), func(t *testing.T) {
			s, f, r := offered(t)
			f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
				if p.Opcode == opcode.CMSGQuestgiverAcceptQuest {
					var payload []byte
					switch operation {
					case opcode.SMSGQuestgiverQuestInvalid:
						payload = []byte{6, 0, 0, 0}
					case opcode.SMSGQuestgiverQuestFailed:
						payload = []byte{7, 0, 0, 0, 9, 0, 0, 0}
					}
					packet(t, s, operation, payload)
				}
				return nil
			}
			result, err := s.Accept(context.Background(), r)
			if err != nil || result.State.LastAcceptance.Status != questsv1.AcceptanceStatus_SERVER_REJECTED {
				t.Fatal(result, err)
			}
		})
	}
	s, f, r := offered(t)
	f.world.SelfQuestLogKnown = false
	if _, err := s.Accept(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("unknown log accepted", err)
	}
	if n, _, _, held := f.counters(); n != 2 || held {
		t.Fatal("unknown log emitted accept or leaked lease", n, held)
	}
}

type delayedSnapshot struct {
	*fakeSession
	mu                sync.Mutex
	delayed           bool
	captured, release chan struct{}
}

func (d *delayedSnapshot) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, options ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	d.mu.Lock()
	delay := r.Module == "buddy-world" && !d.delayed
	if delay {
		d.delayed = true
	}
	d.mu.Unlock()
	result, err := d.fakeSession.InvokeModule(ctx, r, options...)
	if delay {
		close(d.captured)
		select {
		case <-d.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return result, err
}

func TestOverlappingWorldReadsCannotRegressQuestLogBaseline(t *testing.T) {
	f := fixture()
	d := &delayedSnapshot{fakeSession: f, captured: make(chan struct{}), release: make(chan struct{})}
	s := NewService(d, "body-one", 1)
	finished := make(chan error, 1)
	go func() {
		_, err := s.State(context.Background(), &emptypb.Empty{})
		finished <- err
	}()
	<-d.captured
	f.confirm(7)
	current := snapshot(t, s)
	close(d.release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.state.QuestLogKnown || s.state.QuestLogRevision != current.QuestLogRevision || !s.inLogLocked(7) {
		t.Fatal("older completed read regressed acceptance baseline", s.state)
	}
}

func TestInteractionGatesAndReadOnlyDiscovery(t *testing.T) {
	for _, change := range []func(*fakeSession){
		func(f *fakeSession) { f.world.Self.Alive = false }, func(f *fakeSession) { f.world.QuestGivers[0].AliveKnown = false }, func(f *fakeSession) { f.world.QuestGivers[0].NpcFlags = 0 }, func(f *fakeSession) { f.world.QuestGivers[0].X = 5 },
		func(f *fakeSession) { f.world.QuestGivers[0].MapId = 1 }, func(f *fakeSession) { f.actions.Position.Known = false }, func(f *fakeSession) { f.actions.Moving = true }, func(f *fakeSession) { f.actions.MotionUnconfirmed = true },
		func(f *fakeSession) {
			f.world.QuestGivers[0].MovementFlags = 1
			f.world.QuestGivers[0].PositionAgeMs = 1501
		},
		func(f *fakeSession) { f.world.QuestGivers[0].MovementFlags = 0x08000000 },
	} {
		f := fixture()
		change(f)
		s := NewService(f, "body-one", 1)
		if result, err := s.Discover(context.Background(), &emptypb.Empty{}); err != nil || len(result.Nearby) != 1 || result.Nearby[0].CanInteract {
			t.Fatal("unsafe discover gate", result, err)
		}
		if _, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "body-one", NpcGuid: npcGUID}); err == nil {
			t.Fatal("unsafe NPC query accepted")
		}
		if n, _, _, held := f.counters(); n != 0 || held {
			t.Fatal("gate wrote gameplay or leaked acquired lease", n, held)
		}
	}
	f := fixture()
	s := NewService(f, "body-one", 1)
	result, err := s.Discover(context.Background(), &emptypb.Empty{})
	if err != nil || !result.Nearby[0].CanInteract || result.Nearby[0].Distance != 1 {
		t.Fatal(result, err)
	}
	if n, a, _, held := f.counters(); n != 0 || a != 0 || held {
		t.Fatal("discovery wrote gameplay", n, a, held)
	}
}

func TestQueryTimeoutAndUnsolicitedMalformedPacketsStayLocal(t *testing.T) {
	f := fixture()
	s := NewService(f, "body-one", 1)
	packet(t, s, opcode.SMSGQuestgiverQuestList, unhex(t, listHex))
	if snapshot(t, s).Offer != nil {
		t.Fatal("unsolicited quest offer was trusted")
	}
	for _, op := range []uint32{opcode.SMSGQuestgiverQuestList, opcode.SMSGGossipMessage, opcode.SMSGQuestgiverQuestDetails, opcode.SMSGQuestgiverStatus, opcode.SMSGQuestgiverQuestInvalid, opcode.SMSGQuestgiverQuestFailed, opcode.SMSGGossipComplete, opcode.SMSGQuestlogFull} {
		packet(t, s, op, []byte{255})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err := s.Offer(ctx, &questsv1.OfferRequest{SessionId: "body-one", NpcGuid: npcGUID})
	if err != nil || !result.TransportWritten || result.ServerResponseReceived || result.State.Offer != nil || result.State.MalformedPackets != 8 {
		t.Fatal(result, err)
	}
	if _, _, _, held := f.counters(); held {
		t.Fatal("timeout leaked lease")
	}
	packet(t, s, opcode.SMSGQuestgiverQuestList, unhex(t, listHex))
	if snapshot(t, s).Offer != nil {
		t.Fatal("late reply revived an expired query")
	}
}

func TestBusyQueryDoesNotBlockPacketOrStateCallbacks(t *testing.T) {
	f := fixture()
	s := NewService(f, "body-one", 1)
	entered, release := make(chan struct{}), make(chan struct{})
	f.onSend = func(ctx context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverHello {
			close(entered)
			select {
			case <-release:
				packet(t, s, opcode.SMSGQuestgiverQuestList, unhex(t, listHex))
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "body-one", NpcGuid: npcGUID})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("query never entered")
	}
	if _, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "body-one", NpcGuid: npcGUID}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("concurrent query accepted", err)
	}
	read := make(chan struct{})
	go func() { snapshot(t, s); packet(t, s, opcode.SMSGGossipComplete, nil); close(read) }()
	select {
	case <-read:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("callback held state mutex during network work")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 50 {
				snapshot(t, s)
				packet(t, s, opcode.SMSGQuestConfirmAccept, nil)
			}
		}()
	}
	workers.Wait()
	if snapshot(t, s).IgnoredPackets != 400 {
		t.Fatal("party share callback incorrectly processed")
	}
}

type sessionServer struct {
	modv1.UnimplementedSessionServer
	fake *fakeSession
}

func (s *sessionServer) SendPacket(ctx context.Context, r *modv1.SendPacketRequest) (*emptypb.Empty, error) {
	return s.fake.SendPacket(ctx, r)
}
func (s *sessionServer) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
	return s.fake.InvokeModule(ctx, r)
}
func connect(t *testing.T, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	register(server)
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	connection, err := grpc.NewClient("passthrough:///quests-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxRetryRPCBufferSize(0)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	return connection
}
func TestGeneratedGRPCAndSessionCallbacks(t *testing.T) {
	f := fixture()
	sessionConn := connect(t, func(server *grpc.Server) { modv1.RegisterSessionServer(server, &sessionServer{fake: f}) })
	s := NewService(modv1.NewSessionClient(sessionConn), "body-one", 1)
	f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
		if p.Opcode == opcode.CMSGQuestgiverHello {
			packet(t, s, opcode.SMSGQuestgiverQuestList, unhex(t, listHex))
		}
		return nil
	}
	connection := connect(t, func(server *grpc.Server) { questsv1.RegisterBuddyQuestsServer(server, s) })
	client := questsv1.NewBuddyQuestsClient(connection)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := client.Offer(ctx, &questsv1.OfferRequest{SessionId: "body-one", NpcGuid: npcGUID})
	if err != nil || result.State.Offer.Quests[0].QuestId != 7 {
		t.Fatal(result, err)
	}
}

func TestManifestDescriptorAndDependencies(t *testing.T) {
	root := t.TempDir()
	for _, module := range []string{"buddy-world", "buddy-party", "buddy-actions", "buddy-quests"} {
		dir := filepath.Join(root, module)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"module.yaml", "module.pb", "compose.yaml"} {
			data, err := os.ReadFile(filepath.Join("..", module, name))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	registry, err := moddisc.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	module, ok := registry.Lookup("buddy-quests")
	if !ok || !module.Requires("buddy-world") || !module.Requires("buddy-actions") {
		t.Fatal("missing module/dependencies")
	}
	for _, alias := range []string{"discover", "state", "offer", "details", "accept", "journal", "turn_in", "claim_reward"} {
		if _, ok := module.RPC(alias); !ok {
			t.Error("missing alias", alias)
		}
	}
	state, _ := module.RPC("state")
	log := state.Descriptor().Output().Fields().ByName("quest_log")
	if log == nil || log.Message().Fields().ByName("complete") == nil || log.Message().Fields().ByName("objective_counts") == nil {
		t.Fatal("descriptor imports stale world quest progress schema")
	}
}
