package buddynav

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/o0olele/detour-go/detour"
)

func detailHillMesh(t *testing.T, stacked ...bool) *planner {
	t.Helper()
	const edge = uint16(0xffff)
	params := detour.DtNavMeshCreateParams{Verts: []uint16{0, 0, 0, 0, 0, 20, 20, 0, 20, 20, 0, 0}, VertCount: 4, Polys: []uint16{0, 1, 2, 3, edge, edge, edge, edge}, PolyFlags: []uint16{1}, PolyAreas: []uint8{1}, PolyCount: 1, Nvp: 4, Bmin: [3]float32{-100, 90, -8900}, Bmax: [3]float32{-80, 96, -8880}, WalkableHeight: 2, WalkableRadius: .5, WalkableClimb: .5, Cs: 1, Ch: 1, BuildBvTree: true,
		DetailMeshes: []uint32{0, 5, 0, 4}, DetailVerts: []float32{-100, 90, -8900, -100, 90, -8880, -80, 90, -8880, -80, 90, -8900, -90, 94, -8890}, DetailVertsCount: 5, DetailTris: []uint8{0, 1, 4, 1, 1, 2, 4, 1, 2, 3, 4, 1, 3, 0, 4, 1}, DetailTriCount: 4}
	if len(stacked) > 0 && stacked[0] {
		params.Verts = append(params.Verts, 0, 10, 0, 0, 10, 20, 20, 10, 20, 20, 10, 0)
		params.VertCount = 8
		params.PolyCount = 2
		params.Polys = append(params.Polys, 4, 5, 6, 7, edge, edge, edge, edge)
		params.PolyFlags = append(params.PolyFlags, 1)
		params.PolyAreas = append(params.PolyAreas, 1)
		params.DetailMeshes = append(params.DetailMeshes, 5, 4, 4, 2)
		params.DetailVerts = append(params.DetailVerts, -100, 100, -8900, -100, 100, -8880, -80, 100, -8880, -80, 100, -8900)
		params.DetailVertsCount = 9
		params.DetailTris = append(params.DetailTris, 0, 1, 2, 5, 0, 2, 3, 20)
		params.DetailTriCount = 6
		params.Bmax[1] = 102
	}
	var data []byte
	var size int
	if !detour.DtCreateNavMeshData(&params, &data, &size) {
		t.Fatal("detail hill fixture")
	}
	mesh := detour.DtAllocNavMesh()
	mp := detour.DtNavMeshParams{Orig: params.Bmin, TileWidth: 20, TileHeight: 20, MaxTiles: 1, MaxPolys: 16}
	if st := mesh.Init(&mp); detour.DtStatusFailed(st) {
		t.Fatal(st)
	}
	var ref detour.DtTileRef
	if st := mesh.AddTile(data, size, detour.DT_TILE_FREE_DATA, 0, &ref); detour.DtStatusFailed(st) {
		t.Fatal(st)
	}
	q := detour.DtAllocNavMeshQuery()
	if st := q.Init(mesh, 128); detour.DtStatusFailed(st) {
		t.Fatal(st)
	}
	f := detour.DtAllocDtQueryFilter()
	f.SetIncludeFlags(1 | 8)
	f.SetExcludeFlags(^uint16(1 | 8))
	return &planner{mesh: mesh, query: q, filter: f}
}

type hillBackend struct {
	*planner
	floorFn func(Point, float32) (float32, error)
	walkErr error
}

func (h *hillBackend) terrain(p Point) (float32, bool) { return p.Z, true }
func (h *hillBackend) floor(_ context.Context, p Point, height, _ float32) (float32, error) {
	if h.floorFn != nil {
		return h.floorFn(p, height)
	}
	return height, nil
}
func (h *hillBackend) walkable(a, b Point) error {
	if h.walkErr != nil {
		return h.walkErr
	}
	return h.planner.walkable(a, b)
}

func TestLayerBoundSurfaceSamplesActualDetailHillInsteadOfRawZChord(t *testing.T) {
	p := detailHillMesh(t, true)
	a, b := Point{-8899, -90, 90.4}, Point{-8881, -90, 90.4}
	r, e := p.findPath(a, b)
	if e != nil || len(r.Points) != 2 {
		t.Fatal(r, e)
	}
	q := Point{-8890, -90, 90.4}
	height, e := p.ground(q)
	if e != nil || height-q.Z < 3 {
		t.Fatal("fixture no longer exposes raw chord gap", height, e)
	}
	h, water, bound, e := p.groundOnRoute(q, a, b)
	if e != nil || !bound || water || math.Abs(float64(h-94)) > .001 {
		t.Fatal(h, water, bound, e)
	}
	path, e := plan(context.Background(), &hillBackend{planner: p}, a, b)
	if e != nil {
		t.Fatal(e)
	}
	peak := float32(0)
	for i, q := range path {
		peak = max(peak, q.Z)
		if i > 0 && (distance(path[i-1], q) > 2 || math.Abs(float64(q.Z-path[i-1].Z)) > 1) {
			t.Fatal("physical caps changed")
		}
	}
	if peak < 93.5 || distance(path[0], a) > EndpointTolerance || distance(path[len(path)-1], b) > EndpointTolerance {
		t.Fatal(path)
	}
}

func TestLayerBoundSurfaceCannotEscapePolygonOrSubstituteAnotherLayer(t *testing.T) {
	p := detailHillMesh(t, true)
	a, b := Point{-8899, -90, 90.4}, Point{-8881, -90, 90.4}
	if _, e := p.findPath(a, b); e != nil {
		t.Fatal(e)
	}
	// A real disconnected polygon at100 overlaps the entire lower route.
	// Nearest-poly at100 sees it, while the bound corridor stays on lower94.
	if h, e := p.ground(Point{-8890, -90, 100}); e != nil || h != 100 {
		t.Fatal("upper layer fixture", h, e)
	}
	if h, _, known, e := p.groundOnRoute(Point{-8890, -90, 100}, a, b); e != nil || !known || h != 94 {
		t.Fatal(h, known, e)
	}
	for _, q := range []Point{{-8890, -80, 90}, {-8890, -89.9, 90}, {-8870, -90, 90}, {float32(math.NaN()), -90, 90}} {
		if _, _, bound, e := p.groundOnRoute(q, a, b); !bound || e == nil {
			t.Fatal("escaped route", q, bound, e)
		}
	}
	if _, _, bound, e := p.groundOnRoute(Point{-8890, -90, 90}, Point{-8899, -90, 100}, b); bound || e != nil {
		t.Fatal("unmatched segment obtained authority")
	}
	ref := p.routeSurfaces[0].ref
	p.mesh.SetPolyFlags(ref, 0)
	if _, _, bound, e := p.groundOnRoute(Point{-8890, -90, 90}, a, b); !bound || e == nil {
		t.Fatal("blocked route polygon accepted")
	}
	// A failed new findPath cannot retain a previous route's layer authority.
	p.findPath(Point{-8800, -80, 90}, b)
	if len(p.routeSurfaces) != 0 {
		t.Fatal("failed path retained route surfaces")
	}
}

func TestLayerBoundResamplingStillRequiresPhysicalFloorRaysAndShallowWater(t *testing.T) {
	a, b := Point{-8899, -90, 90.4}, Point{-8881, -90, 90.4}
	for _, mode := range []string{"physical_layer", "cliff", "wall", "deep_water"} {
		t.Run(mode, func(t *testing.T) {
			p := detailHillMesh(t)
			backend := &hillBackend{planner: p}
			switch mode {
			case "physical_layer":
				backend.floorFn = func(_ Point, h float32) (float32, error) { return h + 3, nil }
			case "cliff":
				backend.floorFn = func(q Point, h float32) (float32, error) {
					if q.X > -8890 {
						return h - 1.5, nil
					}
					return h, nil
				}
			case "wall":
				backend.walkErr = errors.New("collision wall")
			case "deep_water":
				r, e := p.findPath(a, b)
				if e != nil || r == nil {
					t.Fatal(e)
				}
				p.mesh.SetPolyFlags(p.routeSurfaces[0].ref, 8)
				backend.floorFn = func(_ Point, h float32) (float32, error) { return h - 1.9, nil }
			}
			if _, e := plan(context.Background(), backend, a, b); e == nil {
				t.Fatal("resampling bypassed physical gate", mode)
			}
		})
	}
}
