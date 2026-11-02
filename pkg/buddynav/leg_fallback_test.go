package buddynav

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/o0olele/detour-go/detour"
)

func longFlatLegFixture(t *testing.T) *planner {
	t.Helper()
	const edge = uint16(0xffff)
	params := detour.DtNavMeshCreateParams{
		Verts: []uint16{0, 0, 0, 0, 0, 200, 20, 0, 200, 20, 0, 0}, VertCount: 4,
		Polys: []uint16{0, 1, 2, 3, edge, edge, edge, edge}, PolyFlags: []uint16{1}, PolyAreas: []uint8{1}, PolyCount: 1, Nvp: 4,
		Bmin: [3]float32{-100, 90, -9000}, Bmax: [3]float32{-80, 91, -8800},
		WalkableHeight: 2, WalkableRadius: .5, WalkableClimb: .5, Cs: 1, Ch: 1, BuildBvTree: true,
	}
	var data []byte
	var size int
	if !detour.DtCreateNavMeshData(&params, &data, &size) {
		t.Fatal("build long floor fixture")
	}
	mesh := detour.DtAllocNavMesh()
	if st := mesh.Init(&detour.DtNavMeshParams{Orig: params.Bmin, TileWidth: 20, TileHeight: 200, MaxTiles: 1, MaxPolys: 16}); detour.DtStatusFailed(st) {
		t.Fatal(st)
	}
	var ref detour.DtTileRef
	if st := mesh.AddTile(data, size, detour.DT_TILE_FREE_DATA, 0, &ref); detour.DtStatusFailed(st) {
		t.Fatal(st)
	}
	query := detour.DtAllocNavMeshQuery()
	if st := query.Init(mesh, 128); detour.DtStatusFailed(st) {
		t.Fatal(st)
	}
	filter := detour.DtAllocDtQueryFilter()
	filter.SetIncludeFlags(1)
	filter.SetExcludeFlags(^uint16(1))
	return &planner{mesh: mesh, query: query, filter: filter}
}

func TestLegShorterPrefixRetainsIndependentPhysicalProof(t *testing.T) {
	for _, supported := range []float32{30, 20} {
		p := longFlatLegFixture(t)
		start, goal := Point{-8990, -90, 90}, Point{-8890, -90, 90}
		w, err := newRecoveryWindow(0, start, goal)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		path, err := recoveryLeg(context.Background(), p, w, start, goal, func(candidate Point) ([]Point, error) {
			calls++
			physical := &hillBackend{planner: p, floorFn: func(at Point, height float32) (float32, error) {
				if at.X-start.X > supported {
					return 0, errors.New("independent collision floor absent")
				}
				return height, nil
			}}
			return planWithin(context.Background(), physical, w.contains, "short prefix fixture", start, candidate)
		})
		if calls != 3 { // The two identical full-goal candidates are deduplicated.
			t.Fatal("unexpected physical attempts", calls)
		}
		if supported == 20 {
			if err == nil {
				t.Fatal("fallback crossed missing physical floor")
			}
			continue
		}
		if err != nil || len(path) < 2 || distance(start, path[len(path)-1]) < 24 || distance(start, path[len(path)-1]) > 26 {
			t.Fatal("valid short prefix unavailable", path, err)
		}
	}
}

func TestLegRecordedStormwindBuildingExitAndNormalReplan(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_STORMWIND_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set copied client Stormwind assets")
	}
	start, goal := Point{-8836.146, 490.3753, 109.61511}, Point{-9662.75, 694.321, 36.9357}
	path, err := PlanLegContext(context.Background(), dir, 0, start, goal)
	if err != nil {
		t.Fatal(err)
	}
	end := path[len(path)-1]
	if distance(start, end) < 3 || distance(start, end) > 30 {
		t.Fatal("expected short verified building exit", end)
	}
	replanned, err := PlanMapContext(context.Background(), dir, 0, start, end)
	if err != nil || len(replanned) < 2 || distance(replanned[len(replanned)-1], end) > EndpointTolerance {
		t.Fatal("normal movement cannot independently replan prefix", replanned, err)
	}
	t.Logf("recorded static exit points=%d endpoint=%+v normalReplanPoints=%d", len(path), end, len(replanned))
}
