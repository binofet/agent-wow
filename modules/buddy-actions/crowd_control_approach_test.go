package buddyactions

import (
	"context"
	"errors"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func controlMovementContext(t *testing.T, s *Service, f *fakeSession) context.Context {
	t.Helper()
	activateEncounterWriter(t, s, f)
	ctx := modcb.WithPolicySpell(context.Background(), testEncounterKey)
	ctx = modcb.WithPolicyEncounterAccess(ctx, testEncounterKey, 1)
	md, _ := metadata.FromOutgoingContext(ctx)
	return metadata.NewIncomingContext(context.Background(), md)
}
func advanceControlMovement(t *testing.T, s *Service, ctx context.Context, advance func(time.Duration), r *actionsv1.ApproachTargetRequest) *actionsv1.State {
	t.Helper()
	advance(200 * time.Millisecond)
	if _, err := s.RenewPolicyEncounterLease(ctx, wrapperspb.UInt64(1)); err != nil {
		t.Fatal(err)
	}
	result, err := s.ApproachTarget(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestPolymorphApproachStopsAtVisibleRangeAndWaitsForExactCommit(t *testing.T) {
	s, f, advance, cast := polymorphFixture(t)
	ctx := controlMovementContext(t, s, f)
	f.lookupCreatures[sheepTarget].X = 40
	s.crowdControlSight = func(_ context.Context, _ string, _ uint32, from, _ position) (bool, error) { return from.x >= 19, nil }
	move := &actionsv1.ApproachTargetRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: sheepTarget, VisitId: 1, RangeYards: 30, RequireLos: true}
	var result *actionsv1.State
	for range 30 {
		result = advanceControlMovement(t, s, ctx, advance, move)
		if result.GetTargetMovement().GetReady() {
			break
		}
	}
	if !result.GetTargetMovement().GetReady() || s.pos.x < 19 || s.pos.x >= 23 || s.state.Moving || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("exact positioning failed its observed range/LoS constraint", result, s.pos, f.packets)
	}
	s.Tick(ctx)
	if countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("native maintenance authored a CC cast")
	}
	result, err := s.Polymorph(ctx, cast)
	if err != nil || !result.GetLastSpellOperation().GetTransportWritten() || result.GetLastSpellOperation().GetEffectObserved() {
		t.Fatal("caller-selected exact commit failed or claimed effect", result, err)
	}
	_, _ = s.Polymorph(ctx, cast)
	if countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("exact commit duplicated", f.packets)
	}
}
func TestPolymorphApproachFindsClearStandOffWithoutRouteToCreature(t *testing.T) {
	s, f, advance, cast := polymorphFixture(t)
	ctx := controlMovementContext(t, s, f)
	f.lookupCreatures[sheepTarget].X = 40
	s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
		if to.x >= 35 {
			return nil, errors.New("no walkable route to creature")
		}
		return []position{from, to}, nil
	}
	s.crowdControlSight = func(_ context.Context, _ string, _ uint32, from, _ position) (bool, error) { return from.y > 4, nil }
	move := &actionsv1.ApproachTargetRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: sheepTarget, VisitId: 1, RangeYards: 30, RequireLos: true}
	var result *actionsv1.State
	for range 35 {
		result = advanceControlMovement(t, s, ctx, advance, move)
		if result.GetTargetMovement().GetReady() {
			break
		}
	}
	if !result.GetTargetMovement().GetReady() || s.pos.y <= 4 || s.pos.x >= 35 || s.state.Moving || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("generic movement lost legal stand-off geometry", result, s.pos, f.packets)
	}
	if _, err := s.Polymorph(ctx, cast); err != nil || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatal("exact cast after clear stand-off failed", err, s.stateLocked())
	}
}
func TestSapApproachFlanksMovingTargetAndCastsOnlyFromObservedRear(t *testing.T) {
	s, f, cast := policySapFixture(t)
	ctx := controlMovementContext(t, s, f)
	now := s.now()
	s.now = func() time.Time { return now }
	advance := func(d time.Duration) { now = now.Add(d) }
	target := f.lookupCreatures[sheepTarget]
	target.X = 20
	target.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1400, AgeMs: 50, DestinationKnown: true, DestinationX: 14}
	f.world.Self.RunSpeed = 4.9
	s.crowdControlSight = func(context.Context, string, uint32, position, position) (bool, error) { return true, nil }
	move := &actionsv1.ApproachTargetRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: sheepTarget, VisitId: 1, RangeYards: 5, RequireLos: true, Behind: true}
	result := advanceControlMovement(t, s, ctx, advance, move)
	if len(s.targetMovement.nav.path) < 2 || !sapRearPathValid(position{}, s.targetMovement.constraintTarget, s.targetMovement.nav.path) || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("generic behind constraint did not keep a safe flank", result, s.targetMovement.nav.path)
	}
	target.X, target.Orientation = 14, 3.1415927
	target.Trajectory = &worldv1.CreatureTrajectory{Known: true, Stopped: true}
	for range 60 {
		result = advanceControlMovement(t, s, ctx, advance, move)
		if result.GetTargetMovement().GetReady() {
			break
		}
	}
	if !result.GetTargetMovement().GetReady() || !behindEnemy(s.pos, position{x: 14, o: 3.1415927}) || s.state.Moving || countPackets(f, opcode.CMSGAttackswing) != 0 || countPackets(f, opcode.CMSGCastSpell) != 0 {
		t.Fatal("new observed rear pose was not attained", result, s.pos, f.packets)
	}
	if _, err := s.Sap(ctx, cast); err != nil || countPackets(f, opcode.CMSGCastSpell) != 1 || countPackets(f, opcode.CMSGAttackswing) != 0 {
		t.Fatal("exact Sap failed after Lua-requested physical approach", err, s.stateLocked())
	}
}
func TestNativeLegacyCrowdControlCannotStartAnApproachController(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		s, f, advance, req := polymorphFixture(t)
		req.Approach, req.Cancel = !cancel, cancel
		if _, err := s.Polymorph(context.Background(), req); status.Code(err) != codes.InvalidArgument || len(f.packets) != 0 {
			t.Fatal("native legacy flags authored a task", err, f.packets)
		}
		advance(time.Minute)
		s.Tick(context.Background())
		if len(f.packets) != 0 || s.state.CastPending || s.state.Moving {
			t.Fatal("native maintenance resurrected a legacy CC controller", s.stateLocked(), f.packets)
		}
	}
}
