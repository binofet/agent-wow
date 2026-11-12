package buddynav

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"
)

func surfaceFixtureFloor(t *testing.T, dir string, q Point) Point {
	t.Helper()
	w, e := newMapWindow(0, q, q)
	if e != nil {
		t.Fatal(e)
	}
	<-currentMapPlanners.gate
	p, e := cachedMapPlanner(context.Background(), dir, w)
	if e == nil {
		terrain, known := p.terrain(q)
		if !known {
			terrain = float32(math.NaN())
		}
		if q.Z == 0 {
			q.Z = terrain
		}
		var h float32
		h, e = p.ground(q)
		if e == nil {
			q.Z, e = p.floor(context.Background(), q, h, terrain)
		}
	}
	currentMapPlanners.gate <- struct{}{}
	if e != nil {
		t.Fatal(q, e)
	}
	return q
}

// Static copied-client fixtures reproduce the observed raw-Z chord hill bug.
// They are not observed NPC positions or authority for a live movement target.
func TestPhysicalLayerBoundHillRoutesAndWestGoldshire(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_SURFACE_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set independent client surface-regression assets")
	}
	fixtures := []struct {
		name string
		a, b Point
	}{
		{"sarvis_hill", Point{1754.9044, 1654.9657, 119.99064}, Point{1808.5333, 1658.6666, 108.32813}},
		{"exact_reported_hill_edge", Point{1792, 1658.6666, 113.66147}, Point{1808.5333, 1658.6666, 108.32813}},
		{"dun_morogh_hill", Point{-6000, 400, 0}, Point{-5970, 400, 0}},
		{"goldshire_west", Point{-9465.52, 74.0069, 56.7785}, Point{-9605, 74.0069, 0}},
		{"goldshire_party_to_west", Point{-9410, 65, 59}, Point{-9605, 74.0069, 0}},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			a, b := surfaceFixtureFloor(t, dir, f.a), surfaceFixtureFloor(t, dir, f.b)
			for _, ends := range [][2]Point{{a, b}, {b, a}} {
				path, e := PlanMapContext(context.Background(), dir, 0, ends[0], ends[1])
				if e != nil {
					t.Fatal(e)
				}
				var length, step, dz float64
				for i := 1; i < len(path); i++ {
					d := distance(path[i-1], path[i])
					length += d
					step = math.Max(step, d)
					dz = math.Max(dz, math.Abs(float64(path[i].Z-path[i-1].Z)))
				}
				if len(path) > MaxPoints || length > MaxRouteLength || step > 2 || dz > 1 {
					t.Fatal("physical bounds changed")
				}
				t.Logf("%s from=%+v to=%+v length=%.6f points=%d maxstep=%.6f maxdz=%.6f", f.name, ends[0], ends[1], length, len(path), step, dz)
			}
		})
	}
}

func TestPhysicalOriginalSarvisGoalStillRejectsIndependentRealLedge(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_SURFACE_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set independent client surface-regression assets")
	}
	a := surfaceFixtureFloor(t, dir, Point{1754.9044, 1654.9657, 119.99064})
	b := surfaceFixtureFloor(t, dir, Point{1843.32, 1639.9, 97.8113})
	_, err := PlanMapContext(context.Background(), dir, 0, a, b)
	var ledge *physicalDiscontinuity
	if !errors.As(err, &ledge) || math.Abs(float64(ledge.floorTo.Z-ledge.floorFrom.Z)) <= 1 {
		t.Fatal("full Sarvis goal must not bypass separate actual physical ledge", err)
	}
	t.Logf("remaining full-goal limitation: %v", err)
}
