package buddytrade

import (
	"context"
	"fmt"
	"testing"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIndependentTradeRefusesExplicitOffersEvenWithOwnerObserved(t *testing.T) {
	for _, self := range []uint64{3, 6} {
		t.Run(fmt.Sprint(self), func(t *testing.T) {
			_, f := fixture()
			f.world.Self.Guid = self
			s := NewIndependentService(f, "independent", self)
			if _, err := s.Open(context.Background(), &tradev1.BoundRequest{SessionId: "independent"}); status.Code(err) != codes.FailedPrecondition {
				t.Fatal("independent Open reached owner trade", err)
			}
			// A leaked or unexpected open state must not grant offer authority.
			s.state.Phase, s.state.PartnerGuid = "OPEN", ownerGUID
			if _, err := s.OfferItem(context.Background(), &tradev1.ItemRequest{SessionId: "independent", ExpectedItemGuid: 7}); status.Code(err) != codes.FailedPrecondition {
				t.Fatal("independent item offer reached owner trade", err)
			}
			if _, err := s.OfferGold(context.Background(), &tradev1.GoldRequest{SessionId: "independent", Copper: 100}); status.Code(err) != codes.FailedPrecondition {
				t.Fatal("independent gold offer reached owner trade", err)
			}
			if len(f.packets) != 0 || f.leases != 0 {
				t.Fatal("disabled trade acquired a lease or wrote a packet", f.packets, f.leases)
			}
		})
	}
}

func TestIndependentTradeIncomingServerFramesCannotOpenAcceptOrComplete(t *testing.T) {
	_, f := fixture()
	s := NewIndependentService(f, "independent", 1)
	// The real status-specific wire layouts cover a Joel invitation, window
	// opening, partner acceptance, unaccept, extended offer and completion.
	frames := []struct {
		op      uint32
		payload []byte
	}{
		{opcode.SMSGTradeStatus, append(u32(1), u64(ownerGUID)...)},
		{opcode.SMSGTradeStatus, append(u32(2), u32(0)...)},
		{opcode.SMSGTradeStatus, u32(4)},
		{opcode.SMSGTradeStatus, u32(7)},
		{opcode.SMSGTradeStatusExtended, extended(true, 117, 2, 0, 0)},
		{opcode.SMSGTradeStatus, u32(8)},
	}
	for _, frame := range frames {
		packet(s, frame.op, frame.payload)
		s.Tick(context.Background())
		s.Tick(context.Background())
		st := s.snapshot()
		if st.PartnerGuid != 0 || st.PartnerAccepted || st.AcceptTransportWritten || st.CompletedTrades != 0 || active(st.Phase) || len(st.PartnerItems) != 0 {
			t.Fatal("server frame granted owner trade authority", st)
		}
	}
	if f.leases != 0 || f.count(opcode.CMSGCancelTrade) != 5 {
		t.Fatal("unsolicited trades must only be canceled once per allocated frame", f.packets, f.leases)
	}
	for _, p := range f.packets {
		if p.Opcode != opcode.CMSGCancelTrade {
			t.Fatal("incoming trade generated an opening, offer or acceptance packet", p)
		}
	}
}

func TestIndependentTradeBlocksUnexpectedControllerAndDirectTradeWrites(t *testing.T) {
	_, f := fixture()
	s := NewIndependentService(f, "independent", 1)
	s.state.Phase, s.state.PartnerGuid, s.state.PartnerAccepted = "OPEN", ownerGUID, true
	s.pendingBegin = true
	s.Tick(context.Background())
	if f.count(opcode.CMSGCancelTrade) != 1 || f.leases != 0 {
		t.Fatal("unexpected active state was not closed without a trade lease", f.packets, f.leases)
	}
	for _, op := range []uint32{opcode.CMSGInitiateTrade, opcode.CMSGBeginTrade, opcode.CMSGBusyTrade, opcode.CMSGIgnoreTrade, opcode.CMSGAcceptTrade, opcode.CMSGUnacceptTrade, opcode.CMSGSetTradeItem, opcode.CMSGClearTradeItem, opcode.CMSGSetTradeGold} {
		if err := s.send(context.Background(), op, nil); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("trade packet bypassed independent mode", op, err)
		}
	}
	if len(f.packets) != 1 {
		t.Fatal("disabled trade write reached transport", f.packets)
	}
}

func TestIndependentTradeRetainsOwnWalletAndServerConfirmedJunkDisposal(t *testing.T) {
	_, f := junkFixture()
	f.world.Self.Guid = 6
	f.world.Owner = nil
	f.world.SelfCopperKnown, f.world.SelfCopper, f.world.SelfCopperRevision = true, 1234, 1
	s := NewIndependentService(f, "independent", 6)
	st, err := s.GetState(context.Background(), nil)
	if err != nil || !st.CopperKnown || st.Copper != 1234 || !st.BackpackKnown || len(st.Backpack) != 16 {
		t.Fatal("independent wallet or backpack unavailable", st, err)
	}
	packet(s, opcode.SMSGItemQuerySingleResponse, itemQuery(4865, 0, 15, 0))
	packet(s, opcode.SMSGItemQuerySingleResponse, itemQuery(117, 1, 0, 0))
	st, err = s.DestroyJunk(context.Background(), requestJunk(s, f))
	if err != nil || st.JunkStacksDestroyed != 0 || st.LastJunkDisposal.Status != "AWAITING_SERVER_SLOT_UPDATE" {
		t.Fatal("own junk disposal unavailable or transport treated as success", st, err)
	}
	f.world.Backpack[2] = &worldv1.BackpackSlot{Slot: 2}
	f.world.BackpackRevision++
	s.Tick(context.Background())
	st = s.snapshot()
	if st.JunkStacksDestroyed != 1 || st.LastJunkDisposal.Status != "SERVER_SLOT_REMOVAL_CONFIRMED" || f.count(opcode.CMSGDestroyitem) != 1 {
		t.Fatal("independent disposal lost actual inventory confirmation", st)
	}
}
