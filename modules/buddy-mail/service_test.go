package buddymail

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	mailv1 "github.com/agent-wow/agent-wow/modules/buddy-mail/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

const testMailbox uint64 = 0xf110022b0e000001

type fakeSession struct {
	mu                sync.Mutex
	s                 *Service
	w                 *worldv1.SnapshotResponse
	a                 *actionsv1.State
	sends             []*modv1.SendPacketRequest
	releases          int
	list              []byte
	ack               bool
	ackID, ackError   uint32
	walletDelta       uint32
	writeError        bool
	afterAcquire      func()
	beforeWriteReturn func()
}

func (f *fakeSession) InvokeModule(_ context.Context, r *modv1.InvokeModuleRequest, _ ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	f.mu.Lock()
	var result proto.Message
	var hook func()
	switch r.Module + "." + r.Method {
	case "buddy-world.snapshot":
		result = proto.Clone(f.w)
	case "buddy-actions.status":
		result = proto.Clone(f.a)
	case "buddy-actions.acquire_interaction":
		var req actionsv1.InteractionRequest
		_ = r.Request.UnmarshalTo(&req)
		if req.Owner != "mail" {
			f.mu.Unlock()
			return nil, errors.New("wrong lease owner")
		}
		result = &actionsv1.InteractionLease{Token: "mail-token", State: &actionsv1.State{InteractionOwner: "mail"}}
		hook = f.afterAcquire
	case "buddy-actions.release_interaction":
		var req actionsv1.InteractionRelease
		_ = r.Request.UnmarshalTo(&req)
		if req.Owner != "mail" || req.Token != "mail-token" {
			f.mu.Unlock()
			return nil, errors.New("wrong lease release")
		}
		f.releases++
		result = &actionsv1.State{}
	default:
		f.mu.Unlock()
		return nil, errors.New("unexpected dependency")
	}
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	v, err := anypb.New(result)
	return &modv1.InvokeModuleResponse{Result: v}, err
}
func (f *fakeSession) SendPacket(_ context.Context, r *modv1.SendPacketRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.mu.Lock()
	f.sends = append(f.sends, proto.Clone(r).(*modv1.SendPacketRequest))
	var event *modv1.WorldPacket
	if r.Opcode == opcode.CMSGGetMailList && f.list != nil {
		event = &modv1.WorldPacket{Opcode: opcode.SMSGMailListResult, Payload: append([]byte{}, f.list...)}
	}
	if r.Opcode == opcode.CMSGMailTakeMoney {
		if f.walletDelta > 0 {
			f.w.SelfCopper += f.walletDelta
			f.w.SelfCopperRevision++
		}
		if f.ack {
			p := make([]byte, 12)
			binary.LittleEndian.PutUint32(p, f.ackID)
			binary.LittleEndian.PutUint32(p[4:], 1)
			binary.LittleEndian.PutUint32(p[8:], f.ackError)
			if f.ackError == 1 {
				p = append(p, 0, 0, 0, 0)
			}
			event = &modv1.WorldPacket{Opcode: opcode.SMSGSendMailResult, Payload: p}
		}
	}
	fail := f.writeError
	f.mu.Unlock()
	if event != nil {
		_, _ = f.s.OnPacket(context.Background(), event)
	}
	if f.beforeWriteReturn != nil {
		f.beforeWriteReturn()
	}
	if fail {
		return nil, errors.New("ambiguous write")
	}
	return &emptypb.Empty{}, nil
}
func rig(t *testing.T) (*Service, *fakeSession) {
	t.Helper()
	f := &fakeSession{ack: true, ackID: 77, walletDelta: 10000, list: goldenList(t), w: &worldv1.SnapshotResponse{
		Self:            &worldv1.PlayerState{Guid: 1, Present: true, AliveKnown: true, Alive: true, UnitFlagsKnown: true, MapKnown: true},
		SelfCopperKnown: true, SelfCopper: 500, SelfCopperRevision: 1,
		Mailboxes: []*worldv1.GameObjectState{{Guid: testMailbox, Entry: 142094, Present: true, MapKnown: true, PositionKnown: true, X: 2, TypeKnown: true, GameObjectType: 19}},
	}, a: &actionsv1.State{Position: &actionsv1.ClientPosition{Known: true, MapKnown: true}}}
	s := NewService(f, "mail-session", 1)
	s.timeout = 75 * time.Millisecond
	f.s = s
	return s, f
}
func listing(t *testing.T, s *Service) *mailv1.ClaimRequest {
	t.Helper()
	v, err := s.List(context.Background(), &mailv1.ListRequest{})
	if err != nil || !v.ListKnown || len(v.Stipends) != 1 {
		t.Fatalf("list=%+v err=%v", v, err)
	}
	return &mailv1.ClaimRequest{SessionId: v.SessionId, ListRevision: v.ListRevision, MailboxGuid: v.MailboxGuid, MailId: v.Stipends[0].MailId}
}
func TestStipendNeedsServerAckAndActualWalletRevision(t *testing.T) {
	for _, test := range []struct {
		name  string
		ack   bool
		delta uint32
		want  mailv1.ClaimStatus
	}{{"both", true, 10000, mailv1.ClaimStatus_CLAIM_CONFIRMED}, {"ack_only", true, 0, mailv1.ClaimStatus_CLAIM_UNCONFIRMED}, {"wallet_only", false, 10000, mailv1.ClaimStatus_CLAIM_UNCONFIRMED}, {"partial_money", true, 9999, mailv1.ClaimStatus_CLAIM_UNCONFIRMED}} {
		t.Run(test.name, func(t *testing.T) {
			s, f := rig(t)
			req := listing(t, s)
			f.ack, f.walletDelta = test.ack, test.delta
			v, err := s.ClaimStipend(context.Background(), req)
			if err != nil || v.LastClaim.Status != test.want {
				t.Fatalf("v=%+v err=%v", v, err)
			}
			if (v.LastClaim.ConfirmedCopper == 10000) != (test.want == mailv1.ClaimStatus_CLAIM_CONFIRMED) {
				t.Fatal("optimistic claim")
			}
			if f.releases != 2 {
				t.Fatal("lease leaked")
			}
		})
	}
}
func TestClaimBindingExpirationAndOneShot(t *testing.T) {
	for _, change := range []func(*mailv1.ClaimRequest){func(r *mailv1.ClaimRequest) { r.SessionId = "other" }, func(r *mailv1.ClaimRequest) { r.ListRevision++ }, func(r *mailv1.ClaimRequest) { r.MailId++ }, func(r *mailv1.ClaimRequest) { r.MailboxGuid++ }} {
		s, f := rig(t)
		req := listing(t, s)
		change(req)
		if _, err := s.ClaimStipend(context.Background(), req); err == nil {
			t.Fatal("bad binding accepted")
		}
		if len(f.sends) != 1 {
			t.Fatal("bad binding wrote")
		}
	}
	s, f := rig(t)
	req := listing(t, s)
	s.now = func() time.Time { return s.listAt.Add(16 * time.Second) }
	if _, err := s.ClaimStipend(context.Background(), req); err == nil {
		t.Fatal("expired list claimed")
	}
	if len(f.sends) != 1 {
		t.Fatal("expired write")
	}
	s, f = rig(t)
	req = listing(t, s)
	if _, err := s.ClaimStipend(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimStipend(context.Background(), req); err == nil {
		t.Fatal("replayed claim")
	}
	if len(f.sends) != 2 {
		t.Fatal("claim repeated")
	}
}
func TestNoRetryAfterAmbiguousWriteOrWrongAck(t *testing.T) {
	for _, mode := range []string{"write", "wrong_ack", "server_reject"} {
		t.Run(mode, func(t *testing.T) {
			s, f := rig(t)
			req := listing(t, s)
			switch mode {
			case "write":
				f.writeError = true
				f.ack = false
				f.walletDelta = 0
			case "wrong_ack":
				f.ackID = 78
			case "server_reject":
				f.ackError = 6
				f.walletDelta = 0
			}
			v, err := s.ClaimStipend(context.Background(), req)
			if err != nil || v.LastClaim.Status == mailv1.ClaimStatus_CLAIM_CONFIRMED {
				t.Fatalf("v=%v err=%v", v, err)
			}
			if _, err = s.ClaimStipend(context.Background(), req); err == nil {
				t.Fatal("ambiguous replay")
			}
			if len(f.sends) != 2 || f.releases != 2 {
				t.Fatal("replay or lease leak")
			}
		})
	}
}
func TestMailboxMustBeActuallyObservedNearbyAndSafe(t *testing.T) {
	for name, change := range map[string]func(*fakeSession){"far": func(f *fakeSession) { f.w.Mailboxes[0].X = 5.01 }, "wrong_type": func(f *fakeSession) { f.w.Mailboxes[0].GameObjectType = 3 }, "unknown_type": func(f *fakeSession) { f.w.Mailboxes[0].TypeKnown = false }, "absent": func(f *fakeSession) { f.w.Mailboxes[0].Present = false }, "map": func(f *fakeSession) { f.w.Mailboxes[0].MapId = 1 }, "combat": func(f *fakeSession) { f.w.Self.UnitFlags = 0x80000 }, "wrong_self": func(f *fakeSession) { f.w.Self.Guid = 4 }, "stop_uncertain": func(f *fakeSession) { f.a.MotionUnconfirmed = true }} {
		t.Run(name, func(t *testing.T) {
			s, f := rig(t)
			change(f)
			if _, err := s.List(context.Background(), &mailv1.ListRequest{}); err == nil {
				t.Fatal("unsafe mailbox accepted")
			}
			if len(f.sends) != 0 {
				t.Fatal("unsafe packet")
			}
		})
	}
}
func TestPostAcquireRevalidationAndCleanup(t *testing.T) {
	s, f := rig(t)
	f.afterAcquire = func() { f.mu.Lock(); f.w.Mailboxes[0].X = 8; f.mu.Unlock() }
	if _, err := s.List(context.Background(), &mailv1.ListRequest{}); err == nil {
		t.Fatal("changed range accepted")
	}
	if len(f.sends) != 0 || f.releases != 1 {
		t.Fatal("packet or lease leak")
	}
	s, f = rig(t)
	req := listing(t, s)
	f.afterAcquire = func() { f.mu.Lock(); f.w.SelfCopperKnown = false; f.mu.Unlock() }
	if _, err := s.ClaimStipend(context.Background(), req); err == nil {
		t.Fatal("unknown wallet accepted")
	}
	if len(f.sends) != 1 || f.releases != 2 {
		t.Fatal("claim or leaked lease")
	}
}
func TestConfirmedReceiptRemainsHistoricalAfterSpendingOrOlderSnapshot(t *testing.T) {
	s, f := rig(t)
	req := listing(t, s)
	v, err := s.ClaimStipend(context.Background(), req)
	if err != nil || v.LastClaim.Status != mailv1.ClaimStatus_CLAIM_CONFIRMED {
		t.Fatal(err)
	}
	f.w.SelfCopper = 100
	f.w.SelfCopperRevision = 3
	v, err = s.GetState(context.Background(), &emptypb.Empty{})
	if err != nil || !v.LastClaim.WalletIncreaseObserved || v.LastClaim.ObservedCopper != 10500 {
		t.Fatal("historical proof lost")
	}
}
func TestUnsolicitedListsMalformedPacketsAndTransferCannotAuthorizeClaims(t *testing.T) {
	s, f := rig(t)
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGMailListResult, Payload: f.list})
	v, _ := s.GetState(context.Background(), &emptypb.Empty{})
	if v.ListKnown {
		t.Fatal("unsolicited list became scoped")
	}
	req := listing(t, s)
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGMailListResult, Payload: []byte{1}})
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGNewWorld})
	if _, err := s.ClaimStipend(context.Background(), req); err == nil {
		t.Fatal("transfer left valid list")
	}
	if s.state.MalformedPackets != 1 {
		t.Fatal("malformed not local")
	}
}
func TestConcurrentStateAndPacketCallbacks(t *testing.T) {
	s, _ := rig(t)
	var workers sync.WaitGroup
	for range 20 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 20 {
				_, _ = s.GetState(context.Background(), &emptypb.Empty{})
				_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSendMailResult, Payload: []byte{0}})
			}
		}()
	}
	workers.Wait()
	if s.state.MalformedPackets != 400 {
		t.Fatal(s.state.MalformedPackets)
	}
	_, _ = s.BeforeLogout(context.Background(), &emptypb.Empty{})
	if _, err := s.List(context.Background(), &mailv1.ListRequest{}); err == nil {
		t.Fatal("closed module wrote")
	}
}

func TestCancellationAfterAcquireReleasesTokenWithoutGameplayWrite(t *testing.T) {
	s, f := rig(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.afterAcquire = cancel
	_, _ = s.List(ctx, &mailv1.ListRequest{})
	if len(f.sends) != 0 || f.releases != 1 {
		t.Fatal("canceled listing wrote or leaked lease")
	}
	s, f = rig(t)
	req := listing(t, s)
	ctx, cancel = context.WithCancel(context.Background())
	f.afterAcquire = cancel
	_, _ = s.ClaimStipend(ctx, req)
	if len(f.sends) != 1 || f.releases != 2 {
		t.Fatal("canceled claim wrote or leaked lease")
	}
}

func TestLateAckAndWalletCanConfirmButOlderSnapshotsCannotEraseProof(t *testing.T) {
	s, f := rig(t)
	req := listing(t, s)
	f.ack = false
	f.walletDelta = 0
	v, err := s.ClaimStipend(context.Background(), req)
	if err != nil || v.LastClaim.Status != mailv1.ClaimStatus_CLAIM_UNCONFIRMED {
		t.Fatal("missing proof was confirmed")
	}
	f.mu.Lock()
	f.w.SelfCopper = 10500
	f.w.SelfCopperRevision = 2
	f.mu.Unlock()
	p := make([]byte, 12)
	binary.LittleEndian.PutUint32(p, 77)
	binary.LittleEndian.PutUint32(p[4:], 1)
	_, _ = s.OnPacket(context.Background(), &modv1.WorldPacket{Opcode: opcode.SMSGSendMailResult, Payload: p})
	v, err = s.GetState(context.Background(), &emptypb.Empty{})
	if err != nil || v.LastClaim.Status != mailv1.ClaimStatus_CLAIM_CONFIRMED || v.LastClaim.ConfirmedCopper != 10000 {
		t.Fatal("late real proof not reconciled")
	}
	f.mu.Lock()
	f.w.SelfCopper = 500
	f.w.SelfCopperRevision = 1
	f.mu.Unlock()
	v, _ = s.GetState(context.Background(), &emptypb.Empty{})
	if !v.LastClaim.WalletIncreaseObserved || v.LastClaim.ObservedCopperRevision != 2 {
		t.Fatal("older wallet undid proof")
	}
}

func TestAmbiguousTransportCannotOverwriteAlreadyObservedServerOutcome(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		s, f := rig(t)
		req := listing(t, s)
		f.writeError = true
		want := mailv1.ClaimStatus_CLAIM_CONFIRMED
		if rejected {
			f.ackError = 6
			f.walletDelta = 0
			want = mailv1.ClaimStatus_CLAIM_SERVER_REJECTED
		}
		f.beforeWriteReturn = func() { _, _ = s.GetState(context.Background(), &emptypb.Empty{}) }
		v, err := s.ClaimStipend(context.Background(), req)
		if err != nil || v.LastClaim.Status != want {
			t.Fatalf("overwritten server outcome:%v %v", v, err)
		}
	}
}
