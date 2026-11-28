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

type terrainTile struct {
	height bool
	flat   bool
	holes  *[16 * 16]uint16
	err    error
}

// GridTerrainData::isHole indexes two-by-two cells in each eight-by-eight
// block. Use the same float32 grid coordinates as the pinned height reader.
func (tile terrainTile) holeAt(q Point) bool {
	// GridTerrainData returns the encoded base for flat height tiles. Only
	// its interpolated height functions omit the flagged terrain quads.
	if tile.flat || tile.holes == nil {
		return false
	}
	row := int(128*(32-q.X/float32(533.3333))) & 127
	col := int(128*(32-q.Y/float32(533.3333))) & 127
	mask := tile.holes[(row/8)*16+col/8]
	return mask&(1<<uint((row%8/2)*4+col%8/2)) != 0
}

// The pinned interpolator ignores MAP holes and treats a missing height
// section as flat zero. Bind its heights to actual encoded ground.
func (p *planner) terrainTile(q Point) terrainTile {
	x, y := int(32-q.X/float32(533.3333)), int(32-q.Y/float32(533.3333))
	key := [2]int{x, y}
	if tile, ok := p.terrainHeaders[key]; ok {
		return tile
	}
	if p.floors.window == nil && !p.region.allowsFloorTile(fmt.Sprintf("%03d_%02d_%02d.vmtile", p.mapID, y, x)) {
		return terrainTile{err: errors.New("terrain query outside navigation tiles")}
	}
	if p.terrainHeaders == nil {
		p.terrainHeaders = make(map[[2]int]terrainTile)
	}
	name := fmt.Sprintf("%03d%02d%02d.map", p.mapID, x, y)
	tile := readTerrainTile(filepath.Join(filepath.Dir(p.floors.dir), "maps", name))
	p.terrainHeaders[key] = tile
	return tile
}

func readTerrainTile(path string) terrainTile {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return terrainTile{}
	}
	if err != nil {
		return terrainTile{err: err}
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() < 44 || stat.Size() > 4<<20 {
		return terrainTile{err: errors.New("invalid MAP terrain file")}
	}
	var hdr [44]byte
	if _, err = io.ReadFull(f, hdr[:]); err != nil || binary.LittleEndian.Uint32(hdr[:]) != 0x5350414d || binary.LittleEndian.Uint32(hdr[4:]) != 9 {
		return terrainTile{err: errors.New("invalid MAP terrain header")}
	}
	u32 := func(at int) int64 { return int64(binary.LittleEndian.Uint32(hdr[at:])) }
	var tile terrainTile
	if offset, size := u32(36), u32(40); offset != 0 || size != 0 {
		malformed := terrainTile{err: errors.New("invalid MAP terrain hole section")}
		if offset < int64(len(hdr)) || size != 512 || offset+size > stat.Size() {
			return malformed
		}
		for _, at := range []int{12, 20, 28} {
			start, length := u32(at), u32(at+4)
			if length != 0 && offset < start+length && start < offset+size {
				return malformed
			}
		}
		var b [512]byte
		if _, err := f.ReadAt(b[:], offset); err != nil {
			return malformed
		}
		tile.holes = new([256]uint16)
		for i := range tile.holes {
			tile.holes[i] = binary.LittleEndian.Uint16(b[i*2:])
		}
	}
	offset, size := u32(20), u32(24)
	var h [16]byte
	if offset == 0 && size == 0 {
		return tile
	}
	if offset < int64(len(hdr)) || size < int64(len(h)) || offset+size > stat.Size() {
		return terrainTile{err: errors.New("invalid MAP terrain height section")}
	}
	_, err = f.ReadAt(h[:], offset)
	base := math.Float32frombits(binary.LittleEndian.Uint32(h[8:]))
	if err != nil || binary.LittleEndian.Uint32(h[:]) != 0x5447484d || !finite(base) {
		return terrainTile{err: errors.New("invalid MAP terrain height header")}
	}
	flags := binary.LittleEndian.Uint32(h[4:])
	tile.height = base > -10000
	tile.flat = flags&1 != 0
	if !tile.flat {
		bytesPerHeight := int64(4)
		switch {
		case flags&4 != 0:
			bytesPerHeight = 1
		case flags&2 != 0:
			bytesPerHeight = 2
		}
		if size < 16+(129*129+128*128)*bytesPerHeight {
			return terrainTile{err: errors.New("truncated MAP terrain height grid")}
		}
	}
	return tile
}

const worldVerticalLimit = 100000

func worldPoint(p Point) bool {
	return finite(p.X) && finite(p.Y) && finite(p.Z) && math.Abs(float64(p.X)) < 32*mapGridSize && math.Abs(float64(p.Y)) < 32*mapGridSize && math.Abs(float64(p.Z)) <= worldVerticalLimit
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
	bodyOnly  bool
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
	ctx, cancel := context.WithTimeout(ctx, time.Second)
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
	return p.groundRoute(ctx, w.contains, fmt.Sprintf("map %d tile window", mapID), start, dest)
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
	return cachedMapPlannerMode(ctx, dir, w, false)
}

// A physical body input needs terrain, water and collision. It does not need
// a ground polygon under a moving deck or a swimmer in open water.
func cachedMapPlannerMode(ctx context.Context, dir string, w mapWindow, bodyOnly bool) (*planner, error) {
	key := mapPlannerKey{directory: dir, window: w, bodyOnly: bodyOnly}
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
	var mesh *detour.DtNavMesh
	var query *detour.DtNavMeshQuery
	if !bodyOnly {
		var err error
		mesh, err = loadMeshTiles(ctx, filepath.Join(dir, "mmaps"), w.mapID, w.meshTiles(), maxMapMeshBytes)
		if err != nil {
			return nil, fmt.Errorf("map %d installed navigation metadata/tiles unavailable: %w", w.mapID, err)
		}
		query = detour.DtAllocNavMeshQuery()
		if st := query.Init(mesh, 4096); detour.DtStatusFailed(st) {
			return nil, errors.New("initialize navigation query failed")
		}
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
