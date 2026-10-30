package buddynav

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/azerothcore/AzerothGhost/pathfinding"
	"github.com/o0olele/detour-go/detour"
)

var testStart = Point{-8900, -100, 90}
var testEnd = Point{-8895, -100, 90}

type fakeBackend struct {
	result    *pathfinding.PathResult
	findErr   error
	onFind    func()
	groundFn  func(Point) (float32, error)
	terrainOK bool
	walkErr   error
	samples   []Point
	walks     int
}

func fakeRoute(points ...Point) *fakeBackend {
	r := &pathfinding.PathResult{Type: pathfinding.PathfindNormal}
	for _, p := range points {
		r.Points = append(r.Points, pathfinding.Point3D{X: p.X, Y: p.Y, Z: p.Z})
	}
	return &fakeBackend{result: r, terrainOK: true}
}
func (f *fakeBackend) findPath(Point, Point) (*pathfinding.PathResult, error) {
	if f.onFind != nil {
		f.onFind()
	}
	return f.result, f.findErr
}
func (f *fakeBackend) ground(p Point) (float32, error) {
	f.samples = append(f.samples, p)
	if f.groundFn != nil {
		return f.groundFn(p)
	}
	return p.Z, nil
}
func (f *fakeBackend) terrain(p Point) (float32, bool) { return p.Z, f.terrainOK }
func (f *fakeBackend) walkable(Point, Point) error     { f.walks++; return f.walkErr }

func TestPlanSamplesEntireCompleteRoute(t *testing.T) {
	f := fakeRoute(testStart, testEnd)
	points, err := plan(context.Background(), f, testStart, testEnd)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 6 || len(f.samples) != 6 || f.walks != 5 {
		t.Fatalf("points=%d samples=%d walks=%d", len(points), len(f.samples), f.walks)
	}
	for i := 1; i < len(points); i++ {
		if distance(points[i-1], points[i]) > 1.001 {
			t.Fatal("unsampled segment")
		}
	}
	if points[0] != testStart || points[len(points)-1] != testEnd {
		t.Fatalf("wrong endpoints: %v", points)
	}
}

func TestPlanRejectsEveryFallbackFlag(t *testing.T) {
	for _, kind := range []pathfinding.PathType{pathfinding.PathfindBlank, pathfinding.PathfindNopath, pathfinding.PathfindNormal | pathfinding.PathfindNotUsingPath, pathfinding.PathfindNormal | pathfinding.PathfindIncomplete, pathfinding.PathfindNormal | pathfinding.PathfindShort, pathfinding.PathfindShortcut, pathfinding.PathfindNormal | pathfinding.PathfindFarFromPolyStart, pathfinding.PathfindNormal | pathfinding.PathfindFarFromPolyEnd} {
		t.Run(fmt.Sprintf("0x%x", kind), func(t *testing.T) {
			f := fakeRoute(testStart, testEnd)
			f.result.Type = kind
			if _, err := plan(context.Background(), f, testStart, testEnd); err == nil {
				t.Fatalf("accepted fallback flags%x", kind)
			}
			if len(f.samples) != 0 {
				t.Fatal("sampled rejected route")
			}
		})
	}
}

func TestPlanRejectsIncompleteAndExcessiveGeometry(t *testing.T) {
	tests := map[string][]Point{
		"capped short endpoint":    {testStart, {-8898, -100, 90}},
		"start displaced":          {{-8897, -100, 90}, testEnd},
		"out of bounds midpoint":   {testStart, {-8600, -100, 90}, testEnd},
		"nonfinite midpoint":       {testStart, {-8898, float32(math.NaN()), 90}, testEnd},
		"detour over route budget": {testStart, {-8900, 0, 90}, testEnd},
		"one point":                {testStart},
	}
	tooMany := make([]Point, MaxPoints+1)
	for i := range tooMany {
		tooMany[i] = testStart
	}
	tests["too many points"] = tooMany
	for name, points := range tests {
		t.Run(name, func(t *testing.T) {
			f := fakeRoute(points...)
			if _, err := plan(context.Background(), f, testStart, testEnd); err == nil {
				t.Fatal("accepted invalid geometry")
			}
		})
	}
}

func TestPlanRejectsUnsafeIntermediateSamples(t *testing.T) {
	tests := map[string]func(*fakeBackend){
		"water": func(f *fakeBackend) {
			f.groundFn = func(p Point) (float32, error) {
				if p.X >= -8898 {
					return 0, errors.New("no ground-only polygon")
				}
				return p.Z, nil
			}
		},
		"blocked ground segment": func(f *fakeBackend) { f.walkErr = errors.New("surface wall") },
		"missing terrain":        func(f *fakeBackend) { f.terrainOK = false },
		"wrong height":           func(f *fakeBackend) { f.groundFn = func(p Point) (float32, error) { return p.Z + 2, nil } },
		"nonfinite height":       func(f *fakeBackend) { f.groundFn = func(Point) (float32, error) { return float32(math.NaN()), nil } },
		"cliff": func(f *fakeBackend) {
			f.groundFn = func(p Point) (float32, error) {
				if p.X >= -8898 {
					return p.Z + 1.25, nil
				}
				return p.Z, nil
			}
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			f := fakeRoute(testStart, testEnd)
			setup(f)
			if _, err := plan(context.Background(), f, testStart, testEnd); err == nil {
				t.Fatal("accepted unsafe sample")
			}
		})
	}
}

func TestPlanRejectsInvalidRequestBeforeAssets(t *testing.T) {
	for _, tc := range []struct {
		mapID      uint32
		start, end Point
	}{
		{1, testStart, testEnd}, {0, Point{float32(math.Inf(1)), 0, 90}, testEnd}, {0, testStart, Point{-8900, -501, 90}}, {0, testStart, Point{-8700, -100, 90}}, {0, testStart, Point{-8895, -100, 39}},
	} {
		if _, err := Plan("", tc.mapID, tc.start, tc.end); err == nil {
			t.Fatalf("accepted invalid request %+v", tc)
		}
	}
	if _, err := Plan("", 0, testStart, testEnd); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("missing assets=%v", err)
	}
	if _, err := Plan(t.TempDir(), 0, testStart, testEnd); err == nil {
		t.Fatal("missing mesh accepted")
	}
}

func TestPlanCancellationProducesNoRoute(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := fakeRoute(testStart, testEnd)
	f.onFind = cancel
	if _, err := plan(ctx, f, testStart, testEnd); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if len(f.samples) != 0 {
		t.Fatal("canceled route sampled")
	}
	if _, err := PlanContext(ctx, "", 0, testStart, testEnd); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func validTileHeader() []byte {
	b := make([]byte, 156)
	for i, v := range []uint32{0x4d4d4150, 7, 20, 100} {
		binary.LittleEndian.PutUint32(b[i*4:], v)
	}
	b[16] = 1
	binary.LittleEndian.PutUint32(b[56:], uint32(detour.DT_NAVMESH_MAGIC))
	binary.LittleEndian.PutUint32(b[60:], 7)
	return b
}
func TestTileHeaderRequiresVerifiedMMAP20Format(t *testing.T) {
	if body, err := tileBody(validTileHeader()); err != nil || len(body) != 100 {
		t.Fatalf("body=%d error=%v", len(body), err)
	}
	for _, tc := range []struct {
		name   string
		offset int
		value  uint32
	}{
		{"wrong magic", 0, 0}, {"wrong Detour version", 4, 8}, {"older mmap", 8, 19}, {"future mmap", 8, 21}, {"truncated size", 12, 101}, {"bad body magic", 56, 0}, {"bad body version", 60, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := validTileHeader()
			binary.LittleEndian.PutUint32(b[tc.offset:], tc.value)
			if _, err := tileBody(b); err == nil {
				t.Fatal("accepted malformed tile")
			}
		})
	}
	for _, n := range []int{0, 55, 56, 99, 155} {
		if _, err := tileBody(validTileHeader()[:n]); err == nil {
			t.Fatalf("accepted truncated length%d", n)
		}
	}
	b := validTileHeader()
	b[16] = 2
	if _, err := tileBody(b); err == nil {
		t.Fatal("accepted invalid liquid marker")
	}
}

func TestReadBoundedDoesNotReadOversizedAssets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "asset")
	if err := os.WriteFile(p, make([]byte, 29), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBounded(p, 28); err == nil {
		t.Fatal("accepted oversized asset")
	}
}

func TestMalformedMeshReturnsErrorWithoutPanic(t *testing.T) {
	dir := t.TempDir()
	params := make([]byte, 28)
	for i, v := range []float32{0, 0, 0, 533.3333, 533.3333} {
		binary.LittleEndian.PutUint32(params[i*4:], math.Float32bits(v))
	}
	binary.LittleEndian.PutUint32(params[20:], 1)
	binary.LittleEndian.PutUint32(params[24:], 128)
	if err := os.WriteFile(filepath.Join(dir, "000.mmap"), params, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0004832.mmtile"), validTileHeader(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMesh(dir); err == nil {
		t.Fatal("malformed Detour body accepted")
	}
}

func testMesh(t *testing.T) *planner {
	t.Helper()
	const edge = uint16(0xffff)
	params := detour.DtNavMeshCreateParams{
		Verts: []uint16{0, 0, 0, 0, 0, 10, 5, 0, 10, 5, 0, 0, 10, 0, 10, 10, 0, 0, 15, 0, 10, 15, 0, 0}, VertCount: 8,
		Polys:     []uint16{0, 1, 2, 3, edge, edge, 1, edge, 3, 2, 4, 5, 0, edge, 2, edge, 5, 4, 6, 7, 1, edge, edge, edge},
		PolyFlags: []uint16{1, 8, 1}, PolyAreas: []uint8{1, 8, 1}, PolyCount: 3, Nvp: 4,
		Bmin: [3]float32{-100, 90, -8900}, Bmax: [3]float32{-85, 92, -8890},
		WalkableHeight: 2, WalkableRadius: 0.5, WalkableClimb: 0.5, Cs: 1, Ch: 1, BuildBvTree: true,
	}
	var data []byte
	var size int
	if !detour.DtCreateNavMeshData(&params, &data, &size) {
		t.Fatal("build mesh fixture")
	}
	mesh := detour.DtAllocNavMesh()
	mp := detour.DtNavMeshParams{Orig: params.Bmin, TileWidth: 15, TileHeight: 10, MaxTiles: 1, MaxPolys: 16}
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
	f.SetIncludeFlags(1)
	f.SetExcludeFlags(^uint16(1))
	return &planner{query: q, mesh: mesh, filter: f}
}

func TestGroundOnlyDetourRejectsWaterAndPartialPath(t *testing.T) {
	p := testMesh(t)
	a := Point{-8898, -98, 90}
	near := Point{-8895, -98, 90}
	water := Point{-8898, -92, 90}
	b := Point{-8898, -87, 90}
	if r, err := p.findPath(a, near); err != nil || len(r.Points) < 2 {
		t.Fatalf("ground route %v %v", r, err)
	}
	if _, err := p.ground(water); err == nil {
		t.Fatal("water polygon accepted as ground")
	}
	if _, err := p.findPath(a, b); err == nil {
		t.Fatal("partial route across excluded water accepted")
	}
	if err := p.walkable(a, b); err == nil {
		t.Fatal("ground ray crossed water")
	}
	if err := p.walkable(a, near); err != nil {
		t.Fatalf("ground ray rejected: %v", err)
	}
}

// Explicit opt-in uses independently copied static data, never a realm's live
// filesystem or a game connection. Default CI uses the deterministic tests above.
func TestPhysicalNorthshireRoutes(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_TEST_DATA_DIR to static extracted data")
	}
	start := Point{-8914.757, -131.393, 80.94807}
	for _, dest := range []Point{{-8902.59, -162.606, 82.0223}, {-8947.64, -132.319, 83.7199}} {
		route, err := Plan(dir, 0, start, dest)
		if err != nil {
			t.Fatal(err)
		}
		if len(route) < 2 || len(route) > MaxPoints || distance(route[len(route)-1], dest) > EndpointTolerance {
			t.Fatalf("invalid physical route%v", route)
		}
	}
	for _, pair := range [][2]Point{{{-8902.59, -162.606, 82.0223}, {-8947.64, -132.319, 83.7199}}, {{-8947.64, -132.319, 83.7199}, {-8902.59, -162.606, 82.0223}}} {
		if _, err := Plan(dir, 0, pair[0], pair[1]); err != nil {
			t.Fatalf("questgiver-to-questgiver route: %v", err)
		}
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Plan(dir, 0, start, Point{-8902.59, -162.606, 82.0223}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
