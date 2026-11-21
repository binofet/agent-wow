package buddyactions

import (
	"context"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestBusyHealingCustodyRetainsFactualPatientsAndRanksWithoutCastReadiness(t *testing.T) {
	s, f, _ := rig(t)
	s.sessionID = f.party.SessionId
	s.applySpellbookLocked(map[uint32]bool{2050: true})
	f.world.Owner.Health = 20
	s.state.InteractionOwner = "loot"
	view, err := s.HealingOptions(context.Background(), &emptypb.Empty{})
	if err != nil || view.Caster.Ready || view.Reason != "healing_caster_busy" || len(view.Patients) != 1 || len(view.Spells) != 1 || view.Patients[0].Guid != ownerGUID {
		t.Fatal("custody erased raw triage facts or invented readiness", view, err)
	}
	_, err = s.CastHeal(context.Background(), &actionsv1.CastHealRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: ownerGUID, SpellId: 2050})
	if status.Code(err) != codes.FailedPrecondition || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("factual rank publication bypassed exact cast custody", err, f.packets)
	}
}
