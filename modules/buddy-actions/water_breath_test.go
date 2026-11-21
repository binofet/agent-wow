package buddyactions

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testUnendingBreath = 5697

func TestExactWaterBreathingWorksSwimmingMovingAndWithoutBuffToggle(t *testing.T) {
	s, f, _ := buffRig(t, 9, testUnendingBreath)
	s.Buffs(context.Background(), &actionsv1.ToggleRequest{Enabled: false})
	for _, p := range []*worldv1.PlayerState{f.world.Self, f.world.Owner} {
		p.MapId = 48
		p.MovementFlags = swimMovementFlag | swimAscendingFlag
	}
	s.state.Following, s.state.Moving = true, true
	s.followUntil = s.now().Add(time.Hour)
	castBuff(t, s, ownerGUID, testUnendingBreath)
	if len(buffCasts(f)) != 1 || !s.state.Following || !s.state.Moving {
		t.Fatal("instant water buff canceled native movement intent")
	}
	confirmBuff(t, s, false)
	if s.state.BuffsConfirmed != 1 {
		t.Fatal("swimming aura not confirmed")
	}
}
func TestExactWaterBreathingDoesNotBypassUnsupportedMovement(t *testing.T) {
	for _, flags := range []uint32{0x800, 0x1000000, 0x8000000} {
		s, f, _ := buffRig(t, 9, testUnendingBreath)
		f.world.Self.MovementFlags = swimMovementFlag | flags
		_, err := s.CastBuff(context.Background(), &actionsv1.CastBuffRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: ownerGUID, SpellId: testUnendingBreath})
		if status.Code(err) != codes.FailedPrecondition || len(buffCasts(f)) != 0 {
			t.Fatal("unsupported swimming state wrote buff", flags, err)
		}
	}
}
