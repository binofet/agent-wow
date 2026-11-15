package buddynav

// Complete non-tiled collision maps store their global ModelSpawns after GOBJ.
// Framing follows public AzerothCore StaticMapTree::InitMap, BIH::writeToFile,
// and ModelSpawn::writeToFile. Unlike .vmtile spawns, they have no referencedVal.
// Mixed global/tile representations remain unsupported and fail closed.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const globalFloorBytes = 2 << 20

func readGlobalFloorSpawns(dir string, mapID uint32) ([]floorSpawn, bool, error) {
	if mapID > 999 {
		return nil, false, errors.New("collision map ID exceeds asset filename range")
	}
	f, err := os.Open(filepath.Join(dir, fmt.Sprintf("%03d.vmtree", mapID)))
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	var header [9]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return nil, false, fmt.Errorf("collision tree header: %w", err)
	}
	if string(header[:8]) != "VMAP_4.8" || header[8] > 1 {
		return nil, false, errors.New("invalid collision tree header")
	}
	if header[8] == 1 {
		// Tiled map trees may exceed 2MiB. Their BIH is not used by our
		// height reader: individual bounded .vmtile files supply the spawns.
		return nil, true, nil
	}
	tail, err := io.ReadAll(io.LimitReader(f, int64(globalFloorBytes-len(header)+1)))
	if err != nil {
		return nil, false, err
	}
	if len(tail) > globalFloorBytes-len(header) {
		return nil, false, errors.New("global collision tree exceeds size limit")
	}
	b := append(header[:], tail...)
	spawns, err := parseGlobalFloorSpawns(b)
	return spawns, false, err
}

func parseGlobalFloorSpawns(b []byte) ([]floorSpawn, error) {
	if len(b) > globalFloorBytes {
		return nil, errors.New("global collision tree exceeds size limit")
	}
	c := floorCursor{b: b}
	c.tag("VMAP_4.8")
	if tiled := c.take(1); len(tiled) != 1 || tiled[0] != 0 {
		return nil, errors.New("expected non-tiled collision tree")
	}
	c.tag("NODE")
	n, err := globalPrimitiveCount(&c)
	if err != nil {
		return nil, err
	}
	c.tag("GOBJ")
	// Support complete global trees only: every primitive has one record.
	// This retains exact EOF/truncation checks; a valid prefix cannot hide a
	// missing collidable model. Actual map369 has539 primitives and records.
	out := make([]floorSpawn, 0, n)
	ids := make(map[uint32]bool, n)
	for range n {
		flags := c.u32()
		c.take(2)     // ADT ID
		id := c.u32() // model spawn ID
		pos, rot := c.point(), c.point()
		scale := c.f32()
		if flags&^uint32(7) != 0 || flags&4 == 0 || scale <= 0 || scale > 100 {
			return nil, errors.New("invalid global collision spawn transform or bounds")
		}
		spawnLo, spawnHi := c.point(), c.point()
		if spawnLo.X > spawnHi.X || spawnLo.Y > spawnHi.Y || spawnLo.Z > spawnHi.Z {
			return nil, errors.New("invalid global collision spawn bounds")
		}
		length := c.u32()
		if length == 0 || length > 255 {
			return nil, errors.New("invalid global collision model name length")
		}
		name := strings.TrimRight(string(c.take(int(length))), "\x00")
		if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." {
			return nil, errors.New("invalid global collision model filename")
		}
		if c.err != nil {
			return nil, c.err
		}
		if ids[id] {
			return nil, errors.New("duplicate global collision spawn ID")
		}
		ids[id] = true
		out = append(out, floorSpawn{name: name, pos: pos, lo: spawnLo, hi: spawnHi, scale: scale, inv: floorInverse(rot)})
	}
	if err := c.done(); err != nil {
		return nil, err
	}
	return out, nil
}

// Validate the producer's BIH framing without using it to accelerate floor
// queries. Counts fit the existing2MiB tree and10000-spawn tile-reader budgets.
// Active edges matter: an empty child has an infinite clip and an unused
// offset that can legally point back to its parent.
func globalPrimitiveCount(c *floorCursor) (uint32, error) {
	lo, hi := c.point(), c.point()
	if c.err != nil {
		return 0, c.err
	}
	if lo.X > hi.X || lo.Y > hi.Y || lo.Z > hi.Z {
		return 0, errors.New("invalid global collision BIH bounds")
	}
	n := c.u32()
	if n < 3 || n%3 != 0 || n > 1<<20 {
		return 0, errors.New("invalid global collision BIH node count")
	}
	nodes := c.take(int(n) * 4)
	count := c.u32()
	if c.err != nil {
		return 0, c.err
	}
	if count == 0 || count > 10000 {
		return 0, errors.New("global collision primitive count exceeds budget")
	}
	objects := c.take(int(count) * 4)
	if c.err != nil {
		return 0, c.err
	}
	seenIDs := make([]bool, count)
	for i := uint32(0); i < count; i++ {
		id := binary.LittleEndian.Uint32(objects[i*4:])
		if id >= count || seenIDs[id] {
			return 0, errors.New("invalid global collision primitive reference")
		}
		seenIDs[id] = true
	}
	word := func(i uint32) uint32 { return binary.LittleEndian.Uint32(nodes[i*4:]) }
	type visit struct {
		index uint32
		depth int
	}
	stack := []visit{{0, 1}}
	visited := make([]bool, n/3)
	covered := make([]bool, count)
	for len(stack) > 0 {
		last := len(stack) - 1
		v := stack[last]
		stack = stack[:last]
		if v.depth > 64 || v.index%3 != 0 || v.index > n-3 || visited[v.index/3] {
			return 0, errors.New("invalid global collision BIH edge or depth")
		}
		visited[v.index/3] = true
		tag := word(v.index)
		axis, offset := tag>>30, tag&0x1fffffff
		bvh2 := tag&(1<<29) != 0
		if axis == 3 {
			size := word(v.index + 1)
			if bvh2 || word(v.index+2) != 0 || size == 0 || offset > count || size > count-offset {
				return 0, errors.New("invalid global collision BIH leaf")
			}
			for j := offset; j < offset+size; j++ {
				if covered[j] {
					return 0, errors.New("overlapping global collision BIH leaves")
				}
				covered[j] = true
			}
			continue
		}
		left, right := math.Float32frombits(word(v.index+1)), math.Float32frombits(word(v.index+2))
		if math.IsNaN(float64(left)) || math.IsNaN(float64(right)) {
			return 0, errors.New("invalid global collision BIH clip")
		}
		if bvh2 {
			if !finite(left) || !finite(right) || left > right {
				return 0, errors.New("invalid global collision BVH2 bounds")
			}
			stack = append(stack, visit{offset, v.depth + 1})
		} else {
			if math.IsInf(float64(left), 1) || math.IsInf(float64(right), -1) || math.IsInf(float64(left), -1) && math.IsInf(float64(right), 1) {
				return 0, errors.New("invalid global collision BIH empty children")
			}
			if !math.IsInf(float64(left), -1) {
				stack = append(stack, visit{offset, v.depth + 1})
			}
			if !math.IsInf(float64(right), 1) {
				stack = append(stack, visit{offset + 3, v.depth + 1})
			}
		}
	}
	for _, ok := range visited {
		if !ok {
			return 0, errors.New("unreachable global collision BIH node")
		}
	}
	for _, ok := range covered {
		if !ok {
			return 0, errors.New("uncovered global collision primitive")
		}
	}
	return count, nil
}
