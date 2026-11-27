package buddynav

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/o0olele/detour-go/detour"
)

// GroundCorridor is static same-map guidance on the installed dry ground mesh.
// Corners are funnel turns, not movement reports or a sampled elevation profile.
type GroundCell struct {
	TileX   int    `json:"tileX"`
	TileY   int    `json:"tileY"`
	Layer   int32  `json:"layer"`
	Polygon uint32 `json:"polygon"`
}

// Cells is the ordered result of the search. Consecutive entries share a
// traversable portal. Identities refer to the same installed bake and map.
type GroundCorridor struct {
	MapID        uint32       `json:"mapId"`
	Cells        []GroundCell `json:"cells"`
	search       *groundSearch
	start, goal  Point
	surface      *planner
	strip        []detour.DtPolyRef
	leg          []Point
	Corners      []Point `json:"corners"`
	PolygonCount int     `json:"polygonCount"`
}

type groundTile struct{ x, y int }

// A search holds stable tile references until funnel extraction has finished.
// Only endpoint windows and tiles bordering discovered polygons are read.
type groundSearch struct {
	*planner
	meshDir string
	checked map[groundTile]bool
	grid    map[*detour.DtMeshTile]groundTile
	fringe  map[groundTile]bool
}

func (s *groundSearch) load(ctx context.Context, key groundTile) error {
	if key.x < 0 || key.x >= 64 || key.y < 0 || key.y >= 64 || s.checked[key] {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	name := fmt.Sprintf("%03d%02d%02d.mmtile", s.mapID, key.x, key.y)
	data, err := readBounded(filepath.Join(s.meshDir, name), 32<<20)
	if errors.Is(err, os.ErrNotExist) {
		s.checked[key] = true
		return nil
	}
	if err != nil {
		return err
	}
	body, err := tileBody(data)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	var ref detour.DtTileRef
	if st := s.mesh.AddTile(body, len(body), detour.DT_TILE_FREE_DATA, 0, &ref); detour.DtStatusFailed(st) {
		return fmt.Errorf("load %s: Detour status %x", name, st)
	}
	s.checked[key] = true
	s.grid[s.mesh.GetTileByRef(ref)] = key
	return nil
}

func (s *groundSearch) loadNeighbors(ctx context.Context, k groundTile) error {
	if s.fringe[k] {
		return nil
	}
	for x := k.x - 1; x <= k.x+1; x++ {
		for y := k.y - 1; y <= k.y+1; y++ {
			if err := s.load(ctx, groundTile{x, y}); err != nil {
				return err
			}
		}
	}
	s.fringe[k] = true
	return nil
}

// Connect any external edges before Detour expands this node. A missing tile
// supplies no link. Reaching a neighboring tile still requires a real shared
// portal with compatible floor height and the active traversal flags.
func (s *groundSearch) connectFrontier(ctx context.Context, ref detour.DtPolyRef) error {
	var tile *detour.DtMeshTile
	var poly *detour.DtPoly
	if st := s.mesh.GetTileAndPolyByRef(ref, &tile, &poly); detour.DtStatusFailed(st) {
		return errors.New("invalid polygon in ground search frontier")
	}
	grid, ok := s.grid[tile]
	if !ok {
		return errors.New("polygon belongs to an unknown tile")
	}
	if s.fringe[grid] {
		return nil
	}
	for _, neighbor := range poly.Neis[:poly.VertCount] {
		if neighbor&detour.DT_EXT_LINK != 0 {
			return s.loadNeighbors(ctx, grid)
		}
	}
	return nil
}

func (s *groundSearch) corridor(ctx context.Context, start, goal Point, nextLeg bool) (GroundCorridor, error) {
	for _, p := range []Point{start, goal} {
		x, y := mapGrid(p)
		if err := s.loadNeighbors(ctx, groundTile{x, y}); err != nil {
			return GroundCorridor{}, err
		}
	}
	start, startRef, err := s.bind(start)
	if err != nil {
		return GroundCorridor{}, fmt.Errorf("rider ground polygon: %w", err)
	}
	goal, endRef, err := s.bind(goal)
	if err != nil {
		return GroundCorridor{}, fmt.Errorf("destination ground polygon: %w", err)
	}
	path, err := s.searchStrip(ctx, start, goal, startRef, endRef)
	if err != nil {
		return GroundCorridor{}, err
	}
	return s.funnel(ctx, recast(start), recast(goal), path, nextLeg)
}

func (s *groundSearch) bind(p Point) (Point, detour.DtPolyRef, error) {
	ref, h, err := s.groundRef(p)
	if err != nil {
		return Point{}, 0, err
	}
	if math.Abs(float64(h-p.Z)) > EndpointTolerance {
		return Point{}, 0, errors.New("observed point is outside the ground layer")
	}
	p.Z = h
	return p, ref, nil
}

func (s *groundSearch) searchStrip(ctx context.Context, start, goal Point, startRef, endRef detour.DtPolyRef) ([]detour.DtPolyRef, error) {
	if startRef == endRef {
		return []detour.DtPolyRef{startRef}, nil
	}
	a, b := recast(start), recast(goal)
	st := s.query.InitSlicedFindPath(startRef, endRef, a[:], b[:], s.filter, 0)
	pool := s.query.GetNodePool()
	var connected uint32
	for detour.DtStatusInProgress(st) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for connected < pool.GetNodeCount() {
			connected++
			if err := s.connectFrontier(ctx, pool.GetNodeAtIdx(connected).Id); err != nil {
				return nil, err
			}
		}
		st = s.query.UpdateSlicedFindPath(1, nil)
	}
	// Detour may exhaust its open-node pool. Never return its partial path as
	// an arrival. A complete endpoint is checked independently below.
	if !detour.DtStatusFailed(st) && st&detour.DT_OUT_OF_NODES != 0 {
		return s.largeCorridor(ctx, startRef, endRef)
	}
	if detour.DtStatusFailed(st) || st&detour.DT_PARTIAL_RESULT != 0 {
		return nil, fmt.Errorf("no complete ground corridor (Detour %x)", st)
	}
	// This Detour port sets BUFFER_TOO_SMALL when the last ref exactly fills
	// the output. An unbranched route can include every allocated node.
	polys := make([]detour.DtPolyRef, int(pool.GetNodeCount())+1)
	var count int
	st = s.query.FinalizeSlicedFindPath(polys, &count, len(polys))
	if st != detour.DT_SUCCESS || count == 0 || polys[count-1] != endRef {
		return nil, errors.New("ground search did not reach its destination polygon")
	}
	return polys[:count], nil
}

func (s *groundSearch) funnel(ctx context.Context, a, b [3]float32, polys []detour.DtPolyRef, nextLeg bool) (GroundCorridor, error) {
	// The default funnel emits steering corners only. It does not introduce a
	// control point each time the straight line crosses another walkable poly.
	n := len(polys) + 2
	xyz := make([]float32, 3*n)
	flags := make([]detour.DtStraightPathFlags, n)
	var count int
	var mode detour.DtStraightPathOptions
	var refs []detour.DtPolyRef
	if nextLeg {
		mode = detour.DT_STRAIGHTPATH_ALL_CROSSINGS
		refs = make([]detour.DtPolyRef, n)
	}
	st := s.query.FindStraightPath(a[:], b[:], polys, len(polys), xyz, flags, refs, &count, n, mode)
	if st != detour.DT_SUCCESS || count == 0 || flags[count-1]&detour.DT_STRAIGHTPATH_END == 0 {
		return GroundCorridor{}, errors.New("ground funnel did not reach the destination")
	}
	path := make([]Point, count)
	for i := range path {
		if flags[i]&detour.DT_STRAIGHTPATH_OFFMESH_CONNECTION != 0 {
			return GroundCorridor{}, errors.New("route requires an off-mesh traversal")
		}
		path[i] = Point{xyz[3*i+2], xyz[3*i], xyz[3*i+1]}
	}
	if nextLeg {
		leg, err := s.groundLeg(ctx, path, refs)
		return GroundCorridor{leg: leg, PolygonCount: len(polys)}, err
	}
	cells := make([]GroundCell, len(polys))
	for i, ref := range polys {
		var tile *detour.DtMeshTile
		var poly *detour.DtPoly
		if st := s.mesh.GetTileAndPolyByRef(ref, &tile, &poly); detour.DtStatusFailed(st) {
			return GroundCorridor{}, errors.New("invalid corridor cell")
		}
		grid := s.grid[tile]
		cells[i] = GroundCell{TileX: grid.x, TileY: grid.y, Layer: tile.Header.Layer, Polygon: s.mesh.DecodePolyIdPoly(ref)}
	}
	return GroundCorridor{MapID: s.mapID, Cells: cells, Corners: path, PolygonCount: len(polys), search: s, start: Point{a[2], a[0], a[1]}, goal: Point{b[2], b[0], b[1]}, surface: s.planner, strip: polys}, nil
}

// CellAt looks up the observed 3-D polygon, then checks its identity in the
// requested slice. Similar XY and elevation cannot substitute a disconnected
// deck. This is a local mesh lookup and performs no graph search.
func (c *GroundCorridor) CellAt(point Point, first, last int) (int, bool) {
	if c.surface == nil || !worldPoint(point) || first < 0 || last < first || last >= len(c.strip) {
		return 0, false
	}
	ref, h, err := c.surface.groundRef(point)
	if err != nil || math.Abs(float64(h-point.Z)) > EndpointTolerance {
		return 0, false
	}
	for i := first; i <= last; i++ {
		if c.strip[i] == ref {
			return i, true
		}
	}
	return 0, false
}

// Return one controller-sized section of a complete corridor. The crossed
// polygon owns the height at every point, including overpasses and slopes.
func (s *groundSearch) groundLeg(ctx context.Context, path []Point, refs []detour.DtPolyRef) ([]Point, error) {
	if len(path) < 2 {
		return nil, errors.New("ground corridor has no progressing leg")
	}
	var profile []Point
	var length float64
	var previousClimb float32
	for i := 1; i < len(path); i++ {
		part, err := s.groundSegmentProfile(ctx, path[i-1], path[i], refs[i-1])
		if err != nil {
			return nil, err
		}
		var tile *detour.DtMeshTile
		var poly *detour.DtPoly
		if st := s.mesh.GetTileAndPolyByRef(refs[i-1], &tile, &poly); detour.DtStatusFailed(st) {
			return nil, errors.New("invalid funnel crossing")
		}
		climb := tile.Header.WalkableClimb
		if !finite(climb) || climb < 0 || len(profile) > 0 && math.Abs(float64(profile[len(profile)-1].Z-part[0].Z)) > float64(min(previousClimb, climb))+.001 {
			return nil, errors.New("ground corridor crosses an unsupported step")
		}
		previousClimb = climb
		for _, q := range part {
			if len(profile) > 0 {
				length += distance(profile[len(profile)-1], q)
			}
			profile = appendGroundPoint(profile, q)
			if length >= LegMaxLength {
				return denseGroundPrefix(profile), nil
			}
		}
	}
	return denseGroundPrefix(profile), nil
}

// PlanGroundCorridorContext uses polygon A* and string-pulling across tiled
// ground. Both short and long queries use the same algorithm. Cancellation
// applies during tile streaming and search; there is no fixed yardage limit.
func PlanGroundCorridorContext(ctx context.Context, root string, mapID uint32, start, goal Point) (GroundCorridor, error) {
	return planGroundCorridor(ctx, root, mapID, start, goal, false)
}

func planGroundCorridor(ctx context.Context, root string, mapID uint32, start, goal Point, nextLeg bool) (result GroundCorridor, err error) {
	if !clientMap(mapID) || !worldPoint(start) || !worldPoint(goal) {
		return result, errors.New("ground route requires an installed client map and finite endpoints")
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	dir, err := mapDataDirectory(root)
	if err != nil {
		return result, err
	}
	defer func() {
		if recover() != nil {
			result, err = GroundCorridor{}, errors.New("invalid tiled ground mesh or query")
		}
	}()
	meshDir := filepath.Join(dir, "mmaps")
	mesh, err := openMeshHeader(meshDir, mapID)
	if err != nil {
		return result, err
	}
	query := detour.DtAllocNavMeshQuery()
	if st := query.Init(mesh, int(detour.DT_NULL_IDX)); detour.DtStatusFailed(st) {
		return result, errors.New("initialize ground corridor query")
	}
	f := detour.DtAllocDtQueryFilter()
	f.SetIncludeFlags(1)
	f.SetExcludeFlags(^uint16(1))
	s := groundSearch{planner: &planner{mapID: mapID, mesh: mesh, query: query, filter: f}, meshDir: meshDir, checked: make(map[groundTile]bool), grid: make(map[*detour.DtMeshTile]groundTile), fringe: make(map[groundTile]bool)}
	return s.corridor(ctx, start, goal, nextLeg)
}
