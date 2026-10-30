// Package buddynav plans bounded Northshire ground and shallow-wading routes from read-only
// client map assets. It does not connect to a game server or move a character.
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
	"sync"
	"time"

	"github.com/azerothcore/AzerothGhost/pathfinding"
	"github.com/o0olele/detour-go/detour"
)

const (
	MaxPoints         = 256
	MaxRouteLength    = 180.0
	EndpointTolerance = 2.0
	MaxWadingDepth    = 1.8
	// This exact terrain-loader dependency is tagged v1.0.10. Path queries
	// use its pinned Detour fork directly, retaining complete-result checks.
	UpstreamRevision = "be53c70850e6c3bdfa776cb22d3a5734ebe0a440"
)

// Point uses WoW world axes and yards, without a Recast axis conversion.
type Point struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

// Plan requires dataDir/{mmaps,maps,vmaps} from independently extracted
// static client assets. Missing/partial meshes never produce a straight fallback.
func Plan(dataDir string, mapID uint32, start, dest Point) ([]Point, error) {
	return PlanContext(context.Background(), dataDir, mapID, start, dest)
}

// PlanContext checks cancellation while waiting for the planner and between
// bounded upstream queries/samples. The upstream file/query call is synchronous.
func PlanContext(ctx context.Context, dataDir string, mapID uint32, start, dest Point) (points []Point, err error) {
	return PlanRegionContext(ctx, dataDir, mapID, RegionNorthshire, start, dest)
}

// PlanRegionContext plans inside one sealed region, with the same bounded
// physical-floor, water, complete-path and cancellation checks as PlanContext.
// It only reads static client assets; it cannot move a game character.
func PlanRegionContext(ctx context.Context, dataDir string, mapID uint32, region Region, start, dest Point) (points []Point, err error) {
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			points = nil
			err = errors.New("invalid navigation data or query")
		}
	}()
	if err := validateRegionRequest(mapID, region, start, dest); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := cachedPlannerRegion(dataDir, region)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.gate:
	}
	defer func() { p.gate <- struct{}{} }()
	return planWithReroutes(ctx, p, start, dest)
}

type physicalDiscontinuity struct{ from, to Point }

func (e *physicalDiscontinuity) Error() string {
	return fmt.Sprintf("physical ground has an unsupported vertical ledge at %.4f,%.4f", e.from.X, e.from.Y)
}

// A smoothed polygon path can cross the edge of a collision prop. At most two
// complete replans may avoid the exact polygon that proved discontinuous. All
// temporary flags are restored under the same planner gate on every exit.
func planWithReroutes(ctx context.Context, p *planner, start, dest Point) ([]Point, error) {
	return rerouteBounded(ctx, p, func() ([]Point, error) { return planRegion(ctx, p, p.region, start, dest) })
}
func rerouteBounded(ctx context.Context, p *planner, attemptPath func() ([]Point, error)) ([]Point, error) {
	restore := make(map[detour.DtPolyRef]uint16, 2)
	defer func() {
		for ref, flags := range restore {
			p.mesh.SetPolyFlags(ref, flags)
		}
	}()
	for attempt := 0; attempt < 3; attempt++ {
		points, err := attemptPath()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil {
			return points, nil
		}
		var edge *physicalDiscontinuity
		if attempt == 2 || !errors.As(err, &edge) {
			return nil, err
		}
		ref, _, e := p.groundRef(edge.from)
		if e != nil {
			return nil, err
		}
		if _, already := restore[ref]; already {
			return nil, err
		}
		var flags uint16
		if st := p.mesh.GetPolyFlags(ref, &flags); detour.DtStatusFailed(st) || !walkSurfaceFlags(flags) {
			return nil, err
		}
		if st := p.mesh.SetPolyFlags(ref, 0); detour.DtStatusFailed(st) {
			return nil, err
		}
		restore[ref] = flags
	}
	return nil, errors.New("navigation reroute budget exhausted")
}

type backend interface {
	findPath(start, dest Point) (*pathfinding.PathResult, error)
	ground(Point) (float32, error)
	water(Point) (bool, error)
	terrain(Point) (float32, bool)
	floor(context.Context, Point, float32, float32) (float32, error)
	walkable(Point, Point) error
}

func validateRequest(mapID uint32, start, dest Point) error {
	return validateRegionRequest(mapID, RegionNorthshire, start, dest)
}

func validateRegionRequest(mapID uint32, region Region, start, dest Point) error {
	name, err := region.name()
	if err != nil {
		return err
	}
	if mapID != 0 {
		return errors.New("navigation supports map 0 only")
	}
	if !region.contains(start) || !region.contains(dest) {
		return fmt.Errorf("navigation endpoints must be finite and inside %s bounds", name)
	}
	if distance(start, dest) > MaxRouteLength {
		return errors.New("navigation endpoints exceed 180 yard route budget")
	}
	return nil
}

func plan(ctx context.Context, b backend, start, dest Point) ([]Point, error) {
	return planRegion(ctx, b, RegionNorthshire, start, dest)
}

func planRegion(ctx context.Context, b backend, region Region, start, dest Point) ([]Point, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateRegionRequest(0, region, start, dest); err != nil {
		return nil, err
	}
	name, _ := region.name()
	r, err := b.findPath(start, dest)
	if err != nil {
		return nil, fmt.Errorf("find navigation path: %w", err)
	}
	if r == nil || r.Type != pathfinding.PathfindNormal {
		return nil, errors.New("navigation did not return a complete normal mesh path")
	}
	if len(r.Points) < 2 || len(r.Points) > MaxPoints {
		return nil, errors.New("navigation path point count is outside 2..256")
	}
	points := make([]Point, 0, len(r.Points)+1)
	points = append(points, start)
	for _, p := range r.Points {
		q := Point{p.X, p.Y, p.Z}
		if !region.contains(q) {
			return nil, fmt.Errorf("navigation path leaves %s bounds or contains non-finite coordinates", name)
		}
		if distance(points[len(points)-1], q) > 0.001 {
			points = append(points, q)
		}
	}
	if distance(start, Point{r.Points[0].X, r.Points[0].Y, r.Points[0].Z}) > EndpointTolerance {
		return nil, errors.New("navigation start is more than 2 yards from requested start")
	}
	if distance(points[len(points)-1], dest) > EndpointTolerance {
		return nil, errors.New("navigation endpoint is more than 2 yards from requested destination")
	}
	var length float64
	for i := 1; i < len(points); i++ {
		length += distance(points[i-1], points[i])
	}
	if length > MaxRouteLength {
		return nil, errors.New("navigation route exceeds 180 yards")
	}
	// Densify to at most one yard before querying the ground surface. No result
	// can hide a water polygon, wall, or large vertical discontinuity between
	// the polygon-crossing waypoints.
	out := make([]Point, 0, MaxPoints)
	var previousMesh Point
	var add func(Point, int) error
	add = func(q Point, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := b.ground(q)
		if err != nil {
			return fmt.Errorf("navigation ground sample at %.4f,%.4f,%.4f: %w", q.X, q.Y, q.Z, err)
		}
		if !finite(h) || math.Abs(float64(h-q.Z)) > 2 {
			return errors.New("navigation sample differs from mesh height by more than 2 yards")
		}
		water, err := b.water(q)
		if err != nil {
			return err
		}
		terrain, ok := b.terrain(q)
		if !ok || !finite(terrain) || terrain <= -10000 {
			return errors.New("navigation terrain height is unavailable")
		}
		meshPoint := Point{q.X, q.Y, h}
		floor, err := b.floor(ctx, q, h, terrain)
		if err != nil {
			return fmt.Errorf("navigation physical floor at %+v mesh=%g terrain=%g: %w", q, h, terrain, err)
		}
		if !finite(floor) || floor < h-2 || floor > h+.5 {
			return errors.New("navigation physical floor differs from verified mesh layer")
		}
		if water && (h-floor < -.01 || float64(h-floor) > MaxWadingDepth+.0001) {
			return errors.New("navigation water depth exceeds shallow wading limit")
		}
		q.Z = floor
		if !region.contains(q) {
			return fmt.Errorf("navigation mesh height leaves %s bounds", name)
		}
		if len(out) > 0 {
			prev := out[len(out)-1]
			if err := b.walkable(previousMesh, meshPoint); err != nil {
				return fmt.Errorf("navigation segment %.4f,%.4f -> %.4f,%.4f is not ground-walkable: %w", prev.X, prev.Y, q.X, q.Y, err)
			}
			if distance(prev, q) < 0.001 {
				return nil
			}
			if math.Abs(float64(q.Z-prev.Z)) > 1.0 {
				if depth >= 4 {
					return &physicalDiscontinuity{from: previousMesh, to: meshPoint}
				}
				// Sample physical stair edges more finely without relaxing the cap.
				mid := Point{(previousMesh.X + meshPoint.X) / 2, (previousMesh.Y + meshPoint.Y) / 2, (previousMesh.Z + meshPoint.Z) / 2}
				if err := add(mid, depth+1); err != nil {
					return err
				}
				return add(meshPoint, depth+1)
			}
			if distance(prev, q) > 2 {
				return errors.New("navigation physical floor segment exceeds 2 yards")
			}
		}
		if len(out) >= MaxPoints {
			return errors.New("navigation sampled path exceeds 256 points")
		}
		out = append(out, q)
		previousMesh = meshPoint
		return nil
	}
	if err := add(points[0], 0); err != nil {
		return nil, err
	}
	for i := 1; i < len(points); i++ {
		a, z := points[i-1], points[i]
		n := int(math.Ceil(distance(a, z)))
		for j := 1; j <= n; j++ {
			t := float32(j) / float32(n)
			if err := add(Point{a.X + (z.X-a.X)*t, a.Y + (z.Y-a.Y)*t, a.Z + (z.Z-a.Z)*t}, 0); err != nil {
				return nil, err
			}
		}
	}
	if len(out) == 0 || distance(out[0], start) > EndpointTolerance {
		return nil, errors.New("navigation normalized start is outside start tolerance")
	}
	if distance(out[len(out)-1], dest) > EndpointTolerance {
		return nil, errors.New("navigation normalized endpoint is outside destination tolerance")
	}
	length = 0
	for i := 1; i < len(out); i++ {
		length += distance(out[i-1], out[i])
	}
	if length > MaxRouteLength {
		return nil, errors.New("navigation normalized route exceeds 180 yards")
	}
	return out, nil
}

func finite(v float32) bool { return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) }
func inBounds(p Point) bool {
	return RegionNorthshire.contains(p)
}
func distance(a, b Point) float64 {
	x, y, z := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(x*x + y*y + z*z)
}

type planner struct {
	region         Region
	gate           chan struct{}
	terrainManager *pathfinding.TerrainManager
	query          *detour.DtNavMeshQuery
	mesh           *detour.DtNavMesh
	filter         *detour.DtQueryFilter
	floors         *floorReader
}

type plannerCacheKey struct {
	directory string
	region    Region
}

var planners = struct {
	sync.Mutex
	items map[plannerCacheKey]*planner
}{items: make(map[plannerCacheKey]*planner)}

func cachedPlanner(dataDir string) (*planner, error) {
	return cachedPlannerRegion(dataDir, RegionNorthshire)
}

func cachedPlannerRegion(dataDir string, region Region) (*planner, error) {
	if _, err := region.name(); err != nil {
		return nil, err
	}
	if dataDir == "" {
		return nil, errors.New("navigation data directory is required")
	}
	dir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("navigation data directory: %w", err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("navigation data directory: %w", err)
	}
	for _, name := range []string{"mmaps", "maps", "vmaps"} {
		info, e := os.Stat(filepath.Join(dir, name))
		if e != nil || !info.IsDir() {
			return nil, fmt.Errorf("navigation data requires %s directory", name)
		}
	}
	planners.Lock()
	defer planners.Unlock()
	key := plannerCacheKey{directory: dir, region: region}
	if p := planners.items[key]; p != nil {
		return p, nil
	}
	if len(planners.items) >= 4 {
		return nil, errors.New("navigation data directory cache limit reached")
	}
	mesh, err := loadMeshRegion(filepath.Join(dir, "mmaps"), region)
	if err != nil {
		return nil, err
	}
	q := detour.DtAllocNavMeshQuery()
	if st := q.Init(mesh, 4096); detour.DtStatusFailed(st) {
		return nil, errors.New("initialize navigation query failed")
	}
	f := detour.DtAllocDtQueryFilter()
	f.SetIncludeFlags(0x01 | 0x08) // NAV_GROUND and only depth-verified NAV_WATER.
	f.SetExcludeFlags(^uint16(0x01 | 0x08))
	p := &planner{region: region, gate: make(chan struct{}, 1), mesh: mesh, query: q, filter: f,
		terrainManager: pathfinding.NewTerrainManager(filepath.Join(dir, "maps")), floors: newFloorReaderRegion(filepath.Join(dir, "vmaps"), region)}
	p.gate <- struct{}{}
	planners.items[key] = p
	return p, nil
}

func (p *planner) findPath(a, b Point) (*pathfinding.PathResult, error) {
	start, _, err := p.groundRef(a)
	if err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	end, _, err := p.groundRef(b)
	if err != nil {
		return nil, fmt.Errorf("destination: %w", err)
	}
	x, y := recast(a), recast(b)
	var polys [512]detour.DtPolyRef
	var count int
	st := p.query.FindPath(start, end, x[:], y[:], p.filter, polys[:], &count, len(polys))
	if st != detour.DT_SUCCESS || count == 0 || polys[count-1] != end {
		return nil, errors.New("navigation polygon path is partial, exhausted, or unavailable")
	}
	var positions [MaxPoints * 3]float32
	var flags [MaxPoints]detour.DtStraightPathFlags
	var refs [MaxPoints]detour.DtPolyRef
	var n int
	st = p.query.FindStraightPath(x[:], y[:], polys[:], count, positions[:], flags[:], refs[:], &n, MaxPoints, detour.DT_STRAIGHTPATH_ALL_CROSSINGS)
	if st != detour.DT_SUCCESS || n < 2 || flags[n-1]&detour.DT_STRAIGHTPATH_END == 0 {
		return nil, errors.New("navigation straight path is partial or unavailable")
	}
	r := &pathfinding.PathResult{Type: pathfinding.PathfindNormal, Points: make([]pathfinding.Point3D, n)}
	for i := range n {
		if flags[i]&detour.DT_STRAIGHTPATH_OFFMESH_CONNECTION != 0 {
			return nil, errors.New("navigation off-mesh connection is unsupported")
		}
		r.Points[i] = pathfinding.Point3D{X: positions[i*3+2], Y: positions[i*3], Z: positions[i*3+1]}
	}
	return r, nil
}
func (p *planner) terrain(q Point) (float32, bool) {
	return p.terrainManager.GetGridHeight(0, q.X, q.Y)
}
func recast(p Point) [3]float32 { return [3]float32{p.Y, p.Z, p.X} }
func (p *planner) groundRef(q Point) (detour.DtPolyRef, float32, error) {
	v := recast(q)
	ext := [3]float32{0.5, 2, 0.5}
	var ref detour.DtPolyRef
	var nearest [3]float32
	if st := p.query.FindNearestPoly(v[:], ext[:], p.filter, &ref, nearest[:]); detour.DtStatusFailed(st) || ref == 0 {
		return 0, 0, errors.New("no nearby ground polygon")
	}
	var flags uint16
	if st := p.mesh.GetPolyFlags(ref, &flags); detour.DtStatusFailed(st) || !walkSurfaceFlags(flags) {
		return 0, 0, errors.New("polygon is not ground or shallow-wading candidate")
	}
	var over bool
	if st := p.query.ClosestPointOnPoly(ref, v[:], nearest[:], &over); detour.DtStatusFailed(st) || (!over && math.Hypot(float64(nearest[0]-v[0]), float64(nearest[2]-v[2])) > 0.02) {
		return 0, 0, errors.New("sample is outside ground polygon")
	}
	var height float32
	if st := p.query.GetPolyHeight(ref, nearest[:], &height); detour.DtStatusFailed(st) {
		// Shared float32 portal vertices can pass the polygon containment
		// check but miss all detail triangles by rounding at their boundary.
		// Sample at most 1cm inside this same already ground-only polygon;
		// never substitute terrain height or a different navigation layer.
		if err := p.insetRayPoint(ref, &nearest); err != nil {
			return 0, 0, err
		}
		if st := p.query.GetPolyHeight(ref, nearest[:], &height); detour.DtStatusFailed(st) {
			return 0, 0, errors.New("ground polygon height is unavailable")
		}
	}
	return ref, height, nil
}
func (p *planner) ground(q Point) (float32, error) { _, h, err := p.groundRef(q); return h, err }
func walkSurfaceFlags(flags uint16) bool           { return flags != 0 && flags & ^uint16(1|8) == 0 }
func (p *planner) water(q Point) (bool, error) {
	ref, _, err := p.groundRef(q)
	if err != nil {
		return false, err
	}
	var flags uint16
	if st := p.mesh.GetPolyFlags(ref, &flags); detour.DtStatusFailed(st) {
		return false, errors.New("navigation surface flags unavailable")
	}
	return flags&8 != 0, nil
}
func (p *planner) walkable(a, b Point) error {
	// Prefer the polygon containing the segment interior when a portal vertex
	// belongs to several polygons. Nearest-poly tie order at the vertex alone
	// can otherwise select a ground polygon on the wrong side of a wall.
	mid := Point{(a.X + b.X) / 2, (a.Y + b.Y) / 2, (a.Z + b.Z) / 2}
	ref, _, err := p.groundRef(mid)
	endRef := ref
	if err != nil || !p.nearPoly(ref, a) || !p.nearPoly(ref, b) {
		startHint := Point{a.X + (b.X-a.X)*0.01, a.Y + (b.Y-a.Y)*0.01, a.Z + (b.Z-a.Z)*0.01}
		endHint := Point{b.X + (a.X-b.X)*0.01, b.Y + (a.Y-b.Y)*0.01, b.Z + (a.Z-b.Z)*0.01}
		ref, _, err = p.groundRef(startHint)
		if err != nil {
			return err
		}
		endRef, _, err = p.groundRef(endHint)
	}
	if err != nil {
		return err
	}
	x, y := recast(a), recast(b)
	// Float32 portal coordinates can fall a few millimetres outside one of
	// the two sharing polygons. groundRef limits this edge tolerance to 2cm.
	var snapped [3]float32
	if st := p.query.ClosestPointOnPoly(ref, x[:], snapped[:], nil); detour.DtStatusFailed(st) {
		return errors.New("cannot snap ground ray start")
	}
	x = snapped
	if st := p.query.ClosestPointOnPoly(endRef, y[:], snapped[:], nil); detour.DtStatusFailed(st) {
		return errors.New("cannot snap ground ray destination")
	}
	y = snapped
	// Portal vertices are shared exactly, but the float32 ray/edge test can
	// classify a boundary start as outside (T=0, no visited polygons). Move
	// each ray endpoint at most 1cm into its already verified ground polygon.
	// This is below the mesh cell resolution and never permits a liquid poly.
	if err := p.insetRayPoint(ref, &x); err != nil {
		return err
	}
	if err := p.insetRayPoint(endRef, &y); err != nil {
		return err
	}
	var t float32
	var visited [32]detour.DtPolyRef
	var n int
	st := p.query.Raycast(ref, x[:], y[:], p.filter, &t, nil, visited[:], &n, len(visited))
	if detour.DtStatusFailed(st) || st&detour.DT_BUFFER_TOO_SMALL != 0 || n == 0 || !finite(t) || t < 1-1e-5 {
		return fmt.Errorf("ground-only surface ray is blocked or incomplete (t=%g, polygons=%d, status=%x)", t, n, st)
	}
	return nil
}

func (p *planner) nearPoly(ref detour.DtPolyRef, q Point) bool {
	v := recast(q)
	var nearest [3]float32
	if st := p.query.ClosestPointOnPoly(ref, v[:], nearest[:], nil); detour.DtStatusFailed(st) {
		return false
	}
	return math.Hypot(float64(nearest[0]-v[0]), float64(nearest[2]-v[2])) <= 0.02
}

func (p *planner) insetRayPoint(ref detour.DtPolyRef, v *[3]float32) error {
	var tile *detour.DtMeshTile
	var poly *detour.DtPoly
	if st := p.mesh.GetTileAndPolyByRef(ref, &tile, &poly); detour.DtStatusFailed(st) || poly.VertCount < 3 {
		return errors.New("invalid ground polygon")
	}
	var cx, cy float32
	for i := 0; i < int(poly.VertCount); i++ {
		k := int(poly.Verts[i]) * 3
		cx += tile.Verts[k]
		cy += tile.Verts[k+2]
	}
	cx /= float32(poly.VertCount)
	cy /= float32(poly.VertCount)
	dx, dy := cx-v[0], cy-v[2]
	d := math.Hypot(float64(dx), float64(dy))
	if d > 0 {
		factor := float32(math.Min(0.01/d, 1))
		v[0] += dx * factor
		v[2] += dy * factor
	}
	return nil
}

// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/common/Collision/Maps/MapDefines.h
// specifies MMAP20, Detour7 and a 56-byte
// tile header (36-byte recast config). Read that format explicitly rather than
// rewriting public assets to fool upstream's older MMAP19 loader.
func loadMesh(dir string) (mesh *detour.DtNavMesh, err error) {
	return loadMeshRegion(dir, RegionNorthshire)
}

func loadMeshRegion(dir string, region Region) (mesh *detour.DtNavMesh, err error) {
	// The pure-Go Detour decoder uses slice-backed binary structures. Reject
	// a corrupt local asset as a planner error rather than crashing the body.
	defer func() {
		if r := recover(); r != nil {
			mesh = nil
			err = errors.New("invalid navigation mesh data")
		}
	}()
	tiles, err := region.meshTiles()
	if err != nil {
		return nil, err
	}
	data, err := readBounded(filepath.Join(dir, "000.mmap"), 28)
	if err != nil {
		return nil, err
	}
	if len(data) != 28 {
		return nil, errors.New("navigation mmap parameters must contain exactly 28 bytes")
	}
	f32 := func(i int) float32 { return math.Float32frombits(binary.LittleEndian.Uint32(data[i : i+4])) }
	params := detour.DtNavMeshParams{Orig: [3]float32{f32(0), f32(4), f32(8)}, TileWidth: f32(12), TileHeight: f32(16), MaxTiles: binary.LittleEndian.Uint32(data[20:24]), MaxPolys: binary.LittleEndian.Uint32(data[24:28])}
	for _, v := range params.Orig {
		if !finite(v) || math.Abs(float64(v)) > 100000 {
			return nil, errors.New("invalid mmap origin")
		}
	}
	// MaxPolys is a reference-bit capacity, not an allocation count; actual
	// AzerothCore map0 parameters use 2^31 with 64-bit polygon references.
	if !finite(params.TileWidth) || !finite(params.TileHeight) || params.TileWidth < 100 || params.TileWidth > 1000 || params.TileHeight < 100 || params.TileHeight > 1000 || params.MaxTiles == 0 || params.MaxTiles > 4096 || params.MaxPolys == 0 || params.MaxPolys > 1<<31 {
		return nil, errors.New("invalid or excessive mmap parameters")
	}
	mesh = detour.DtAllocNavMesh()
	if st := mesh.Init(&params); detour.DtStatusFailed(st) {
		return nil, errors.New("initialize navigation mesh failed")
	}
	loaded := 0
	// Only the named region's sealed tile list is considered. Missing portions
	// still fail closed rather than loading neighbors or returning a shortcut.
	for _, name := range tiles {
		data, err := readBounded(filepath.Join(dir, name), 32<<20)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		body, err := tileBody(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		var ref detour.DtTileRef
		if st := mesh.AddTile(body, len(body), detour.DT_TILE_FREE_DATA, 0, &ref); detour.DtStatusFailed(st) {
			return nil, fmt.Errorf("add navigation tile %s failed: %x", name, st)
		}
		loaded++
	}
	if loaded == 0 {
		name, _ := region.name()
		return nil, fmt.Errorf("%s navigation tiles are missing", name)
	}
	return mesh, nil
}
func readBounded(name string, max int64) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, errors.New("navigation asset exceeds size limit")
	}
	return data, nil
}
func tileBody(data []byte) ([]byte, error) {
	if len(data) < 56 {
		return nil, errors.New("navigation tile header is truncated")
	}
	u32 := func(i int) uint32 { return binary.LittleEndian.Uint32(data[i : i+4]) }
	if u32(0) != 0x4d4d4150 || u32(4) != 7 || u32(8) != 20 {
		return nil, errors.New("navigation tile requires MMAP20 and Detour7")
	}
	if u32(12) != uint32(len(data)-56) || u32(12) < 100 || data[16] > 1 {
		return nil, errors.New("navigation tile size or liquid indicator is invalid")
	}
	body := data[56:]
	if binary.LittleEndian.Uint32(body[:4]) != uint32(detour.DT_NAVMESH_MAGIC) || binary.LittleEndian.Uint32(body[4:8]) != 7 {
		return nil, errors.New("navigation Detour tile header is invalid")
	}
	return body, nil
}
