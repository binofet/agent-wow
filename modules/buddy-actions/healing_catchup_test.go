package buddyactions

import (
	"context"
	"testing"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestHealingOptionsKeepDistantPatientWithDeadOwnerAndExactHealAfterCatchup(t *testing.T) {
	for _, distance := range []float32{36, 60} {
		s, f, _ := rig(t)
		s.sessionID = f.party.SessionId
		learnSpells(s, 2050)
		s.UseHostStandingHealing()
		f.world.Owner.Alive, f.world.Owner.Health = false, 0
		ally := triagePlayer(f, 4, 20)
		ally.X, ally.Alive = distance, true
		triageMember(f, ally.Guid)
		f.world.NearbyPlayers = []*worldv1.PlayerState{ally}
		view, err := s.HealingOptions(context.Background(), &emptypb.Empty{})
		if err != nil || !view.Caster.Ready || len(view.Patients) != 1 || view.Patients[0].Guid != ally.Guid || view.Patients[0].GetDistance() != float64(distance) {
			t.Fatal("distant injured ally disappeared behind dead owner or range cap", distance, view, err)
		}
		req := &actionsv1.CastHealRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: ally.Guid, SpellId: 2050}
		if distance > 40 {
			if _, err := s.CastHeal(context.Background(), req); status.Code(err) != codes.FailedPrecondition || countPackets(f, opcode.CMSGCastSpell) != 0 {
				t.Fatal("distant observation bypassed mechanical cast range", err)
			}
			// The movement harness later reports this new authoritative position.
			f.world.Self.X, f.world.Self.PositionRevision = distance-30, f.world.Self.PositionRevision+1
		}
		if _, err := s.CastHeal(context.Background(), req); err != nil || lastCast(f) != 2050 || healingCastTarget(f) != ally.Guid {
			t.Fatal("catch-up did not make the same selected heal executable", distance, err, f.packets)
		}
	}
}
