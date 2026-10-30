package buddynav

// Height-only VMAP_4.8 reader. Geometry, spawn transforms and triangle-ray
// conventions follow the public AzerothCore ModelInstance/WorldModel format.
// The pinned AzerothGhost reader omits .vmo and inter-group LIQU framing, so
// cannot currently read these independently extracted client assets correctly.
// No assets are rewritten and no collision/server state is queried.

import (
	"container/list"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
)

const floorCacheBytes = 32 << 20
const floorModelBytes = 8 << 20
const vmapMid = float32(32 * 533.33333333)

type floorSpawn struct {
	name        string
	pos, lo, hi Point // VMAP internal axes, deliberately not WoW world axes.
	scale       float32
	inv         [3][3]float32
}
type floorGeometry struct {
	vertices  []Point
	triangles [][3]uint32
}
type floorModel struct {
	groups []floorGeometry
	bytes  int
	elem   *list.Element
	name   string
}
type floorReader struct {
	region       Region
	mapID        uint32
	window       *mapWindow
	modelLimit   int64
	globalLoaded bool
	globalTiled  bool
	globalSpawns []floorSpawn
	dir          string
	tiles        map[string][]floorSpawn
	models       map[string]*floorModel
	lru          *list.List
	bytes        int
	lastModel    string
}

func newFloorReader(dir string) *floorReader {
	return newFloorReaderRegion(dir, RegionNorthshire)
}

func newFloorReaderRegion(dir string, region Region) *floorReader {
	return &floorReader{dir: dir, region: region, tiles: make(map[string][]floorSpawn), models: make(map[string]*floorModel), lru: list.New()}
}

func (p *planner) floor(ctx context.Context, q Point, mesh, terrain float32) (float32, error) {
	h, ok, err := p.floors.heightContext(ctx, q, mesh, .5, 2.5)
	if err != nil {
		return 0, err
	}
	return selectPhysicalFloor(mesh, terrain, h, ok)
}
func selectPhysicalFloor(mesh, terrain, collision float32, collisionKnown bool) (float32, error) {
	valid := func(h float32) bool { return finite(h) && h >= mesh-2 && h <= mesh+.5 }
	known := valid(terrain)
	h := terrain
	if collisionKnown && valid(collision) && (!known || collision > h) {
		h = collision
		known = true
	}
	if !known {
		return 0, errors.New("no physical floor near verified ground layer")
	}
	return h, nil
}

// Called only under the planner gate. The fixed region loads at most nine
// spawn tiles; decoded model geometry uses a strict 32MiB LRU, 8MiB/model cap.
func (f *floorReader) height(q Point, mesh float32) (float32, bool, error) {
	return f.heightRange(q, mesh, .5, 2.5)
}
func (f *floorReader) heightRange(q Point, mesh, above, depth float32) (float32, bool, error) {
	return f.heightContext(context.Background(), q, mesh, above, depth)
}
func (f *floorReader) heightContext(ctx context.Context, q Point, mesh, above, depth float32) (float32, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	f.lastModel = ""
	gx, gy := int(math.Floor(32-float64(q.X)/533.33333333)), int(math.Floor(32-float64(q.Y)/533.33333333))
	name := fmt.Sprintf("%03d_%02d_%02d.vmtile", f.mapID, gy, gx)
	if f.window != nil {
		if !f.window.contains(q) {
			return 0, false, errors.New("collision query leaves bounded map tile window")
		}
	} else {
		regionName, err := f.region.name()
		if err != nil {
			return 0, false, err
		}
		if !f.region.allowsFloorTile(name) {
			return 0, false, fmt.Errorf("collision query leaves bounded %s tiles", regionName)
		}
	}
	var spawns []floorSpawn
	global := false
	if f.window != nil {
		if !f.globalLoaded {
			var err error
			f.globalSpawns, f.globalTiled, err = readGlobalFloorSpawns(f.dir, f.mapID)
			if err != nil {
				return 0, false, fmt.Errorf("collision map tree unavailable: %w", err)
			}
			f.globalLoaded = true
		}
		global = !f.globalTiled
		if global {
			spawns = f.globalSpawns
		}
	}
	if !global {
		var ok bool
		spawns, ok = f.tiles[name]
		if !ok {
			b, err := readBounded(filepath.Join(f.dir, name), 2<<20)
			if err != nil {
				return 0, false, fmt.Errorf("collision tile unavailable: %w", err)
			}
			spawns, err = parseFloorSpawns(b)
			if err != nil {
				return 0, false, err
			}
			f.tiles[name] = spawns
		}
	}
	// Start close to the verified navigation layer. Terrain/collision more
	// than 2yd below it or .5yd above it cannot substitute a roof/floor.
	origin := Point{vmapMid - q.X, vmapMid - q.Y, mesh + above}
	best := depth
	hit := false
	for si, s := range spawns {
		if si%64 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, false, err
			}
		}
		if origin.X < s.lo.X || origin.X > s.hi.X || origin.Y < s.lo.Y || origin.Y > s.hi.Y || origin.Z < s.lo.Z || origin.Z-best > s.hi.Z {
			continue
		}
		m, err := f.model(s.name)
		if err != nil {
			return 0, false, err
		}
		p := floorMul(s.inv, Point{(origin.X - s.pos.X) / s.scale, (origin.Y - s.pos.Y) / s.scale, (origin.Z - s.pos.Z) / s.scale})
		d := floorMul(s.inv, Point{0, 0, -1})
		for _, g := range m.groups {
			for ti, tri := range g.triangles {
				if ti%1024 == 0 {
					if err := ctx.Err(); err != nil {
						return 0, false, err
					}
				}
				if t, ok := floorTriangle(p, d, g.vertices[tri[0]], g.vertices[tri[1]], g.vertices[tri[2]], best/s.scale); ok {
					best = t * s.scale
					hit = true
					f.lastModel = s.name
				}
			}
		}
	}
	return origin.Z - best, hit, nil
}
func allowedFloorTile(name string) bool {
	return RegionNorthshire.allowsFloorTile(name)
}
func (f *floorReader) model(name string) (*floorModel, error) {
	if m := f.models[name]; m != nil {
		f.lru.MoveToFront(m.elem)
		return m, nil
	}
	limit := f.modelLimit
	if limit == 0 {
		limit = floorModelBytes
	}
	b, err := readBounded(filepath.Join(f.dir, name+".vmo"), limit)
	if err != nil {
		return nil, fmt.Errorf("collision model unavailable: %w", err)
	}
	m, err := parseFloorModelBounded(b, int(limit))
	if err != nil {
		return nil, err
	}
	if m.bytes > floorCacheBytes {
		return nil, errors.New("collision geometry exceeds cache budget")
	}
	for f.bytes+m.bytes > floorCacheBytes {
		e := f.lru.Back()
		if e == nil {
			return nil, errors.New("collision cache budget exceeded")
		}
		old := e.Value.(*floorModel)
		delete(f.models, old.name)
		f.bytes -= old.bytes
		f.lru.Remove(e)
	}
	m.name = name
	m.elem = f.lru.PushFront(m)
	f.models[name] = m
	f.bytes += m.bytes
	return m, nil
}

type floorCursor struct {
	b   []byte
	i   int
	err error
}

func (c *floorCursor) take(n int) []byte {
	if c.err != nil {
		return nil
	}
	if n < 0 || n > len(c.b)-c.i {
		c.err = errors.New("truncated collision asset")
		return nil
	}
	v := c.b[c.i : c.i+n]
	c.i += n
	return v
}
func (c *floorCursor) u32() uint32 {
	v := c.take(4)
	if v == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(v)
}
func (c *floorCursor) f32() float32 {
	v := math.Float32frombits(c.u32())
	if !finite(v) {
		c.err = errors.New("non-finite collision coordinate")
	}
	return v
}
func (c *floorCursor) point() Point { return Point{c.f32(), c.f32(), c.f32()} }
func (c *floorCursor) tag(tag string) {
	if string(c.take(len(tag))) != tag && c.err == nil {
		c.err = errors.New("unexpected collision chunk")
	}
}
func (c *floorCursor) bih() {
	c.take(24)
	n := c.u32()
	if n > 1<<20 {
		c.err = errors.New("collision BIH count exceeds budget")
		return
	}
	c.take(int(n) * 4)
	n = c.u32()
	if n > 1<<20 {
		c.err = errors.New("collision BIH count exceeds budget")
		return
	}
	c.take(int(n) * 4)
}
func (c *floorCursor) done() error {
	if c.err != nil {
		return c.err
	}
	if c.i != len(c.b) {
		return errors.New("trailing collision asset data")
	}
	return nil
}

func parseFloorSpawns(b []byte) ([]floorSpawn, error) {
	c := floorCursor{b: b}
	c.tag("VMAP_4.8")
	n := c.u32()
	if n > 10000 || uint64(n)*47 > uint64(len(b)-c.i) {
		return nil, errors.New("collision spawn count exceeds budget")
	}
	out := make([]floorSpawn, 0, n)
	for range n {
		flags := c.u32()
		c.take(2)
		c.u32()
		pos, rot := c.point(), c.point()
		scale := c.f32()
		var lo, hi Point
		if flags&4 != 0 {
			lo, hi = c.point(), c.point()
		}
		length := c.u32()
		if length == 0 || length > 255 {
			return nil, errors.New("invalid collision model name length")
		}
		name := strings.TrimRight(string(c.take(int(length))), "\x00")
		c.u32()
		if c.err != nil {
			return nil, c.err
		}
		if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." {
			return nil, errors.New("invalid collision model filename")
		}
		if scale <= 0 || scale > 100 || flags & ^uint32(7) != 0 {
			return nil, errors.New("invalid collision spawn transform")
		}
		if flags&4 == 0 {
			continue
		}
		if lo.X > hi.X || lo.Y > hi.Y || lo.Z > hi.Z {
			return nil, errors.New("invalid collision bounds")
		}
		out = append(out, floorSpawn{name: name, pos: pos, lo: lo, hi: hi, scale: scale, inv: floorInverse(rot)})
	}
	if err := c.done(); err != nil {
		return nil, err
	}
	return out, nil
}
func parseFloorModel(b []byte) (*floorModel, error) {
	return parseFloorModelBounded(b, floorModelBytes)
}

func parseFloorModelBounded(b []byte, maxDecoded int) (*floorModel, error) {
	if maxDecoded < 1 || maxDecoded > floorCacheBytes || len(b) > maxDecoded {
		return nil, errors.New("collision model exceeds bounded read/decode budget")
	}
	c := floorCursor{b: b}
	c.tag("VMAP_4.8")
	c.tag("WMOD")
	if c.u32() != 8 {
		return nil, errors.New("invalid collision WMOD size")
	}
	c.u32()
	c.tag("GMOD")
	n := c.u32()
	if n > 512 || uint64(n)*44 > uint64(len(b)-c.i) {
		return nil, errors.New("collision group count exceeds budget")
	}
	m := &floorModel{groups: make([]floorGeometry, 0, n), bytes: int(n) * 64}
	for range n {
		c.point()
		c.point()
		c.u32()
		c.u32()
		c.tag("VERT")
		size, count := c.u32(), c.u32()
		if count > 250000 || uint64(size) != 4+uint64(count)*12 {
			return nil, errors.New("invalid collision vertex chunk")
		}
		if c.err != nil || uint64(count)*12 > uint64(len(b)-c.i) {
			return nil, errors.New("truncated collision vertices")
		}
		g := floorGeometry{vertices: make([]Point, count)}
		for i := range g.vertices {
			g.vertices[i] = c.point()
		}
		m.bytes += int(count) * 12
		if count == 0 {
			m.groups = append(m.groups, g)
			continue
		}
		c.tag("TRIM")
		size, count = c.u32(), c.u32()
		if count > 250000 || uint64(size) != 4+uint64(count)*12 {
			return nil, errors.New("invalid collision triangle chunk")
		}
		if c.err != nil || uint64(count)*12 > uint64(len(b)-c.i) {
			return nil, errors.New("truncated collision triangles")
		}
		g.triangles = make([][3]uint32, count)
		for i := range g.triangles {
			for j := range 3 {
				v := c.u32()
				if v >= uint32(len(g.vertices)) {
					return nil, errors.New("collision triangle index out of bounds")
				}
				g.triangles[i][j] = v
			}
		}
		m.bytes += int(count) * 12
		c.tag("MBIH")
		c.bih()
		c.tag("LIQU")
		size = c.u32()
		if size > floorModelBytes {
			return nil, errors.New("collision liquid chunk exceeds budget")
		}
		c.take(int(size))
		if c.err != nil {
			return nil, c.err
		}
		if m.bytes > maxDecoded {
			return nil, errors.New("decoded collision model exceeds budget")
		}
		m.groups = append(m.groups, g)
	}
	c.tag("GBIH")
	c.bih()
	if err := c.done(); err != nil {
		return nil, err
	}
	return m, nil
}

func floorMul(m [3][3]float32, p Point) Point {
	return Point{m[0][0]*p.X + m[0][1]*p.Y + m[0][2]*p.Z, m[1][0]*p.X + m[1][1]*p.Y + m[1][2]*p.Z, m[2][0]*p.X + m[2][1]*p.Y + m[2][2]*p.Z}
}
func floorInverse(r Point) [3][3]float32 {
	a, b, c := float64(r.Y)*math.Pi/180, float64(r.X)*math.Pi/180, float64(r.Z)*math.Pi/180
	ca, sa, cb, sb, cc, sc := float32(math.Cos(a)), float32(math.Sin(a)), float32(math.Cos(b)), float32(math.Sin(b)), float32(math.Cos(c)), float32(math.Sin(c))
	return [3][3]float32{{ca * cb, sa * cb, -sb}, {ca*sb*sc - sa*cc, sa*sb*sc + ca*cc, cb * sc}, {ca*sb*cc + sa*sc, sa*sb*cc - ca*sc, cb * cc}}
}
func floorSub(a, b Point) Point   { return Point{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func floorDot(a, b Point) float32 { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }
func floorCross(a, b Point) Point {
	return Point{a.Y*b.Z - a.Z*b.Y, a.Z*b.X - a.X*b.Z, a.X*b.Y - a.Y*b.X}
}
func floorTriangle(origin, dir, a, b, c Point, max float32) (float32, bool) {
	e1, e2 := floorSub(b, a), floorSub(c, a)
	p := floorCross(dir, e2)
	det := floorDot(e1, p)
	if math.Abs(float64(det)) < 1e-5 {
		return 0, false
	}
	inv := 1 / det
	s := floorSub(origin, a)
	u := inv * floorDot(s, p)
	if u < 0 || u > 1 {
		return 0, false
	}
	q := floorCross(s, e1)
	v := inv * floorDot(dir, q)
	if v < 0 || u+v > 1 {
		return 0, false
	}
	d := inv * floorDot(e2, q)
	return d, d > 0.00001 && d < max
}
