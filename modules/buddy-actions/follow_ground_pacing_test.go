package buddyactions

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

// Sample a continuous walkable ramp with a different local grade each yard.
// Both the goal and its complete route change as the mounted leader advances.
func TestMountedFollowPacesVerifiedGroundWithMovingOwner(t *testing.T) {
	s, f, advance := formationRig(t, 1, 5)
	ctx := context.Background()
	start := playerPosition(f.world.Self)
	height := func(x float32) float32 {
		d := float64(x - start.x)
		return start.z + float32(-0.28*d+0.055*math.Sin(1.7*d))
	}
	s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) {
		n := int(math.Ceil(followHorizontalDistance(a, b)))
		path := []position{a}
		for i := 1; i <= n; i++ {
			part := float32(i) / float32(n)
			x := a.x + part*(b.x-a.x)
			path = append(path, position{x: x, y: a.y + part*(b.y-a.y), z: height(x)})
		}
		return path, nil
	}
	mountForTravel(t, s, f.world.Self, 23238)
	mountForTravel(t, s, f.world.Owner, 23240)
	f.world.Owner.MovementFlags = 1
	transport := &delayedExplorationSession{fakeSession: f, now: s.now, epoch: s.now()}
	s.session = transport
	toggle(t, s, true)
	elapsed, periodic := time.Duration(0), time.Duration(0)
	for elapsed < 5*time.Second {
		f.world.Owner.X = start.x + 30 + float32(elapsed.Seconds()*14)
		f.world.Owner.Z = height(f.world.Owner.X)
		f.world.Owner.PositionRevision++
		early := s.runTick(ctx)
		if periodic <= elapsed {
			periodic += 200 * time.Millisecond
		}
		delay := periodic - elapsed
		if early > 0 && early < delay {
			delay = early
		}
		advance(delay)
		elapsed += delay
	}
	if countPackets(f, opcode.MSGMoveStartForward) != 1 || countPackets(f, opcode.MSGMoveStop) != 0 || !s.state.Moving || s.state.MotionUnconfirmed {
		t.Fatal("follow interrupted while the mounted leader was advancing", s.stateLocked())
	}
	first, last := -1, -1
	travelled := 0.0
	var previous position
	for i, packet := range f.packets {
		switch packet.Opcode {
		case opcode.MSGMoveStartForward:
			first, last = i, i
			previous = explorationPacketPosition(t, packet.Payload)
		case opcode.MSGMoveHeartbeat:
			p := explorationPacketPosition(t, packet.Payload)
			d := distance(previous, p)
			interval := transport.times[i].Sub(transport.times[last]).Seconds()
			if d > 14*interval+0.002 || math.Abs(float64(p.z-previous.z)) > 2 {
				t.Fatalf("packet exceeded its observed travel budget: %.4f yd in %.4fs", d, interval)
			}
			travelled += d
			previous, last = p, i
		}
	}
	if first < 0 || last <= first {
		t.Fatal("missing physical ground reports")
	}
	speed := travelled / transport.times[last].Sub(transport.times[first]).Seconds()
	if speed < 13.5 || speed > 14.01 {
		t.Fatalf("wrote %.3f yd at %.3f yd/s for a mounted 14 yd/s body", travelled, speed)
	}
	toggle(t, s, false)
	if countPackets(f, opcode.MSGMoveStop) != 1 || s.state.Moving {
		t.Fatal("manual stop failed", s.stateLocked())
	}
}

func TestDelayedGroundFollowSplitsVerifiedSlopeAtPacketHeightLimit(t *testing.T) {
	s, f, advance, target := followNavRig(t)
	mountForTravel(t, s, f.world.Self, 23238)
	start := s.pos
	target.x, target.z = start.x+30, start.z-15
	s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
		path := []position{from}
		for i := 1; i <= 30; i++ {
			path = append(path, position{x: start.x + float32(i), y: start.y, z: start.z - float32(i)*.5})
		}
		return path, nil
	}
	ctx := context.Background()
	if !s.followFormationStepLocked(ctx, target, .25, target) {
		t.Fatal("failed to start verified slope", s.stateLocked())
	}
	for _, gap := range []time.Duration{500 * time.Millisecond, 600 * time.Millisecond, 500 * time.Millisecond} {
		before := s.pos
		advance(gap)
		if !s.followFormationStepLocked(ctx, target, .25, target) || !s.state.Moving || s.state.MotionUnconfirmed {
			t.Fatal("valid downhill prefix stopped", s.stateLocked())
		}
		dz := before.z - s.pos.z
		if dz > 2 || dz < 1.99 || distance(before, s.pos) > 14*gap.Seconds()+.001 || countPackets(f, opcode.MSGMoveStop) != 0 {
			t.Fatal("bad delayed descent", before, s.pos, s.stateLocked())
		}
	}
	advance(601 * time.Millisecond)
	if s.followFormationStepLocked(ctx, target, .25, target) || s.state.Moving || countPackets(f, opcode.MSGMoveStop) != 1 || s.state.Reason != "follow_tick_late" {
		t.Fatal("overdue follow lost its one ordinary stop", s.stateLocked())
	}
}

func TestGroundFollowKeepsUnsupportedJumpAndTurnsBounded(t *testing.T) {
	t.Run("unsupported single edge", func(t *testing.T) {
		s, f, advance, target := followNavRig(t)
		mountForTravel(t, s, f.world.Self, 23238)
		target.x, target.z = s.pos.x+1, s.pos.z-10
		ctx := context.Background()
		if !s.followFormationStepLocked(ctx, target, .25, target) {
			t.Fatal(s.state.Reason)
		}
		start := s.pos
		advance(500 * time.Millisecond)
		if s.followFormationStepLocked(ctx, target, .25, target) || s.state.Moving || countPackets(f, opcode.MSGMoveHeartbeat) != 0 || distance(s.pos, start) != 0 || s.state.Reason != "follow_navigation_invalid_step" {
			t.Fatal("unverified edge was subdivided into ground movement", s.stateLocked())
		}
	})
	t.Run("outgoing facing at turn", func(t *testing.T) {
		s, f, advance, target := followNavRig(t)
		corner := s.pos
		corner.x++
		target.x, target.y = corner.x, corner.y+5
		s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
			return []position{from, corner, to}, nil
		}
		ctx := context.Background()
		s.followFormationStepLocked(ctx, target, .25, target)
		advance(143 * time.Millisecond)
		if !s.followFormationStepLocked(ctx, target, .25, target) || countPackets(f, opcode.MSGMoveHeartbeat) != 1 || distance(s.pos, corner) > .001 || math.Abs(float64(s.pos.o)-math.Pi/2) > .001 {
			t.Fatal("did not write the corner with its outgoing heading", s.pos)
		}
	})
}

func TestNormalizedSlotOnSlopeUsesOnlyVerifiedObservedOwnerFloor(t *testing.T) {
	for _, mode := range []string{"owner", "planner rejected destination floor", "planner rejected start floor", "no observed owner", "unreachable owner floor", "horizontal slot miss"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _, target := followNavRig(t)
			owner := target
			owner.x += 4
			s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
				end := to
				if to == target {
					switch mode {
					case "planner rejected destination floor":
						return nil, errors.New("navigation normalized endpoint is outside destination tolerance")
					case "planner rejected start floor":
						return nil, errors.New("navigation normalized start is outside start tolerance")
					}
					end.z += 1.75
					if mode == "horizontal slot miss" {
						end.x++
						end.z = to.z
					}
				} else if mode == "unreachable owner floor" {
					end.z -= 1.75
				}
				return []position{from, end}, nil
			}
			var observed []position
			if mode != "no observed owner" {
				observed = []position{owner}
			}
			path, arrival, _, ownerArrival, err := s.formationNavigationPath(context.Background(), target, .8, observed...)
			if mode == "owner" || mode == "planner rejected destination floor" {
				if err != nil || !ownerArrival || arrival != owner || path[len(path)-1] != owner {
					t.Fatal("independent verified owner route missing", path, arrival, ownerArrival, err)
				}
			} else if err == nil || len(path) != 0 || ownerArrival {
				t.Fatal("unverified owner or personal slot admitted", path, arrival, ownerArrival)
			}
		})
	}
}

func TestMovingObservedOwnerCanUseOnlyAnAdvancingVerifiedPrefix(t *testing.T) {
	for _, mode := range []string{"moving", "stopped", "turned back", "stopped during clock", "lost during clock", "new visit", "new follow", "unknown stop"} {
		t.Run(mode, func(t *testing.T) {
			s, f, advance, target := followNavRig(t)
			ctx := context.Background()
			f.world.VisitRevision = 1
			f.world.Owner.MovementFlags = 1
			target.x = s.pos.x + 18
			owner := target
			owner.x += 4
			f.world.Owner.X, f.world.Owner.Y, f.world.Owner.Z, f.world.Owner.Orientation = owner.x, owner.y, owner.z, owner.o
			s.planPath = func(_ context.Context, _ string, a, b position) ([]position, error) {
				if b == target {
					b.z += 1.75
				}
				return []position{a, b}, nil
			}
			toggle(t, s, true)
			if !s.followFormationStepLocked(ctx, target, .8, owner) || !s.followNav.ownerArrival || !s.state.Moving {
				t.Fatal("verified route to observed owner did not start", s.stateLocked())
			}
			advance(200 * time.Millisecond)
			owner.x += 3
			target.x += 3
			f.world.Owner.X = owner.x
			switch mode {
			case "stopped", "unknown stop":
				f.world.Owner.MovementFlags = 0
			case "turned back":
				owner.x, f.world.Owner.X = s.pos.x-3, s.pos.x-3
			case "stopped during clock", "lost during clock":
				f.onClock = func() {
					f.onClock = nil
					if mode == "stopped during clock" {
						f.world.Owner.MovementFlags = 0
					} else {
						f.world.Owner.Present = false
					}
				}
			case "new visit":
				f.world.VisitRevision++
			case "new follow":
				s.followRevision++
			}
			f.failSend = mode == "unknown stop"
			// The ordinary controller reads World before selecting each step.
			if _, err := s.snapshot(ctx); err != nil {
				t.Fatal(err)
			}
			s.followFormationStepLocked(ctx, target, .8, owner)
			if mode == "moving" {
				if countPackets(f, opcode.MSGMoveHeartbeat) != 1 || countPackets(f, opcode.MSGMoveStop) != 0 || !s.state.Moving || s.state.Reason == "within_follow_formation" {
					t.Fatal("safe verified prefix was discarded or called arrival", s.stateLocked())
				}
				return
			}
			if countPackets(f, opcode.MSGMoveHeartbeat) != 0 || countPackets(f, opcode.MSGMoveStop) != 1 || s.state.Moving || s.state.MotionUnconfirmed != (mode == "unknown stop") {
				t.Fatal("retired prefix continued or lost stop custody", mode, s.stateLocked())
			}
			if mode == "unknown stop" {
				advance(200 * time.Millisecond)
				s.followFormationStepLocked(ctx, target, .8, owner)
				if !s.state.MotionUnconfirmed || countPackets(f, opcode.MSGMoveStop) != 1 || countPackets(f, opcode.MSGMoveStartForward) != 1 {
					t.Fatal("ambiguous stop replayed", s.stateLocked())
				}
			}
		})
	}
}
