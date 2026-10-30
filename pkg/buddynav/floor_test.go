package buddynav

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/azerothcore/AzerothGhost/pathfinding"
)

// Explicit opt-in measurements use only static, independently extracted client
// assets. Nothing in this test connects to the realm or moves a character.
func TestPhysicalFloorMeasurements(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_FLOOR_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_FLOOR_TEST_DATA_DIR to static client assets")
	}
	p, err := cachedPlanner(dir)
	if err != nil {
		t.Fatal(err)
	}
	v := pathfinding.NewVMapManager(filepath.Join(dir, "vmaps"))
	f := newFloorReader(filepath.Join(dir, "vmaps"))
	for i, q := range []Point{
		{-8947.64, -132.319, 83.7199},
		{-8902.59, -162.606, 82.0223},
		{-8869.22, -163.237, 80.9719},
		{-8901.77, -161.66782, 81.93899},
		{-8648.084, -129.24467, 87.75133},
		{-8917.333, -140.45744, 81.77461},
		{-8574.933, -148.26666, 92.31511},
		{-8575.299, -148.17499, 92.24593},
		{-8575.266, -148.18332, 90.80919},
		{-8873.155, -150.74417, 81.029144},
		{-8873.139, -150.79642, 80.0192},
	} {
		h, e := p.ground(q)
		th, tok := p.terrain(q)
		vh, vok := v.GetHeight(0, q.X, q.Y, q.Z+2, 5)
		fh, fok, fe := f.height(q, h)
		if e != nil || fe != nil {
			t.Fatalf("floor fixture mesh=%v floor=%v", e, fe)
		}
		if (i == 3 || i == 4) && (!fok || math.Abs(float64(fh-q.Z)) > .01) {
			t.Fatalf("floor disagrees with actual server observation: point=%+v floor=%g known=%t", q, fh, fok)
		}
		if i == 6 && (!fok || math.Abs(float64(fh-90.67409)) > .01) {
			t.Fatalf("measured cave portal mismatch%g", fh)
		}
		model := f.lastModel
		if !fok {
			v, known, e := f.heightRange(q, h, .5, 25)
			t.Logf("broad diagnostic probe=%+v floor=%g known=%t err=%v", q, v, known, e)
		}
		m := map[string]any{"point": q, "mesh": h, "terrain": th, "terrainKnown": tok, "vmap": vh, "vmapKnown": vok}
		m["floor"], m["floorKnown"] = fh, fok
		m["model"] = model
		if fe != nil {
			m["floorError"] = fe.Error()
		}
		if e != nil {
			m["meshError"] = e.Error()
		}
		b, _ := json.Marshal(m)
		t.Log(string(b))
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	t.Logf("heapAlloc=%d heapSys=%d", m.HeapAlloc, m.HeapSys)
}

func TestPhysicalEchoRidgeRoutes(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_FLOOR_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_FLOOR_TEST_DATA_DIR to static client assets")
	}
	start := Point{-8648.084, -129.24467, 87.75133}
	deep := Point{-8532.177, -207.2, 84.32273}
	for _, pair := range [][2]Point{{start, deep}, {deep, start}} {
		now := time.Now()
		route, err := PlanContext(context.Background(), dir, 0, pair[0], pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if len(route) < 2 || len(route) > MaxPoints {
			t.Fatalf("invalid mine route count%d", len(route))
		}
		var length float64
		for i := 1; i < len(route); i++ {
			length += distance(route[i-1], route[i])
			if math.Abs(float64(route[i].Z-route[i-1].Z)) > 1 || distance(route[i-1], route[i]) > 2 {
				t.Fatal("mine step exceeded cap")
			}
		}
		if length > MaxRouteLength || distance(route[len(route)-1], pair[1]) > EndpointTolerance {
			t.Fatal("mine route exceeded cap")
		}
		t.Logf("mine from=%+v to=%+v points=%d length=%.3f elapsed=%s", pair[0], pair[1], len(route), length, time.Since(now))
	}
}
