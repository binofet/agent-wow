package buddynav

// Non-tiled collision maps contain one global ModelSpawn in their .vmtree.
// Framing follows public AzerothCore StaticMapTree::InitMap, BIH::writeToFile,
// and ModelSpawn::writeToFile. Unlike .vmtile spawns, it has no referencedVal.

import (
	"errors"
	"fmt"
	"io"
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
	lo, hi := c.point(), c.point()
	if lo.X > hi.X || lo.Y > hi.Y || lo.Z > hi.Z {
		return nil, errors.New("invalid global collision BIH bounds")
	}
	// The producer builds a single leaf for the sole global primitive.
	// Enforce that representation rather than trusting arbitrary BIH indices.
	if c.u32() != 3 || c.u32() != 3<<30 || c.u32() != 1 || c.u32() != 0 || c.u32() != 1 || c.u32() != 0 {
		return nil, errors.New("global collision tree must contain one BIH primitive")
	}
	c.tag("GOBJ")
	flags := c.u32()
	c.take(2) // ADT ID
	c.u32()   // model spawn ID
	pos, rot := c.point(), c.point()
	scale := c.f32()
	if flags&^uint32(7) != 0 || flags&4 == 0 || scale <= 0 || scale > 100 {
		return nil, errors.New("invalid global collision spawn transform or bounds")
	}
	spawnLo, spawnHi := c.point(), c.point()
	if spawnLo.X > spawnHi.X || spawnLo.Y > spawnHi.Y || spawnLo.Z > spawnHi.Z {
		return nil, errors.New("invalid global collision spawn bounds")
	}
	n := c.u32()
	if n == 0 || n > 255 {
		return nil, errors.New("invalid global collision model name length")
	}
	name := strings.TrimRight(string(c.take(int(n))), "\x00")
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." {
		return nil, errors.New("invalid global collision model filename")
	}
	if err := c.done(); err != nil {
		return nil, err
	}
	return []floorSpawn{{name: name, pos: pos, lo: spawnLo, hi: spawnHi, scale: scale, inv: floorInverse(rot)}}, nil
}
