package buddyactions

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/agent-wow/agent-wow/pkg/opcode"
)

func followNavRig(t *testing.T) (*Service, *fakeSession, func(time.Duration), position) {
	t.Helper()
	s, f, advance := rig(t)
	s.pos = position{x: -8900, y: -100, z: 90}
	s.posKnown, s.positionRevision, s.posMap = true, 1, 0
	s.posAt = s.now()
	s.navDataDir = "/private/client-fixture"
	s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
		return []position{from, to}, nil
	}
	target := s.pos
	target.x += 10
	return s, f, advance, target
}

func TestFollowPathStepCoalescesSamplesWithoutCuttingCorners(t *testing.T) {
	from := position{x: -8900, y: -100, z: 90}
	point := func(x, y, z float32) position { return position{x: from.x + x, y: from.y + y, z: from.z + z} }
	straight := []position{from, point(1, 0, 0), point(2, 0, 0), point(3, 0, 0)}
	p, index, ok := followPathStep(from, straight, 0, 1.4)
	if !ok || math.Abs(distance(from, p)-1.4) > 0.001 || index != 2 {
		t.Fatalf("straight route was limited to a sample: p=%+v index=%d ok=%v", p, index, ok)
	}
	for name, corner := range map[string]position{"horizontal": point(1, 1, 0), "vertical": point(2, 0, 1), "reverse": from} {
		t.Run(name, func(t *testing.T) {
			path := []position{from, point(1, 0, 0), corner}
			p, index, ok := followPathStep(from, path, 0, 1.4)
			if !ok || distance(p, point(1, 0, 0)) > 0.001 || index != 2 {
				t.Fatalf("crossed a real corner: p=%+v index=%d ok=%v", p, index, ok)
			}
		})
	}
	for _, budget := range []float64{-1, 0, math.NaN(), math.Inf(1)} {
		if _, _, ok := followPathStep(from, straight, 0, budget); ok {
			t.Fatalf("accepted invalid budget %v", budget)
		}
	}
}

func TestFollowNavigationUsesRunBudgetAndBoundedPlannerContext(t *testing.T) {
	s, f, advance, target := followNavRig(t)
	start := s.pos
	calls := 0
	s.planPath = func(ctx context.Context, _ string, from, to position) ([]position, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 251*time.Millisecond {
			t.Fatal("planner did not receive a bounded deadline")
		}
		path := []position{from}
		for x := from.x + 1; x < to.x; x++ {
			path = append(path, position{x: x, y: from.y, z: from.z})
		}
		return append(path, to), nil
	}
	if !s.followFormationStepLocked(context.Background(), target, 0.8) || len(f.packets) != 1 || f.packets[0].Opcode != opcode.MSGMoveStartForward {
		t.Fatal("did not start normal movement")
	}
	advance(200 * time.Millisecond)
	if !s.followFormationStepLocked(context.Background(), target, 0.8) || math.Abs(distance(start, s.pos)-1.4) > 0.001 {
		t.Fatalf("did not use 7yd/s budget: %+v", s.pos)
	}
	if len(f.packets) != 2 || f.packets[1].Opcode != opcode.MSGMoveHeartbeat || calls != 1 || !s.state.NavAvailable {
		t.Fatalf("unexpected movement/planning: calls=%d packets=%d state=%+v", calls, len(f.packets), &s.state)
	}
}

func TestFollowNavigationTargetRefreshThrottlesWithoutUnverifiedChord(t *testing.T) {
	s, _, advance, target := followNavRig(t)
	calls := 0
	s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
		calls++
		return []position{from, to}, nil
	}
	s.followFormationStepLocked(context.Background(), target, 0.8)
	advance(200 * time.Millisecond)
	moved := target
	moved.y += 5
	s.followFormationStepLocked(context.Background(), moved, 0.8)
	if calls != 1 || s.pos.y != -100 {
		t.Fatalf("replanned too soon or cut toward unplanned target: calls=%d pos=%+v", calls, s.pos)
	}
	for range 3 {
		advance(200 * time.Millisecond)
		s.followFormationStepLocked(context.Background(), moved, 0.8)
	}
	if calls != 2 || s.pos.y <= -100 {
		t.Fatalf("moving target did not refresh: calls=%d pos=%+v", calls, s.pos)
	}
}

func TestFollowNavigationCorrectionOrOtherActuatorStopsUntilReplan(t *testing.T) {
	for _, correction := range []bool{false, true} {
		t.Run(map[bool]string{false: "other_actuator", true: "authoritative_revision"}[correction], func(t *testing.T) {
			s, f, advance, target := followNavRig(t)
			s.followFormationStepLocked(context.Background(), target, 0.8)
			advance(200 * time.Millisecond)
			if correction {
				s.positionRevision++
			} else {
				s.pos.y++
			}
			before := s.pos
			if !s.followFormationStepLocked(context.Background(), target, 0.8) || s.state.Moving || distance(before, s.pos) != 0 || f.packets[len(f.packets)-1].Opcode != opcode.MSGMoveStop {
				t.Fatalf("reused stale path: %+v", &s.state)
			}
			if s.state.NavAvailable {
				t.Fatal("stale route remained available")
			}
		})
	}
}

func TestFollowNavigationFailureStopsWithoutFallbackOrRapidRetry(t *testing.T) {
	s, f, advance, target := followNavRig(t)
	s.state.Moving = true
	calls := 0
	s.planPath = func(context.Context, string, position, position) ([]position, error) {
		calls++
		return nil, errors.New("cannot read /private/client-fixture/tiles\nprivate")
	}
	if s.followFormationStepLocked(context.Background(), target, 0.8) || s.state.Moving || s.state.NavAvailable || len(f.packets) != 1 || f.packets[0].Opcode != opcode.MSGMoveStop {
		t.Fatal("planner failure did not stop")
	}
	if strings.Contains(s.state.NavReason, s.navDataDir) || strings.Contains(s.state.NavReason, "\n") {
		t.Fatalf("unbounded private diagnostic: %q", s.state.NavReason)
	}
	advance(200 * time.Millisecond)
	s.followFormationStepLocked(context.Background(), target, 0.8)
	if calls != 1 || len(f.packets) != 1 {
		t.Fatal("rapid retry or direct fallback after planner failure")
	}
}

func TestFollowNavigationRejectsEndpointOutsideSlotAndOversizedRoute(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(map[bool]string{false: "short_endpoint", true: "point_limit"}[oversized], func(t *testing.T) {
			s, f, _, target := followNavRig(t)
			s.planPath = func(_ context.Context, _ string, from, to position) ([]position, error) {
				if oversized {
					path := make([]position, 257)
					for i := range path {
						path[i] = from
					}
					path[len(path)-1] = to
					return path, nil
				}
				to.x -= 1.5
				return []position{from, to}, nil
			}
			if s.followFormationStepLocked(context.Background(), target, 0.8) || len(f.packets) != 0 || s.state.NavAvailable {
				t.Fatal("invalid route started movement")
			}
		})
	}
}

func TestFollowNavigationGroundNormalizedArrivalStopsWithoutVerticalChase(t *testing.T) {
	s, f, _, _ := followNavRig(t)
	target := s.pos
	target.x += 0.5
	target.z += 0.6
	s.state.Moving = true
	s.planPath = func(context.Context, string, position, position) ([]position, error) {
		t.Fatal("planned after reaching horizontal slot")
		return nil, nil
	}
	if !s.followFormationStepLocked(context.Background(), target, 0.8) || s.state.Moving || len(f.packets) != 1 || f.packets[0].Opcode != opcode.MSGMoveStop {
		t.Fatal("did not stop at normalized slot")
	}
}

func TestFollowNavigationLateCheckpointStopsAndAmbiguousStopFails(t *testing.T) {
	s, f, advance, target := followNavRig(t)
	s.followFormationStepLocked(context.Background(), target, 0.8)
	advance(601 * time.Millisecond)
	if s.followFormationStepLocked(context.Background(), target, 0.8) || s.state.Moving || f.packets[len(f.packets)-1].Opcode != opcode.MSGMoveStop {
		t.Fatal("late tick continued movement")
	}
	s.followNav.Reset()
	s.followFormationStepLocked(context.Background(), target, 0.8)
	advance(200 * time.Millisecond)
	s.positionRevision++
	f.failSend = true
	if s.followFormationStepLocked(context.Background(), target, 0.8) || !s.state.MotionUnconfirmed {
		t.Fatal("ambiguous stop was reported successful")
	}
}
