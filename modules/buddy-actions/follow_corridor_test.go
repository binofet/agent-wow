package buddyactions

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

type recordedFollowCorridor struct {
	target       buddynav.Point
	starts       []buddynav.Point
	targets      []buddynav.Point
	bindError    error
	projectDelta float32
	inside       bool
	limit        int
}

func (r *recordedFollowCorridor) ContainsGroundSegment(context.Context, buddynav.Point, buddynav.Point) bool {
	return r.inside
}
func (r *recordedFollowCorridor) FormationDestination(_ context.Context, owner, slot buddynav.Point) (buddynav.Point, error) {
	slot.Z = owner.Z + r.projectDelta
	return slot, r.bindError
}
func (r *recordedFollowCorridor) Destination() buddynav.Point { return r.target }
func (r *recordedFollowCorridor) PathAhead(_ context.Context, from, to buddynav.Point) ([]buddynav.Point, error) {
	r.starts, r.targets = append(r.starts, from), append(r.targets, to)
	r.target = to
	d := distance(navPosition(from), navPosition(to))
	limit := r.limit
	if limit == 0 {
		limit = 5
	}
	n := min(limit, int(math.Ceil(d)))
	path := []buddynav.Point{from}
	for i := 1; i <= n; i++ {
		part := float32(math.Min(1, float64(i)/d))
		path = append(path, buddynav.Point{X: from.X + (to.X-from.X)*part, Y: from.Y + (to.Y-from.Y)*part, Z: from.Z + (to.Z-from.Z)*part})
	}
	return path, nil
}

func TestOrdinaryFollowRetainsGroundStripThroughMotionAndCorrection(t *testing.T) {
	s, f, advance := formationRig(t, 1, 5)
	f.world.VisitRevision, f.world.Owner.MovementFlags = 1, 1
	f.world.Owner.X += 40
	mountForTravel(t, s, f.world.Self, 23238)
	mountForTravel(t, s, f.world.Owner, 23240)
	var plots int
	r := &recordedFollowCorridor{}
	s.planFollowCorridor = func(_ context.Context, _ string, _ uint32, from, owner buddynav.Point) (followCorridor, error) {
		if owner.X != f.world.Owner.X || owner.Y != f.world.Owner.Y {
			t.Fatal("first polygon strip was not bound to the actual owner", owner)
		}
		plots++
		return r, nil
	}
	ctx := context.Background()
	toggle(t, s, true)
	for i := 0; i < 10; i++ {
		s.Tick(ctx)
		advance(200 * time.Millisecond)
	}
	if plots != 1 || len(r.starts) < 2 || !s.state.Moving || s.state.NavReason != "client_ground_retained_corridor" || countPackets(f, opcode.MSGMoveStop) != 0 {
		t.Fatal("short physical prefixes changed the plot or stopped follow", plots, r.starts, s.stateLocked())
	}
	old := r.target
	f.world.Owner.X += 4
	f.world.Owner.PositionRevision++
	s.Tick(ctx)
	if plots != 1 || r.target == old || s.state.Reason != "following_formation" {
		t.Fatal("owner movement failed to repair the retained strip", plots, r.target, s.stateLocked())
	}
	// World reports a new grounded position after an actual displacement.
	// The previously written forward motion first owes its normal STOP.
	f.world.Self.Y += 13
	f.world.Self.PositionRevision++
	advance(200 * time.Millisecond)
	s.Tick(ctx)
	if s.state.Moving || countPackets(f, opcode.MSGMoveStop) != 1 {
		t.Fatal("authoritative displacement lost its normal stop", s.stateLocked())
	}
	for i := 0; i < 4 && !s.state.Moving; i++ {
		advance(200 * time.Millisecond)
		s.Tick(ctx)
	}
	if plots != 1 || !s.state.Moving || r.starts[len(r.starts)-1] != swimPoint(playerPosition(f.world.Self)) {
		t.Fatal("new own ground position did not reanchor the original strip", plots, r.starts, s.stateLocked())
	}
	toggle(t, s, false)
	advance(200 * time.Millisecond)
	s.Tick(ctx)
	if s.state.Moving || countPackets(f, opcode.MSGMoveStop) != 2 {
		t.Fatal("manual STOP lost custody", s.stateLocked())
	}
	toggle(t, s, true)
	for i := 0; i < 5 && plots == 1; i++ {
		advance(200 * time.Millisecond)
		s.Tick(ctx)
	}
	if plots != 2 {
		t.Fatal("new follow intent reused a retired strip", plots)
	}
}

func TestRetainedFollowGroundUsesOnlyAnUnboundSlotForOwnerFallback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		writes bool
		nav    string
	}{
		{"projected", nil, true, "client_ground_retained_corridor"},
		{"truly unbound", buddynav.ErrGroundDestination, true, "client_ground_retained_corridor_owner"},
		{"missing data", errors.New("missing mesh layer"), false, "missing mesh layer"},
		{"canceled", context.Canceled, false, "context canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, advance := formationRig(t, 1, 5)
			f.world.VisitRevision = 1
			f.world.Owner.X += 25
			r := &recordedFollowCorridor{bindError: tc.err, projectDelta: 1.75}
			s.planFollowCorridor = func(context.Context, string, uint32, buddynav.Point, buddynav.Point) (followCorridor, error) {
				return r, nil
			}
			toggle(t, s, true)
			for i := 0; i < 5; i++ {
				s.Tick(context.Background())
				advance(200 * time.Millisecond)
			}
			if (countPackets(f, opcode.MSGMoveStartForward) != 0) != tc.writes || s.state.NavReason != tc.nav {
				t.Fatal(tc.name, s.stateLocked())
			}
			if tc.writes && (r.target == swimPoint(playerPosition(f.world.Owner))) != errors.Is(tc.err, buddynav.ErrGroundDestination) {
				t.Fatal("wrong destination choice", r.target)
			}
		})
	}
}

func TestRetainedFollowPreservesCurrentLastSightingAndUnknown(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		s, f, advance := mountedLastSeenFixture(t)
		goal := swimPoint(s.lastSeenFollow.target)
		plots := 0
		r := &recordedFollowCorridor{}
		s.planFollowCorridor = func(_ context.Context, _ string, _ uint32, _, target buddynav.Point) (followCorridor, error) {
			if plots == 0 && target != goal {
				t.Fatal("wrong last-sighting target", target)
			}
			plots++
			return r, nil
		}
		if uncertain {
			f.failSend, f.failSendOpcode = true, opcode.MSGMoveStop
		}
		for i := 0; i < 30 && !s.state.MotionUnconfirmed && s.state.Reason != "at_last_seen_follow_position"; i++ {
			advance(200 * time.Millisecond)
			s.Tick(context.Background())
		}
		if plots != 1 || r.target != goal || s.state.NavReason != "client_ground_retained_corridor_last_seen" || countPackets(f, opcode.MSGMoveStop) != 1 {
			t.Fatal("missing witnessed ground route/stop", plots, s.stateLocked())
		}
		if uncertain {
			before := len(f.packets)
			advance(200 * time.Millisecond)
			s.Tick(context.Background())
			if !s.state.MotionUnconfirmed || s.state.Reason == "at_last_seen_follow_position" || len(f.packets) != before {
				t.Fatal("ambiguous stop became arrival or replay", s.stateLocked())
			}
			continue
		}
		f.world.Owner.Present, f.world.Owner.X = true, -24
		f.world.Owner.PositionRevision++
		for i := 0; i < 15 && s.state.NavReason != "client_ground_retained_corridor"; i++ {
			advance(200 * time.Millisecond)
			s.Tick(context.Background())
		}
		if plots != 1 || r.target == goal || s.state.NavReason != "client_ground_retained_corridor" {
			t.Fatal("reacquisition did not repair the present body's retained strip", plots, r.target, s.stateLocked())
		}
	}
}

func TestRetainedFollowHazardCanUseASeparateVerifiedStrip(t *testing.T) {
	for _, inside := range []bool{true, false} {
		s, _, _, target := followNavRig(t)
		target.x = s.pos.x + 6
		original := &recordedFollowCorridor{inside: inside}
		plots := 0
		s.planFollowCorridor = func(context.Context, string, uint32, buddynav.Point, buddynav.Point) (followCorridor, error) {
			plots++
			return &recordedFollowCorridor{limit: 32}, nil
		}
		path, err := s.followAvoidancePath(context.Background(), original, s.pos, target)
		if err != nil || len(path) < 2 || distance(path[len(path)-1], target) > .01 || (plots == 0) != inside {
			t.Fatal("failed to verify the hazard's own ground route", inside, plots, path, err)
		}
		if original.target != (buddynav.Point{}) {
			t.Fatal("hazard replaced the follow destination", original.target)
		}
	}
}
