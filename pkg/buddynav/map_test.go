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
	"time"

	"github.com/o0olele/detour-go/detour"
)

func TestCurrentMapWindowIsLocalBoundedAndIndependentOfStarterRegions(t *testing.T) {
	for _, mid := range []uint32{0, 1, 36, 530, 571} {
		for _, a := range []Point{{-9900, -1200, 45}, {-10630, 1100, 30}, {1500, -4200, 50}, {-6000, 400, 380}, {16600, 16600, -20}} {
			b := a
			b.X += 30
			w, err := newMapWindow(mid, a, b)
			if err != nil || !w.contains(a) || !w.contains(b) || len(w.meshTiles()) > 9 {
				t.Fatal(w, err)
			}
			for _, name := range w.meshTiles() {
				if !strings.HasPrefix(name, fmt.Sprintf("%03d", mid)) || filepath.Base(name) != name {
					t.Fatal(name)
				}
			}
			far := a
			far.X += 1700
			if w.contains(far) {
				t.Fatal("window expanded arbitrarily", w)
			}
		}
	}
	for _, tc := range []struct {
		mid  uint32
		a, b Point
	}{{1000, testStart, testEnd}, {0, Point{X: float32(math.NaN())}, testEnd}, {0, testStart, Point{X: float32(math.Inf(1))}}, {0, Point{18000, 0, 0}, Point{18001, 0, 0}}, {0, Point{0, 0, 100001}, Point{1, 0, 100001}}, {0, Point{}, Point{X: 241}}} {
		if _, err := PlanMapContext(context.Background(), "", tc.mid, tc.a, tc.b); err == nil || strings.Contains(err.Error(), "directory") {
			t.Fatal(tc, err)
		}
	}
}

func mapFixture(t *testing.T, dir string, mid uint32, a Point) {
	t.Helper()
	const edge = uint16(0xffff)
	params := detour.DtNavMeshCreateParams{Verts: []uint16{0, 0, 0, 0, 0, 20, 20, 0, 20, 20, 0, 0}, VertCount: 4, Polys: []uint16{0, 1, 2, 3, edge, edge, edge, edge}, PolyFlags: []uint16{1}, PolyAreas: []uint8{1}, PolyCount: 1, Nvp: 4, Bmin: [3]float32{a.Y - 5, a.Z, a.X - 5}, Bmax: [3]float32{a.Y + 15, a.Z + 2, a.X + 15}, WalkableHeight: 2, WalkableRadius: .5, WalkableClimb: .5, Cs: 1, Ch: 1, BuildBvTree: true}
	var data []byte
	var size int
	if !detour.DtCreateNavMeshData(&params, &data, &size) {
		t.Fatal("fixture mesh")
	}
	mmap := make([]byte, 28)
	for i, v := range []float32{params.Bmin[0], params.Bmin[1], params.Bmin[2], 533.3333, 533.3333} {
		binary.LittleEndian.PutUint32(mmap[i*4:], math.Float32bits(v))
	}
	binary.LittleEndian.PutUint32(mmap[20:], 1)
	binary.LittleEndian.PutUint32(mmap[24:], 128)
	tile := make([]byte, 56)
	for i, v := range []uint32{0x4d4d4150, 7, 20, uint32(size)} {
		binary.LittleEndian.PutUint32(tile[i*4:], v)
	}
	tile = append(tile, data...)
	x, y := mapGrid(a)
	assets := map[string][]byte{fmt.Sprintf("mmaps/%03d.mmap", mid): mmap, fmt.Sprintf("mmaps/%03d%02d%02d.mmtile", mid, x, y): tile, fmt.Sprintf("vmaps/%03d_%02d_%02d.vmtile", mid, y, x): append([]byte("VMAP_4.8"), 0, 0, 0, 0)}
	assets[fmt.Sprintf("vmaps/%03d.vmtree", mid)] = append([]byte("VMAP_4.8"), 1)
	terrain := make([]byte, 56)
	binary.LittleEndian.PutUint32(terrain, 0x5350414d)
	binary.LittleEndian.PutUint32(terrain[4:], 9)
	binary.LittleEndian.PutUint32(terrain[20:], 40)
	binary.LittleEndian.PutUint32(terrain[24:], 16)
	binary.LittleEndian.PutUint32(terrain[40:], 0x5447484d)
	binary.LittleEndian.PutUint32(terrain[44:], 1)
	binary.LittleEndian.PutUint32(terrain[48:], math.Float32bits(a.Z))
	binary.LittleEndian.PutUint32(terrain[52:], math.Float32bits(a.Z))
	assets[fmt.Sprintf("maps/%03d%02d%02d.map", mid, x, y)] = terrain
	for name, b := range assets {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCurrentMapCatalogAndAbsentTerrainNeverBecomeInventedFloor(t *testing.T) {
	if len(clientMapIDs) != 135 {
		t.Fatal("client catalog changed without source receipt")
	}
	for i, id := range clientMapIDs {
		if !clientMap(id) || i > 0 && id <= clientMapIDs[i-1] {
			t.Fatal("invalid client catalog")
		}
	}
	for _, id := range []uint32{2, 99, 725, 999, ^uint32(0)} {
		if clientMap(id) {
			t.Fatal("invented client map", id)
		}
	}
	isolateMapCache(t)
	dir := regionAssetDirectories(t)
	a, b := Point{1500, -4200, 0}, Point{1505, -4200, 0}
	mapFixture(t, dir, 1, a)
	x, y := mapGrid(a)
	file := filepath.Join(dir, fmt.Sprintf("maps/001%02d%02d.map", x, y))
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for i := 20; i < 28; i++ {
		data[i] = 0
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanMapContext(context.Background(), dir, 1, a, b); err == nil {
		t.Fatal("absent height section invented physical zero floor")
	}
}

func TestCurrentMapVMAPAloneMustActuallyProveMissingTerrainFloor(t *testing.T) {
	isolateMapCache(t)
	dir := regionAssetDirectories(t)
	a, b := Point{-8900, -100, 90}, Point{-8899, -100, 90}
	mapFixture(t, dir, 36, a)
	x, y := mapGrid(a)
	if err := os.Remove(filepath.Join(dir, fmt.Sprintf("maps/036%02d%02d.map", x, y))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("vmaps/036_%02d_%02d.vmtile", y, x)), floorSpawnFixture("actual.wmo", Point{}, 1), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vmaps/actual.wmo.vmo"), floorModelFixture(false), 0600); err != nil {
		t.Fatal(err)
	}
	path, err := PlanMapContext(context.Background(), dir, 36, a, b)
	if err != nil || len(path) < 2 || path[0].Z != 90 || path[len(path)-1].Z != 90 {
		t.Fatal("real VMAP floor rejected", path, err)
	}
	key := currentMapPlanners.order[0]
	p := currentMapPlanners.items[key]
	if _, known := p.terrain(a); known {
		t.Fatal("absent terrain became known")
	}
}

func isolateMapCache(t *testing.T) {
	t.Helper()
	<-currentMapPlanners.gate
	oldItems, oldOrder := currentMapPlanners.items, currentMapPlanners.order
	currentMapPlanners.items = make(map[mapPlannerKey]*planner)
	currentMapPlanners.order = nil
	currentMapPlanners.gate <- struct{}{}
	t.Cleanup(func() {
		<-currentMapPlanners.gate
		currentMapPlanners.items, currentMapPlanners.order = oldItems, oldOrder
		currentMapPlanners.gate <- struct{}{}
	})
}

func TestCurrentMapUsesActualMapFilesAndEvictsBoundedCache(t *testing.T) {
	isolateMapCache(t)
	dir := regionAssetDirectories(t)
	a, b := Point{1500, -4200, 50}, Point{1505, -4200, 50}
	for _, mid := range []uint32{0, 1, 530, 571} {
		mapFixture(t, dir, mid, a)
		path, err := PlanMapContext(context.Background(), dir, mid, a, b)
		if err != nil || len(path) < 2 || distance(path[0], a) > .01 || distance(path[len(path)-1], b) > .01 {
			t.Fatal(mid, path, err)
		}
		if len(currentMapPlanners.items) > maxMapPlanners || len(currentMapPlanners.order) > maxMapPlanners {
			t.Fatal("unbounded cache")
		}
	}
	if len(currentMapPlanners.items) != 2 || currentMapPlanners.order[0].window.mapID != 530 || currentMapPlanners.order[1].window.mapID != 571 {
		t.Fatal(currentMapPlanners.order)
	}
	if _, err := PlanMapContext(context.Background(), dir, 36, a, b); err == nil {
		t.Fatal("unknown installed map invented")
	}
	// A legitimate map header cannot supply missing client terrain/collision.
	mapFixture(t, dir, 36, a)
	x, y := mapGrid(a)
	os.Remove(filepath.Join(dir, fmt.Sprintf("vmaps/036_%02d_%02d.vmtile", y, x)))
	if _, err := PlanMapContext(context.Background(), dir, 36, a, b); err == nil {
		t.Fatal("missing collision accepted")
	}
}

func TestCurrentMapPhysicsAndQueryWindowCannotBeBypassed(t *testing.T) {
	a, b := Point{-10630, 1100, 30}, Point{-10625, 1100, 30}
	w, _ := newMapWindow(0, a, b)
	for _, mode := range []string{"wall", "deep_water", "floor_layer", "raw_escape", "normalized_start"} {
		f := fakeRoute(a, b)
		switch mode {
		case "wall":
			f.walkErr = errors.New("wall")
		case "deep_water":
			f.waterFn = func(Point) (bool, error) { return true, nil }
			f.floorFn = func(_ Point, h, _ float32) (float32, error) { return h - 1.9, nil }
		case "floor_layer":
			f.floorFn = func(_ Point, h, _ float32) (float32, error) { return h + 4, nil }
		case "raw_escape":
			far := a
			far.Y += 1800
			f = fakeRoute(a, far, b)
		case "normalized_start":
			f.floorFn = func(p Point, h, _ float32) (float32, error) {
				if p == a {
					return h - 2.01, nil
				}
				return h, nil
			}
		}
		if _, err := planWithin(context.Background(), f, w.contains, "map window", a, b); err == nil {
			t.Fatal(mode, "loosened physical checks")
		}
	}
}

func TestCurrentMapGateCancellationAndConcurrentCopies(t *testing.T) {
	isolateMapCache(t)
	dir := regionAssetDirectories(t)
	a, b := Point{1500, -4200, 50}, Point{1505, -4200, 50}
	mapFixture(t, dir, 1, a)
	<-currentMapPlanners.gate
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	_, err := PlanMapContext(ctx, dir, 1, a, b)
	cancel()
	currentMapPlanners.gate <- struct{}{}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, e := PlanMapContext(context.Background(), dir, 1, a, b)
			if e != nil || len(p) < 2 {
				t.Error(e)
				return
			}
			p[0].Z = 900
		}()
	}
	wg.Wait()
	p, e := PlanMapContext(context.Background(), dir, 1, a, b)
	if e != nil || p[0].Z != 50 {
		t.Fatal("caller mutated cached route", p, e)
	}
}
