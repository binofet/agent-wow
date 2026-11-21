package buddyinventory

import (
	"testing"

	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	"google.golang.org/protobuf/proto"
)

func completedWaterTrade(partner uint64) *tradev1.State {
	return &tradev1.State{SessionId: "body", Phase: "COMPLETE", PartnerGuid: partner,
		PartnerAccepted: true, AcceptTransportWritten: true, LastServerStatus: 8,
		Reason: "server_confirmed_trade_complete", CompletedTrades: 1,
		OwnRequestedItems: []*tradev1.Item{{Entry: 3772, Count: 20}},
		PartnerItems:      []*tradev1.Item{{Entry: 3772, Count: 16}}}
}

func peerTradeReadinessFixture(self uint64) (*Service, *fakeInventory) {
	s, f := fixtureInventory()
	s.selfGUID, s.state.SelfGuid = self, self
	f.w.Self.Guid = self
	f.a.SessionId, f.a.SelfGuid = "body", self
	f.prof = &professionsv1.State{SessionId: "body", SelfGuid: self}
	return s, f
}

func TestCompletedPeerTradeRestoresObservedConsumableBodyReadiness(t *testing.T) {
	for _, row := range []struct {
		name          string
		self, partner uint64
	}{
		{"Arlen_receives_from_Mira", 1, 4}, {"Mira_gives_to_Arlen", 4, 1},
		{"Nixwick_receives_from_Mira", 8, 4}, {"Mira_gives_to_Nixwick", 4, 8},
		{"Joel_trade", 1, 3},
	} {
		t.Run(row.name, func(t *testing.T) {
			s, f := peerTradeReadinessFixture(row.self)
			f.trade = completedWaterTrade(row.partner)
			view := stateInventory(t, s)
			if !view.InventoryKnown || !view.GetConsumableContext().GetBodyReady() || len(f.packets) != 0 || f.leases != 0 {
				t.Fatal("server-confirmed peer trade blocked a quiet ready body or caused an action", view, f.packets, f.leases)
			}
		})
	}
}

func TestCompletedPeerTradeKeepsUnsettledOrForeignEvidenceBlocked(t *testing.T) {
	for _, mode := range []string{"zero_partner", "foreign_session", "active", "unknown", "not_completed", "unconfirmed_reason", "zero_completions", "junk_pending", "junk_unknown", "motion_unknown", "combat"} {
		t.Run(mode, func(t *testing.T) {
			s, f := peerTradeReadinessFixture(1)
			f.trade = proto.Clone(completedWaterTrade(4)).(*tradev1.State)
			switch mode {
			case "zero_partner":
				f.trade.PartnerGuid = 0
			case "foreign_session":
				f.trade.SessionId = "old-body"
			case "active":
				f.trade.Phase = "ACCEPT_SENT"
			case "unknown":
				f.trade.Phase = "TRANSPORT_UNCONFIRMED_NO_RETRY"
			case "not_completed":
				f.trade.LastServerStatus = 7
			case "unconfirmed_reason":
				f.trade.Reason = "completion_unconfirmed"
			case "zero_completions":
				f.trade.CompletedTrades = 0
			case "junk_pending":
				f.trade.LastJunkDisposal = &tradev1.JunkDisposal{Status: "AWAITING_SERVER_SLOT_UPDATE"}
			case "junk_unknown":
				f.trade.LastJunkDisposal = &tradev1.JunkDisposal{Status: "TRANSPORT_UNCONFIRMED_NO_RETRY"}
			case "motion_unknown":
				f.a.MotionUnconfirmed = true
			case "combat":
				f.w.Self.UnitFlags |= 0x80000
			}
			view := stateInventory(t, s)
			if view.GetConsumableContext().GetBodyReady() || len(f.packets) != 0 || f.leases != 0 {
				t.Fatal("unsettled or foreign evidence became drink readiness", mode, view, f.packets, f.leases)
			}
		})
	}
}
