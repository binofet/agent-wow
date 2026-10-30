package buddynav

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/azerothcore/AzerothGhost/pathfinding"
	"github.com/o0olele/detour-go/detour"
)

const (
	mapGridSize     = 533.33333333
	maxMapPlanners  = 2
	maxMapMeshBytes = 24 << 20
)

// mapWindow names at most nine neighboring client tiles. It is derived only
// from finite current-map endpoints, never from caller-supplied filenames.
type mapWindow struct {
	mapID uint32
	x, y  int
}

// The pinned upstream terrain reader treats an absent height section as known
// flat zero. Require an actual bounded height header before considering terrain
// a physical floor; VMAP-only interiors remain independently verifiable.
func (p *planner) mapTerrainPresent(q Point) bool {
	if !p.floors.window.contains(q) {
		return false
	}
	x, y := mapGrid(q)
	name := fmt.Sprintf("%03d%02d%02d.map", p.mapID, x, y)
	if known, ok := p.terrainHeaders[name]; ok {
		return known
	}
	if p.terrainHeaders == nil {
		p.terrainHeaders = make(map[string]bool)
	}
	valid := func() bool {
		f, err := os.Open(filepath.Join(filepath.Dir(p.floors.dir), "maps", name))
		if err != nil {
			return false
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() < 56 || info.Size() > 4<<20 {
			return false
		}
		var b [40]byte
		if _, err = io.ReadFull(f, b[:]); err != nil {
			return false
		}
		if binary.LittleEndian.Uint32(b[:]) != 0x5350414d || binary.LittleEndian.Uint32(b[4:]) != 9 {
			return false
		}
		offset, size := binary.LittleEndian.Uint32(b[20:]), binary.LittleEndian.Uint32(b[24:])
		if offset < 40 || size < 16 || uint64(offset)+uint64(size) > uint64(info.Size()) {
			return false
		}
		if _, err = f.Seek(int64(offset), io.SeekStart); err != nil {
			return false
		}
		var h [16]byte
		if _, err = io.ReadFull(f, h[:]); err != nil {
			return false
		}
		if binary.LittleEndian.Uint32(h[:]) != 0x5447484d {
			return false
		}
		base := math.Float32frombits(binary.LittleEndian.Uint32(h[8:]))
		return finite(base) && base > -10000
	}()
	p.terrainHeaders[name] = valid
	return valid
}

func worldPoint(p Point) bool {
	return finite(p.X) && finite(p.Y) && finite(p.Z) && math.Abs(float64(p.X)) < 32*mapGridSize && math.Abs(float64(p.Y)) < 32*mapGridSize && math.Abs(float64(p.Z)) <= 100000
}

func mapGrid(p Point) (int, int) {
	return int(math.Floor(32 - float64(p.X)/mapGridSize)), int(math.Floor(32 - float64(p.Y)/mapGridSize))
}

func newMapWindow(mapID uint32, a, b Point) (mapWindow, error) {
	if !clientMap(mapID) || !worldPoint(a) || !worldPoint(b) {
		return mapWindow{}, errors.New("navigation requires a valid client map ID and finite world coordinates")
	}
	if distance(a, b) > MaxRouteLength {
		return mapWindow{}, fmt.Errorf("navigation endpoints exceed %.0f yard route budget", MaxRouteLength)
	}
	x, y := mapGrid(Point{(a.X + b.X) / 2, (a.Y + b.Y) / 2, (a.Z + b.Z) / 2})
	w := mapWindow{mapID: mapID, x: x, y: y}
	if !w.contains(a) || !w.contains(b) {
		return mapWindow{}, errors.New("navigation endpoints exceed bounded tile window")
	}
	return w, nil
}

func (w mapWindow) contains(p Point) bool {
	if !worldPoint(p) {
		return false
	}
	x, y := mapGrid(p)
	return x >= 0 && x < 64 && y >= 0 && y < 64 && x >= w.x-1 && x <= w.x+1 && y >= w.y-1 && y <= w.y+1
}

func (w mapWindow) meshTiles() []string {
	tiles := make([]string, 0, 9)
	for x := w.x - 1; x <= w.x+1; x++ {
		for y := w.y - 1; y <= w.y+1; y++ {
			if x >= 0 && x < 64 && y >= 0 && y < 64 {
				tiles = append(tiles, fmt.Sprintf("%03d%02d%02d.mmtile", w.mapID, x, y))
			}
		}
	}
	return tiles
}

type mapPlannerKey struct {
	directory string
	window    mapWindow
}

var currentMapPlanners = struct {
	gate  chan struct{}
	items map[mapPlannerKey]*planner
	order []mapPlannerKey
}{gate: make(chan struct{}, 1), items: make(map[mapPlannerKey]*planner)}

func init() { currentMapPlanners.gate <- struct{}{} }

// PlanMapContext plans on the character's actual current client map. Unlike
// PlanRegionContext it has no starter-area fence. Every query is limited to
// nine local tiles, 240 yards, 256 samples and the same physical-floor, wall,
// layer, shallow-water and cancellation checks. Missing data fails closed.
// At most two map/window planners are cached, each with a 24MiB encoded-mesh
// budget, nine terrain/spawn grids and a 32MiB decoded collision-model LRU.
func PlanMapContext(ctx context.Context, dataDir string, mapID uint32, start, dest Point) (points []Point, err error) {
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	defer func() {
		if recover() != nil {
			points = nil
			err = errors.New("invalid navigation data or query")
		}
	}()
	w, err := newMapWindow(mapID, start, dest)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := mapDataDirectory(dataDir)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-currentMapPlanners.gate:
	}
	defer func() { currentMapPlanners.gate <- struct{}{} }()
	p, err := cachedMapPlanner(ctx, dir, w)
	if err != nil {
		return nil, err
	}
	return rerouteBounded(ctx, p, func() ([]Point, error) {
		return planWithin(ctx, p, w.contains, fmt.Sprintf("map %d tile window", mapID), start, dest)
	})
}

func mapDataDirectory(root string) (string, error) {
	if root == "" {
		return "", errors.New("navigation data directory is required")
	}
	dir, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("navigation data directory: %w", err)
	}
	for _, name := range []string{"mmaps", "maps", "vmaps"} {
		info, e := os.Stat(filepath.Join(dir, name))
		if e != nil || !info.IsDir() {
			return "", fmt.Errorf("navigation data requires %s directory", name)
		}
	}
	return dir, nil
}

// Called only while holding the cancellable current-map planner gate. The
// verified .mmap header is installed static client map metadata; a caller's
// numeric map ID alone cannot create a map or choose an arbitrary asset path.
func cachedMapPlanner(ctx context.Context, dir string, w mapWindow) (*planner, error) {
	key := mapPlannerKey{directory: dir, window: w}
	if p := currentMapPlanners.items[key]; p != nil {
		for i, k := range currentMapPlanners.order {
			if k == key {
				currentMapPlanners.order = append(currentMapPlanners.order[:i], currentMapPlanners.order[i+1:]...)
				break
			}
		}
		currentMapPlanners.order = append(currentMapPlanners.order, key)
		return p, nil
	}
	mesh, err := loadMeshTiles(ctx, filepath.Join(dir, "mmaps"), w.mapID, w.meshTiles(), maxMapMeshBytes)
	if err != nil {
		return nil, fmt.Errorf("map %d installed navigation metadata/tiles unavailable: %w", w.mapID, err)
	}
	query := detour.DtAllocNavMeshQuery()
	if st := query.Init(mesh, 4096); detour.DtStatusFailed(st) {
		return nil, errors.New("initialize navigation query failed")
	}
	filter := detour.DtAllocDtQueryFilter()
	filter.SetIncludeFlags(1 | 8)
	filter.SetExcludeFlags(^uint16(1 | 8))
	floors := newFloorReader(filepath.Join(dir, "vmaps"))
	floors.mapID = w.mapID
	floors.window = &w
	floors.modelLimit = 16 << 20 // Measured Stormwind WMO is14.0MiB; decoded LRU stays32MiB.
	p := &planner{mapID: w.mapID, mesh: mesh, query: query, filter: filter, terrainManager: pathfinding.NewTerrainManager(filepath.Join(dir, "maps")), floors: floors}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(currentMapPlanners.order) >= maxMapPlanners {
		old := currentMapPlanners.order[0]
		delete(currentMapPlanners.items, old)
		currentMapPlanners.order = currentMapPlanners.order[1:]
	}
	currentMapPlanners.items[key] = p
	currentMapPlanners.order = append(currentMapPlanners.order, key)
	return p, nil
}
