package buddyactions

import (
	"context"
	"os"
	"testing"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/movementinfo"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func TestNormalBodyTapSettlesElevatedTaxiEndpointThenJumps(t *testing.T) {
	root := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if root == "" {
		t.Skip("copied client navigation not configured")
	}
	s, f, _, ctx, advance := targetMovementFixture(t)
	s.navDataDir = root
	p := f.world.Self
	p.X, p.Y, p.Z, p.MapId = -5421.1006, -2928.6516, 350.9113, 0
	p.PositionRevision++
	s.bodyMotionWait = func(_ context.Context, d time.Duration) error { advance(d); return nil }
	r := &actionsv1.BodyMovementRequest{SessionId: s.sessionID, SelfGuid: s.selfGUID, VisitId: 1, SequenceId: 7, DurationMs: 100}
	out, err := s.MoveBody(ctx, r)
	if err != nil || out.GetBodyMovement().GetPhase() != "body_settled_written" || countPackets(f, opcode.MSGMoveFallLand) != 1 ||
		countPackets(f, opcode.MSGMoveJump) != 0 || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatalf("first tap: %v, %v", out.GetBodyMovement(), err)
	}
	for _, packet := range f.packets {
		i, ok := movementinfo.OwnFrame(packet.Payload, s.selfGUID)
		if !ok || i.X != p.X || i.Y != p.Y || i.Z < 346.8 || i.Z > p.Z {
			t.Fatalf("first contact changed horizontal position or exceeded floor: %#v", i)
		}
	}
	r.SequenceId++
	r.Jump = true
	out, err = s.MoveBody(ctx, r)
	if err != nil || out.GetBodyMovement().GetPhase() != "body_input_written" || countPackets(f, opcode.MSGMoveJump) != 1 || countPackets(f, opcode.MSGMoveFallLand) != 2 {
		t.Fatalf("ground input after contact: %v %v", out.GetBodyMovement(), err)
	}
}

func TestFollowAndBeforeLogoutUseOnlyCurrentPostTaxiBody(t *testing.T) {
	root := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if root == "" {
		t.Skip("copied client navigation not configured")
	}
	for _, mode := range []string{"ground after natural arrival", "unknown during fall", "logout with old position", "unconfirmed logout", "follow with old position", "wrong visit", "obsolete origin"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, _, advance := targetMovementFixture(t)
			s.navDataDir = root
			p := f.world.Self
			origin := &modv1.TransportScope{ConnectionId: "paid-flight", SelfGuid: s.selfGUID, MapId: p.MapId, VisitBound: true, VisitRevision: 7, MotionRevision: 30}
			s.flightpathActivation.operation = &actionsv1.FlightpathActivationOperation{SelfGuid: s.selfGUID, SessionId: s.sessionID, VisitId: f.world.VisitRevision, ActivationAccepted: true, ReplyKnown: true, TransportWritten: true, AcceptanceScope: cloneFlightpathScope(origin)}
			origin.MotionRevision = 69
			f.world.SelfMotionAuthority = &worldv1.OwnMotionAuthority{SelfGuid: s.selfGUID, VisitRevision: f.world.VisitRevision, TransportScope: origin, UnitFlagsKnown: true, UnitFlags: p.UnitFlags}
			p.X, p.Y, p.Z = 120, 130, 10
			f.world.Owner.X, f.world.Owner.Y, f.world.Owner.Z = -5424, -2934, 347
			s.refreshPositionLocked(f.world)
			toggle(t, s, true)
			if mode == "logout with old position" || mode == "unconfirmed logout" {
				s.state.Moving = true
				s.state.MotionUnconfirmed = mode == "unconfirmed logout"
				_, err := s.BeforeLogout(context.Background(), nil)
				if err != nil || len(f.packets) != 0 {
					t.Fatalf("logout sent the departure position: writes=%d error=%v", len(f.packets), err)
				}
				return
			}
			if mode != "follow with old position" {
				p.X, p.Y, p.Z = -5421.1006, -2928.6516, 350.9113
				p.PositionRevision++
				p.PositionInferred = true
				f.world.SelfMotionAuthority.TaxiBodyOriginMotionRevision = 66
				if mode == "wrong visit" {
					origin.VisitRevision++
				}
				if mode == "obsolete origin" {
					f.world.SelfMotionAuthority.TaxiBodyOriginMotionRevision = 29
				}
			}
			s.bodyMotionWait = func(_ context.Context, d time.Duration) error {
				advance(d)
				if mode == "unknown during fall" {
					f.world.Self.PositionRevision++
				}
				return nil
			}
			s.Tick(context.Background())
			if mode == "ground after natural arrival" {
				if countPackets(f, opcode.MSGMoveFallLand) != 1 || countPackets(f, opcode.MSGMoveStop) != 1 || !s.state.Following {
					t.Fatalf("normal follow failed to ground: %s, %d writes", s.state.Reason, len(f.packets))
				}
				for _, packet := range f.packets {
					i, good := movementinfo.OwnFrame(packet.Payload, s.selfGUID)
					if !good || i.X != p.X || i.Y != p.Y {
						t.Fatal("wrote departure or horizontal travel before contact", packet)
					}
				}
			} else if mode == "unknown during fall" {
				if !s.state.MotionUnconfirmed || len(f.packets) != 1 {
					t.Fatal("interrupted written trajectory must remain unconfirmed", s.state.Reason, len(f.packets))
				}
			} else if len(f.packets) != 0 {
				t.Fatal("unverified source reached follow", s.state.Reason, len(f.packets))
			}
		})
	}
}
