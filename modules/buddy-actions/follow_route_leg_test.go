package buddyactions

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/buddynav"
	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func followLegRig(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := formationRig(t, 4, 8)
	f.world.VisitRevision = 1
	f.world.Owner.X += 152
	r, err := behavior.NewWithOptions(behavior.SessionOptions{ExecutionLimit: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Reload(behaviors.FollowFormationSource, ""); err != nil {
		t.Fatal(err)
	}
	s.formation.runtime = r
	toggle(t, s, true)
	return s, f, advance
}

// Each test controls the sampled physical prefix on the same retained surface.
type followLegTestSurface struct {
	recordedFollowCorridor
	prefix func(context.Context, position, position) ([]position, error)
}

func (r *followLegTestSurface) PathAhead(ctx context.Context, from, goal buddynav.Point) ([]buddynav.Point, error) {
	r.target = goal
	path, err := r.prefix(ctx, navPosition(from), navPosition(goal))
	if err != nil {
		return nil, err
	}
	out := make([]buddynav.Point, len(path))
	for i, point := range path {
		out[i] = swimPoint(point)
	}
	return out, nil
}

func testFollowPlanner(surface followCorridor) followCorridorPlanner {
	return func(context.Context, string, uint32, buddynav.Point, buddynav.Point) (followCorridor, error) {
		return surface, nil
	}
}

func bindFollowTestOwner(t *testing.T, s *Service, f *fakeSession, owner position) {
	t.Helper()
	f.world.VisitRevision = 1
	f.world.Owner.X, f.world.Owner.Y, f.world.Owner.Z = owner.x, owner.y, owner.z
	toggle(t, s, true)
	if _, err := s.snapshot(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// Dense sampled corners simulate the full ground proof returned by the existing
// planner. The nearby owner requires a winding route longer than 240 yards.
func followDetour(from, goal position, high float32) []position {
	path := []position{from}
	appendTo := func(to position) {
		leg := denseRouteLeg(path[len(path)-1], to)
		path = append(path, leg[1:]...)
	}
	if from.y < high && from.x < goal.x-1 {
		appendTo(position{x: from.x, y: high, z: from.z})
	}
	if from.x < goal.x-1 {
		appendTo(position{x: goal.x, y: high, z: goal.z})
	}
	appendTo(goal)
	return path
}

func TestFollowWindingRouteContinuesOnTheSameStripAtObservedSpeed(t *testing.T) {
	for _, speed := range []float32{7, 7.5600004} {
		t.Run(string(rune('a'+int(speed))), func(t *testing.T) {
			s, f, advance := followLegRig(t)
			f.world.Self.RunSpeed = speed
			high := f.world.Self.Y + 65
			plots, prefixes := 0, 0
			var endpoint position
			continued := false
			surface := &followLegTestSurface{}
			surface.prefix = func(_ context.Context, from, goal position) ([]position, error) {
				prefixes++
				path := followDetour(from, goal, high)
				if len(path) > 151 {
					path = path[:151]
				}
				if prefixes == 1 {
					endpoint = path[len(path)-1]
				} else if !continued && distance(from, endpoint) < 1.6 {
					if !s.state.Moving || countPackets(f, opcode.MSGMoveStop) != 0 {
						t.Fatal("prefix endpoint lost the continuing movement", from, endpoint, s.stateLocked())
					}
					continued = true
				}
				return path, nil
			}
			s.planFollowCorridor = func(context.Context, string, uint32, buddynav.Point, buddynav.Point) (followCorridor, error) {
				plots++
				return surface, nil
			}
			for tick := 0; tick < 350; tick++ {
				before, writes := s.pos, len(f.packets)
				s.Tick(context.Background())
				if len(f.packets) > writes && s.state.Moving && distance(before, s.pos) > float64(speed)*.2+.015 {
					t.Fatal("exceeded observed run speed", before, s.pos)
				}
				advance(200 * time.Millisecond)
			}
			if plots != 1 || prefixes < 2 || !continued || s.state.Moving || distance(s.pos, playerPosition(f.world.Owner)) > 8 {
				t.Fatal("normal follower failed bounded progression", plots, prefixes, continued, s.pos, s.stateLocked())
			}
			if f.world.Self.RunSpeed != speed || !s.state.Following {
				t.Fatal("changed observed speed or follow intent")
			}
		})
	}
}

func TestFollowLegRejectsUnprovenPlannerResultsAndOtherFailures(t *testing.T) {
	for _, mode := range []string{"partial", "missing_assets", "missing", "nan", "gap", "height", "length", "no_progress", "cancel", "not_follow"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := followLegRig(t)
			calls := 0
			surface := &followLegTestSurface{prefix: func(_ context.Context, from, goal position) ([]position, error) {
				calls++
				end := from
				end.y += 100
				path := denseRouteLeg(from, end)
				switch mode {
				case "missing":
					return nil, nil
				case "nan":
					path[5].x = float32(math.NaN())
				case "gap":
					path[5].x += 5
				case "height":
					path[5].z += 1.1
				case "length":
					end.y += 81
					path = denseRouteLeg(from, end)
				case "no_progress":
					path = []position{from, from}
				case "cancel":
					return nil, context.Canceled
				}
				return path, nil
			}}
			s.planFollowCorridor = func(context.Context, string, uint32, buddynav.Point, buddynav.Point) (followCorridor, error) {
				switch mode {
				case "partial":
					return nil, errors.New("navigation polygon path is partial, exhausted, or unavailable")
				case "missing_assets":
					return nil, errors.New("navigation tile missing")
				}
				return surface, nil
			}
			if mode == "not_follow" {
				toggle(t, s, false)
			}
			for range 5 {
				s.Tick(context.Background())
				advance(200 * time.Millisecond)
			}
			if s.state.Moving || countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal("unproved leg moved", mode, s.stateLocked())
			}
			if (mode == "partial" || mode == "missing_assets" || mode == "not_follow") && calls != 0 {
				t.Fatal("unavailable or disabled route requested a prefix")
			}
		})
	}
}

func TestFollowLegFinalBoundaryPreservesBodyVisitOwnerIntentAndUnknowns(t *testing.T) {
	for _, mode := range []string{"owner", "owner_pose", "body", "visit", "intent", "speed", "cast", "interaction", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := followLegRig(t)
			boundary := false
			s.planFollowCorridor = testFollowPlanner(&followLegTestSurface{prefix: func(_ context.Context, from, _ position) ([]position, error) {
				to := from
				to.y += 100
				f.onClock = func() {
					boundary = true
					switch mode {
					case "owner":
						f.world.Owner.Guid = 99
					case "owner_pose":
						f.world.Owner.Y += 20
					case "body":
						f.world.Self.Guid = 99
					case "visit":
						f.world.VisitRevision++
					case "intent":
						s.state.Following = false
						s.followRevision++
					case "speed":
						f.world.Self.SpeedsKnown = false
					case "cast":
						s.state.CastPending = true
					case "interaction":
						s.state.InteractionOwner = "existing"
					case "unknown":
						s.state.MotionUnconfirmed = true
					}
				}
				return denseRouteLeg(from, to), nil
			}})
			for range 5 {
				s.Tick(context.Background())
				if boundary {
					break
				}
				advance(200 * time.Millisecond)
			}
			if !boundary {
				t.Fatal("did not reach final movement boundary")
			}
			if countPackets(f, opcode.MSGMoveStartForward) != 0 || countPackets(f, opcode.MSGMoveHeartbeat) != 0 {
				t.Fatal("stale leg sent movement", mode, f.packets)
			}
			if mode == "unknown" && !s.state.MotionUnconfirmed {
				t.Fatal("cleared unknown movement")
			}
			if mode == "cast" && !s.state.CastPending {
				t.Fatal("cleared pending cast")
			}
		})
	}
}

func startFollowTestLeg(t *testing.T) (*Service, *fakeSession, func(time.Duration)) {
	t.Helper()
	s, f, advance := followLegRig(t)
	f.world.Self.RunSpeed = 7.5600004
	s.planFollowCorridor = func(context.Context, string, uint32, buddynav.Point, buddynav.Point) (followCorridor, error) {
		return &recordedFollowCorridor{limit: 100}, nil
	}
	for range 5 {
		s.Tick(context.Background())
		if s.state.Moving && s.state.NavReason == "client_ground_retained_corridor" {
			return s, f, advance
		}
		advance(200 * time.Millisecond)
	}
	t.Fatal("leg never started")
	return nil, nil, nil
}

func TestFollowCachedLegOwnerDriftAtWriteStopsThenRepairs(t *testing.T) {
	s, f, advance := startFollowTestLeg(t)
	before, beats := s.pos, countPackets(f, opcode.MSGMoveHeartbeat)
	advance(200 * time.Millisecond)
	// The old route was chosen before the final movement clock. A moved
	// owner must revoke that write, then the next tick can repair the strip.
	f.onClock = func() {
		f.onClock = nil
		f.world.Owner.Y += 20
		f.world.Owner.PositionRevision++
	}
	s.Tick(context.Background())
	if countPackets(f, opcode.MSGMoveHeartbeat) != beats || s.pos != before || s.state.Moving {
		t.Fatal("wrote past owner change", s.pos, s.stateLocked())
	}
	advance(200 * time.Millisecond)
	s.Tick(context.Background())
	if !s.state.Moving || s.followGround.owner != swimPoint(playerPosition(f.world.Owner)) {
		t.Fatal("did not rebind the fresh observation", s.stateLocked())
	}
}

func TestFollowLegLateObservedSlowdownCannotWriteFasterHeartbeat(t *testing.T) {
	s, f, advance := startFollowTestLeg(t)
	before, beats := s.pos, countPackets(f, opcode.MSGMoveHeartbeat)
	advance(200 * time.Millisecond)
	f.onClock = func() { f.world.Self.RunSpeed = 3.5 }
	s.Tick(context.Background())
	if countPackets(f, opcode.MSGMoveHeartbeat) != beats || s.pos != before || s.state.Moving {
		t.Fatal("late slowdown used old faster elapsed budget", s.pos, s.stateLocked())
	}
	f.onClock = nil
	// A genuinely fresh later decision may plan/advance at the new speed.
	for range 6 {
		advance(200 * time.Millisecond)
		p := s.pos
		s.Tick(context.Background())
		if distance(p, s.pos) > 3.5*.2+.015 {
			t.Fatal("new decision exceeded observed reduced speed", p, s.pos)
		}
	}
}
