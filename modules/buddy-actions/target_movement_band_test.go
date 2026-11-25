package buddyactions

import (
	"context"
	"errors"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"testing"
	"time"
)

func TestTargetMovementBandWithdrawsTooCloseAndVerifiesPosition(t *testing.T) {
	s, f, r, ctx, advance := targetMovementFixture(t)
	f.world.RequestedCreature.X = 3
	r.RangeYards = 23
	r.MinRangeYards = 17
	r.ClearanceYards = 17
	r.ClearanceTargetGuids = []uint64{enemyGUID}
	first, err := s.ApproachTarget(ctx, r)
	if err != nil || first.TargetMovement.Ready || !first.Moving {
		t.Fatal("too-close body reported in position", first, err)
	}
	for i := 0; i < 30 && !s.targetMovement.view.Ready; i++ {
		advance(200 * time.Millisecond)
		if _, err := s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, 1), wrapperspb.UInt64(1)); err != nil {
			t.Fatal(err)
		}
		if _, err = s.ApproachTarget(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	d := distance(s.pos, position{x: 3})
	if !s.targetMovement.view.Ready || d < 17 || d > 23 || s.pos.x >= 0 || countOpcode(f, opcode.MSGMoveHeartbeat) == 0 {
		t.Fatal("no verified physical withdrawal", s.pos, s.stateLocked(), f.packets)
	}
}
func TestTargetMovementBandNeverUsesBodyCenterLoSFallback(t *testing.T) {
	s, f, r, ctx, _ := targetMovementFixture(t)
	f.world.RequestedCreature.X = 3
	r.RangeYards = 23
	r.MinRangeYards = 17
	r.RequireLos = true
	s.crowdControlSight = func(context.Context, string, uint32, position, position) (bool, error) { return false, nil }
	calls := 0
	s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
		calls++
		if distance(to, position{x: 3}) < 17 {
			t.Fatal("unsafe body-center candidate", to)
		}
		return []position{from, to}, nil
	}
	got, err := s.ApproachTarget(ctx, r)
	if err != nil || calls == 0 || got.Moving || got.TargetMovement.Ready || got.TargetMovement.Phase != "range_band_route_unavailable" || countOpcode(f, opcode.MSGMoveStartForward) != 0 {
		t.Fatal(got, err, f.packets)
	}
}
func TestTargetMovementBandPathChecksWholeSegmentsAndWithdrawal(t *testing.T) {
	if targetBandPathValid([]position{{x: 0}, {x: -17}}, position{}, position{x: 3}, 17, 23, []position{{x: -10}}, 5) {
		t.Fatal("route crossed a different visible exclusion")
	}
	if targetBandPathValid([]position{{x: 2}, {x: 1}, {x: 15}}, position{x: 2}, position{}, 10, 20, nil, 0) {
		t.Fatal("inside withdrawal first moved toward body")
	}
	if !targetBandPathValid([]position{{x: 2}, {x: 15}}, position{x: 2}, position{}, 10, 20, nil, 0) {
		t.Fatal("safe outward route rejected")
	}
}
func TestTargetMovementBandEdgesUnknownClearanceAndLegacyDefault(t *testing.T) {
	for _, d := range []float32{17, 23} {
		s, f, r, ctx, _ := targetMovementFixture(t)
		f.world.RequestedCreature.X = d
		r.RangeYards = 23
		r.MinRangeYards = 17
		got, err := s.ApproachTarget(ctx, r)
		if err != nil || !got.TargetMovement.Ready {
			t.Fatal("valid band edge rejected", d, got, err)
		}
	}
	s, f, r, ctx, _ := targetMovementFixture(t)
	f.world.RequestedCreature.X = 3
	r.RangeYards = 23
	got, err := s.ApproachTarget(ctx, r)
	if err != nil || !got.TargetMovement.Ready {
		t.Fatal("unrequested legacy radius changed", got, err)
	}
	r.MinRangeYards = 17
	r.ClearanceYards = 17
	r.ClearanceTargetGuids = []uint64{enemyGUID + 1}
	got, err = s.ApproachTarget(ctx, r)
	if err != nil || got.TargetMovement.Ready || got.TargetMovement.Phase != "clearance_target_unavailable" {
		t.Fatal("unknown hazard became empty space", got, err)
	}
	s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
		return nil, errors.New("unavailable")
	}
	r.ClearanceTargetGuids = nil
	r.ClearanceYards = 0
	got, err = s.ApproachTarget(ctx, r)
	if err != nil || got.Moving || got.TargetMovement.Ready {
		t.Fatal("missing route became direct withdrawal", got, err)
	}
}
