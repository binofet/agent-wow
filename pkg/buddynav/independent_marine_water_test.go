package buddynav

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIndependentActualSurfaceWaterAndReleasedLaunch(t *testing.T) {
	dir := os.Getenv("BUDDY_NAV_SURFACE_TEST_DATA_DIR")
	if dir == "" {
		t.Skip("set BUDDY_NAV_SURFACE_TEST_DATA_DIR for copied liquid and ship controls")
	}
	ctx := context.Background()
	route, e := LoadTransportPath(ctx, dir, 292, 30, 3)
	if e != nil {
		t.Fatal(e)
	}
	mesh, e := LoadSurfaceMesh(ctx, dir, 3015)
	if e != nil {
		t.Fatal(e)
	}
	sample, e := route.Sample(12000)
	if e != nil {
		t.Fatal(e)
	}
	tr := SurfaceTransform{Origin: sample.Position, Yaw: sample.Yaw, Scale: 1}
	h := MovingSurface{Transform: tr, Model: mesh.Collision()}
	inside := Point{X: 36.27977, Y: 2.2660532, Z: 6.094307}
	body := tr.World(inside)
	yaw := float64(tr.Yaw + float32(math.Pi))
	r, e := PlanSurfaceGravityContext(ctx, dir, sample.MapID, body, yaw, 0, BodyJumpVelocity, h)
	if e != nil || len(r) < 3 {
		t.Fatalf("retiring a real jump on the opposite end of the copied deck: %v (%d rows)", e, len(r))
	}
	highest := body.Z
	for j, q := range r {
		if j > 0 && q.At <= r[j-1].At || q.Point.X != body.X || q.Point.Y != body.Y || q.Swimming || !q.Settling {
			t.Fatalf("released input produced nonvertical/impossible packet %d %+v", j, q)
		}
		highest = max(highest, q.Point.Z)
	}
	if highest-body.Z < 1 || distance(r[len(r)-1].Point, body) > .05 {
		t.Fatalf("released ballistic excursion %.3f final error %.3f", highest-body.Z, distance(r[len(r)-1].Point, body))
	}
	for _, neg := range []struct {
		age time.Duration
		v   float64
	}{{0, 0}, {time.Millisecond, BodyJumpVelocity}, {0, 4}, {time.Millisecond, -BodyJumpVelocity}, {-time.Millisecond, BodyJumpVelocity}} {
		if path, err := PlanSurfaceGravityContext(ctx, dir, sample.MapID, body, yaw, neg.age, neg.v, h); err == nil {
			t.Errorf("unsupported new takeoff age=%v v=%f gave %d rows", neg.age, neg.v, len(path))
		}
	}
	blockedOutside := Point{Y: -70}
	blockedEdge, e := mesh.BoardingEntry(ctx, blockedOutside, inside)
	if e != nil {
		t.Fatal(e)
	}
	a, b := tr.World(blockedEdge), tr.World(blockedOutside)
	wallHeading := math.Atan2(float64(b.Y-a.Y), float64(b.X-a.X))
	if rows, e := PlanSurfaceBodyContext(ctx, dir, sample.MapID, a, wallHeading, 7, 500*time.Millisecond, true, true, h); e == nil || !strings.Contains(e.Error(), "intersects static collision") || len(rows) != 0 {
		t.Fatalf("actual solid rail was crossed: %v %v", rows, e)
	}
	off := Point{Y: 65}
	edge, e := mesh.BoardingEntry(ctx, off, inside)
	if e != nil {
		t.Fatal(e)
	}
	start, end := tr.World(edge), tr.World(off)
	angle := math.Atan2(float64(end.Y-start.Y), float64(end.X-start.X))
	rows, e := PlanSurfaceBodyContext(ctx, dir, sample.MapID, start, angle, 7, 500*time.Millisecond, true, true, h)
	if e != nil || len(rows) < 2 || !rows[len(rows)-1].Swimming {
		t.Fatalf("other side of sampled moving ferry has no verified water landing: err=%v last=%+v", e, rows)
	}
	last := rows[len(rows)-1]
	if last.Point.Z >= start.Z || last.FallTime <= 0 {
		t.Fatal("invalid liquid landing", last)
	}
	// Mutable links only in the test temp dir, leaving original installed data untouched.
	masked := t.TempDir()
	for _, which := range []string{"maps", "vmaps"} {
		if err := os.Symlink(filepath.Join(dir, which), filepath.Join(masked, which)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(masked, "mmaps"), 0755); err != nil {
		t.Fatal(err)
	}
	// At the observed pose the deck itself is sufficient for ordinary ground input.
	steps, e := PlanSurfaceBodyContext(ctx, masked, sample.MapID, body, yaw, 3, 100*time.Millisecond, false, true, h)
	if e != nil || len(steps) < 2 {
		t.Fatalf("dry deck cannot move without irrelevant liquid tiles: %v", e)
	}
	if wet, e := PlanSurfaceBodyContext(ctx, masked, sample.MapID, start, angle, 7, 500*time.Millisecond, true, true, h); e == nil || len(wet) != 0 {
		t.Fatalf("missing liquid tiles established wet crossing %v", wet)
	}
	beforeCancel, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := PlanSurfaceBodyContext(beforeCancel, dir, sample.MapID, start, angle, 7, 500*time.Millisecond, true, true, h); e == nil {
		t.Error("cancelled contact returned a plan")
	}
	t.Logf("opposite deck at travel time 12000: vertical apex %.3fy, landed in %.3fs; other rail → verified sea drop %.2fy in %.3fs", highest-body.Z, r[len(r)-1].At.Seconds(), start.Z-last.Point.Z, last.At.Seconds())
}
