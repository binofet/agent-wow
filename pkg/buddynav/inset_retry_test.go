package buddynav

import (
	"math"
	"testing"

	"github.com/o0olele/detour-go/detour"
)

// Real float32 Fargodeep portal coordinates. A thin-axis centroid offset can
// round back onto an edge even though the intended vector is 1cm long.
func insetPortalFixture(t *testing.T) (*planner, detour.DtPolyRef) {
	t.Helper()
	const edge uint16 = 0xffff
	params := detour.DtNavMeshCreateParams{
		Nvp: 3, Cs: 1, Ch: 1, WalkableHeight: 2, WalkableRadius: .5, WalkableClimb: .5,
		Bmin: [3]float32{}, Bmax: [3]float32{30, 20, 30}, BuildBvTree: true,
		Verts: []uint16{0, 0, 0, 10, 0, 0, 0, 0, 10}, VertCount: 3,
		Polys: []uint16{0, 1, 2, edge, edge, edge}, PolyCount: 1,
		PolyFlags: []uint16{1}, PolyAreas: []uint8{1},
	}
	var data []byte
	var size int
	if !detour.DtCreateNavMeshData(&params, &data, &size) {
		t.Fatal("build thin portal")
	}
	mesh := detour.DtAllocNavMesh()
	if st := mesh.Init(&detour.DtNavMeshParams{Orig: params.Bmin, TileWidth: 40, TileHeight: 40, MaxTiles: 1, MaxPolys: 16}); detour.DtStatusFailed(st) {
		t.Fatal(st)
	}
	var tileRef detour.DtTileRef
	if st := mesh.AddTile(data, size, detour.DT_TILE_FREE_DATA, 0, &tileRef); detour.DtStatusFailed(st) {
		t.Fatal(st)
	}
	ref := detour.DtPolyRef(tileRef)
	var tile *detour.DtMeshTile
	var poly *detour.DtPoly
	if st := mesh.GetTileAndPolyByRef(ref, &tile, &poly); detour.DtStatusFailed(st) {
		t.Fatal(st)
	}
	copy(tile.Verts, []float32{195.19998, 48.424896, -9791.733, 196.26666, 47.091553, -9791.467, 213.33331, 41.2249, -9792})
	query := detour.DtAllocNavMeshQuery()
	if st := query.Init(mesh, 128); detour.DtStatusFailed(st) {
		t.Fatal(st)
	}
	filter := detour.DtAllocDtQueryFilter()
	filter.SetIncludeFlags(1 | 8)
	filter.SetExcludeFlags(^uint16(1 | 8))
	return &planner{mesh: mesh, query: query, filter: filter}, ref
}

func TestInsetRetryThinPortalKeepsSamePolygonAndOneCentimetreBound(t *testing.T) {
	p, ref := insetPortalFixture(t)
	original := [3]float32{210.2664, 42.4426, -9791.9551}
	// Reproduce the previous centroid-only query at the actual world scale.
	old := original
	var tile *detour.DtMeshTile
	var poly *detour.DtPoly
	p.mesh.GetTileAndPolyByRef(ref, &tile, &poly)
	var cx, cy float32
	for i := 0; i < int(poly.VertCount); i++ {
		k := int(poly.Verts[i]) * 3
		cx += tile.Verts[k]
		cy += tile.Verts[k+2]
	}
	cx /= float32(poly.VertCount)
	cy /= float32(poly.VertCount)
	dx, dy := cx-old[0], cy-old[2]
	f := float32(math.Min(.01/math.Hypot(float64(dx), float64(dy)), 1))
	old[0] += dx * f
	old[2] += dy * f
	var height float32
	if st := p.query.GetPolyHeight(ref, old[:], &height); !detour.DtStatusFailed(st) {
		t.Fatal("fixture did not reproduce centroid-only height rejection", old, height)
	}
	got := original
	if err := p.insetRayPoint(ref, &got); err != nil {
		t.Fatal(err)
	}
	if d := math.Hypot(float64(got[0]-original[0]), float64(got[2]-original[2])); d > .010001 || got[1] != original[1] {
		t.Fatal("query inset widened/moved floor", got, d)
	}
	if st := p.query.GetPolyHeight(ref, got[:], &height); detour.DtStatusFailed(st) || !finite(height) {
		t.Fatal("retry lacks actual same-poly height", st, height)
	}
	// This is a query adjustment; the caller's actual movement point is intact.
	if original != [3]float32{210.2664, 42.4426, -9791.9551} {
		t.Fatal("movement sample changed")
	}
}

func TestInsetRetryCannotProjectOutsideOrForbiddenPolygon(t *testing.T) {
	for _, mode := range []string{"outside", "forbidden", "foreign_ref"} {
		t.Run(mode, func(t *testing.T) {
			p, ref := insetPortalFixture(t)
			v := [3]float32{210.2664, 42.4426, -9791.9551}
			switch mode {
			case "outside":
				v[2] -= 1
			case "forbidden":
				p.mesh.SetPolyFlags(ref, 2)
			case "foreign_ref":
				ref = 0
			}
			before := v
			if err := p.insetRayPoint(ref, &v); err == nil || v != before {
				t.Fatal("unsafe inset accepted or mutated input", mode, v, err)
			}
		})
	}
}
