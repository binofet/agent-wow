package buddyactions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func mountedLastSeenFixture(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, _, ctx, advance := targetMovementFixture(t)
	f.world.Owner.X, f.world.Owner.Y = 12, 6
	mountForTravel(t, s, f.world.Self, 23229)
	mountForTravel(t, s, f.world.Owner, 458)
	toggle(t, s, true)
	s.Tick(ctx)
	if s.lastSeenFollow.target != playerPosition(f.world.Owner) || s.state.Moving {
		t.Fatal("missing initial stationary sighting", s.lastSeenFollow, s.stateLocked())
	}
	f.world.Owner.Present = false
	return s, f, advance
}

func TestMountedLastSeenFollowsGroundCornersAndStopsAtLastSighting(t *testing.T) {
	s, f, advance := mountedLastSeenFixture(t)
	goal := s.lastSeenFollow.target
	corner := position{y: goal.y}
	s.planMapPath = func(_ context.Context, _ string, _ uint32, from, to position) ([]position, error) {
		if to != goal {
			t.Fatalf("planned toward %v, wanted last sighting %v", to, goal)
		}
		return []position{from, corner, to}, nil
	}
	touchedCorner := false
	for i := 0; i < 18; i++ {
		advance(200 * time.Millisecond)
		s.Tick(context.Background())
		if distance(s.pos, corner) < .01 {
			touchedCorner = true
		}
		if s.pos.x > .01 && !touchedCorner {
			t.Fatal("cut the ground route's turn", s.pos)
		}
		if s.state.Reason == "at_last_seen_follow_position" {
			break
		}
	}
	if !touchedCorner || countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveHeartbeat) == 0 ||
		countPackets(f, opcode.MSGMoveStop) != 1 || s.state.MotionUnconfirmed || s.state.Moving ||
		!s.state.Following || s.state.Reason != "at_last_seen_follow_position" || distance(s.pos, goal) > 1.5 {
		t.Fatal("grounded last-seen trip did not settle", s.pos, s.state.Reason, len(f.packets))
	}
}

func TestMountedLastSeenRequiresGroundRoute(t *testing.T) {
	s, f, advance := mountedLastSeenFixture(t)
	s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
		return nil, errors.New("water depth exceeds shallow wading limit")
	}
	s.planSwimPath = func(context.Context, string, uint32, buddynav.Point, buddynav.Point) ([]buddynav.SwimPoint, error) {
		t.Fatal("ground mount requested a swim route")
		return nil, nil
	}
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if len(f.packets) != 0 || s.state.Reason != "last_seen_follow_route_unavailable" {
		t.Fatal("missing dry ground route", s.state.Reason, len(f.packets))
	}
}

func TestMountedLastSeenYieldsToVisibleOwner(t *testing.T) {
	for _, state := range []string{"en route", "arrived"} {
		t.Run(state, func(t *testing.T) {
			s, f, advance := mountedLastSeenFixture(t)
			tick := func() {
				advance(200 * time.Millisecond)
				s.Tick(context.Background())
			}
			for i := 0; i < 15; i++ {
				tick()
				if s.state.Moving && state == "en route" || s.state.Reason == "at_last_seen_follow_position" {
					break
				}
			}
			if !s.swimFollow.owner.lastSeen || (state == "arrived") != (s.state.Reason == "at_last_seen_follow_position") {
				t.Fatal("expected retained last-seen ground route", state, s.state.Reason)
			}
			f.world.Owner.Present = true
			f.world.Owner.X, f.world.Owner.Y = -20, 0
			f.world.Owner.PositionRevision++
			for i := 0; i < 20 && s.pos.x >= -2; i++ {
				tick()
			}
			if s.swimFollow.owner.lastSeen || !s.state.Following || s.state.MotionUnconfirmed || s.pos.x >= -2 {
				t.Fatal("visible owner did not take over", state, s.pos, s.state.Reason)
			}
		})
	}
}

func TestMountedLastSeenUnknownStopIsNotArrival(t *testing.T) {
	s, f, advance := mountedLastSeenFixture(t)
	f.failSend, f.failSendOpcode = true, opcode.MSGMoveStop
	for i := 0; i < 16 && !s.state.MotionUnconfirmed; i++ {
		advance(200 * time.Millisecond)
		s.Tick(context.Background())
	}
	if !s.state.MotionUnconfirmed || s.state.Reason == "at_last_seen_follow_position" || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal("unconfirmed stop was recorded as arrival", s.state.Reason, len(f.packets))
	}
	written := len(f.packets)
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if len(f.packets) != written {
		t.Fatal("unconfirmed motion was replayed")
	}
}

func TestMountingAtLastSeenSwimDispatchRevokesWetRoute(t *testing.T) {
	s, f, advance := mountedLastSeenFixture(t)
	// The body is still on foot at selection. Its mounted state becomes known
	// after the route is planned, during the actual movement-clock callback.
	f.world.Self.UnitFlags &^= movementflags.Mounted
	s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
		return nil, errors.New("water depth exceeds shallow wading limit")
	}
	planned := false
	s.planSwimPath = func(_ context.Context, _ string, _ uint32, from, to buddynav.Point) ([]buddynav.SwimPoint, error) {
		planned = true
		return []buddynav.SwimPoint{{Point: from}, {Point: to, Swimming: true}}, nil
	}
	f.onClock = func() { f.world.Self.UnitFlags |= movementflags.Mounted }
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if !planned || f.world.Self.UnitFlags&movementflags.Mounted == 0 || len(f.packets) != 0 {
		t.Fatal("mounting at dispatch did not revoke wet route", planned, s.state.Reason, len(f.packets))
	}
}

func TestMountedLastSeenRechecksBeforeStarting(t *testing.T) {
	for _, change := range []string{"owner visible", "mount unproved", "taxi", "unknown motion", "explicit stop", "expired follow", "different visit", "offline owner", "foreign party session"} {
		t.Run(change, func(t *testing.T) {
			s, f, advance := mountedLastSeenFixture(t)
			switch change {
			case "owner visible":
				f.onClock = func() { f.world.Owner.Present = true }
			case "mount unproved":
				f.onClock = func() { delete(s.buffAuras, s.selfGUID) }
			case "taxi":
				f.onClock = func() { f.world.Self.UnitFlags |= 0x00100000 } // UNIT_FLAG_TAXI_FLIGHT
			case "unknown motion":
				s.state.MotionUnconfirmed = true
			case "explicit stop":
				toggle(t, s, false)
			case "expired follow":
				advance(lease)
			case "different visit":
				f.world.VisitRevision++
			case "offline owner":
				f.party.Group.Members[1].Online = false
			case "foreign party session":
				f.party.SessionId = "another-session"
			}
			advance(200 * time.Millisecond)
			s.Tick(context.Background())
			if countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || countPackets(f, opcode.MSGMoveStartSwim) != 0 {
				t.Fatal("stale or unsafe last-seen write", s.state.Reason, len(f.packets))
			}
		})
	}
}
