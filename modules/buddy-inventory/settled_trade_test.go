package buddyinventory

import (
	"context"
	"testing"
	"time"

	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/protobuf/proto"
)

func TestInventorySettledCompletedTradeDoesNotBlockFutureUpkeep(t *testing.T) {
	complete := &tradev1.State{SessionId: "body", Phase: "COMPLETE", PartnerGuid: 3, PartnerAccepted: true,
		AcceptTransportWritten: true, LastServerStatus: 8, Reason: "server_confirmed_trade_complete", CompletedTrades: 1,
		PartnerItems: []*tradev1.Item{{Entry: 2962, Count: 1, MetadataKnown: true}}, JunkDisposalEnabled: true}
	for _, phase := range []string{"IDLE", "COMPLETE"} {
		t.Run(phase, func(t *testing.T) {
			s, f := fixtureInventory()
			written := make(chan struct{})
			f.afterSend = func(*modv1.SendPacketRequest) { close(written) }
			f.trade = proto.Clone(complete).(*tradev1.State)
			if phase == "IDLE" {
				f.trade = &tradev1.State{SessionId: "body", Phase: "IDLE", JunkDisposalEnabled: true}
			}
			r := moveRequest(t, s)
			st, err := s.Move(context.Background(), r)
			if err != nil {
				t.Fatal(st, err)
			}
			select {
			case <-written:
			case <-time.After(2 * time.Second):
				t.Fatal("settled trade blocked the normal inventory write")
			}
			st = stateInventory(t, s)
			f.mu.Lock()
			packets, leases := len(f.packets), f.leases
			f.mu.Unlock()
			if !st.Operation.TransportAttempted || packets != 1 || leases != 1 {
				t.Fatal(st, packets, leases)
			}
		})
	}
	for _, mode := range []string{"status", "reason", "count", "partner", "phase", "junk-awaiting", "junk-unconfirmed", "junk-unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, f := fixtureInventory()
			f.trade = proto.Clone(complete).(*tradev1.State)
			switch mode {
			case "status":
				f.trade.LastServerStatus = 7
			case "reason":
				f.trade.Reason = "unexpected_completion_status"
			case "count":
				f.trade.CompletedTrades = 0
			case "partner":
				f.trade.PartnerGuid = 4
			case "phase":
				f.trade.Phase = "ACCEPT_SENT"
			case "junk-awaiting":
				f.trade.LastJunkDisposal = &tradev1.JunkDisposal{Status: "AWAITING_SERVER_SLOT_UPDATE"}
			case "junk-unconfirmed":
				f.trade.LastJunkDisposal = &tradev1.JunkDisposal{Status: "UNCONFIRMED_NO_RETRY"}
			case "junk-unknown":
				f.trade.LastJunkDisposal = &tradev1.JunkDisposal{Status: "unknown"}
			}
			st, err := s.Move(context.Background(), moveRequest(t, s))
			if err != nil || st.Operation.Status != "BLOCKED" || st.Operation.TransportAttempted || len(f.packets) != 0 || f.leases != 0 {
				t.Fatal(st, err, len(f.packets), f.leases)
			}
		})
	}
}
