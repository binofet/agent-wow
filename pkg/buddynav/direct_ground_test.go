package buddynav

import (
	"context"
	"math"
	"os"
	"slices"
	"testing"

	"github.com/o0olele/detour-go/detour"
)

// Exercise the steering query only after the normal Detour adjacency search.
func searchedDirectStrip(t *testing.T, p *planner, a, b Point) []detour.DtPolyRef {
	t.Helper()
	first, _, err := p.groundRef(a)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := p.groundRef(b)
	if err != nil {
		t.Fatal(err)
	}
	x, y := recast(a), recast(b)
	var refs [512]detour.DtPolyRef
	var n int
	if st := p.query.FindPath(first, last, x[:], y[:], p.filter, refs[:], &n, len(refs)); st != detour.DT_SUCCESS || n == 0 || refs[n-1] != last {
		t.Fatalf("incomplete fixture search, status %x count %d", st, n)
	}
	return slices.Clone(refs[:n])
}

func TestDirectGroundPreservesDetailGradeAndActualLayer(t *testing.T) {
	p := detailHillMesh(t, true)
	a, b := Point{-8899, -90, 90.4}, Point{-8881, -90, 90.4}
	for _, pair := range [][2]Point{{a, b}, {b, a}} {
		strip := searchedDirectStrip(t, p, pair[0], pair[1])
		path, err := p.directGroundPath(t.Context(), pair[0], pair[1], strip)
		if err != nil || len(path) != 3 || path[1] != (Point{-8890, -90, 94}) || distance(path[0], pair[0]) > .001 || distance(path[2], pair[1]) > .001 {
			t.Fatal("grade break or endpoint lost", path, err)
		}
	}
	low := searchedDirectStrip(t, p, a, b)
	upperA, upperB := a, b
	upperA.Z, upperB.Z = 100, 100
	high := searchedDirectStrip(t, p, upperA, upperB)
	if high[0] == low[0] {
		t.Fatal("fixture lost stacked floors")
	}
	if path, err := p.directGroundPath(t.Context(), upperA, upperB, high); err != nil || len(path) != 2 || path[0] != upperA || path[1] != upperB {
		t.Fatal(path, err)
	}
	for _, strip := range [][]detour.DtPolyRef{nil, low, high, {low[0], high[0]}} {
		if path, err := p.directGroundPath(t.Context(), a, upperB, strip); err == nil || path != nil {
			t.Fatal("allowed unrelated floor or absent strip", strip, path)
		}
	}
	if path, err := p.directGroundPath(t.Context(), a, a, low); err != nil || len(path) != 1 || distance(path[0], a) > .001 {
		t.Fatal("stationary point on its actual layer", path, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.directGroundPath(ctx, a, b, low); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestDirectGroundStaysInsideSearchedAdjacentCells(t *testing.T) {
	p := testMesh(t)
	base := p.mesh.GetPolyRefBase(p.mesh.GetTile(0))
	middle := base | 1
	if st := p.mesh.SetPolyFlags(middle, 1); st != detour.DT_SUCCESS {
		t.Fatal(st)
	}
	a, b := Point{-8898, -98, 90}, Point{-8898, -87, 90}
	strip := searchedDirectStrip(t, p, a, b)
	if len(strip) != 3 {
		t.Fatalf("fixture expected three linked cells, got %v", strip)
	}
	path, err := p.directGroundPath(t.Context(), a, b, strip)
	if err != nil || len(path) != 2 || path[0] != a || path[1] != b {
		t.Fatal(path, err)
	}
	for _, wrong := range [][]detour.DtPolyRef{{strip[0], strip[2]}, {strip[0], strip[2], strip[1]}, {strip[0], strip[1]}, {strip[2], strip[1], strip[0]}} {
		if path, err := p.directGroundPath(t.Context(), a, b, wrong); err == nil || path != nil {
			t.Fatal("steered outside the retained strip", wrong, path)
		}
	}
	// Feed the broad ordinary ground/wading filter: the direct contract still
	// excludes a wet polygon that has succeeded in the graph and XY ray.
	p.filter.SetIncludeFlags(1 | 8)
	p.filter.SetExcludeFlags(^uint16(1 | 8))
	p.mesh.SetPolyFlags(middle, 8)
	if path, err := p.directGroundPath(t.Context(), a, b, searchedDirectStrip(t, p, a, b)); err == nil || path != nil {
		t.Fatal("direct ground steered through water", path)
	}
}

func TestDirectGroundRejectsWallGapAndUnsupportedEndpoint(t *testing.T) {
	p := surfaceRetryFixture(t, "wall")
	a, b := Point{-8898, -99, 90}, Point{-8898, -91, 90}
	first, _, err := p.groundRef(a)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := p.groundRef(b)
	if err != nil {
		t.Fatal(err)
	}
	// Even fabricated adjacency cannot make the ray cross a real wall.
	for _, end := range []Point{b, {-8898, -95, 90}} {
		for _, strip := range [][]detour.DtPolyRef{{first}, {first, last}} {
			if path, err := p.directGroundPath(t.Context(), a, end, strip); err == nil || path != nil {
				t.Fatal("crossed void", strip, path)
			}
		}
	}
}

func TestGroundChordRetainsRoundedBakedBoundaryVertex(t *testing.T) {
	a, b := Point{-1736.7999, 5361.5996, -12.300926}, Point{-1728, 5362.133, -8.034264}
	vertex := []float32{5361.9556, -9.634254, -1730.9332}
	fraction, ok := groundChordVertex(a, b, vertex)
	if !ok || math.Abs(float64(fraction)-2./3.) > .001 {
		t.Fatal("lost baked edge vertex", fraction, ok)
	}
	vertex[0] += .01
	if _, ok := groundChordVertex(a, b, vertex); ok {
		t.Fatal("snapped to an unrelated edge")
	}
}

func TestDirectGroundCopiedIronforgeRamp(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_POOL_TEST_DATA_DIR to copied navigation assets")
	}
	isolateMapCache(t)
	a, b := Point{-5248.4316, -583.0111, 409.1023}, Point{-5252.9126, -499.65808, 386.1125}
	w, err := newMapWindow(0, a, b)
	if err != nil {
		t.Fatal(err)
	}
	p, err := cachedMapPlanner(t.Context(), dir, w)
	if err != nil {
		t.Fatal(err)
	}
	strip := searchedDirectStrip(t, p, a, b)
	path, err := p.directGroundPath(t.Context(), a, b, strip)
	if err != nil || len(path) < 2 {
		t.Fatal("ramp direct", len(strip), path, err)
	}
	if len(strip) < 2 || distance(path[0], a) > EndpointTolerance || distance(path[len(path)-1], b) > EndpointTolerance {
		t.Fatal(strip, path)
	}
	// A physical observation on the sloping ramp, materially above the straight
	// 3-D chord. The baked detail should support the same intermediate floor.
	probe := Point{-5250.1523, -551.00464, 397.57202}
	var found bool
	for i := 1; i < len(path); i++ {
		u, v := path[i-1], path[i]
		if probe.Y >= u.Y && probe.Y <= v.Y && v.Y > u.Y {
			z := u.Z + (v.Z-u.Z)*(probe.Y-u.Y)/(v.Y-u.Y)
			found = true
			if math.Abs(float64(z-probe.Z)) > EndpointTolerance {
				t.Fatalf("ramp floor %v, retained height %v", probe.Z, z)
			}
		}
	}
	if !found {
		t.Fatal("missing middle of ramp")
	}
	t.Logf("searched %d polygons, retained %d surface points", len(strip), len(path))
}

func TestDirectGroundRejectsNearbyOverlappingFloorWithinEndpointTolerance(t *testing.T) {
	p := detailHillMesh(t, true)
	a, b := Point{-8899, -90, 90.4}, Point{-8881, -90, 90.4}
	strip := searchedDirectStrip(t, p, a, b)
	tile := p.mesh.GetTile(0)
	// Move the test's second, disconnected deck close enough for both layers
	// to pass the broad XY/height lookup and rebuild its search bounds by
	// forcing ordinary precise per-polygon bounds instead of the old BV tree.
	tile.BvTree = nil
	tile.Header.BvNodeCount = 0
	for _, v := range tile.Polys[1].Verts[:tile.Polys[1].VertCount] {
		tile.Verts[3*int(v)+1] = 92
	}
	deck := b
	deck.Z = 92
	found, _, err := p.groundRef(deck)
	if err != nil || found == strip[0] || math.Abs(float64(deck.Z-b.Z)) >= EndpointTolerance {
		t.Fatal("fixture has no distinct nearby deck", found, strip, err)
	}
	if path, err := p.directGroundPath(t.Context(), a, deck, strip); err == nil || path != nil {
		t.Fatal("steered below target deck", path)
	}
}

func TestDirectGroundHonorsBakedPortalClimb(t *testing.T) {
	for _, step := range []float32{.25, 1.25} {
		p := testMesh(t)
		tile := p.mesh.GetTile(0)
		middle := &tile.Polys[1]
		middle.Flags = 1
		a, b := Point{-8898, -98, 90}, Point{-8898, -87, 90}
		strip := searchedDirectStrip(t, p, a, b)
		// Keep the original paired portals. Give the middle polygon its own
		// height samples so both sides of each actual step can be inspected.
		for i, v := range middle.Verts[:middle.VertCount] {
			n := int(v) * 3
			middle.Verts[i] = uint16(len(tile.Verts) / 3)
			tile.Verts = append(tile.Verts, tile.Verts[n], tile.Verts[n+1]+step, tile.Verts[n+2])
		}
		path, err := p.directGroundPath(t.Context(), a, b, strip)
		if step > tile.Header.WalkableClimb {
			if err == nil || path != nil {
				t.Fatal("steered over excessive step", step, path)
			}
		} else {
			if err != nil || !slices.Contains(path, Point{-8898, -95, 90 + step}) || !slices.Contains(path, Point{-8898, -95, 90}) {
				t.Fatal("lost actual measured portal step", step, path, err)
			}
		}
	}
}
