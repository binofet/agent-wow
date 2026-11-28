package buddynav

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"

	"github.com/o0olele/detour-go/detour"
)

type SurfaceMeshMetadata struct {
	Version     int     `json:"version"`
	DisplayID   uint32  `json:"displayId"`
	Model       string  `json:"model"`
	ModelSHA    string  `json:"modelSHA256"`
	MeshSHA     string  `json:"meshSHA256"`
	Generator   string  `json:"generator"`
	PolyRefBits int     `json:"polyRefBits"`
	BodyRadius  float32 `json:"bodyRadius"`
	BodyHeight  float32 `json:"bodyHeight"`
	BodyClimb   float32 `json:"bodyClimb"`
	CellSize    float32 `json:"cellSize"`
	CellHeight  float32 `json:"cellHeight"`
}

// SurfaceMesh is a Detour polygon corridor in a moving model's local frame.
// The caller serializes its query, as with the ordinary map planner.
type SurfaceMesh struct {
	Metadata SurfaceMeshMetadata
	surface  *TransportSurface
	mesh     *detour.DtNavMesh
	query    *detour.DtNavMeshQuery
	filter   *detour.DtQueryFilter
	polygons int
}

func LoadSurfaceMesh(ctx context.Context, dataDir string, displayID uint32) (*SurfaceMesh, error) {
	surface, err := LoadTransportSurface(ctx, dataDir, displayID)
	if err != nil {
		return nil, err
	}
	dir, err := mapDataDirectory(dataDir)
	if err != nil {
		return nil, err
	}
	data, err := readBounded(filepath.Join(dir, "surfaces", strconv.FormatUint(uint64(displayID), 10)+".nav"), floorCacheBytes)
	if err != nil {
		return nil, fmt.Errorf("moving surface navmesh: %w", err)
	}
	return readSurfaceMesh(data, surface)
}

func readSurfaceMesh(data []byte, surface *TransportSurface) (result *SurfaceMesh, err error) {
	// Detour's native-layout reader assumes a complete local generator output.
	// Malformed installed assets must fail without bringing down a game client.
	defer func() {
		if recover() != nil {
			result, err = nil, errors.New("malformed Detour surface tile")
		}
	}()
	if len(data) < 12 || string(data[:8]) != "AWSURF1\x00" {
		return nil, errors.New("invalid moving surface navmesh header")
	}
	n := int(binary.LittleEndian.Uint32(data[8:12]))
	if n < 2 || n > len(data)-12-100 {
		return nil, errors.New("invalid moving surface metadata length")
	}
	var meta SurfaceMeshMetadata
	decoder := json.NewDecoder(bytes.NewReader(data[12 : 12+n]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&meta); err != nil {
		return nil, err
	}
	tile := data[12+n:]
	sum := sha256.Sum256(tile)
	if meta.Version != 1 || meta.DisplayID != surface.DisplayID || meta.Model != surface.ModelName ||
		meta.ModelSHA != surface.ModelSHA || meta.MeshSHA != hex.EncodeToString(sum[:]) ||
		meta.Generator != "recastnavigation/b4554541b658630816dba41466eb1cefb624519e" || meta.PolyRefBits != 64 ||
		meta.BodyRadius < .25 || meta.BodyHeight < 1.8 || meta.BodyClimb > .5 ||
		!finite(meta.BodyRadius) || !finite(meta.BodyHeight) || !finite(meta.BodyClimb) || meta.BodyClimb <= 0 ||
		!finite(meta.CellSize) || !finite(meta.CellHeight) || meta.CellSize <= 0 || meta.CellHeight <= 0 {
		return nil, errors.New("moving surface does not match its collision model and body")
	}
	if binary.LittleEndian.Uint32(tile) != uint32(detour.DT_NAVMESH_MAGIC) || binary.LittleEndian.Uint32(tile[4:]) != 7 {
		return nil, errors.New("moving surface requires a Detour7 tile")
	}
	if err := ValidateSurfaceTile(tile, meta); err != nil {
		return nil, err
	}
	polygons := int(binary.LittleEndian.Uint32(tile[24:]))
	if polygons < 1 || polygons > 65535 {
		return nil, errors.New("moving surface polygon count is invalid")
	}
	mesh := detour.DtAllocNavMesh()
	if st := mesh.Init2(tile, len(tile), detour.DT_TILE_FREE_DATA); detour.DtStatusFailed(st) {
		return nil, fmt.Errorf("cannot load moving surface mesh: %x", st)
	}
	query := detour.DtAllocNavMeshQuery()
	if st := query.Init(mesh, max(32, polygons*2)); detour.DtStatusFailed(st) {
		return nil, fmt.Errorf("cannot initialize moving surface query: %x", st)
	}
	filter := detour.DtAllocDtQueryFilter()
	filter.SetIncludeFlags(1)
	filter.SetExcludeFlags(^uint16(1))
	return &SurfaceMesh{Metadata: meta, surface: surface, mesh: mesh, query: query, filter: filter, polygons: polygons}, nil
}

func surfaceNav(p Point) []float32   { return []float32{p.X, p.Z, -p.Y} }
func surfacePoint(p []float32) Point { return Point{p[0], -p[2], p[1]} }

func (s *SurfaceMesh) nearby(local Point) (detour.DtPolyRef, Point, error) {
	if !worldPoint(local) {
		return 0, Point{}, errors.New("finite local surface position required")
	}
	var ref detour.DtPolyRef
	var closest [3]float32
	extent := s.Metadata.BodyRadius + s.Metadata.CellSize
	vertical := s.Metadata.BodyClimb + s.Metadata.CellHeight
	status := s.query.FindNearestPoly(surfaceNav(local), []float32{extent, vertical, extent}, s.filter, &ref, closest[:])
	point := surfacePoint(closest[:])
	if detour.DtStatusFailed(status) || ref == 0 ||
		math.Hypot(float64(local.X-point.X), float64(local.Y-point.Y)) > float64(extent) ||
		math.Abs(float64(local.Z-point.Z)) > float64(vertical) {
		return 0, Point{}, errors.New("position has no nearby moving-surface polygon")
	}
	return ref, point, nil
}

// Route retains every crossed polygon. Recast supplies the eroded walkable
// topology; Detour performs A* and the funnel inside that same local mesh.
func (s *SurfaceMesh) Route(ctx context.Context, start, destination Point) ([]Point, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	actualStart, err := s.endpoint(ctx, start)
	if err != nil {
		return nil, err
	}
	actualEnd, err := s.endpoint(ctx, destination)
	if err != nil {
		return nil, err
	}
	first, from, err := s.nearby(start)
	if err != nil {
		return nil, err
	}
	last, to, err := s.nearby(destination)
	if err != nil {
		return nil, err
	}
	polygons := make([]detour.DtPolyRef, s.polygons)
	var count int
	status := s.query.FindPath(first, last, surfaceNav(from), surfaceNav(to), s.filter, polygons, &count, len(polygons))
	if status != detour.DT_SUCCESS || count == 0 || polygons[count-1] != last {
		return nil, errors.New("moving-surface polygon corridor is incomplete")
	}
	points := make([]float32, (count+2)*3)
	flags := make([]detour.DtStraightPathFlags, count+2)
	var written int
	refs := make([]detour.DtPolyRef, count+2)
	status = s.query.FindStraightPath(surfaceNav(from), surfaceNav(to), polygons, count, points, flags, refs, &written, len(flags), detour.DT_STRAIGHTPATH_ALL_CROSSINGS)
	if status != detour.DT_SUCCESS || written == 0 || flags[written-1]&detour.DT_STRAIGHTPATH_END == 0 {
		return nil, errors.New("moving-surface funnel is incomplete")
	}
	path := []Point{actualStart}
	if err := s.appendPhysicalSegment(ctx, &path, from, 0); err != nil {
		return nil, err
	}
	for i := 0; i < written-1; i++ {
		if err := s.appendPhysicalSegment(ctx, &path, surfacePoint(points[3*(i+1):]), refs[i]); err != nil {
			return nil, err
		}
	}
	if err := s.appendPhysicalSegment(ctx, &path, actualEnd, 0); err != nil {
		return nil, err
	}
	return path, ctx.Err()
}

func (s *SurfaceMesh) endpoint(ctx context.Context, point Point) (Point, error) {
	if !worldPoint(point) {
		return Point{}, errors.New("finite local surface endpoint required")
	}
	h, supported, err := s.surface.Height(ctx, point, .5, .5)
	if err != nil {
		return Point{}, err
	}
	if !supported {
		return Point{}, errors.New("requested local endpoint has no actual hull support")
	}
	point.Z = h
	return point, nil
}

func (s *SurfaceMesh) appendPhysicalSegment(ctx context.Context, path *[]Point, end Point, ref detour.DtPolyRef) error {
	start := (*path)[len(*path)-1]
	steps := max(1, int(math.Ceil(distance(start, end)/.25)))
	for j := 1; j <= steps; j++ {
		fraction := float32(j) / float32(steps)
		point := Point{start.X + fraction*(end.X-start.X), start.Y + fraction*(end.Y-start.Y), start.Z + fraction*(end.Z-start.Z)}
		if ref != 0 {
			var height float32
			if status := s.query.GetPolyHeight(ref, surfaceNav(point), &height); detour.DtStatusFailed(status) {
				return errors.New("moving corridor detail height unavailable")
			}
			point.Z = height
		}
		actual, err := s.endpoint(ctx, point)
		if err != nil {
			return err
		}
		prior := (*path)[len(*path)-1]
		if math.Abs(float64(actual.Z-prior.Z)) > float64(s.Metadata.BodyClimb) {
			return errors.New("moving corridor exceeds the body's physical step")
		}
		bearing := math.Atan2(float64(actual.Y-prior.Y), float64(actual.X-prior.X))
		if err := clearBodyMotionSegment(prior, actual, bearing, func(a, b Point) (bool, error) { return s.surface.Clear(ctx, a, b) }); err != nil {
			return err
		}
		*path = append(*path, actual)
	}
	return ctx.Err()
}

// ValidateSurfaceTile binds the native Detour64 layout and encoded body to
// the generator manifest before Detour allocates or follows any offsets.
func ValidateSurfaceTile(tile []byte, meta SurfaceMeshMetadata) error {
	if len(tile) < 100 || binary.LittleEndian.Uint32(tile) != uint32(detour.DT_NAVMESH_MAGIC) || binary.LittleEndian.Uint32(tile[4:]) != 7 {
		return errors.New("invalid Detour surface tile header")
	}
	u32 := func(at int) uint64 { return uint64(binary.LittleEndian.Uint32(tile[at:])) }
	polys, vertices, links, details, detailVertices, detailTris, bounds := u32(24), u32(28), u32(32), u32(36), u32(40), u32(44), u32(48)
	length := uint64(100) + vertices*12 + polys*32 + links*16 + details*12 + detailVertices*12 + detailTris*4 + bounds*16
	if polys == 0 || polys > 65535 || vertices < 3 || vertices > 65535 || details != polys ||
		u32(52) != 0 || u32(56) != polys || length != uint64(len(tile)) || meta.PolyRefBits != 64 {
		return errors.New("surface tile is not the complete expected Detour64 mesh")
	}
	for at, want := range map[int]float32{60: meta.BodyHeight, 64: meta.BodyRadius, 68: meta.BodyClimb, 96: 1 / meta.CellSize} {
		got := math.Float32frombits(binary.LittleEndian.Uint32(tile[at:]))
		if !finite(got) || !finite(want) || math.Abs(float64(got-want)) > .0001 {
			return errors.New("surface tile body or cell dimensions differ from its manifest")
		}
	}
	for axis := 0; axis < 3; axis++ {
		lo := math.Float32frombits(binary.LittleEndian.Uint32(tile[72+axis*4:]))
		hi := math.Float32frombits(binary.LittleEndian.Uint32(tile[84+axis*4:]))
		if !finite(lo) || !finite(hi) || lo >= hi {
			return errors.New("surface tile has invalid model bounds")
		}
	}
	return nil
}
