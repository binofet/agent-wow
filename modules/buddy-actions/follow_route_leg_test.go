package buddyactions

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/behaviors"
	"github.com/agent-wow/agent-wow/pkg/behavior"
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

// Dense sampled corners simulate the full ground proof returned by the existing
// planner. The owner is nearby, but the complete winding route exceeds240yd.
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

func TestFollowWindingRouteAdvancesVerifiedLegThenReplansAtObservedSpeed(t *testing.T) {
	for _, speed := range []float32{7, 7.5600004} {
		t.Run(string(rune('a'+int(speed))), func(t *testing.T) {
			s, f, advance := followLegRig(t)
			f.world.Self.RunSpeed = speed
			high := f.world.Self.Y + 65
			plans, legs := 0, 0
			s.planMapPath = func(_ context.Context, _ string, _ uint32, from, goal position) ([]position, error) {
				plans++
				path := followDetour(from, goal, high)
				if len(path) > 241 {
					return nil, errors.New("navigation route exceeds 240 yards")
				}
				return path, nil
			}
			s.planLegPath = func(ctx context.Context, _ string, _ uint32, from, goal position) ([]position, error) {
				legs++
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 251*time.Millisecond {
					t.Fatal("leg renewed planning budget")
				}
				path := followDetour(from, goal, high)
				if len(path) > 151 {
					path = path[:151]
				}
				return path, nil
			}
			sawIntermediate := false
			for tick := 0; tick < 350; tick++ {
				before, writes := s.pos, len(f.packets)
				s.Tick(context.Background())
				if len(f.packets) > writes && s.state.Moving && distance(before, s.pos) > float64(speed)*.2+.015 {
					t.Fatal("exceeded observed run speed", before, s.pos)
				}
				if s.state.Reason == "follow_route_leg_complete" {
					sawIntermediate = true
					if s.followNav.arrivalKnown || s.state.Moving {
						t.Fatal("intermediate leg claimed final arrival", s.stateLocked())
					}
				}
				advance(200 * time.Millisecond)
			}
			if legs != 1 || plans < 2 || !sawIntermediate || s.state.Moving || distance(s.pos, playerPosition(f.world.Owner)) > 8 {
				t.Fatal("normal follower failed bounded progression", plans, legs, sawIntermediate, s.pos, s.stateLocked())
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
			s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
				if mode == "partial" {
					return nil, errors.New("navigation polygon path is partial, exhausted, or unavailable")
				}
				if mode == "missing_assets" {
					return nil, errors.New("navigation tile missing")
				}
				return nil, errors.New("navigation route exceeds 240 yards")
			}
			s.planLegPath = func(_ context.Context, _ string, _ uint32, from, goal position) ([]position, error) {
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
				t.Fatal("unrelated failure used leg fallback")
			}
		})
	}
}

func TestFollowLegFinalBoundaryPreservesBodyVisitOwnerIntentAndUnknowns(t *testing.T) {
	for _, mode := range []string{"owner", "owner_pose", "body", "visit", "intent", "speed", "cast", "interaction", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance := followLegRig(t)
			boundary := false
			s.planMapPath = func(context.Context, string, uint32, position, position) ([]position, error) {
				return nil, errors.New("navigation route exceeds 240 yards")
			}
			s.planLegPath = func(_ context.Context, _ string, _ uint32, from, _ position) ([]position, error) {
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
			}
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
