package buddynav

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"github.com/o0olele/detour-go/detour"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func floorU32(b *bytes.Buffer, v uint32) { _ = binary.Write(b, binary.LittleEndian, v) }
func floorPoint(b *bytes.Buffer, p Point) {
	for _, v := range []float32{p.X, p.Y, p.Z} {
		floorU32(b, math.Float32bits(v))
	}
}
func floorBIH(b *bytes.Buffer) { b.Write(make([]byte, 24)); floorU32(b, 0); floorU32(b, 0) }
func floorModelFixture(emptyFirst bool) []byte {
	var b bytes.Buffer
	b.WriteString("VMAP_4.8WMOD")
	floorU32(&b, 8)
	floorU32(&b, 7)
	b.WriteString("GMOD")
	n := uint32(2)
	if emptyFirst {
		n++
	}
	floorU32(&b, n)
	if emptyFirst {
		b.Write(make([]byte, 32))
		b.WriteString("VERT")
		floorU32(&b, 4)
		floorU32(&b, 0)
	}
	for _, z := range []float32{0, -3} {
		floorPoint(&b, Point{-2, -2, z})
		floorPoint(&b, Point{2, 2, z})
		floorU32(&b, 0)
		floorU32(&b, 3)
		b.WriteString("VERT")
		floorU32(&b, 40)
		floorU32(&b, 3)
		for _, p := range []Point{{-2, -2, z}, {2, -2, z}, {0, 2, z}} {
			floorPoint(&b, p)
		}
		b.WriteString("TRIM")
		floorU32(&b, 16)
		floorU32(&b, 1)
		for _, v := range []uint32{0, 1, 2} {
			floorU32(&b, v)
		}
		b.WriteString("MBIH")
		floorBIH(&b)
		b.WriteString("LIQU")
		floorU32(&b, 4)
		floorU32(&b, 123)
	}
	b.WriteString("GBIH")
	floorBIH(&b)
	return b.Bytes()
}
func floorSpawnFixture(name string, rot Point, scale float32) []byte {
	var b bytes.Buffer
	b.WriteString("VMAP_4.8")
	floorU32(&b, 1)
	floorU32(&b, 4)
	b.Write(make([]byte, 2))
	floorU32(&b, 1)
	floorPoint(&b, Point{vmapMid + 8900, vmapMid + 100, 90})
	floorPoint(&b, rot)
	floorU32(&b, math.Float32bits(scale))
	floorPoint(&b, Point{vmapMid + 8890, vmapMid + 90, 80})
	floorPoint(&b, Point{vmapMid + 8910, vmapMid + 110, 100})
	floorU32(&b, uint32(len(name)))
	b.WriteString(name)
	floorU32(&b, 5)
	return b.Bytes()
}

func TestCollisionModelFramingAndAllTruncations(t *testing.T) {
	for _, empty := range []bool{false, true} {
		b := floorModelFixture(empty)
		m, err := parseFloorModel(b)
		if err != nil || len(m.groups) < 2 {
			t.Fatalf("empty=%t model=%v err=%v", empty, m, err)
		}
		for n := 0; n < len(b); n++ {
			if _, err := parseFloorModel(b[:n]); err == nil {
				t.Fatalf("accepted model truncation%d", n)
			}
		}
		if _, err := parseFloorModel(append(b, 0)); err == nil {
			t.Fatal("accepted trailing model data")
		}
	}
}
func TestCollisionSpawnFramingAndAllTruncations(t *testing.T) {
	b := floorSpawnFixture("test.wmo\x00", Point{}, 1)
	s, err := parseFloorSpawns(b)
	if err != nil || len(s) != 1 || s[0].name != "test.wmo" {
		t.Fatalf("spawn=%v err=%v", s, err)
	}
	for n := 0; n < len(b); n++ {
		if _, err := parseFloorSpawns(b[:n]); err == nil {
			t.Fatalf("accepted spawn truncation%d", n)
		}
	}
	for _, name := range []string{"../test.wmo", "/tmp/test.wmo", "bad\\file.wmo", "bad\x00name.wmo", "\x00"} {
		if _, err := parseFloorSpawns(floorSpawnFixture(name, Point{}, 1)); err == nil {
			t.Fatalf("accepted filename%q", name)
		}
	}
	for _, scale := range []float32{0, -1, 101, float32(math.NaN())} {
		if _, err := parseFloorSpawns(floorSpawnFixture("test.wmo", Point{}, scale)); err == nil {
			t.Fatalf("accepted scale%g", scale)
		}
	}
}
func TestCollisionReaderInternalAxesScaleRotationAndSuffix(t *testing.T) {
	for _, rot := range []Point{{}, {0, 90, 0}, {180, 45, 0}} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "000_32_48.vmtile"), floorSpawnFixture("test.wmo", rot, 2), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "test.wmo.vmo"), floorModelFixture(false), 0600); err != nil {
			t.Fatal(err)
		}
		f := newFloorReader(dir)
		h, ok, err := f.height(Point{-8900, -100, 90}, 90.25)
		if err != nil || !ok || math.Abs(float64(h-90)) > .001 {
			t.Fatalf("rotation=%v height=%g known=%t err=%v", rot, h, ok, err)
		}
		if f.bytes <= 0 || f.bytes > floorCacheBytes || len(f.tiles) != 1 || len(f.models) != 1 {
			t.Fatal("unbounded or missing cache")
		}
		if _, _, err := f.height(Point{-8400, 100, 90}, 90); err == nil {
			t.Fatal("loaded unsupported fourth tile")
		}
	}
}
func TestPhysicalFloorSelectionRejectsOtherLayers(t *testing.T) {
	for _, tc := range []struct {
		mesh, terrain, collision, want float32
		known                          bool
		err                            bool
	}{
		{92.31511, 158.506, 90.67409, 90.67409, true, false},
		{92.31511, 158.506, 96.888, 0, true, true},
		{90, 87.99, 0, 0, false, true},
		{90, 89.75, 89.8, 89.8, true, false},
		{90, 89.75, float32(math.NaN()), 89.75, true, false},
	} {
		h, err := selectPhysicalFloor(tc.mesh, tc.terrain, tc.collision, tc.known)
		if (err != nil) != tc.err || (!tc.err && math.Abs(float64(h-tc.want)) > .0001) {
			t.Fatalf("case=%+v height=%g err=%v", tc, h, err)
		}
	}
}
func TestPhysicalFloorProjectionKeepsMeshWalkProof(t *testing.T) {
	f := fakeRoute(testStart, testEnd)
	f.floorFn = func(_ Point, h, _ float32) (float32, error) { return h - .45, nil }
	out, err := plan(context.Background(), f, testStart, testEnd)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range out {
		if p.Z != testStart.Z-.45 {
			t.Fatalf("mesh height emitted%v", p)
		}
	}
	if f.walks != 5 {
		t.Fatal("mesh segment validation omitted")
	}
}

func TestPhysicalFloorProjectionPreservesStartTolerance(t *testing.T) {
	f := fakeRoute(testStart, testEnd)
	f.groundFn = func(p Point) (float32, error) {
		fraction := (p.X - testStart.X) / (testEnd.X - testStart.X)
		return p.Z - 1.5*(1-fraction), nil
	}
	f.floorFn = func(p Point, h, _ float32) (float32, error) {
		fraction := (p.X - testStart.X) / (testEnd.X - testStart.X)
		return h - 1.5*(1-fraction), nil
	}
	if _, err := plan(context.Background(), f, testStart, testEnd); err == nil || !strings.Contains(err.Error(), "normalized start") {
		t.Fatalf("expected normalized-start rejection, got%v", err)
	}
}
func TestPhysicalFloorStairsRefineButCliffsFail(t *testing.T) {
	f := fakeRoute(testStart, testEnd)
	f.floorFn = func(p Point, h, _ float32) (float32, error) {
		return h + float32(math.Min(math.Max(float64(p.X+8899), 0), 1.2)) - .8, nil
	}
	out, err := plan(context.Background(), f, testStart, testEnd)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(out); i++ {
		if math.Abs(float64(out[i].Z-out[i-1].Z)) > 1 {
			t.Fatal("vertical step limit exceeded")
		}
	}
	f = fakeRoute(testStart, testEnd)
	f.floorFn = func(p Point, h, _ float32) (float32, error) {
		if p.X >= -8898 {
			return h - 1.5, nil
		}
		return h, nil
	}
	if _, err := plan(context.Background(), f, testStart, testEnd); err == nil {
		t.Fatal("accepted abrupt1.5yd cliff")
	}
}
func FuzzCollisionAssets(f *testing.F) {
	f.Add(floorSpawnFixture("test.wmo", Point{}, 1))
	f.Add(floorModelFixture(false))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<20 {
			t.Skip()
		}
		_, _ = parseFloorSpawns(b)
		_, _ = parseFloorModel(b)
	})
}

func TestRerouteAlwaysRestoresMeshFlags(t *testing.T) {
	for _, mode := range []string{"success", "error", "cancel", "panic", "budget"} {
		t.Run(mode, func(t *testing.T) {
			p := testMesh(t)
			a, b := Point{-8898, -98, 90}, Point{-8898, -87, 90}
			ref, _, err := p.groundRef(a)
			if err != nil {
				t.Fatal(err)
			}
			other, _, err := p.groundRef(b)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			func() {
				defer func() {
					if r := recover(); r != nil && mode != "panic" {
						panic(r)
					}
				}()
				_, _ = rerouteBounded(ctx, p, func() ([]Point, error) {
					calls++
					if calls == 1 {
						return nil, &physicalDiscontinuity{from: a}
					}
					var flags uint16
					p.mesh.GetPolyFlags(ref, &flags)
					if flags != 0 {
						t.Fatal("reroute did not exclude exact failed polygon")
					}
					switch mode {
					case "success":
						return []Point{a, b}, nil
					case "error":
						return nil, errors.New("no route")
					case "cancel":
						cancel()
						return nil, context.Canceled
					case "panic":
						panic("test query panic")
					default:
						if calls == 2 {
							return nil, &physicalDiscontinuity{from: b}
						}
						return nil, &physicalDiscontinuity{from: a}
					}
				})
			}()
			for _, r := range []detour.DtPolyRef{ref, other} {
				var flags uint16
				p.mesh.GetPolyFlags(r, &flags)
				if flags != 1 {
					t.Fatalf("persistent mesh mutation after%s flags=%d", mode, flags)
				}
			}
			if calls > 3 {
				t.Fatal("unbounded retries")
			}
		})
	}
}
