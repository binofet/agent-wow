package buddyactions

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestBodyMovementSlowHopResumesActualFormationTick(t *testing.T) {
	s, f, _, ctx, advance := targetMovementFixture(t)
	f.world.Owner.Health = f.world.Owner.MaxHealth
	f.world.Owner.X = 20
	s.state.Following = true
	s.followUntil = s.now().Add(time.Minute)
	s.lastStep = s.now()
	originalRevision := f.world.Self.PositionRevision
	started := s.now()
	s.bodyMotionPlan = func(_ context.Context, _ string, _ uint32, from buddynav.Point, _, _ float64, _ time.Duration, _ bool) ([]buddynav.BodyMotionSample, error) {
		advance(800 * time.Millisecond)
		to := from
		to.X += 1.4
		return []buddynav.BodyMotionSample{{Point: from}, {At: 850 * time.Millisecond, Point: to}}, nil
	}
	s.bodyMotionWait = func(_ context.Context, duration time.Duration) error {
		advance(duration)
		// The ordinary controller runs while the host's synchronous input is
		// in flight; it must not steal the body or expire that input's lease.
		s.Tick(context.Background())
		return nil
	}
	out, err := s.MoveBody(ctx, &actionsv1.BodyMovementRequest{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1,
		SequenceId: 17, DurationMs: 200, Jump: true,
	})
	if err != nil || out.GetBodyMovement().GetPhase() != "body_input_written" || s.now().Sub(started) <= time.Second {
		t.Fatalf("slow physical input did not complete: %v, %v, elapsed %v", out, err, s.now().Sub(started))
	}
	if !s.hostEncounterActiveLocked() || s.state.MotionUnconfirmed || !s.state.Following || s.lastStep != s.now() {
		t.Fatal("completed hop lost ownership, standing follow or the fresh checkpoint", s.stateLocked())
	}
	startsBeforeFollow := countPackets(f, opcode.MSGMoveStartForward)
	for i := 0; i < 6; i++ {
		advance(200 * time.Millisecond)
		if _, err := s.RenewPolicyEncounterLease(encounterHostContext(testEncounterKey, 1), wrapperspb.UInt64(1)); err != nil {
			t.Fatal(err)
		}
		s.Tick(context.Background())
		if s.state.Reason == "follow_tick_late" || !s.state.Following || s.state.CastPending {
			t.Fatal("follow after a bounded hop used its pre-hop checkpoint or unrelated action", s.stateLocked())
		}
	}
	if countPackets(f, opcode.MSGMoveStartForward) <= startsBeforeFollow || !s.state.Moving {
		t.Fatal("normal formation did not resume after the hop", s.stateLocked())
	}
	if f.world.Self.PositionRevision != originalRevision {
		t.Fatal("physical movement replaced the authoritative correction revision", f.world.Self)
	}
	for _, publication := range f.movementObservations {
		if publication.Valid && publication.ServerPositionRevision != originalRevision {
			t.Fatal("client motion publication changed the authoritative correction baseline", publication)
		}
	}
}
