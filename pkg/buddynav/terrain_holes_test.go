package buddynav

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Encode a real v9 MAP tile with flat or uint8 heights and a hole table.
func terrainHoleFixture(flat bool) []byte {
	heightSize := 16
	if !flat {
		heightSize += 129*129 + 128*128
	}
	b := make([]byte, 44+heightSize+512)
	set := func(at int, value uint32) { binary.LittleEndian.PutUint32(b[at:], value) }
	set(0, 0x5350414d)
	set(4, 9)
	set(20, 44)
	set(24, uint32(heightSize))
	set(36, uint32(44+heightSize))
	set(40, 512)
	set(44, 0x5447484d)
	set(48, 4)
	if flat {
		set(48, 1)
	}
	set(52, math.Float32bits(90))
	set(56, math.Float32bits(90))
	return b
}

func TestTerrainHoleBitOrderAndMalformedTables(t *testing.T) {
	b := terrainHoleFixture(false)
	off := len(b) - 512
	// In block (7,15), flag local rows 4,5 and columns 4,5.
	binary.LittleEndian.PutUint16(b[off+2*(7*16+15):], 0x0400)
	path := filepath.Join(t.TempDir(), "tile.map")
	read := func(b []byte) terrainTile {
		t.Helper()
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		return readTerrainTile(path)
	}
	tile := read(b)
	for row := 0; row < 128; row++ {
		for col := 0; col < 128; col++ {
			q := Point{-(float32(row) + .5) * float32(533.3333) / 128, -(float32(col) + .5) * float32(533.3333) / 128, 90}
			expect := row >= 60 && row <= 61 && col >= 124 && col <= 125
			if !tile.height || tile.err != nil || tile.holeAt(q) != expect {
				t.Fatalf("row %d col %d expected hole=%t: %+v", row, col, expect, tile)
			}
		}
	}
	for _, tc := range []struct {
		name  string
		at    int
		value uint32
	}{
		{"missing_offset", 36, 0}, {"missing_size", 40, 0}, {"partial_mask", 40, 510}, {"overlaps_header", 36, 40},
		{"overlaps_height", 36, 44}, {"overflows_file", 36, ^uint32(0)}, {"short_height", 24, 8},
		{"short_grid", 24, 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := append([]byte(nil), b...)
			binary.LittleEndian.PutUint32(bad[tc.at:], tc.value)
			if tile := read(bad); tile.err == nil {
				t.Fatalf("accepted malformed MAP: %+v", tile)
			}
		})
	}
	if tile := read(b[:len(b)-1]); tile.err == nil {
		t.Fatal("accepted truncated hole table")
	}
}

func TestTerrainHoleOnOrdinaryGroundRequiresMeasuredSupport(t *testing.T) {
	a, b := Point{-8900, -100, 90}, Point{-8899, -100, 90}
	for _, tc := range []struct {
		name        string
		flat, floor bool
	}{
		{"interpolated_missing_floor", false, false}, {"interpolated_model_floor", false, true}, {"encoded_flat_base", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateMapCache(t)
			dir := regionAssetDirectories(t)
			mapFixture(t, dir, 36, a)
			data := terrainHoleFixture(tc.flat)
			for i := len(data) - 512; i < len(data); i++ {
				data[i] = 255
			}
			x, y := mapGrid(a)
			put := func(file string, data []byte) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, file), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			put(fmt.Sprintf("maps/036%02d%02d.map", x, y), data)
			if tc.floor {
				put(fmt.Sprintf("vmaps/036_%02d_%02d.vmtile", y, x), floorSpawnFixture("actual.wmo", Point{}, 1))
				put("vmaps/actual.wmo.vmo", floorModelFixture(false))
			}
			path, err := PlanMapContext(context.Background(), dir, 36, a, b)
			if !tc.flat && !tc.floor {
				if err == nil || len(path) != 0 {
					t.Fatal("mesh and masked height supplied a floor", path, err)
				}
				return
			}
			if err != nil || len(path) < 2 || path[0].Z != 90 || path[len(path)-1].Z != 90 {
				t.Fatal(path, err)
			}
			planner := currentMapPlanners.items[currentMapPlanners.order[0]]
			h, known, err := planner.terrain(a)
			if err != nil || known != tc.flat || known && h != 90 {
				t.Fatal("encoded terrain", h, known, err)
			}
		})
	}
}

func TestInteriorGroundDetourRequiresClearConnectedLegs(t *testing.T) {
	start, dest := Point{0, 0, 0}, Point{2, 0, 0}
	inside := func(Point) bool { return true }
	// A solid wall at x=1 with no opening at any search height or width.
	clear := func(a, b Point) (bool, error) { return (a.X < 1) == (b.X < 1), nil }
	path := []Point{start, dest}
	for _, disconnected := range []bool{false, true} {
		attempts := 0
		plan := func(a, b Point) ([]Point, error) {
			attempts++
			if disconnected {
				a.X = b.X
			}
			return []Point{a, b}, nil
		}
		route, err := new(planner).clearInteriorGround(context.Background(), path, dest, inside, plan, clear)
		if !errors.Is(err, errBodyCollision) || len(route) != 0 || attempts == 0 || attempts > 24 {
			t.Fatal(route, err, attempts)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	route, err := new(planner).clearInteriorGround(cancelled, path, dest, inside, nil, clear)
	if !errors.Is(err, context.Canceled) || len(route) != 0 {
		t.Fatal(route, err)
	}
}

func TestCollisionTriangleBlockBoundsPreserveExactHits(t *testing.T) {
	var group floorGeometry
	for i := 0; i < 3*floorBlockSize; i++ {
		z := float32(i)
		base := uint32(len(group.vertices))
		group.vertices = append(group.vertices, Point{-1, -1, z}, Point{1, -1, z}, Point{0, 1, z})
		group.triangles = append(group.triangles, [3]uint32{base, base + 1, base + 2})
	}
	plain := group
	if group.indexBounds(23) != 0 || group.indexBounds(72) != 72 {
		t.Fatal("triangle index ignored storage budget")
	}
	for _, p := range []Point{{0, 0, 32.1}, {0, 0, 64.1}, {-.5, 0, 31.01}, {3, 0, 29}, {0, 0, 90}} {
		for _, direction := range []Point{{0, 0, -1}, {0, 0, 1}, {.02, 0, -.99}} {
			for _, anyHit := range []bool{true, false} {
				want, hit, e := plain.ray(context.Background(), p, direction, 45, anyHit)
				got, ok, err := group.ray(context.Background(), p, direction, 45, anyHit)
				if e != nil || err != nil || hit != ok || want != got {
					t.Fatal(p, direction, anyHit, want, got, hit, ok, err)
				}
			}
		}
	}
}

func TestGroundSweepCacheSeparatesStepClearanceAndFailures(t *testing.T) {
	ctx := context.Background()
	p := new(planner)
	s := groundSweep{from: Point{}, to: Point{X: 1}}
	count := 0
	chord := func(a, b Point) (bool, error) { count++; return a.Z > .2, nil }
	if err := p.clearGroundSweep(ctx, s, chord); err != nil {
		t.Fatal(err)
	}
	if count != 6 {
		t.Fatal("upper body rays", count)
	}
	s.feet = true
	if err := p.clearGroundSweep(ctx, s, chord); !errors.Is(err, errBodyCollision) || count != 7 {
		t.Fatal("feet clearance reused an upper-body result", err, count)
	}
	if err := p.clearGroundSweep(ctx, s, chord); !errors.Is(err, errBodyCollision) || count != 7 {
		t.Fatal(err, count)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := p.clearGroundSweep(canceled, s, chord); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.to.Y++
	unavailable := errors.New("unavailable collision model")
	if err := p.clearGroundSweep(ctx, s, func(Point, Point) (bool, error) { return false, unavailable }); !errors.Is(err, unavailable) {
		t.Fatal(err)
	}
	if err := p.clearGroundSweep(ctx, s, func(Point, Point) (bool, error) { return true, nil }); err != nil {
		t.Fatal("cached unavailable source", err)
	}
}
