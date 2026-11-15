package buddynav

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/azerothcore/AzerothGhost/pathfinding"
)

type testSwimCorridor struct {
	water       func(Point) bool
	bottom      func(Point) (float32, error)
	clear, fail bool
	rays        int
}

func (b *testSwimCorridor) findSwimPath(a, z Point) (*pathfinding.PathResult, error) {
	return &pathfinding.PathResult{Type: pathfinding.PathfindNormal, Points: []pathfinding.Point3D{{X: a.X, Y: a.Y}, {X: z.X, Y: z.Y}}}, nil
}

func (b *testSwimCorridor) groundOnRoute(p, _, _ Point) (float32, bool, bool, error) {
	return 0, b.water(p), !b.fail, nil
}

func (b *testSwimCorridor) terrain(Point) (float32, bool) { return 0, false }
func (b *testSwimCorridor) floor(_ context.Context, p Point, _, _ float32) (float32, error) {
	return b.bottom(p)
}

func (b *testSwimCorridor) swimSegmentClear(context.Context, Point, Point) (bool, error) {
	b.rays++
	return b.clear, nil
}

func TestMixedWaterRouteWadesSwimsAndReturnsToDryFloor(t *testing.T) {
	b := &testSwimCorridor{clear: true, water: func(p Point) bool { return p.X >= 1 && p.X <= 3 }, bottom: func(p Point) (float32, error) {
		switch p.X {
		case 1, 3:
			return -1, nil
		case 2:
			return 0, errNoPhysicalFloor
		default:
			return 0, nil
		}
	}}
	start, end := Point{}, Point{X: 4}
	for _, pair := range [][2]Point{{start, end}, {end, start}} {
		points, err := planSwimWithin(context.Background(), b, worldPoint, pair[0], pair[1])
		if err != nil || len(points) != 5 {
			t.Fatalf("mixed navigation failed: %v, %v", points, err)
		}
		if points[0].Swimming || points[1].Swimming || !points[2].Swimming || points[3].Swimming || points[4].Swimming || points[1].Z != -1 || points[2].Z != -swimImmersion || points[3].Z != -1 || points[4].Point != pair[1] {
			t.Fatalf("incorrect shore modes or elevation: %v", points)
		}
	}
	if b.rays != 4 {
		t.Fatal("shore/swim steps did not check collision", b.rays)
	}
}

func TestSwimRouteUsesActualSubmergedEndpointsAndRejectsWalls(t *testing.T) {
	b := &testSwimCorridor{clear: true, water: func(Point) bool { return true }, bottom: func(Point) (float32, error) {
		return 0, errNoPhysicalFloor
	}}
	a, z := Point{X: 1, Z: -4}, Point{X: 2, Z: -3}
	points, err := planSwimWithin(context.Background(), b, worldPoint, a, z)
	if err != nil || points[0].Point != a || points[len(points)-1].Point != z {
		t.Fatalf("underwater endpoints were not retained: %v, %v", points, err)
	}
	for _, p := range points {
		if !p.Swimming || p.Z >= 0 {
			t.Fatal("submerged movement left water", p)
		}
	}
	b.clear = false
	if _, err := planSwimWithin(context.Background(), b, worldPoint, a, z); err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatal("accepted blocked water route", err)
	}
	b.clear, b.fail = true, true
	if _, err := planSwimWithin(context.Background(), b, worldPoint, a, z); err == nil {
		t.Fatal("accepted unknown water polygon")
	}
	b.fail = false
	b.bottom = func(Point) (float32, error) { return 0, errors.New("collision assets missing") }
	if _, err := planSwimWithin(context.Background(), b, worldPoint, a, z); err == nil {
		t.Fatal("mistook missing geometry for deep water")
	}
}

func TestBlackfathomDeepsMixedWaterRoute(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_BFD_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set the installed Blackfathom Deeps client assets")
	}
	start := Point{-238.1115, 25.58485, -54.486095}
	end := Point{-312.843, 71.85261, -53.53344}
	path, err := PlanSwimContext(context.Background(), dir, 48, start, end)
	if err != nil || len(path) < 3 {
		t.Fatalf("Blackfathom Deeps shore crossing failed: %v, %v", path, err)
	}
	if path[0].Swimming || path[len(path)-1].Swimming {
		t.Fatal("expected dry ground at both observed endpoints")
	}
	for _, p := range path {
		if p.Swimming {
			return
		}
	}
	t.Fatal("route did not cross the deep water")
}
