package buddynav

import (
	"context"
	"math"
	"os"
	"testing"
)

// These are static client-asset fixtures, not live observations or commands.
// A zero fixtureZ asks the independent terrain for its floor; nonzero Z is an
// explicit known city/instance layer. Production endpoint tolerances stay2yd.
func TestPhysicalCurrentMapRoutesBeyondStarterFences(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_MAP_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set independently copied static client map assets")
	}
	fixtures := []struct {
		name string
		mid  uint32
		a, b Point
	}{
		{"elwynn_south", 0, Point{-9900, -300, 0}, Point{-9870, -300, 0}},
		{"elwynn_east", 0, Point{-9550, -1200, 0}, Point{-9520, -1200, 0}},
		{"stormwind", 0, Point{-8913.23, 554.633, 93.7944}, Point{-8918.23, 554.633, 93.7944}},
		{"westfall", 0, Point{-10630, 1100, 0}, Point{-10600, 1100, 0}},
		{"dun_morogh", 0, Point{-6240, 331, 383}, Point{-6230, 331, 383}},
		{"durotar_map1", 1, Point{-618, -4251, 38.718}, Point{-605, -4251, 38.718}},
		{"outland_map530", 530, Point{-1887, 5359, -12.43}, Point{-1877, 5359, -12.43}},
		{"northrend_map571", 571, Point{2275, 5178, 0}, Point{2273, 5178, 0}},
		{"deadmines_map36", 36, Point{-16.4, -383.07, 61.78}, Point{-16.4, -388.07, 63.42}},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			a, b := f.a, f.b
			w, err := newMapWindow(f.mid, a, b)
			if err != nil {
				t.Fatal(err)
			}
			<-currentMapPlanners.gate
			p, err := cachedMapPlanner(context.Background(), dir, w)
			if err == nil {
				for _, q := range []*Point{&a, &b} {
					terrain, known := p.terrain(*q)
					if !known {
						terrain = float32(math.NaN())
					}
					if q.Z == 0 {
						q.Z = terrain
					}
					var mesh float32
					mesh, err = p.ground(*q)
					if err != nil {
						break
					}
					q.Z, err = p.floor(context.Background(), *q, mesh, terrain)
					if err != nil {
						break
					}
				}
			}
			currentMapPlanners.gate <- struct{}{}
			if err != nil {
				t.Fatalf("static fixture floors a=%+v b=%+v: %v", a, b, err)
			}
			for _, ends := range [][2]Point{{a, b}, {b, a}} {
				path, err := PlanMapContext(context.Background(), dir, f.mid, ends[0], ends[1])
				if err != nil {
					t.Fatal(err)
				}
				var length, maxStep, maxDZ float64
				for i := 1; i < len(path); i++ {
					d := distance(path[i-1], path[i])
					length += d
					maxStep = math.Max(maxStep, d)
					maxDZ = math.Max(maxDZ, math.Abs(float64(path[i].Z-path[i-1].Z)))
				}
				if len(path) > MaxPoints || length > MaxRouteLength || maxStep > 2 || maxDZ > 1 {
					t.Fatal("physical route bounds changed")
				}
				t.Logf("map%d %v -> %v length%.3f points%d maxstep%.4f maxdz%.4f", f.mid, ends[0], ends[1], length, len(path), maxStep, maxDZ)
			}
		})
	}
}
