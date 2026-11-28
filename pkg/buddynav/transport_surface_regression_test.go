package buddynav

import (
	"context"
	"math"
	"os"
	"testing"
)

func loadDeckRegressionMesh(t *testing.T, display uint32) *SurfaceMesh {
	t.Helper()
	dir := os.Getenv("BUDDY_NAV_SURFACE_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("requires installed moving-surface tiles and matching client VMAPs")
	}
	mesh, err := LoadSurfaceMesh(context.Background(), dir, display)
	if err != nil {
		t.Fatal(err)
	}
	return mesh
}

func checkMeasuredDeckPath(t *testing.T, mesh *SurfaceMesh, start, end Point, path []Point) {
	t.Helper()
	if len(path) < 2 || distance(path[0], start) > .15 || distance(path[len(path)-1], end) > .15 {
		t.Fatalf("display %d did not reach both measured endpoints", mesh.Metadata.DisplayID)
	}
	ctx := context.Background()
	for i := 1; i < len(path); i++ {
		from, to := path[i-1], path[i]
		samples := max(1, int(math.Ceil(distance(from, to)/.13)))
		for j := 0; j <= samples; j++ {
			f := float32(j) / float32(samples)
			point := Point{from.X + f*(to.X-from.X), from.Y + f*(to.Y-from.Y), from.Z + f*(to.Z-from.Z)}
			if _, supported, err := mesh.surface.Height(ctx, point, .3, .3); err != nil || !supported {
				t.Fatalf("display %d lost physical hull support at %+v: %v", mesh.Metadata.DisplayID, point, err)
			}
		}
		bearing := math.Atan2(float64(to.Y-from.Y), float64(to.X-from.X))
		for _, side := range []float64{-.25, 0, .25} {
			for _, height := range []float32{.4, .92, 1.75} {
				dx, dy := float32(-math.Sin(bearing)*side), float32(math.Cos(bearing)*side)
				a := Point{from.X + dx, from.Y + dy, from.Z + height}
				b := Point{to.X + dx, to.Y + dy, to.Z + height}
				if clear, err := mesh.surface.Clear(ctx, a, b); err != nil || !clear {
					t.Fatalf("display %d struck physical hull between %+v and %+v: %v", mesh.Metadata.DisplayID, a, b, err)
				}
			}
		}
	}
}

func TestSurfaceMeshKnownStandingDeckTrips(t *testing.T) {
	for _, tc := range []struct {
		name    string
		display uint32
		from    Point
		to      Point
	}{
		{"ship_a_open_deck", 476, Point{5.2964363, 5.099385, 6.098609}, Point{39.763103, -2.6339486, 6.091992}},
		{"ship_open_deck", 3015, Point{1.0764359, 4.6327205, 6.0980916}, Point{36.27977, 2.2660532, 6.094307}},
		{"night_elf_ship_open_deck", 7087, Point{-21.20316, 3.756361, 4.2969294}, Point{12.830176, -5.6269727, 6.0626917}},
		{"exact_detail_edge", 3015, Point{25.396437, 3.1327202, 6.1073203}, Point{-2.136898, -2.8006134, 6.097525}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mesh := loadDeckRegressionMesh(t, tc.display)
			for _, ends := range [][2]Point{{tc.from, tc.to}, {tc.to, tc.from}} {
				path, err := mesh.Route(context.Background(), ends[0], ends[1])
				if err != nil {
					t.Fatal(err)
				}
				checkMeasuredDeckPath(t, mesh, ends[0], ends[1], path)
			}
		})
	}
}

func TestSurfaceMeshActualHullEndpointsAndBlockedUpperDeckChord(t *testing.T) {
	ctx := context.Background()
	mesh := loadDeckRegressionMesh(t, 7087)
	unsupported := Point{30.696842, -8.613639, 16.646103}
	standing := Point{30.630177, -6.660306, 16.620815}
	if _, hit, err := mesh.surface.Height(ctx, unsupported, .6, .6); err != nil || hit {
		t.Fatal("expected empty space just outside the deck", hit, err)
	}
	for _, ends := range [][2]Point{{unsupported, standing}, {standing, unsupported}} {
		if _, err := mesh.Route(ctx, ends[0], ends[1]); err == nil {
			t.Fatal("silently moved an unsupported endpoint onto the adjacent deck")
		}
	}

	mesh = loadDeckRegressionMesh(t, 3015)
	from := Point{-26.503565, .5993869, 16.057762}
	to := Point{-45.253567, -.14227962, 9.653343}
	// The original straight 3-D funnel crossed the upper deck at torso height.
	upperTorso := Point{-26.531298, .59828985, 16.044184 + .8}
	lowerTorso := Point{-40.77453, .03489119, 9.508702 + .8}
	if clear, err := mesh.surface.Clear(ctx, upperTorso, lowerTorso); err != nil || clear {
		t.Fatal("expected solid hull across the original torso chord", clear, err)
	}
	for _, ends := range [][2]Point{{from, to}, {to, from}} {
		if path, err := mesh.Route(ctx, ends[0], ends[1]); err == nil {
			checkMeasuredDeckPath(t, mesh, ends[0], ends[1], path)
		}
	}
}
