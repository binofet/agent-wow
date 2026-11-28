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
	status = s.query.FindStraightPath(surfaceNav(from), surfaceNav(to), polygons, count, points, flags, nil, &written, len(flags), detour.DT_STRAIGHTPATH_ALL_CROSSINGS)
	if status != detour.DT_SUCCESS || written == 0 || flags[written-1]&detour.DT_STRAIGHTPATH_END == 0 {
		return nil, errors.New("moving-surface funnel is incomplete")
	}
	path := make([]Point, written)
	for i := range path {
		path[i] = surfacePoint(points[3*i:])
	}
	return path, ctx.Err()
}
