package buddyactions

import (
	"context"
	"math"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func targetMovementFixture(t *testing.T) (*Service, *fakeSession, *actionsv1.ApproachTargetRequest, context.Context, func(time.Duration)) {
	t.Helper()
	s, f, advance := rig(t)
	s.sessionID = f.party.SessionId
	s.navDataDir = "/test/nav"
	f.world.RequestedCreature = &worldv1.CreatureState{Guid: enemyGUID, Present: true, MapKnown: true, PositionKnown: true, X: 20}
	activateEncounterWriter(t, s, f)
	ctx := modcb.WithPolicySpell(context.Background(), testEncounterKey)
	ctx = modcb.WithPolicyEncounterAccess(ctx, testEncounterKey, 1)
	md, _ := metadata.FromOutgoingContext(ctx)
	ctx = metadata.NewIncomingContext(context.Background(), md)
	return s, f, &actionsv1.ApproachTargetRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, TargetGuid: enemyGUID, VisitId: 1, RangeYards: 4.5}, ctx, advance
}

func TestTargetMovementChasesWhileAutoswingAndDeadOwnerPersist(t *testing.T) {
	s, f, r, ctx, advance := targetMovementFixture(t)
	f.world.Owner.Alive = false
	f.world.Owner.Health = 0
	f.world.RequestedCreature.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 2000, AgeMs: 1000, DestinationKnown: true, DestinationX: 30}
	s.state.AutoAttacking = true
	s.state.AutoAttackTargetGuid = enemyGUID
	s.autoTarget = enemyGUID
	st, err := s.ApproachTarget(ctx, r)
	if err != nil || !st.Moving || !st.AutoAttacking || st.TargetMovement.Phase != "approaching" {
		t.Fatalf("first chase: %v, %v", st, err)
	}
	advance(200 * time.Millisecond)
	st, err = s.ApproachTarget(ctx, r)
	if err != nil || math.Abs(float64(s.pos.x)-1.4) > .001 || !st.AutoAttacking || s.autoTarget != enemyGUID {
		t.Fatalf("moving chase: %v, %v, pos%+v", st, err, s.pos)
	}
	// Formation following cannot steer this exact route back toward the owner.
	s.state.Following = true
	s.followUntil = s.now().Add(time.Minute)
	s.Tick(context.Background())
	for _, packet := range f.packets {
		if packet.Opcode == opcode.CMSGAttackstop {
			t.Fatal("chasing or dead owner stopped autoswing")
		}
	}
	if !s.state.Moving || s.targetMovement.nav.target.x <= 10 {
		t.Fatalf("follow replaced chase route: %+v %+v", s.state, s.targetMovement)
	}
}

func TestTargetMovementUsesLosRouteThenStopsAndFacesWithoutStoppingAttack(t *testing.T) {
	s, f, r, ctx, advance := targetMovementFixture(t)
	f.world.RequestedCreature.X = 4
	r.RangeYards = 30
	r.RequireLos = true
	clear := false
	s.crowdControlSight = func(context.Context, string, uint32, position, position) (bool, error) { return clear, nil }
	s.state.AutoAttacking = true
	s.autoTarget = enemyGUID
	st, err := s.ApproachTarget(ctx, r)
	if err != nil || !st.Moving || st.TargetMovement.Ready {
		t.Fatalf("blocked LoS did not approach: %v %v", st, err)
	}
	clear = true
	advance(200 * time.Millisecond)
	st, err = s.ApproachTarget(ctx, r)
	if err != nil || st.Moving || !st.TargetMovement.Ready || !st.AutoAttacking {
		t.Fatalf("arrival: %v %v", st, err)
	}
	for _, packet := range f.packets {
		if packet.Opcode == opcode.CMSGAttackstop {
			t.Fatal("positioning cancelled combat")
		}
	}
}

func TestTargetMovementResolvesDeadPlayersAndCorpsesAndRejectsChangedVisit(t *testing.T) {
	for _, corpse := range []bool{false, true} {
		t.Run(map[bool]string{false: "dead player", true: "corpse"}[corpse], func(t *testing.T) {
			s, f, r, ctx, _ := targetMovementFixture(t)
			r.TargetGuid = 3
			f.world.Owner.Alive = false
			f.world.Owner.Health = 0
			if corpse {
				r.TargetGuid = 0xf101000000000123
				f.world.PlayerCorpses = []*worldv1.CorpseState{{Guid: r.TargetGuid, OwnerGuid: 3, Present: true, MapKnown: true, PositionKnown: true, X: 10}}
			}
			st, err := s.ApproachTarget(ctx, r)
			if err != nil || !st.Moving {
				t.Fatalf("dead ally approach: %v %v", st, err)
			}
			n := len(f.packets)
			f.world.VisitRevision++
			if _, err = s.ApproachTarget(ctx, r); status.Code(err) != codes.FailedPrecondition || len(f.packets) != n {
				t.Fatalf("stale visit wrote: %v", err)
			}
		})
	}
}

func TestTargetMovementBehindUsesRearGoalAndFiniteMovingSpline(t *testing.T) {
	s, f, r, ctx, _ := targetMovementFixture(t)
	r.Behind = true
	f.world.RequestedCreature.X = 10
	if _, err := s.ApproachTarget(ctx, r); err != nil {
		t.Fatal(err)
	}
	if s.targetMovement.nav.target.x >= 10 || !targetMovementBehind(s.targetMovement.nav.target, position{x: 10}) {
		t.Fatalf("not a rear goal: %+v", s.targetMovement.nav.target)
	}
	c := &worldv1.CreatureState{PositionKnown: true, X: 10, Trajectory: &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, AgeMs: 500, DestinationKnown: true, DestinationX: 20, DestinationY: 10}}
	p, known, estimated := creaturePosition(c)
	if !known || !estimated || p.x != 15 || p.y != 5 {
		t.Fatalf("current spline pose: %+v %v %v", p, known, estimated)
	}
	cc, known, ready := crowdControlPose(c)
	if !known || !ready || cc != p {
		t.Fatal("valid moving target refused CC", cc, known, ready)
	}
	c.Trajectory.DestinationX = float32(math.NaN())
	if _, known, _ := creaturePosition(c); known {
		t.Fatal("nonfinite spline admitted")
	}
}

func TestTargetMovementUncertainStopPreservesIntentsAndFreshPoseResumes(t *testing.T) {
	s, f, _, _, _ := targetMovementFixture(t)
	s.state.Following, s.state.Assisting = true, true
	s.followUntil, s.assistUntil = s.now().Add(time.Minute), s.now().Add(time.Minute)
	s.pos, s.posKnown, s.positionRevision, s.posMap = playerPosition(f.world.Self), true, f.world.Self.PositionRevision, f.world.Self.MapId
	s.state.MotionUnconfirmed = true
	s.Tick(context.Background())
	if !s.state.Following || !s.state.Assisting || !s.state.MotionUnconfirmed {
		t.Fatal("uncertain stop revoked standing intent", s.stateLocked())
	}
	f.world.Self.PositionRevision++
	s.Tick(context.Background())
	if s.state.MotionUnconfirmed || !s.state.Following || !s.state.Assisting {
		t.Fatal("fresh pose did not resume standing intent", s.stateLocked())
	}
}

func TestTargetMovementPolicySapAcceptsCurrentMovingPoseWithFollowIntent(t *testing.T) {
	s, f, r := policySapFixture(t)
	activateEncounterWriter(t, s, f)
	s.state.Following, s.state.Buffing = true, true
	s.controlTemplates = map[uint32]controlTemplate{555: {creatureType: 7}}
	target := f.lookupCreatures[sheepTarget]
	target.X = 20
	target.Trajectory = &worldv1.CreatureTrajectory{Known: true, DurationMs: 1000, AgeMs: 900, DestinationKnown: true, DestinationX: 2}
	ctx := modcb.WithPolicySpell(context.Background(), testEncounterKey)
	ctx = modcb.WithPolicyEncounterAccess(ctx, testEncounterKey, 1)
	md, _ := metadata.FromOutgoingContext(ctx)
	ctx = metadata.NewIncomingContext(context.Background(), md)
	st, err := s.Sap(ctx, r)
	if err != nil || !st.GetLastSpellOperation().GetTransportWritten() || countPackets(f, opcode.CMSGCastSpell) != 1 {
		t.Fatalf("follow intent or moving old start blocked Sap: %v %v", st, err)
	}
}
