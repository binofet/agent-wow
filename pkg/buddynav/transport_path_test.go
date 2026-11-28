package buddynav

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// A synthetic, non-planar ferry with two docks and two discarded transfer
// markers. No client asset is required by the ordinary regression.
func transportPathFixture() []byte {
	nodes := []transportNode{
		{point: Point{-60, 0, 0}}, {point: Point{-30, 0, 0}},
		{point: Point{0, 5, 0}, action: 2, delay: 3}, {point: Point{6, 45, 3}},
		{point: Point{0, 80, 8}}, {point: Point{-4, 100, 9}, action: 1},
		{mapID: 1, point: Point{1000, 339, 42}}, {mapID: 1, point: Point{1000, 350, 44}},
		{mapID: 1, point: Point{1006, 361, 50}, action: 2, delay: 2},
		{mapID: 1, point: Point{1010, 400, 53}}, {mapID: 1, point: Point{1012, 430, 55}},
	}
	b := []byte("WDBC")
	add := func(v uint32) { b = binary.LittleEndian.AppendUint32(b, v) }
	for _, v := range []uint32{uint32(len(nodes)), 11, 44, 1} {
		add(v)
	}
	for i := len(nodes) - 1; i >= 0; i-- { // storage order differs from taxi path order
		n := nodes[i]
		for _, v := range []uint32{uint32(i + 1), 7, uint32(i), n.mapID, math.Float32bits(n.point.X), math.Float32bits(n.point.Y), math.Float32bits(n.point.Z), n.action, n.delay, 0, 0} {
			add(v)
		}
	}
	return append(b, 0)
}

func writeTransportPathFixture(t *testing.T, b []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "dbc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dbc", "TaxiPathNode.dbc"), b, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func checkTransportSample(t *testing.T, p *TransportPath, ms uint64, want TransportPathSample) {
	t.Helper()
	s, err := p.Sample(ms)
	if err != nil || s.MapID != want.MapID || s.Stopped != want.Stopped || s.Transfer != want.Transfer || distance(s.Position, want.Position) > 0.003 || math.Abs(float64(s.Yaw-want.Yaw)) > 0.00003 {
		t.Fatalf("phase %d got %+v, %v; want %+v", ms, s, err, want)
	}
}

func TestTransportPathDwellMotionAndTransfer(t *testing.T) {
	p, err := LoadTransportPath(context.Background(), writeTransportPathFixture(t, transportPathFixture()), 7, 12, 4)
	if err != nil {
		t.Fatal(err)
	}
	// Generated independently with the public AzerothCore C++ spline, path
	// builder and movement calculation at cda3f63b. Transfers use its declared
	// receiving orientation; moving samples use its spline derivative.
	if p.Period() != 24456 {
		t.Fatalf("period %d", p.Period())
	}
	checkTransportSample(t, p, 0, TransportPathSample{0, Point{-30, 0, 0}, 3.2247338, false, true})
	checkTransportSample(t, p, 500, TransportPathSample{0, Point{-25.660545, 0.1244654, -0.0452532}, 3.1186333, false, false})
	checkTransportSample(t, p, 5500, TransportPathSample{0, Point{0, 5, 0}, 4.0376482, true, false})
	checkTransportSample(t, p, 9000, TransportPathSample{0, Point{2.679212, 10.113036, 0.3296318}, 4.3696041, false, false})
	checkTransportSample(t, p, 15011, TransportPathSample{0, Point{0.000452, 79.99736, 7.9996233}, 4.8822746, false, false})
	checkTransportSample(t, p, 15012, TransportPathSample{1, Point{1000, 350, 44}, 4.446137, false, true})
	checkTransportSample(t, p, 15013, TransportPathSample{1, Point{1000.002625, 350.00412, 44.002254}, 4.1441255, false, false})
	checkTransportSample(t, p, 19500, TransportPathSample{1, Point{1006, 361, 50}, 4.5149937, true, false})
	checkTransportSample(t, p, uint64(p.Period())*1000000+21000, TransportPathSample{1, Point{1006.44354, 363.57312, 50.38803}, 4.561132, false, false})
	checkTransportSample(t, p, 24456, TransportPathSample{0, Point{-30, 0, 0}, 3.2247338, false, true})
	for _, r := range []struct {
		from, to uint64
		crosses  bool
	}{
		{0, 15011, false}, {15011, 15012, true}, {15012, 15012, false}, {15012, 19500, false},
		{24455, 24456, true}, {17, 24456*1000000 + 17, true}, {17, 16, true},
	} {
		if p.CrossesTransfer(r.from, r.to) != r.crosses {
			t.Errorf("crossing (%d,%d] should be %v", r.from, r.to, r.crosses)
		}
	}
}

func TestTransportPathSameMapTeleport(t *testing.T) {
	b := transportPathFixture()
	for off := 20; off+44 <= len(b); off += 44 {
		binary.LittleEndian.PutUint32(b[off+12:], 0)
	}
	p, err := LoadTransportPath(context.Background(), writeTransportPathFixture(t, b), 7, 12, 4)
	if err != nil {
		t.Fatal(err)
	}
	checkTransportSample(t, p, 15012, TransportPathSample{0, Point{1000, 350, 44}, 4.446137, false, true})
	if !p.CrossesTransfer(15011, 15013) {
		t.Fatal("missed same-map discontinuity")
	}
}

func TestTransportPathUnknownOrMalformed(t *testing.T) {
	b := transportPathFixture()
	dir := writeTransportPathFixture(t, b)
	ctx := context.Background()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, tc := range []struct {
		c            context.Context
		id           uint32
		speed, accel float64
	}{
		{ctx, 0, 12, 4}, {ctx, 8, 12, 4}, {ctx, 7, 0, 4}, {ctx, 7, 12, math.NaN()}, {ctx, 7, math.Inf(1), 4}, {ctx, 7, math.SmallestNonzeroFloat64, 4}, {canceled, 7, 12, 4},
	} {
		if p, err := LoadTransportPath(tc.c, dir, tc.id, tc.speed, tc.accel); err == nil || p != nil {
			t.Fatalf("invalid input produced a path: %+v", tc)
		}
	}
	for _, edit := range []func([]byte) []byte{
		func(b []byte) []byte { return b[:len(b)-1] },
		func(b []byte) []byte { b[8] = 10; return b },
		func(b []byte) []byte { b[20+8] = 8; return b }, // duplicate/hole in node indices
		func(b []byte) []byte {
			binary.LittleEndian.PutUint32(b[20+20:], math.Float32bits(float32(math.NaN())))
			return b
		},
	} {
		if p, err := LoadTransportPath(ctx, writeTransportPathFixture(t, edit(append([]byte(nil), b...))), 7, 12, 4); err == nil || p != nil {
			t.Fatal("malformed table produced a path")
		}
	}
	var unavailable *TransportPath
	if _, err := unavailable.Sample(0); err == nil || unavailable.Period() != 0 || !unavailable.CrossesTransfer(0, 1) {
		t.Fatal("missing path produced usable geometry")
	}
}

func TestCopiedTransportPathHarborClocks(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_POOL_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_POOL_TEST_DATA_DIR to the copied client data")
	}
	// Fixture speeds are supplied template inputs. These are reference
	// checkpoints from the C++ calculation, not observed ship clocks.
	p, err := LoadTransportPath(context.Background(), dir, 292, 30, 3)
	if err != nil {
		t.Fatal(err)
	}
	if p.Period() != 190656 {
		t.Fatalf("map 0/1 period %d", p.Period())
	}
	checkTransportSample(t, p, 10000, TransportPathSample{0, Point{-4051.78052, -610.149475, 0}, 2.9995277, false, false})
	for _, ms := range []uint64{19987, 50000, 79986} {
		checkTransportSample(t, p, ms, TransportPathSample{0, Point{-3905.22485, -585.80847, 0}, 3.6433647, true, false})
	}
	checkTransportSample(t, p, 79987, TransportPathSample{0, Point{-3905.22485, -585.80847, 0}, 3.6433647, false, false})
	checkTransportSample(t, p, 98606, TransportPathSample{0, Point{-3893.84644, -245.59758, 0}, 5.3002334, false, false})
	checkTransportSample(t, p, 98607, TransportPathSample{1, Point{-4263.52539, -4823.31055, 0}, 4.2334037, false, true})
	checkTransportSample(t, p, 98608, TransportPathSample{1, Point{-4263.51758, -4823.29785, 0}, 4.1685505, false, false})
	checkTransportSample(t, p, 150000, TransportPathSample{1, Point{-4016.38989, -4740.58838, 0}, 2.4559445, true, false})
	checkTransportSample(t, p, 190656, TransportPathSample{0, Point{-4199.93652, -372.29944, 0}, 1.6914055, false, true})
	other, err := LoadTransportPath(context.Background(), dir, 964, 12, 5)
	if err != nil {
		t.Fatal(err)
	}
	if other.Period() != 493615 {
		t.Fatalf("map 0/571 period %d", other.Period())
	}
	checkTransportSample(t, other, 210000, TransportPathSample{571, Point{302.28882, -5116.95215, 0}, 2.3943868, false, false})
}
