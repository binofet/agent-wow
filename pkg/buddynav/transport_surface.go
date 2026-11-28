package buddynav

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// SurfaceTransform is a rigid moving model pose. Scale is the observed
// OBJECT_FIELD_SCALE_X, never a radius or an inferred boarding distance.
type SurfaceTransform struct {
	Origin Point
	Yaw    float32
	Scale  float32
}

func (t SurfaceTransform) Valid() bool {
	return worldPoint(t.Origin) && finite(t.Yaw) && finite(t.Scale) && t.Scale > 0
}

func (t SurfaceTransform) World(local Point) Point {
	c, s := float32(math.Cos(float64(t.Yaw))), float32(math.Sin(float64(t.Yaw)))
	return Point{t.Origin.X + t.Scale*(c*local.X-s*local.Y),
		t.Origin.Y + t.Scale*(s*local.X+c*local.Y), t.Origin.Z + t.Scale*local.Z}
}

func (t SurfaceTransform) Local(world Point) Point {
	c, s := float32(math.Cos(float64(t.Yaw))), float32(math.Sin(float64(t.Yaw)))
	x, y := (world.X-t.Origin.X)/t.Scale, (world.Y-t.Origin.Y)/t.Scale
	return Point{c*x + s*y, -s*x + c*y, (world.Z - t.Origin.Z) / t.Scale}
}

// PassengerTransform uses the world and local coordinates of ONE received
// MovementInfo. Combining coordinates from different packets loses the ship's
// motion. Passenger coordinates already include the object's scale.
func PassengerTransform(world, local Point, worldYaw, localYaw float32) (SurfaceTransform, error) {
	if !worldPoint(world) || !worldPoint(local) || !finite(worldYaw) || !finite(localYaw) {
		return SurfaceTransform{}, errors.New("complete finite passenger frame required")
	}
	t := SurfaceTransform{Yaw: float32(math.Remainder(float64(worldYaw-localYaw), 2*math.Pi)), Scale: 1}
	offset := t.World(local)
	t.Origin = floorSub(world, offset)
	return t, nil
}

// TransportSurface is the client collision model, in its own local coordinates.
// The same triangles feed physical contact and the offline Recast bake.
type TransportSurface struct {
	DisplayID uint32
	ModelName string
	ModelSHA  string
	geometry  *floorModel
}

func transportModelName(data []byte, displayID uint32) (string, error) {
	if len(data) < 20 || string(data[:4]) != "WDBC" {
		return "", errors.New("invalid GameObjectDisplayInfo header")
	}
	n, fields, record, stringsSize := binary.LittleEndian.Uint32(data[4:]), binary.LittleEndian.Uint32(data[8:]), binary.LittleEndian.Uint32(data[12:]), binary.LittleEndian.Uint32(data[16:])
	end := uint64(20) + uint64(n)*uint64(record)
	if fields != 19 || record != 76 || end+uint64(stringsSize) != uint64(len(data)) {
		return "", errors.New("invalid GameObjectDisplayInfo layout")
	}
	var name string
	for i := uint32(0); i < n; i++ {
		row := data[20+uint64(i)*uint64(record):]
		if binary.LittleEndian.Uint32(row) != displayID {
			continue
		}
		offset := binary.LittleEndian.Uint32(row[4:])
		if offset >= stringsSize || name != "" {
			return "", errors.New("invalid or duplicate moving model display")
		}
		tail := data[end+uint64(offset):]
		stop := strings.IndexByte(string(tail), 0)
		if stop < 1 {
			return "", errors.New("missing moving model filename")
		}
		name = filepath.Base(strings.ReplaceAll(string(tail[:stop]), "\\", "/"))
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return "", errors.New("observed display has no client collision model")
	}
	if strings.EqualFold(filepath.Ext(name), ".mdx") {
		name = strings.TrimSuffix(name, filepath.Ext(name)) + ".m2"
	}
	// The maintained VMAP extractor capitalizes the first filename letter.
	name = strings.ToUpper(name[:1]) + name[1:]
	return name, nil
}

func LoadTransportSurface(ctx context.Context, dataDir string, displayID uint32) (*TransportSurface, error) {
	if displayID == 0 || ctx.Err() != nil {
		return nil, errors.New("observed moving model display required")
	}
	dir, err := mapDataDirectory(dataDir)
	if err != nil {
		return nil, err
	}
	dbc, err := readBounded(filepath.Join(dir, "dbc", "GameObjectDisplayInfo.dbc"), 16<<20)
	if err != nil {
		return nil, fmt.Errorf("moving model display reference: %w", err)
	}
	name, err := transportModelName(dbc, displayID)
	if err != nil {
		return nil, err
	}
	// Client references use Windows filename semantics. Extracted VMAP names
	// can differ in case; resolve exactly one equal filename on case-sensitive
	// hosts and retain the actual model name in its content binding.
	models := filepath.Join(dir, "vmaps")
	entries, err := os.ReadDir(models)
	if err != nil {
		return nil, err
	}
	resolved := ""
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(entry.Name(), name+".vmo") {
			if resolved != "" {
				return nil, errors.New("ambiguous case-insensitive collision model")
			}
			resolved = entry.Name()
		}
	}
	if resolved == "" {
		return nil, fmt.Errorf("moving collision model %s is missing", name)
	}
	name = strings.TrimSuffix(resolved, ".vmo")
	body, err := readBounded(filepath.Join(models, resolved), floorCacheBytes)
	if err != nil {
		return nil, fmt.Errorf("moving model collision: %w", err)
	}
	model, err := parseFloorModelBounded(body, floorCacheBytes)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	return &TransportSurface{DisplayID: displayID, ModelName: name, ModelSHA: hex.EncodeToString(sum[:]), geometry: model}, nil
}

func (s *TransportSurface) Triangles() ([]Point, [][3]int) {
	var vertices []Point
	var triangles [][3]int
	for _, group := range s.geometry.groups {
		base := len(vertices)
		vertices = append(vertices, group.vertices...)
		for _, triangle := range group.triangles {
			triangles = append(triangles, [3]int{base + int(triangle[0]), base + int(triangle[1]), base + int(triangle[2])})
		}
	}
	return vertices, triangles
}

// Height returns the first actual hull contact under the local query point.
func (s *TransportSurface) Height(ctx context.Context, local Point, above, below float32) (float32, bool, error) {
	if !finite(local.X) || !finite(local.Y) || !finite(local.Z) || !finite(above) || !finite(below) || above < 0 || below < 0 {
		return 0, false, errors.New("finite local hull ray required")
	}
	origin := Point{local.X, local.Y, local.Z + above}
	best, known := above+below, false
	for _, g := range s.geometry.groups {
		d, hit, err := g.ray(ctx, origin, Point{Z: -1}, best, false)
		if err != nil {
			return 0, false, err
		}
		if hit {
			best, known = d, true
		}
	}
	return origin.Z - best, known, nil
}

func (s *TransportSurface) Clear(ctx context.Context, from, to Point) (bool, error) {
	delta := floorSub(to, from)
	reach := float32(math.Sqrt(float64(floorDot(delta, delta))))
	if reach == 0 {
		return true, ctx.Err()
	}
	direction := Point{delta.X / reach, delta.Y / reach, delta.Z / reach}
	for _, group := range s.geometry.groups {
		_, hit, err := group.ray(ctx, from, direction, reach, true)
		if err != nil || hit {
			return false, err
		}
	}
	return true, nil
}
