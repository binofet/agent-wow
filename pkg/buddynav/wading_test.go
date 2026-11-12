package buddynav

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"

	"github.com/azerothcore/AzerothGhost/pathfinding"
)

func TestShallowWadingChecksEveryPhysicalFloorSample(t *testing.T) {
	for _, depth := range []float32{0, 1.48, 1.8, 1.81, 2.5, -.1} {
		t.Run("depth", func(t *testing.T) {
			f := fakeRoute(testStart, testEnd)
			f.waterFn = func(Point) (bool, error) { return true, nil }
			f.floorFn = func(_ Point, h, _ float32) (float32, error) { return h - depth, nil }
			path, err := plan(context.Background(), f, testStart, testEnd)
			if depth >= 0 && depth <= MaxWadingDepth {
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range path {
					if math.Abs(float64(p.Z-(testStart.Z-depth))) > .001 {
						t.Fatal("emittedliquidsurfaceinsteadphysicalbottom")
					}
				}
			} else if err == nil {
				t.Fatalf("acceptedunsafeWaterDepth%g", depth)
			}
		})
	}
	f := fakeRoute(testStart, testEnd)
	f.waterFn = func(p Point) (bool, error) { return p.X >= -8898, nil }
	f.floorFn = func(p Point, h, _ float32) (float32, error) {
		if p.X >= -8898 {
			return h - 1.9, nil
		}
		return h, nil
	}
	if _, err := plan(context.Background(), f, testStart, testEnd); err == nil {
		t.Fatal("accepteddeepintermediatesample")
	}
	f = fakeRoute(testStart, testEnd)
	f.waterFn = func(Point) (bool, error) { return false, errors.New("unknownsurface") }
	if _, err := plan(context.Background(), f, testStart, testEnd); err == nil {
		t.Fatal("ignoredunknownwaterkind")
	}
}

type meshWadingBackend struct {
	p     *planner
	depth float32
}

func (f meshWadingBackend) findPath(a, b Point) (*pathfinding.PathResult, error) {
	return f.p.findPath(a, b)
}
func (f meshWadingBackend) ground(a Point) (float32, error) { return f.p.ground(a) }
func (f meshWadingBackend) water(a Point) (bool, error)     { return f.p.water(a) }
func (f meshWadingBackend) terrain(Point) (float32, bool)   { return 90 - f.depth, true }
func (f meshWadingBackend) floor(_ context.Context, _ Point, _ float32, terrain float32) (float32, error) {
	return terrain, nil
}
func (f meshWadingBackend) walkable(a, b Point) error { return f.p.walkable(a, b) }
func TestDetourWaterCorridorRequiresShallowActualBottom(t *testing.T) {
	for _, depth := range []float32{1.48, 1.9} {
		p := testMesh(t)
		p.filter.SetIncludeFlags(1 | 8)
		p.filter.SetExcludeFlags(^uint16(1 | 8))
		a, b := Point{-8898, -98, 90 - depth}, Point{-8898, -87, 90 - depth}
		path, err := plan(context.Background(), meshWadingBackend{p, depth}, a, b)
		if depth <= MaxWadingDepth {
			if err != nil || len(path) < 2 {
				t.Fatalf("shallowroute%v", err)
			}
		} else if err == nil {
			t.Fatal("accepteddeepwatercorridor")
		}
	}
	for _, flags := range []uint16{0, 2, 4, 16, 0xffff} {
		if walkSurfaceFlags(flags) {
			t.Fatalf("accepteddangerousflags%x", flags)
		}
	}
}

func TestPhysicalNorthshireStreamWading(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_FLOOR_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("setstaticclientassets")
	}
	for _, pair := range [][2]Point{
		{{-9014.413, -214.67183, 71.12956}, {-9027.586, -235.64706, 69.07536}},
		{{-9027.586, -235.64706, 69.07536}, {-9014.413, -214.67183, 71.12956}},
	} {
		path, err := Plan(dir, 0, pair[0], pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if len(path) < 2 {
			t.Fatal("emptywadingroute")
		}
		t.Logf("streamroutepoints=%d end=%+v", len(path), path[len(path)-1])
	}
}
