package buddymail

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	mailv1 "github.com/agent-wow/agent-wow/modules/buddy-mail/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

type sessionAPI interface {
	SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error)
	InvokeModule(context.Context, *modv1.InvokeModuleRequest, ...grpc.CallOption) (*modv1.InvokeModuleResponse, error)
}

type Service struct {
	mailv1.UnimplementedBuddyMailServer
	mu               sync.Mutex
	session          sessionAPI
	self             uint64
	now              func() time.Time
	timeout          time.Duration
	state            *mailv1.State
	closed           bool
	listAt, nextList time.Time
	wait             chan struct{}
	pendingList      bool
	bagBaselines     map[string]*worldv1.SnapshotResponse
}

func NewService(session sessionAPI, sessionID string, self uint64) *Service {
	return &Service{session: session, self: self, now: time.Now, timeout: 6 * time.Second, state: &mailv1.State{SessionId: sessionID, Phase: "idle"}}
}

func (s *Service) snapshotLocked() *mailv1.State {
	out := proto.Clone(s.state).(*mailv1.State)
	if out.ListKnown {
		age := s.now().Sub(s.listAt)
		if age < 0 {
			age = 0
		}
		out.ListAgeMs = uint64(age.Milliseconds())
		out.ListCurrent = age < 15*time.Second
	}
	return out
}
func (s *Service) signalLocked() {
	if s.wait != nil {
		close(s.wait)
		s.wait = nil
	}
}
func (s *Service) reserve(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.state.OperationInFlight {
		return status.Error(codes.FailedPrecondition, "mail operation unavailable or busy")
	}
	s.state.OperationInFlight = true
	return nil
}
func (s *Service) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.OperationInFlight = false
	s.pendingList = false
	s.signalLocked()
}
func (s *Service) invoke(ctx context.Context, module, method string, in, out proto.Message) error {
	if s.session == nil {
		return errors.New("session unavailable")
	}
	request, err := anypb.New(in)
	if err != nil {
		return err
	}
	r, err := s.session.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: module, Method: method, Request: request})
	if err != nil {
		return err
	}
	if r == nil || r.Result == nil {
		return errors.New("missing dependency result")
	}
	return r.Result.UnmarshalTo(out)
}
func (s *Service) world(ctx context.Context) (*worldv1.SnapshotResponse, error) {
	w := &worldv1.SnapshotResponse{}
	if err := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, w); err != nil {
		return nil, err
	}
	if w.Self == nil || w.Self.Guid != s.self {
		return nil, status.Error(codes.FailedPrecondition, "wrong observed character")
	}
	return w, nil
}

func mailboxGate(w *worldv1.SnapshotResponse, a *actionsv1.State, requested uint64) (uint64, error) {
	bad := status.Error(codes.FailedPrecondition, "known alive out-of-combat character and observed mailbox within5yd required")
	if w == nil || w.Self == nil || a == nil || a.Position == nil {
		return 0, bad
	}
	p, pos := w.Self, a.Position
	if !p.Present || !p.AliveKnown || !p.Alive || !p.UnitFlagsKnown || !p.MapKnown || p.MapId != pos.MapId || p.UnitFlags&(1|4|0x40000|0x80000|0x400000|0x800000) != 0 || !pos.Known || !pos.MapKnown || a.MotionUnconfirmed || a.CastPending || a.AutoAttackUnconfirmed || a.AutoAttacking {
		return 0, bad
	}
	finite := func(v float32) bool {
		return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) && math.Abs(float64(v)) < 40000
	}
	if !finite(pos.X) || !finite(pos.Y) || !finite(pos.Z) || len(w.Mailboxes) > 32 {
		return 0, bad
	}
	best, distance := uint64(0), math.Inf(1)
	for _, m := range w.Mailboxes {
		if m == nil || !m.Present || !m.TypeKnown || m.GameObjectType != 19 || !m.MapKnown || m.MapId != pos.MapId || !m.PositionKnown || !finite(m.X) || !finite(m.Y) || !finite(m.Z) || m.Guid>>48 != 0xf110 || m.Guid&0xffffff == 0 || m.Entry == 0 || uint32(m.Guid>>24&0xffffff) != m.Entry || requested != 0 && m.Guid != requested {
			continue
		}
		dx, dy, dz := float64(m.X-pos.X), float64(m.Y-pos.Y), float64(m.Z-pos.Z)
		d := dx*dx + dy*dy + dz*dz
		if d <= 25 && (d < distance || d == distance && m.Guid < best) {
			best, distance = m.Guid, d
		}
	}
	if best == 0 {
		return 0, bad
	}
	return best, nil
}

func (s *Service) gate(ctx context.Context, mailbox uint64) (*worldv1.SnapshotResponse, uint64, error) {
	w, err := s.world(ctx)
	if err != nil {
		return nil, 0, err
	}
	a := &actionsv1.State{}
	if err = s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, a); err != nil {
		return nil, 0, err
	}
	guid, err := mailboxGate(w, a, mailbox)
	return w, guid, err
}
func (s *Service) lease(ctx context.Context) (string, error) {
	r := &actionsv1.InteractionLease{}
	if err := s.invoke(ctx, "buddy-actions", "acquire_interaction", &actionsv1.InteractionRequest{Owner: "mail", LeaseMs: 15000}, r); err != nil {
		return "", err
	}
	if r.Token == "" || len(r.Token) > 128 || r.State == nil || r.State.InteractionOwner != "mail" {
		return "", status.Error(codes.FailedPrecondition, "mail lease unconfirmed")
	}
	if r.State.Moving || r.State.MotionUnconfirmed || r.State.CastPending || r.State.AutoAttacking || r.State.AutoAttackUnconfirmed {
		s.release(r.Token)
		return "", status.Error(codes.FailedPrecondition, "mail interaction stop unconfirmed")
	}
	return r.Token, nil
}
func (s *Service) release(token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.invoke(ctx, "buddy-actions", "release_interaction", &actionsv1.InteractionRelease{Owner: "mail", Token: token}, &actionsv1.State{})
}
func (s *Service) write(ctx context.Context, op uint32, p []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return errors.New("session closing")
	}
	if s.session == nil {
		return errors.New("session unavailable")
	}
	_, err := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: op, Payload: p})
	if err == nil {
		s.mu.Lock()
		s.state.TransportWrites++
		s.mu.Unlock()
	}
	return err
}

func (s *Service) List(ctx context.Context, r *mailv1.ListRequest) (*mailv1.State, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "mail list request required")
	}
	call, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.reserve(call); err != nil {
		return nil, err
	}
	defer s.finish()
	s.mu.Lock()
	limited := s.now().Before(s.nextList)
	s.mu.Unlock()
	if limited {
		return nil, status.Error(codes.ResourceExhausted, "mail list request throttled")
	}
	_, guid, err := s.gate(call, r.MailboxGuid)
	if err != nil {
		return nil, err
	}
	token, err := s.lease(call)
	if err != nil {
		return nil, err
	}
	defer s.release(token)
	if _, _, err = s.gate(call, guid); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "session closing")
	}
	s.state.ListKnown = false
	s.state.ListCurrent = false
	s.state.Stipends = nil
	s.state.BagMails = nil
	s.state.MailboxGuid = guid
	s.state.Phase = "waiting_for_server_mail_list"
	s.pendingList = true
	s.wait = make(chan struct{})
	wait := s.wait
	s.nextList = s.now().Add(time.Second)
	s.mu.Unlock()
	if err = s.write(call, opcode.CMSGGetMailList, mailboxPacket(guid)); err != nil {
		s.mu.Lock()
		s.pendingList = false
		s.state.Phase = "mail_list_write_unconfirmed"
		s.mu.Unlock()
	} else {
		select {
		case <-wait:
		case <-call.Done():
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.state.ListKnown && s.state.Phase == "waiting_for_server_mail_list" {
		s.state.Phase = "mail_list_unconfirmed"
	}
	out := s.snapshotLocked()
	out.OperationInFlight = false
	return out, nil
}

func (s *Service) applyWalletLocked(w *worldv1.SnapshotResponse) {
	r := s.state.LastClaim
	if r == nil || r.Status == mailv1.ClaimStatus_CLAIM_CONFIRMED || !w.SelfCopperKnown || w.SelfCopperRevision < r.ObservedCopperRevision {
		return
	}
	r.ObservedCopper = w.SelfCopper
	r.ObservedCopperRevision = w.SelfCopperRevision
	r.WalletIncreaseObserved = w.SelfCopperRevision > r.BaselineCopperRevision && uint64(w.SelfCopper) >= uint64(r.BaselineCopper)+uint64(stipendCopper)
	if r.ServerAcknowledged && r.WalletIncreaseObserved && r.Status != mailv1.ClaimStatus_CLAIM_SERVER_REJECTED {
		r.Status = mailv1.ClaimStatus_CLAIM_CONFIRMED
		r.ConfirmedCopper = stipendCopper
		s.state.Phase = "stipend_confirmed_by_server_and_wallet"
	}
}
func (s *Service) ClaimStipend(ctx context.Context, r *mailv1.ClaimRequest) (*mailv1.State, error) {
	if r == nil || r.MailId == 0 {
		return nil, status.Error(codes.InvalidArgument, "exact stipend mail required")
	}
	call, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.reserve(call); err != nil {
		return nil, err
	}
	defer s.finish()
	validate := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed || s.state.ClaimReserved || r.SessionId != s.state.SessionId || r.ListRevision != s.state.ListRevision || r.MailboxGuid == 0 || r.MailboxGuid != s.state.MailboxGuid || !s.state.ListKnown || s.now().Sub(s.listAt) >= 15*time.Second {
			return status.Error(codes.FailedPrecondition, "stipend list changed, expired or claim was already reserved")
		}
		for _, m := range s.state.Stipends {
			if m.MailId == r.MailId && m.Copper == stipendCopper {
				return nil
			}
		}
		return status.Error(codes.FailedPrecondition, "mail is not the exact console Training stipend")
	}
	if err := validate(); err != nil {
		return nil, err
	}
	if _, _, err := s.gate(call, r.MailboxGuid); err != nil {
		return nil, err
	}
	token, err := s.lease(call)
	if err != nil {
		return nil, err
	}
	defer s.release(token)
	w, _, err := s.gate(call, r.MailboxGuid)
	if err != nil {
		return nil, err
	}
	if !w.SelfCopperKnown || w.SelfCopperRevision == 0 || uint64(w.SelfCopper)+uint64(stipendCopper) > math.MaxUint32 {
		return nil, status.Error(codes.FailedPrecondition, "known wallet and stipend capacity required")
	}
	if err = validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.state.ClaimReserved = true
	s.state.LastClaim = &mailv1.ClaimReceipt{MailId: r.MailId, Status: mailv1.ClaimStatus_CLAIM_REQUESTED, BaselineCopper: w.SelfCopper, BaselineCopperRevision: w.SelfCopperRevision, ObservedCopper: w.SelfCopper, ObservedCopperRevision: w.SelfCopperRevision}
	s.state.Phase = "waiting_for_stipend_server_and_wallet"
	s.mu.Unlock()
	err = s.write(call, opcode.CMSGMailTakeMoney, takeMoneyPacket(r.MailboxGuid, r.MailId))
	s.mu.Lock()
	if err != nil {
		if s.state.LastClaim.Status != mailv1.ClaimStatus_CLAIM_CONFIRMED && s.state.LastClaim.Status != mailv1.ClaimStatus_CLAIM_SERVER_REJECTED {
			s.state.LastClaim.Status = mailv1.ClaimStatus_CLAIM_WRITE_UNCONFIRMED
			s.state.Phase = "stipend_write_unconfirmed_no_retry"
		}
	} else {
		s.state.LastClaim.TransportWritten = true
	}
	s.mu.Unlock()
	if err == nil {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			if observed, e := s.world(call); e == nil {
				s.mu.Lock()
				s.applyWalletLocked(observed)
				s.mu.Unlock()
			}
			s.mu.Lock()
			done := s.closed || s.state.LastClaim.Status == mailv1.ClaimStatus_CLAIM_CONFIRMED || s.state.LastClaim.Status == mailv1.ClaimStatus_CLAIM_SERVER_REJECTED
			s.mu.Unlock()
			if done {
				break
			}
			select {
			case <-call.Done():
				goto complete
			case <-ticker.C:
			}
		}
	}
complete:
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.LastClaim.Status == mailv1.ClaimStatus_CLAIM_REQUESTED {
		s.state.LastClaim.Status = mailv1.ClaimStatus_CLAIM_UNCONFIRMED
		s.state.Phase = "stipend_unconfirmed_no_retry"
	}
	out := s.snapshotLocked()
	out.OperationInFlight = false
	return out, nil
}

func (s *Service) GetState(ctx context.Context, _ *emptypb.Empty) (*mailv1.State, error) {
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if w, err := s.world(call); err == nil {
		s.mu.Lock()
		s.applyWalletLocked(w)
		s.applyBagInventoryLocked(w)
		s.mu.Unlock()
	}
	s.mu.Lock()
	needBagReleaseObservation := false
	for _, r := range s.state.BagClaims {
		if !r.LeaseReleased {
			needBagReleaseObservation = true
			break
		}
	}
	s.mu.Unlock()
	if needBagReleaseObservation {
		s.observeBagLeaseRelease(call)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked(), nil
}
func (s *Service) OnPacket(_ context.Context, p *modv1.WorldPacket) (*emptypb.Empty, error) {
	if p == nil {
		return &emptypb.Empty{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return &emptypb.Empty{}, nil
	}
	switch p.Opcode {
	case opcode.SMSGMailListResult:
		entries, err := decodeMailList(p.Payload)
		if err != nil {
			s.state.MalformedPackets++
			break
		}
		if !s.pendingList {
			break
		}
		s.pendingList = false
		s.state.ListKnown = true
		s.state.ListRevision++
		s.state.ListedMailCount = uint32(len(entries))
		s.state.Stipends = nil
		s.state.BagMails = nil
		for _, entry := range entries {
			if entry.stipend() {
				s.state.Stipends = append(s.state.Stipends, &mailv1.StipendMail{MailId: entry.id, Copper: entry.money})
			}
			if entry.partyBags() {
				m := &mailv1.BagMail{MailId: entry.id}
				for _, a := range entry.attachments {
					m.Attachments = append(m.Attachments, &mailv1.BagAttachment{AttachmentIndex: uint32(a.index), ItemGuidLow: a.itemLow, Entry: a.entry, Count: a.count})
				}
				s.state.BagMails = append(s.state.BagMails, m)
			}
		}
		s.listAt = s.now()
		s.state.Phase = "server_mail_list_observed"
		s.signalLocked()
	case opcode.SMSGSendMailResult:
		r, err := decodeMailResult(p.Payload)
		if err != nil {
			s.state.MalformedPackets++
			break
		}
		s.applyBagResultLocked(r)
		claim := s.state.LastClaim
		if claim == nil || claim.MailId != r.id || r.action != 1 || claim.Status == mailv1.ClaimStatus_CLAIM_SERVER_REJECTED || claim.Status == mailv1.ClaimStatus_CLAIM_CONFIRMED {
			break
		}
		claim.ServerError = r.result
		claim.EquipmentError = r.equip
		if r.result == 0 {
			claim.ServerAcknowledged = true
		} else {
			claim.Status = mailv1.ClaimStatus_CLAIM_SERVER_REJECTED
			s.state.Phase = "stipend_rejected_by_server"
		}
	case opcode.SMSGNewWorld:
		s.state.ListKnown = false
		s.state.Stipends = nil
		s.state.BagMails = nil
		s.state.MailboxGuid = 0
		s.pendingList = false
		s.signalLocked()
	}
	return &emptypb.Empty{}, nil
}
func (s *Service) BeforeLogout(_ context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.state.ListKnown = false
	s.state.BagMails = nil
	s.pendingList = false
	s.state.Phase = "session_closing"
	s.signalLocked()
	return &emptypb.Empty{}, nil
}
