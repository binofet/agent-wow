package buddynav

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func globalFloorFixture(name string, rot Point, scale float32) []byte {
	var b bytes.Buffer
	b.WriteString("VMAP_4.8\x00NODE")
	floorPoint(&b, Point{vmapMid + 8890, vmapMid + 90, 80})
	floorPoint(&b, Point{vmapMid + 8910, vmapMid + 110, 100})
	for _, v := range []uint32{3, 3 << 30, 1, 0, 1, 0} {
		floorU32(&b, v)
	}
	b.WriteString("GOBJ")
	// Reuse the independently exercised ModelSpawn wire fixture, excluding
	// its tile header and TILE-ONLY trailing referencedVal.
	spawn := floorSpawnFixture(name, rot, scale)
	b.Write(spawn[12 : len(spawn)-4])
	return b.Bytes()
}

func TestGlobalCollisionTreeFullFramingAndAllTruncations(t *testing.T) {
	b := globalFloorFixture("test.wmo\x00", Point{}, 1)
	s, err := parseGlobalFloorSpawns(b)
	if err != nil || len(s) != 1 || s[0].name != "test.wmo" || s[0].pos != (Point{vmapMid + 8900, vmapMid + 100, 90}) {
		t.Fatalf("spawns=%+v err=%v", s, err)
	}
	for n := 0; n < len(b); n++ {
		if _, err := parseGlobalFloorSpawns(b[:n]); err == nil {
			t.Fatalf("accepted truncated global tree at %d", n)
		}
	}
	if _, err := parseGlobalFloorSpawns(append(bytes.Clone(b), 0, 0, 0, 0)); err == nil {
		t.Fatal("accepted tile-only referencedVal or trailing data")
	}
	for _, off := range []int{0, 8, 9, 37, 41, 45, 49, 53, 57, 61} {
		bad := bytes.Clone(b)
		bad[off] ^= 0x80
		if _, err := parseGlobalFloorSpawns(bad); err == nil {
			t.Fatalf("accepted corrupt header/BIH at %d", off)
		}
	}
}

func TestGlobalCollisionTreeRejectsUnsafeMetadata(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../test.wmo", "/tmp/test.wmo", "bad\\model.wmo", "bad\x00name.wmo"} {
		if _, err := parseGlobalFloorSpawns(globalFloorFixture(name, Point{}, 1)); err == nil {
			t.Fatalf("accepted unsafe model name %q", name)
		}
	}
	for _, scale := range []float32{0, -1, 101, float32(math.NaN()), float32(math.Inf(1))} {
		if _, err := parseGlobalFloorSpawns(globalFloorFixture("test.wmo", Point{}, scale)); err == nil {
			t.Fatalf("accepted invalid scale %g", scale)
		}
	}
	// BIH bounds, spawn flags/position/rotation/bounds, and bounded name count.
	for _, tc := range []struct {
		offset int
		value  uint32
	}{
		{13, math.Float32bits(float32(math.NaN()))},
		{13, math.Float32bits(vmapMid + 9000)},
		{65, 0}, {65, 0x84},
		{75, math.Float32bits(float32(math.Inf(1)))},
		{87, math.Float32bits(float32(math.NaN()))},
		{103, math.Float32bits(vmapMid + 9000)},
		{127, 256},
	} {
		bad := globalFloorFixture("test.wmo", Point{}, 1)
		binary.LittleEndian.PutUint32(bad[tc.offset:], tc.value)
		if _, err := parseGlobalFloorSpawns(bad); err == nil {
			t.Fatalf("accepted invalid metadata at %d", tc.offset)
		}
	}
}

func TestGlobalCollisionTreeFileBoundsAndTiledDispatch(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "036.vmtree")
	write := func(b []byte) {
		t.Helper()
		if err := os.WriteFile(name, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(globalFloorFixture("test.wmo", Point{}, 1))
	if s, tiled, err := readGlobalFloorSpawns(dir, 36); err != nil || tiled || len(s) != 1 {
		t.Fatalf("global read: spawns=%v tiled=%t err=%v", s, tiled, err)
	}
	write(append([]byte("VMAP_4.8\x00"), make([]byte, globalFloorBytes)...))
	if _, _, err := readGlobalFloorSpawns(dir, 36); err == nil {
		t.Fatal("accepted oversized non-tiled tree")
	}
	// A continent's unused BIH may exceed the global-reader bound; only its
	// validated mode selects the existing bounded per-tile collision parser.
	write(append([]byte("VMAP_4.8\x01"), make([]byte, globalFloorBytes)...))
	if s, tiled, err := readGlobalFloorSpawns(dir, 36); err != nil || !tiled || s != nil {
		t.Fatalf("tiled dispatch: spawns=%v tiled=%t err=%v", s, tiled, err)
	}
	for _, b := range [][]byte{nil, []byte("VMAP_4.8"), []byte("VMAP_4.7\x01"), []byte("VMAP_4.8\x02")} {
		write(b)
		if _, _, err := readGlobalFloorSpawns(dir, 36); err == nil {
			t.Fatalf("accepted bad header %q", b)
		}
	}
	if _, _, err := readGlobalFloorSpawns(dir, 1000); err == nil {
		t.Fatal("accepted invalid map ID")
	}
	if _, _, err := readGlobalFloorSpawns(dir, 37); err == nil {
		t.Fatal("accepted missing map tree")
	}
}

func TestGlobalCollisionSpawnPreservesInternalCoordinatesAndTransform(t *testing.T) {
	for _, rot := range []Point{{}, {0, 90, 0}, {180, 45, 0}} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "036.vmtree"), globalFloorFixture("test.wmo", rot, 2), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "test.wmo.vmo"), floorModelFixture(false), 0600); err != nil {
			t.Fatal(err)
		}
		f := newFloorReader(dir)
		q := Point{-8900, -100, 90}
		w, err := newMapWindow(36, q, q)
		if err != nil {
			t.Fatal(err)
		}
		f.mapID, f.window = 36, &w
		h, known, err := f.height(q, 90.25)
		if err != nil || !known || math.Abs(float64(h-90)) > .001 {
			t.Fatalf("rotation=%v height=%g known=%t err=%v", rot, h, known, err)
		}
		if !f.globalLoaded || f.globalTiled || len(f.globalSpawns) != 1 || len(f.tiles) != 0 {
			t.Fatal("global collision silently depended on a terrain spawn tile")
		}
	}
}

func FuzzGlobalCollisionTree(f *testing.F) {
	f.Add(globalFloorFixture("test.wmo", Point{}, 1))
	f.Add([]byte("VMAP_4.8\x00NODE"))
	f.Fuzz(func(t *testing.T, data []byte) {
		spawns, err := parseGlobalFloorSpawns(data)
		if err == nil && (len(spawns) != 1 || spawns[0].name == "" || !finite(spawns[0].scale)) {
			t.Fatal("invalid successful global spawn")
		}
	})
}
