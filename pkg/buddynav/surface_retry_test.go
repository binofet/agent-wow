package buddynav

import (
	"testing"

	"github.com/o0olele/detour-go/detour"
)

func surfaceRetryFixture(t *testing.T, kind string) *planner {
	t.Helper()
	const edge uint16 = 0xffff
	params := detour.DtNavMeshCreateParams{Nvp: 6, WalkableHeight: 2, WalkableRadius: .5, WalkableClimb: .5, BuildBvTree: true}
	if kind == "landing" {
		params.Bmin, params.Bmax = [3]float32{-170.66666, 97.91511, -8895.999}, [3]float32{-150, 105, -8880}
		params.Cs, params.Ch = .26666668, .26666668
		params.Verts = []uint16{21, 2, 14, 8, 0, 25, 0, 2, 49, 13, 2, 50, 37, 2, 30, 36, 0, 25, 16, 2, 18}
		params.VertCount, params.PolyCount = 7, 2
		params.Polys = []uint16{0, 6, 1, edge, edge, edge, edge, edge, 1, edge, edge, edge, 0, 1, 2, 3, 4, 5, 0, edge, edge, edge, edge, edge}
		params.PolyFlags, params.PolyAreas = []uint16{1, 1}, []uint8{1, 1}
	} else {
		params.Bmin, params.Bmax = [3]float32{-100, 90, -8900}, [3]float32{-80, 100, -8880}
		params.Cs, params.Ch = 1, 1
		params.Verts = []uint16{0, 0, 0, 0, 0, 4, 4, 0, 4, 4, 0, 0, 6, 0, 0, 6, 0, 4, 10, 0, 4, 10, 0, 0}
		params.VertCount, params.PolyCount = 8, 2
		params.Polys = []uint16{0, 1, 2, 3, edge, edge, edge, edge, edge, edge, edge, edge, 4, 5, 6, 7, edge, edge, edge, edge, edge, edge, edge, edge}
		params.PolyFlags, params.PolyAreas = []uint16{1, 1}, []uint8{1, 1}
	}
	var data []byte
	var size int
	if !detour.DtCreateNavMeshData(&params, &data, &size) {
		t.Fatal("build surface fixture")
	}
	mesh := detour.DtAllocNavMesh()
	meshParams := detour.DtNavMeshParams{Orig: params.Bmin, TileWidth: 40, TileHeight: 40, MaxTiles: 1, MaxPolys: 16}
	if st := mesh.Init(&meshParams); detour.DtStatusFailed(st) {
		t.Fatal(st)
	}
	var tileRef detour.DtTileRef
	if st := mesh.AddTile(data, size, detour.DT_TILE_FREE_DATA, 0, &tileRef); detour.DtStatusFailed(st) {
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

func TestSurfaceRetryThinLandingUsesEndpointPolygon(t *testing.T) {
	p := surfaceRetryFixture(t, "landing")
	a, b := Point{-8891.679, -165.76001, 98.34174}, Point{-8892.266, -165.06668, 98.44844}
	// The bounded same-poly inset can now make the primary ray succeed too.
	// Independently retain the endpoint retry's actual-poly/height proof.
	endpointVerified := false
	for _, hint := range [...]Point{a, b} {
		if ref, _, err := p.groundRef(hint); err == nil && p.endpointPolygonRay(ref, a, b) {
			endpointVerified = true
		}
	}
	if !endpointVerified {
		t.Fatal("thin landing lacks complete endpoint-polygon ray")
	}
	if err := p.walkable(a, b); err != nil {
		t.Fatal(err)
	}
}

func TestSurfaceRetryRejectsWallGapLayerAndOutsidePolygon(t *testing.T) {
	p := surfaceRetryFixture(t, "wall")
	for _, pair := range [][2]Point{
		{{-8898, -99, 90}, {-8898, -91, 90}}, // Disconnected polygons across an actual gap.
		{{-8898, -99, 90}, {-8898, -95, 90}}, // Endpoint beyond the first polygon's wall.
	} {
		if err := p.walkable(pair[0], pair[1]); err == nil {
			t.Fatalf("accepted unsafe segment %+v", pair)
		}
	}
	ref, _, err := p.groundRef(Point{-8898, -99, 90})
	if err != nil {
		t.Fatal(err)
	}
	if p.endpointPolygonRay(ref, Point{-8898, -99, 90}, Point{-8898, -98, 90.2}) {
		t.Fatal("endpoint layer mismatch accepted")
	}
	if p.endpointPolygonRay(ref, Point{-8898, -99, 90}, Point{-8898, -98, 93}) {
		t.Fatal("different floor accepted by endpoint retry")
	}
	if p.endpointPolygonRay(ref, Point{-8898, -99, 90}, Point{-8898, -95, 90}) {
		t.Fatal("endpoint outside shared polygon accepted")
	}
}

func TestSurfaceRetryPreservesSuccessfulPrimaryRay(t *testing.T) {
	p := surfaceRetryFixture(t, "wall")
	a, b := Point{-8898, -99, 90}, Point{-8898, -98, 90}
	if err := p.walkablePrimary(a, b); err != nil {
		t.Fatal(err)
	}
	if err := p.walkable(a, b); err != nil {
		t.Fatal(err)
	}
}
