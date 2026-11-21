package buddytrade

import (
	"context"
	"testing"
	"time"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/proto"
)

func exactTransferFixture() (*Service, *fakeSession, *tradev1.TransferStepRequest) {
	s, f := fixture()
	f.actions.SessionId = "session-A"
	f.actions.SelfGuid = 1
	f.world.NearbyPlayers = []*worldv1.PlayerState{{Guid: 8, Present: true, AliveKnown: true, Alive: true, MapKnown: true, PositionKnown: true, X: 2}}
	r := &tradev1.TransferStepRequest{SessionId: "session-A", SelfGuid: 1, PartnerGuid: 8, Revision: s.snapshot().Revision, TransactionId: "stock-1", RequestId: "stock-1:open", Mode: "open", LeaseMs: 5000, Source: &tradev1.TransferSource{Bag: 0, Slot: 2, ItemGuid: 0x4000000000000007, Entry: 159, Count: 5, BackpackRevision: 4}, OwnItems: []*tradev1.Item{{Slot: 0, Entry: 159, Count: 5}}}
	return s, f, r
}
func transferInput(s *Service, r *tradev1.TransferStepRequest, mode string) *tradev1.TransferStepRequest {
	q := proto.Clone(r).(*tradev1.TransferStepRequest)
	q.Mode = mode
	q.RequestId = "stock-1:" + mode
	q.Revision = s.snapshot().Revision
	return q
}
func TestExactTransferOwnsPartnerTermsAndNormalPackets(t *testing.T) {
	s, f, r := exactTransferFixture()
	ctx := context.Background()
	state, err := s.TransferStep(ctx, r)
	if err != nil || state.PartnerGuid != 8 || f.count(opcode.CMSGInitiateTrade) != 1 {
		t.Fatal(state, err)
	}
	if _, err = s.TransferStep(ctx, r); err != nil || f.count(opcode.CMSGInitiateTrade) != 1 {
		t.Fatal("repeated exact input", err)
	}
	statusPacket(s, 2)
	s.Tick(ctx)
	if f.count(opcode.CMSGAcceptTrade) != 0 {
		t.Fatal("native Tick selected acceptance")
	}
	offer := transferInput(s, r, "offer")
	if _, err = s.TransferStep(ctx, offer); err != nil {
		t.Fatal(err)
	}
	p := f.packets[len(f.packets)-1]
	if p.Opcode != opcode.CMSGSetTradeItem || string(p.Payload) != string([]byte{0, 255, 25}) {
		t.Fatal(p)
	}
	packet(s, opcode.SMSGTradeStatusExtended, extended(true, 0, 0, 0, 0))
	accept := transferInput(s, r, "accept")
	if _, err = s.TransferStep(ctx, accept); err != nil {
		t.Fatal(err)
	}
	if s.snapshot().CompletedTrades != 0 {
		t.Fatal("transport fabricated completion")
	}
	statusPacket(s, 8)
	s.Tick(ctx)
	if s.snapshot().CompletedTrades != 1 || s.snapshot().Phase != "COMPLETE" || f.releases != 1 {
		t.Fatal(s.snapshot(), f.releases)
	}
}
func TestExactTransferRejectsForeignChangedAndUnobservedTerms(t *testing.T) {
	for _, change := range []func(*tradev1.TransferStepRequest, *fakeSession){
		func(r *tradev1.TransferStepRequest, f *fakeSession) { r.PartnerGuid = 9 },
		func(r *tradev1.TransferStepRequest, f *fakeSession) { r.SelfGuid = 8 },
		func(r *tradev1.TransferStepRequest, f *fakeSession) { r.Source.Count-- },
		func(r *tradev1.TransferStepRequest, f *fakeSession) { r.Source.BackpackRevision++ },
		func(r *tradev1.TransferStepRequest, f *fakeSession) { f.actions.SessionId = "foreign" },
	} {
		s, f, r := exactTransferFixture()
		change(r, f)
		if _, err := s.TransferStep(context.Background(), r); err == nil || f.count(opcode.CMSGInitiateTrade) != 0 {
			t.Fatal("unsafe source dispatched", err)
		}
	}
	s, f, r := exactTransferFixture()
	ctx := context.Background()
	if _, err := s.TransferStep(ctx, r); err != nil {
		t.Fatal(err)
	}
	statusPacket(s, 2)
	if _, err := s.TransferStep(ctx, transferInput(s, r, "accept")); err == nil {
		t.Fatal("accepted absent peer terms")
	}
	packet(s, opcode.SMSGTradeStatusExtended, extended(true, 999, 1, 0, 0))
	s.Tick(ctx)
	if f.count(opcode.CMSGAcceptTrade) != 0 {
		t.Fatal("native accepted changed terms")
	}
}
func TestExactTransferUnknownWriteNeverReplaysAndFiniteLeaseExpires(t *testing.T) {
	s, f, r := exactTransferFixture()
	now := time.Now()
	s.now = func() time.Time { return now }
	f.fail = opcode.CMSGInitiateTrade
	out, err := s.TransferStep(context.Background(), r)
	if err != nil || !out.LastTransferStep.TransportAttempted || out.LastTransferStep.TransportWritten {
		t.Fatal(out, err)
	}
	if _, err = s.TransferStep(context.Background(), r); err != nil || f.count(opcode.CMSGInitiateTrade) != 1 {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Second)
	s.Tick(context.Background())
	if s.snapshot().ExactTransfer != nil || f.releases != 1 || f.count(opcode.CMSGCancelTrade) != 1 {
		t.Fatal(s.snapshot(), f.releases)
	}
}
func TestExactTransferRecipientDoesNotBeginOrAcceptWithoutLua(t *testing.T) {
	s, f, r := exactTransferFixture()
	r.Mode = "reserve"
	r.RequestId = "reserve"
	r.Source = nil
	r.PartnerItems = r.OwnItems
	r.OwnItems = nil
	if _, err := s.TransferStep(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	packet(s, opcode.SMSGTradeStatus, append(u32(1), u64(8)...))
	s.Tick(context.Background())
	if s.snapshot().Phase != "INVITED" || f.count(opcode.CMSGBeginTrade) != 0 {
		t.Fatal(s.snapshot())
	}
	if _, err := s.TransferStep(context.Background(), transferInput(s, r, "begin")); err != nil || f.count(opcode.CMSGBeginTrade) != 1 {
		t.Fatal(err)
	}
}

func TestExactTransferRechecksBodyAndStackAfterCustodyAdmission(t *testing.T) {
	for _, mode := range []string{"peer", "stack", "body", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			s, f, r := exactTransferFixture()
			now := time.Now()
			s.now = func() time.Time { return now }
			f.leaseChange = func() {
				switch mode {
				case "peer":
					f.world.NearbyPlayers[0].X = 40
				case "stack":
					f.world.Backpack[2].StackCount = 6
				case "body":
					f.actions.SelfGuid = 8
				case "expiry":
					now = now.Add(6 * time.Second)
				}
			}
			if _, err := s.TransferStep(context.Background(), r); err == nil || f.count(opcode.CMSGInitiateTrade) != 0 || f.releases != 1 {
				t.Fatal("admission callback changed authority", err, f.releases)
			}
		})
	}
}

func TestExactTransferIndependentBodyCanIssueSelectedTradeInputs(t *testing.T) {
	s, f, r := exactTransferFixture()
	s.independent = true
	if _, err := s.TransferStep(context.Background(), r); err != nil || f.count(opcode.CMSGInitiateTrade) != 1 {
		t.Fatal(err)
	}
	if _, err := s.TransferStep(context.Background(), transferInput(s, r, "cancel")); err != nil || f.count(opcode.CMSGCancelTrade) != 1 {
		t.Fatal(err)
	}
}
