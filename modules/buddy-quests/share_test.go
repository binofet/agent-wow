package buddyquests

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	questsv1 "github.com/agent-wow/agent-wow/modules/buddy-quests/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func sharedDetails(t *testing.T) []byte {
	t.Helper()
	p := unhex(t, detailsHex)
	binary.LittleEndian.PutUint64(p, 1)
	binary.LittleEndian.PutUint64(p[8:], 3)
	r := reader{data: p[20:]}
	r.text(512)
	r.text(16384)
	r.text(8192)
	r.u8()
	at := len(p) - len(r.data)
	binary.LittleEndian.PutUint32(p[at:], 8)
	return p
}
func sharedConfirm(id uint32, sharer uint64) []byte {
	p := binary.LittleEndian.AppendUint32(nil, id)
	p = append(p, []byte("Shared title\x00")...)
	return binary.LittleEndian.AppendUint64(p, sharer)
}

type sharedSession struct {
	*fakeSession
	pmu     sync.Mutex
	party   *partyv1.StateResponse
	reads   int
	onParty func(int)
}

func (f *sharedSession) InvokeModule(ctx context.Context, r *modv1.InvokeModuleRequest, opts ...grpc.CallOption) (*modv1.InvokeModuleResponse, error) {
	if r.Module != "buddy-party" {
		return f.fakeSession.InvokeModule(ctx, r, opts...)
	}
	f.pmu.Lock()
	f.reads++
	n, fn := f.reads, f.onParty
	f.pmu.Unlock()
	if fn != nil {
		fn(n)
	}
	f.pmu.Lock()
	v := proto.Clone(f.party)
	f.pmu.Unlock()
	packed, err := anypb.New(v)
	return &modv1.InvokeModuleResponse{Result: packed}, err
}
func sharedRig(t *testing.T, confirm bool) (*Service, *sharedSession, *questsv1.SharedQuestRequest) {
	t.Helper()
	f := &sharedSession{fakeSession: fixture(), party: &partyv1.StateResponse{SessionId: "body-one", Group: &partyv1.Group{Known: true, InGroup: true, Members: []*partyv1.Member{{Guid: 1, Name: "Arlen", Self: true, Online: true}, {Guid: 3, Name: "Joel", Online: true}}}}}
	f.world.QuestGivers = nil
	f.actions.Moving, f.actions.CastPending = true, true // Sharing never interrupts them.
	s := NewService(f, "body-one", 1)
	if confirm {
		packet(t, s, opcode.SMSGQuestConfirmAccept, sharedConfirm(7, 3))
	} else {
		packet(t, s, opcode.SMSGQuestgiverQuestDetails, sharedDetails(t))
	}
	p := snapshot(t, s).PendingShare
	if p == nil {
		t.Fatal("missing actual shared offer")
	}
	return s, f, &questsv1.SharedQuestRequest{SessionId: "body-one", ShareSequence: p.Sequence, SharerGuid: 3, QuestId: 7}
}

func TestSharedQuestFullDetailsAndConfirmHaveDistinctAuthenticatedLayouts(t *testing.T) {
	p := sharedDetails(t)
	o, err := decodeSharedDetails(p, 1)
	if err != nil || o.SharerGuid != 3 || o.Kind != questsv1.SharedQuestKind_SHARED_DETAILS || o.Quest.QuestId != 7 || o.Quest.Title == "" {
		t.Fatal(o, err)
	}
	if _, err := decodeDetails(p); err == nil {
		t.Fatal("shared player offer became an NPC offer")
	}
	for i := range len(p) {
		if _, err := decodeSharedDetails(p[:i], 1); err == nil {
			t.Fatal("accepted prefix", i)
		}
	}
	for _, mode := range []string{"self", "divider", "sharable", "tail"} {
		q := append([]byte(nil), p...)
		switch mode {
		case "self":
			binary.LittleEndian.PutUint64(q, 3)
		case "divider":
			binary.LittleEndian.PutUint64(q[8:], 4)
		case "sharable":
			r := reader{data: q[20:]}
			r.text(512)
			r.text(16384)
			r.text(8192)
			r.u8()
			binary.LittleEndian.PutUint32(q[len(q)-len(r.data):], 0)
		case "tail":
			q = append(q, 0)
		}
		if _, err := decodeSharedDetails(q, 1); err == nil {
			t.Fatal("accepted bad sharing identity/frame", mode)
		}
	}
	p = sharedConfirm(7, 3)
	if o, err := decodeSharedConfirm(p); err != nil || o.Kind != questsv1.SharedQuestKind_PARTY_CONFIRM || o.Quest.QuestId != 7 {
		t.Fatal(o, err)
	}
	for i := range len(p) {
		if _, err := decodeSharedConfirm(p[:i]); err == nil {
			t.Fatal("accepted confirm prefix", i)
		}
	}
	if _, err := decodeSharedConfirm(sharedConfirm(7, 4)); err == nil {
		t.Fatal("accepted foreign confirmer")
	}
}

func TestSharedQuestAcceptsOnlyNewOwnLogInsertionAndUsesNormalGoldenPacket(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		s, f, r := sharedRig(t, confirm)
		f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error { f.confirm(7); return nil }
		out, err := s.AcceptShared(context.Background(), r)
		if err != nil || !out.TransportWritten || !out.ServerResponseReceived || out.State.LastSharedAcceptance.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG || out.State.LastSharedAcceptance.BaselineLogRevision != 1 || out.State.LastSharedAcceptance.ConfirmedLogRevision != 2 || out.State.LastAcceptance != nil || out.State.Offer != nil {
			t.Fatal(out, err)
		}
		if len(f.packets) != 1 || f.acquired != 0 || f.released != 0 || !f.actions.Moving || !f.actions.CastPending {
			t.Fatal("shared acceptance moved/interrupted actor", f.packets)
		}
		want, op := "03000000000000000700000000000000", uint32(opcode.CMSGQuestgiverAcceptQuest)
		if confirm {
			want, op = "07000000", opcode.CMSGQuestConfirmAccept
		}
		if f.packets[0].Opcode != op || hex.EncodeToString(f.packets[0].Payload) != want {
			t.Fatal(f.packets[0])
		}
		if _, err := s.AcceptShared(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("same sequence replayed", err)
		}
	}
}

func TestSharedQuestRequiresCorrectPartySessionAndFreshSource(t *testing.T) {
	for _, mode := range []string{"request_session", "sequence", "quest", "sender", "party_session", "unknown_group", "not_grouped", "missing_owner", "owner_name", "offline_owner", "missing_self", "offline_self", "duplicate_guid", "wrong_self_flag", "dead", "unknown_log", "full_partial_log", "source_changed", "party_changed"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r := sharedRig(t, false)
			switch mode {
			case "request_session":
				r.SessionId = "other"
			case "sequence":
				r.ShareSequence++
			case "quest":
				r.QuestId++
			case "sender":
				r.SharerGuid = 4
			case "party_session":
				f.party.SessionId = "other"
			case "unknown_group":
				f.party.Group.Known = false
			case "not_grouped":
				f.party.Group.InGroup = false
			case "missing_owner":
				f.party.Group.Members = f.party.Group.Members[:1]
			case "owner_name":
				f.party.Group.Members[1].Name = "Other"
			case "offline_owner":
				f.party.Group.Members[1].Online = false
			case "missing_self":
				f.party.Group.Members[0].Guid = 4
				f.party.Group.Members[0].Self = false
			case "offline_self":
				f.party.Group.Members[0].Online = false
			case "duplicate_guid":
				f.party.Group.Members = append(f.party.Group.Members, f.party.Group.Members[0])
			case "wrong_self_flag":
				f.party.Group.Members[1].Self = true
			case "dead":
				f.world.Self.Alive = false
			case "unknown_log":
				f.world.SelfQuestLogKnown = false
			case "full_partial_log":
				f.world.SelfQuestLog = f.world.SelfQuestLog[:24]
			case "source_changed":
				f.onParty = func(n int) {
					if n == 2 {
						packet(t, s, opcode.SMSGQuestConfirmAccept, sharedConfirm(8, 3))
					}
				}
			case "party_changed":
				f.onParty = func(n int) {
					if n == 2 {
						f.party.Group.Members[1].Online = false
					}
				}
			}
			if _, err := s.AcceptShared(context.Background(), r); err == nil || len(f.packets) != 0 || f.acquired != 0 {
				t.Fatal("unproven share wrote", mode, err, f.packets)
			}
		})
	}
}

func TestSharedQuestTimeoutAmbiguityAndLateOwnProofNeverRetry(t *testing.T) {
	for _, mode := range []string{"timeout", "write_error", "server_reject", "inline_proof_error"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r := sharedRig(t, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.onSend = func(_ context.Context, p *modv1.SendPacketRequest) error {
				switch mode {
				case "timeout":
					cancel()
				case "write_error":
					return status.Error(codes.Unavailable, "ambiguous write")
				case "server_reject":
					packet(t, s, opcode.SMSGQuestgiverQuestInvalid, binary.LittleEndian.AppendUint32(nil, 1))
				case "inline_proof_error":
					f.confirm(7)
					snapshot(t, s)
					return status.Error(codes.Unavailable, "ambiguous write")
				}
				return nil
			}
			out, err := s.AcceptShared(ctx, r)
			if (mode == "write_error" || mode == "inline_proof_error") != (err != nil) {
				t.Fatal(out, err)
			}
			a := snapshot(t, s).LastSharedAcceptance
			want := questsv1.AcceptanceStatus_UNCONFIRMED_TIMEOUT
			if mode == "write_error" {
				want = questsv1.AcceptanceStatus_WRITE_UNCONFIRMED
			}
			if mode == "server_reject" {
				want = questsv1.AcceptanceStatus_SERVER_REJECTED
			}
			if mode == "inline_proof_error" {
				want = questsv1.AcceptanceStatus_CONFIRMED_IN_LOG
			}
			if a.Status != want {
				t.Fatal(a)
			}
			if _, err := s.AcceptShared(context.Background(), r); err == nil || len(f.packets) != 1 {
				t.Fatal("ambiguous share replayed", err)
			}
			if mode == "timeout" || mode == "write_error" {
				f.confirm(7)
				if snapshot(t, s).LastSharedAcceptance.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG {
					t.Fatal("late actual log not reconciled")
				}
			}
		})
	}
}

func TestSharedQuestCleanupBindsSourceAndDoesNotClaimAcceptance(t *testing.T) {
	for _, mode := range []string{"already", "expired", "full", "cleanup_error"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r := sharedRig(t, false)
			msg, want := byte(6), questsv1.AcceptanceStatus_ALREADY_IN_LOG
			switch mode {
			case "already", "cleanup_error":
				f.confirm(7)
			case "expired":
				s.shares.until = s.now().Add(-time.Second)
				msg, want = 3, questsv1.AcceptanceStatus_SHARE_EXPIRED
			case "full":
				for i, q := range f.world.SelfQuestLog {
					q.QuestId = uint32(100 + i)
				}
				f.world.SelfQuestLogRevision++
				msg, want = 5, questsv1.AcceptanceStatus_SHARE_LOG_FULL
			}
			if mode == "cleanup_error" {
				f.onSend = func(context.Context, *modv1.SendPacketRequest) error {
					return status.Error(codes.Unavailable, "unknown")
				}
				want = questsv1.AcceptanceStatus_WRITE_UNCONFIRMED
			}
			out, err := s.AcceptShared(context.Background(), r)
			if (mode == "cleanup_error") != (err != nil) {
				t.Fatal(out, err)
			}
			if len(f.packets) != 1 || f.packets[0].Opcode != opcode.MSGQuestPushResult || hex.EncodeToString(f.packets[0].Payload) != hex.EncodeToString(append(binary.LittleEndian.AppendUint32(npcPacket(3), 7), msg)) {
				t.Fatal(f.packets)
			}
			f.world.SelfQuestLogRevision++ // An unrelated later change cannot turn cleanup into a new acceptance.
			if snapshot(t, s).LastSharedAcceptance.Status != want {
				t.Fatal(snapshot(t, s).LastSharedAcceptance)
			}
			if _, err := s.AcceptShared(context.Background(), r); err == nil || len(f.packets) != 1 {
				t.Fatal("cleanup sequence repeated")
			}
		})
	}
}

func TestSharedQuestIndependentModeAndNpcIsolation(t *testing.T) {
	s, f, r := sharedRig(t, false)
	independent := NewIndependentService(f, "body-one", 1)
	packet(t, independent, opcode.SMSGQuestgiverQuestDetails, sharedDetails(t))
	packet(t, independent, opcode.SMSGQuestConfirmAccept, sharedConfirm(7, 3))
	if snapshot(t, independent).PendingShare != nil || snapshot(t, independent).SharedQuestsEnabled {
		t.Fatal("independent accepted owner context")
	}
	if _, err := independent.AcceptShared(context.Background(), r); err == nil || len(f.packets) != 0 {
		t.Fatal("independent shared write")
	}
	if _, err := s.Offer(context.Background(), &questsv1.OfferRequest{SessionId: "body-one", NpcGuid: npcGUID}); err == nil || len(f.packets) != 0 {
		t.Fatal("NPC interaction overwrote undecided share")
	}
	packet(t, s, opcode.MSGQuestPushResult, append(npcPacket(3), 2))
	if snapshot(t, s).LastSharedAcceptance != nil {
		t.Fatal("sender push result treated as acceptance")
	}
}

func TestExpiredShareCannotKeepNpcOperationsBlockedAfterPartyLeaves(t *testing.T) {
	s, f, r := sharedRig(t, false)
	if err := s.reserve(context.Background(), "body-one"); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("fresh shared offer did not keep priority", err)
	}
	s.shares.until = s.now().Add(-time.Second)
	f.party.Group.InGroup = false
	if _, err := s.AcceptShared(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("unproven party allowed expired cleanup", err)
	}
	if err := s.reserve(context.Background(), "body-one"); err != nil {
		t.Fatal("expired share permanently blocked ordinary quest flow", err)
	}
	s.releaseBusy()
	if len(f.packets) != 0 || s.state.PendingShare.AcceptRequested {
		t.Fatal("local expiry wrote or silently decided a share", f.packets)
	}
}

func TestSharedQuestDefiniteRejectionNeedsNewRealOfferBeforeRetry(t *testing.T) {
	s, f, r := sharedRig(t, false)
	f.onSend = func(context.Context, *modv1.SendPacketRequest) error {
		packet(t, s, opcode.SMSGQuestgiverQuestFailed, append(binary.LittleEndian.AppendUint32(nil, 7), 0, 0, 0, 0))
		return nil
	}
	out, err := s.AcceptShared(context.Background(), r)
	if err != nil || out.State.LastSharedAcceptance.Status != questsv1.AcceptanceStatus_SERVER_REJECTED {
		t.Fatal(out, err)
	}
	if _, err := s.AcceptShared(context.Background(), r); err == nil || len(f.packets) != 1 {
		t.Fatal("rejected sequence replayed")
	}
	packet(t, s, opcode.SMSGQuestgiverQuestDetails, sharedDetails(t))
	p := snapshot(t, s).PendingShare
	if p.Sequence == r.ShareSequence || p.AcceptRequested {
		t.Fatal("new definite server offer could not rearm", p)
	}
	r.ShareSequence = p.Sequence
	f.onSend = func(context.Context, *modv1.SendPacketRequest) error { f.confirm(7); return nil }
	out, err = s.AcceptShared(context.Background(), r)
	if err != nil || out.State.LastSharedAcceptance.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG || len(f.packets) != 2 {
		t.Fatal(out, err)
	}
}

func TestSharedQuestAmbiguousManualWriteCannotRetryThroughPartyConfirm(t *testing.T) {
	s, f, r := sharedRig(t, false)
	f.onSend = func(context.Context, *modv1.SendPacketRequest) error {
		return status.Error(codes.Unavailable, "unconfirmed")
	}
	s.AcceptShared(context.Background(), r)
	packet(t, s, opcode.SMSGQuestConfirmAccept, sharedConfirm(7, 3))
	r.ShareSequence = snapshot(t, s).PendingShare.Sequence
	f.onSend = nil
	out, err := s.AcceptShared(context.Background(), r)
	if err != nil || out.State.LastSharedAcceptance.Status != questsv1.AcceptanceStatus_SHARE_ALREADY_REQUESTED || len(f.packets) != 2 || f.packets[1].Opcode != opcode.MSGQuestPushResult {
		t.Fatal("ambiguous acceptance repeated by another share kind", out, err, f.packets)
	}
}

func TestSharedQuestNetworkWriteNeverHoldsCallbackStateLock(t *testing.T) {
	s, f, r := sharedRig(t, false)
	entered, release := make(chan struct{}), make(chan struct{})
	f.onSend = func(context.Context, *modv1.SendPacketRequest) error {
		close(entered)
		<-release
		f.confirm(7)
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := s.AcceptShared(context.Background(), r); done <- err }()
	<-entered
	if _, err := s.AcceptShared(context.Background(), r); status.Code(err) != codes.FailedPrecondition {
		close(release)
		t.Fatal("concurrent shared operation accepted", err)
	}
	callbackDone := make(chan struct{})
	go func() {
		packet(t, s, opcode.SMSGQuestConfirmAccept, sharedConfirm(8, 3))
		snapshot(t, s)
		close(callbackDone)
	}()
	select {
	case <-callbackDone:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("network write held callback lock")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := snapshot(t, s)
	if got.LastSharedAcceptance.QuestId != 7 || got.LastSharedAcceptance.Status != questsv1.AcceptanceStatus_CONFIRMED_IN_LOG || got.PendingShare.Quest.QuestId != 8 || got.PendingShare.AcceptRequested {
		t.Fatal("old receipt replaced new pending source", got)
	}
}
