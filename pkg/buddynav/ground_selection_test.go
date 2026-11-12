package buddynav

import (
	"context"
	"math"
	"os"
	"testing"

	"github.com/o0olele/detour-go/detour"
)

// The actual containing floor is1.5yd above the physical player foot; a lower
// neighboring edge lies0.2yd sideways. Detour's3D metric picks that edge, even
// though only the higher polygon contains the original requested XY.
func groundSelectionFixture(t *testing.T, copies int) (*planner, Point, detour.DtPolyRef) {
	t.Helper()
	const edge uint16 = 0xffff
	params := detour.DtNavMeshCreateParams{
		Nvp: 6, Cs: .1, Ch: .1, WalkableHeight: 2, WalkableRadius: .5, WalkableClimb: .5,
		BuildBvTree: true, Bmin: [3]float32{-100, 90, -8900}, Bmax: [3]float32{-90, 95, -8890},
	}
	add := func(y0, y1, z uint16) {
		first := uint16(len(params.Verts) / 3)
		params.Verts = append(params.Verts, y0, z, 0, y0, z, 40, y1, z, 40, y1, z, 0)
		params.Polys = append(params.Polys, first, first+1, first+2, first+3, edge, edge, edge, edge, edge, edge, edge, edge)
		params.PolyFlags = append(params.PolyFlags, 1)
		params.PolyAreas = append(params.PolyAreas, 1)
	}
	for range copies {
		add(0, 20, 15) // Y[-100,-98], meshZ91.5.
	}
	add(22, 40, 3) // Adjacent Y[-97.8,-96], meshZ90.3.
	params.VertCount, params.PolyCount = int32(len(params.Verts)/3), int32(len(params.PolyFlags))
	var data []byte
	var size int
	if !detour.DtCreateNavMeshData(&params, &data, &size) {
		t.Fatal("build containing-ground fixture")
	}
	mesh := detour.DtAllocNavMesh()
	meshParams := detour.DtNavMeshParams{Orig: params.Bmin, TileWidth: 10, TileHeight: 10, MaxTiles: 1, MaxPolys: 256}
	if st := mesh.Init(&meshParams); detour.DtStatusFailed(st) {
		t.Fatal(st)
	}
	var tileRef detour.DtTileRef
	if st := mesh.AddTile(data, size, detour.DT_TILE_FREE_DATA, 0, &tileRef); detour.DtStatusFailed(st) {
		t.Fatal(st)
	}
	query := detour.DtAllocNavMeshQuery()
	if st := query.Init(mesh, 256); detour.DtStatusFailed(st) {
		t.Fatal(st)
	}
	filter := detour.DtAllocDtQueryFilter()
	filter.SetIncludeFlags(1 | 8)
	filter.SetExcludeFlags(^uint16(1 | 8))
	p := &planner{mesh: mesh, query: query, filter: filter}
	start := Point{-8898, -98.01, 90.2}
	v := recast(start)
	ext := [3]float32{.5, 2, .5}
	var primary detour.DtPolyRef
	var nearest [3]float32
	if st := query.FindNearestPoly(v[:], ext[:], filter, &primary, nearest[:]); detour.DtStatusFailed(st) || primary == 0 {
		t.Fatal(st)
	}
	return p, start, primary
}

func TestGroundSelectionRejectsNearestEdgeAndUsesActualContainingPolygon(t *testing.T) {
	p, start, primary := groundSelectionFixture(t, 1)
	if _, err := p.groundHeightAt(primary, start); err == nil {
		t.Fatal("fixture must reproduce nearest-poly containment failure")
	}
	ref, height, err := p.groundRef(start)
	if err != nil || ref == primary || math.Abs(float64(height-91.5)) > .001 {
		t.Fatal("did not select actual containing layer", ref, height, err)
	}
	// No coordinate or radius adjustment was made to admit the containing poly.
	v := recast(start)
	var closest [3]float32
	if st := p.query.ClosestPointOnPoly(ref, v[:], closest[:], nil); detour.DtStatusFailed(st) || closest[0] != v[0] || closest[2] != v[2] {
		t.Fatal("selection moved the requested XY", closest, v)
	}
	// A valid ordinary primary remains the chosen polygon.
	inside := Point{-8898, -99, 91.5}
	v = recast(inside)
	ext := [3]float32{.5, 2, .5}
	var wanted detour.DtPolyRef
	p.query.FindNearestPoly(v[:], ext[:], p.filter, &wanted, closest[:])
	got, _, err := p.groundRef(inside)
	if err != nil || got != wanted {
		t.Fatal("valid primary changed", got, wanted, err)
	}
}

func TestGroundSelectionCannotBridgeGapChangeLayerOrUseForbiddenSurface(t *testing.T) {
	for _, mode := range []string{"gap", "other_layer", "forbidden", "bounded_candidates"} {
		t.Run(mode, func(t *testing.T) {
			copies := 1
			if mode == "bounded_candidates" {
				copies = maxGroundCandidates + 1
			}
			p, start, _ := groundSelectionFixture(t, copies)
			switch mode {
			case "gap":
				start.Y = -97.9 // At least0.1yd outside either polygon, not the2cm portal tolerance.
			case "other_layer":
				start.Z = 87
			case "forbidden":
				ref, _, err := p.groundRef(start)
				if err != nil {
					t.Fatal(err)
				}
				p.mesh.SetPolyFlags(ref, 2)
			}
			if _, _, err := p.groundRef(start); err == nil {
				t.Fatal("accepted unsupported ground selection", mode)
			}
		})
	}
}

// Static public client assets and saved client poses, never a game connection.
// Replan from every sample and intermediate movement point so an accepted
// route cannot strand the next query through nearest-poly ranking alone.
func TestPhysicalFargodeepContainingGroundReplans(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_FARGODEEP_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set independently extracted Fargodeep client assets")
	}
	owner := Point{-9820.384, 124.580925, 4.334642}
	for _, start := range []Point{{-9829.6, 123.46666, 4.391315}, {-9829.732, 125.2, 4.8503733}, {-9817.838, 127.24923, 4.7873263}} {
		path, err := PlanMapContext(context.Background(), dir, 0, start, owner)
		if err != nil {
			t.Fatal(start, err)
		}
		if path[0].X != start.X || path[0].Y != start.Y || math.Abs(float64(path[0].Z-start.Z)) > .02 {
			t.Fatal("start projected away", path[0], start)
		}
		queries := 0
		for i := 1; i < len(path); i++ {
			for _, fraction := range []float32{0, .25, .5, .75} {
				a, b := path[i-1], path[i]
				q := Point{a.X + (b.X-a.X)*fraction, a.Y + (b.Y-a.Y)*fraction, a.Z + (b.Z-a.Z)*fraction}
				if distance(q, owner) < .02 {
					continue
				}
				got, err := PlanMapContext(context.Background(), dir, 0, q, owner)
				if err != nil {
					t.Fatal("accepted movement point cannot replan", q, err)
				}
				if len(got) < 2 || len(got) > MaxPoints {
					t.Fatal(len(got))
				}
				queries++
			}
		}
		t.Logf("start%+v complete%d samples, %d sample/intermediate replans", start, len(path), queries)
	}
}
