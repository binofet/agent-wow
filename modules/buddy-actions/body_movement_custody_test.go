package buddyactions

import (
	"context"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBodyInputRetainsCustodyAgainstInterleavedTradeAcquireReleaseAndStop(t *testing.T) {
	s, f, _, ctx, advance := targetMovementFixture(t)
	s.state.Following = true
	s.followUntil = s.now().Add(time.Minute)
	var acquisition, release error
	var held *actionsv1.InteractionLease
	var stopWritten, stopChangedMotion bool
	s.bodyMotionPlan = func(_ context.Context, _ string, _ uint32, from buddynav.Point, _, _ float64, _ time.Duration, _ bool) ([]buddynav.BodyMotionSample, error) {
		// Real Mira race: planning releases the mutex before any hop packet.
		held, acquisition = s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "trade"})
		end := from
		end.X += 1.4
		return []buddynav.BodyMotionSample{{Point: from}, {At: 850 * time.Millisecond, Point: end}}, nil
	}
	s.bodyMotionWait = func(_ context.Context, duration time.Duration) error {
		advance(duration)
		token := "stale-trade-token"
		if held != nil {
			token = held.Token
		}
		_, release = s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "trade", Token: token})
		s.mu.Lock()
		stopWritten = s.stopLocked(context.Background())
		stopChangedMotion = !s.state.Moving || s.state.MotionUnconfirmed || !s.state.Following
		s.mu.Unlock()
		return nil
	}
	out, err := s.MoveBody(ctx, &actionsv1.BodyMovementRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID,
		VisitId: 1, SequenceId: 106586, Jump: true})
	if err != nil || out.GetBodyMovement().GetPhase() != "body_input_written" {
		t.Fatal("normal finite hop did not finish", out, err)
	}
	if status.Code(acquisition) != codes.FailedPrecondition || held != nil || status.Code(release) != codes.FailedPrecondition || stopWritten || stopChangedMotion {
		t.Fatal("competing trade/stop stole the body or fabricated uncertain movement", acquisition, release, stopWritten, stopChangedMotion)
	}
	if s.state.InteractionOwner != "" || s.state.MotionUnconfirmed || !s.state.Following || !s.clientPositionLocked().Known ||
		s.pos.x != 1.4 || f.world.Self.X != 0 || countPackets(f, opcode.MSGMoveFallLand) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal("completed packet-proven landing/stop was lost to raw spawn pose or a false latch", s.stateLocked(), f.world.Self)
	}
	// Once the bounded input is complete, ordinary trade custody is available.
	lease, err := s.AcquireInteraction(context.Background(), &actionsv1.InteractionRequest{Owner: "trade"})
	if err != nil || lease.Token == "" {
		t.Fatal("completed body input permanently blocked trade", lease, err)
	}
	if _, err := s.ReleaseInteraction(context.Background(), &actionsv1.InteractionRelease{Owner: "trade", Token: lease.Token}); err != nil {
		t.Fatal(err)
	}
}

func TestStopDistinguishesKnownPrewriteRejectionFromAmbiguousSend(t *testing.T) {
	for _, mode := range []string{"body_input", "clock", "invalid_world", "cancelled_context", "transport"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := rig(t)
			s.sessionID = f.party.SessionId
			s.refreshPositionLocked(f.world)
			s.state.Following, s.state.Assisting = true, true
			if !s.movementLocked(context.Background(), opcode.MSGMoveStartForward, position{x: 1.4}, true) {
				t.Fatal("initial known movement missing")
			}
			ctx := context.Background()
			switch mode {
			case "body_input":
				s.bodyMoving = true
			case "clock":
				f.failClock = true
			case "invalid_world":
				f.world.Self.Present = false
			case "cancelled_context":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "transport":
				f.failSend = true
			}
			before := len(f.packets)
			if s.stopLocked(ctx) {
				t.Fatal("fixture's failed stop was reported as written")
			}
			if mode == "transport" {
				if !s.state.MotionUnconfirmed || s.state.Moving || s.state.Following || s.state.Assisting || len(f.packets) != before+1 {
					t.Fatal("real ambiguous SendPacket was not fenced", s.stateLocked(), len(f.packets)-before)
				}
				if s.stopLocked(context.Background()) || len(f.packets) != before+1 {
					t.Fatal("unknown stop was replayed without new authority")
				}
				return
			}
			if s.state.MotionUnconfirmed || !s.state.Moving || !s.state.Following || !s.state.Assisting || len(f.packets) != before || s.pos.x != 1.4 {
				t.Fatal("undispatched stop revoked standing intent or invalidated actual prior movement", s.stateLocked(), len(f.packets)-before)
			}
			if mode != "invalid_world" && !s.clientPositionLocked().Known {
				t.Fatal("known written position was hidden after a no-write stop")
			}
			s.bodyMoving, f.failClock, f.world.Self.Present = false, false, true
			if !s.stopLocked(context.Background()) || s.state.Moving || s.state.MotionUnconfirmed || len(f.packets) != before+1 {
				t.Fatal("fresh ordinary stop could not finish the still-known movement", s.stateLocked(), f.packets)
			}
		})
	}
}
