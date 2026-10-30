package buddynav

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func TestInteractionStandoffUsesOneDeadlineObservedZAndBoundedGroundGoal(t *testing.T) {
	a, npc := Point{-8900, -100, 90}, Point{-8890, -100, 90}
	calls := 0
	var deadline time.Time
	path, err := PlanInteractionWith(context.Background(), a, npc, func(ctx context.Context, from, goal Point) ([]Point, error) {
		calls++
		d, ok := ctx.Deadline()
		if !ok || time.Until(d) > 250*time.Millisecond || !deadline.IsZero() && !d.Equal(deadline) {
			t.Fatal("candidate reset or omitted shared deadline")
		}
		deadline = d
		if from != a || goal.Z != npc.Z || distance(goal, npc) > 2.501 {
			t.Fatal("candidate changed start/layer/radius", from, goal)
		}
		if calls == 1 {
			if goal != npc {
				t.Fatal("ordinary exact target not tried first")
			}
			return nil, errors.New("find navigation path: destination: sample is outside ground polygon")
		}
		return []Point{from, goal}, nil
	})
	if err != nil || calls != 2 || distance(path[len(path)-1], npc) != 1 {
		t.Fatal(path, calls, err)
	}
}

func TestInteractionStandoffHasFiniteAttemptsAndNeverRepairsUnknownStart(t *testing.T) {
	a, npc := Point{-8900, -100, 90}, Point{-8890, -100, 90}
	for _, badStart := range []bool{false, true} {
		calls := 0
		_, err := PlanInteractionWith(context.Background(), a, npc, func(context.Context, Point, Point) ([]Point, error) {
			calls++
			if badStart {
				return nil, errors.New("client navmesh planner: find navigation path: start: sample is outside ground polygon")
			}
			return nil, errors.New("no complete route")
		})
		want := maxInteractionCandidates
		if badStart {
			want = 1
		}
		if err == nil || calls != want {
			t.Fatal(badStart, calls, err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PlanInteractionWith(canceled, a, npc, func(context.Context, Point, Point) ([]Point, error) { t.Fatal("planned after cancel"); return nil, nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := PlanInteractionWith(ctx, a, npc, func(_ context.Context, from, goal Point) ([]Point, error) {
		calls++
		cancel()
		return []Point{from, goal}, nil
	}); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("late success survived cancellation", calls, err)
	}
}

func TestInteractionStandoffRejectsUnverifiedLayersMalformedAndOverlongRoutes(t *testing.T) {
	a, npc := Point{-8900, -100, 90}, Point{-8890, -100, 90}
	for _, mode := range []string{"layer", "outside_range", "partial", "nan", "points", "length", "short"} {
		t.Run(mode, func(t *testing.T) {
			_, err := PlanInteractionWith(context.Background(), a, npc, func(_ context.Context, from, goal Point) ([]Point, error) {
				end := goal
				switch mode {
				case "layer":
					end.Z += 2.1
				case "outside_range":
					end.X, end.Y = npc.X+3.01, npc.Y
				case "partial":
					end.X -= 5
				case "nan":
					end.X = float32(math.NaN())
				case "points":
					p := make([]Point, MaxPoints+1)
					p[0], p[len(p)-1] = from, end
					return p, nil
				case "length":
					return []Point{from, {from.X + 130, from.Y + 100, from.Z}, end}, nil
				case "short":
					return []Point{from}, nil
				}
				return []Point{from, end}, nil
			})
			if err == nil {
				t.Fatal("accepted unsafe route", mode)
			}
		})
	}
	for _, p := range []Point{{X: float32(math.NaN())}, {X: 20000}, {Z: 100001}} {
		if _, err := PlanInteractionWith(context.Background(), a, p, func(context.Context, Point, Point) ([]Point, error) {
			t.Fatal("invalid point reached planner")
			return nil, nil
		}); err == nil {
			t.Fatal(p)
		}
	}
}

func TestInteractionStandoffUsesActualInstalledGroundWithoutCenterProjection(t *testing.T) {
	isolateMapCache(t)
	dir := regionAssetDirectories(t)
	a := Point{1500, -4200, 50}
	mapFixture(t, dir, 1, a)
	// The actual fixture quad ends atX1515; NPC center1516 is outside it.
	npc := Point{1516, -4200, 50}
	if _, err := PlanMapContext(context.Background(), dir, 1, a, npc); err == nil {
		t.Fatal("fixture center was not outside ground")
	}
	path, err := PlanInteractionContext(context.Background(), dir, 1, a, npc)
	if err != nil || len(path) < 2 || distance(path[len(path)-1], npc) > 3 || path[len(path)-1].X > 1515.001 {
		t.Fatal("reachable interaction route not verified", path, err)
	}
	if _, err := PlanInteractionContext(context.Background(), dir, 1, Point{1518, -4200, 50}, npc); err == nil || !strings.Contains(err.Error(), "start:") {
		t.Fatal("invalid start recovered through standoff", err)
	}
}

func TestPhysicalHeleneInteractionStandoffPreservesCompleteRouteAndFloor(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_STANDOFF_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set independently copied static client assets")
	}
	a, npc := Point{-9458.667, 45.992935, 56.60654}, Point{-9380.23, -69.5295, 64.5206}
	if _, err := PlanMapContext(context.Background(), dir, 0, a, npc); err == nil || !strings.Contains(err.Error(), "destination:") {
		t.Fatal("exact reported center no longer reproduces", err)
	}
	started := time.Now()
	path, err := PlanInteractionContext(context.Background(), dir, 0, a, npc)
	if err != nil || len(path) < 2 || distance(path[len(path)-1], npc) > 3 || math.Abs(float64(path[len(path)-1].Z-npc.Z)) > 2 {
		t.Fatal(path, err)
	}
	var length float64
	for i := 1; i < len(path); i++ {
		step := distance(path[i-1], path[i])
		length += step
		if step > 2 || math.Abs(float64(path[i].Z-path[i-1].Z)) > 1 {
			t.Fatal("physical limits weakened")
		}
	}
	if length > MaxRouteLength || len(path) > MaxPoints {
		t.Fatal("route budgets changed")
	}
	t.Logf("Helene verified endpoint=%+v distance%.6f route%.6f points%d elapsed%s", path[len(path)-1], distance(path[len(path)-1], npc), length, len(path), time.Since(started))
}
